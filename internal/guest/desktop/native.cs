using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;

// Compiled and called exclusively inside the enrolled guest interactive session.
public static class AMCDesktop {
    public struct Point { public int X, Y; }
    public struct Rect { public int Left, Top, Right, Bottom; }
    [StructLayout(LayoutKind.Sequential)] public struct CursorInfo { public int Size, Flags; public IntPtr Handle; public Point Position; }
    [StructLayout(LayoutKind.Sequential)] public struct MouseInput { public int X, Y; public uint Data, Flags, Time; public UIntPtr Extra; }
    [StructLayout(LayoutKind.Explicit)] public struct InputUnion { [FieldOffset(0)] public MouseInput Mouse; }
    [StructLayout(LayoutKind.Sequential)] public struct Input { public uint Type; public InputUnion Union; }
    private delegate bool EnumCallback(IntPtr hwnd, IntPtr extra);
    [DllImport("user32.dll")] private static extern bool EnumWindows(EnumCallback callback, IntPtr extra);
    [DllImport("user32.dll")] public static extern bool IsWindow(IntPtr hwnd);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hwnd);
    [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr hwnd);
    [DllImport("user32.dll")] public static extern bool IsZoomed(IntPtr hwnd);
    [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hwnd, out Rect rect);
    [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hwnd, out uint pid);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] private static extern int GetWindowText(IntPtr hwnd, StringBuilder text, int count);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] private static extern int GetClassName(IntPtr hwnd, StringBuilder text, int count);
    [DllImport("user32.dll", EntryPoint="GetWindowLongW", SetLastError=true)] private static extern int GetWindowStyle(IntPtr hwnd, int index);
    [DllImport("kernel32.dll", EntryPoint="SetLastError")] private static extern void ClearNativeError(uint error);
    [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hwnd);
    [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] public static extern bool ShowWindowAsync(IntPtr hwnd, int command);
    [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr hwnd, IntPtr after, int x, int y, int width, int height, uint flags);
    [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr hwnd, uint message, IntPtr wparam, IntPtr lparam);
    [DllImport("user32.dll")] public static extern bool GetCursorInfo(ref CursorInfo info);
    [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
    [DllImport("user32.dll")] private static extern uint SendInput(uint count, Input[] input, int size);
    [DllImport("user32.dll")] public static extern IntPtr OpenInputDesktop(uint flags, bool inherit, uint access);
    [DllImport("user32.dll")] public static extern bool CloseDesktop(IntPtr desktop);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] private static extern bool GetUserObjectInformation(IntPtr handle, int index, StringBuilder text, uint size, out uint needed);
    public static bool InteractiveDesktop() {
        IntPtr desktop = OpenInputDesktop(0, false, 1);
        if (desktop == IntPtr.Zero) return false;
        try { uint needed; var name = new StringBuilder(256); return GetUserObjectInformation(desktop, 2, name, 512, out needed) && name.ToString() == "Default"; }
        finally { CloseDesktop(desktop); }
    }
    public static IntPtr[] Windows() {
        var windows = new List<IntPtr>();
        EnumWindows(delegate(IntPtr hwnd, IntPtr extra) {
            if (IsWindowVisible(hwnd)) windows.Add(hwnd);
            return windows.Count < 128;
        }, IntPtr.Zero);
        return windows.ToArray();
    }
    public static string Title(IntPtr hwnd) { var text = new StringBuilder(513); GetWindowText(hwnd, text, text.Capacity); return text.ToString(); }
    public static string ClassName(IntPtr hwnd) { var text = new StringBuilder(257); GetClassName(hwnd, text, text.Capacity); return text.ToString(); }
    // Legacy UIA providers can expose a password edit as a non-password Pane.
    // Native styles are meaningful only for real Edit controls, not arbitrary panes.
    public static bool IsPasswordControl(bool uiaPassword, IntPtr hwnd) {
        if (uiaPassword) return true;
        if (hwnd == IntPtr.Zero) return false;
        if (!IsWindow(hwnd)) throw new InvalidOperationException("element_unavailable");
        string name = ClassName(hwnd);
        if (name.Length == 0) throw new InvalidOperationException("element_unavailable");
        if (!name.Equals("Edit", StringComparison.OrdinalIgnoreCase) &&
            !name.StartsWith("WindowsForms10.EDIT.", StringComparison.OrdinalIgnoreCase)) return false;
        ClearNativeError(0);
        int style = GetWindowStyle(hwnd, -16); // GWL_STYLE; zero can be valid
        if (style == 0 && Marshal.GetLastWin32Error() != 0) throw new InvalidOperationException("element_unavailable");
        if (!IsWindow(hwnd)) throw new InvalidOperationException("element_unavailable");
        return (style & 0x0020) != 0; // ES_PASSWORD
    }
    public static bool Wheel(int delta, bool horizontal) {
        var input = new Input { Type = 0, Union = new InputUnion { Mouse = new MouseInput { Data = unchecked((uint)delta), Flags = horizontal ? 0x1000u : 0x0800u } } };
        return SendInput(1, new Input[] { input }, Marshal.SizeOf(typeof(Input))) == 1;
    }
    // Windows command-line quoting; never invoke a shell to launch an application.
    public static string Quote(string argument) {
        var result = new StringBuilder("\""); int slashes = 0;
        foreach (char c in argument) {
            if (c == '\\') { slashes++; continue; }
            if (c == '"') { result.Append('\\', slashes * 2 + 1); result.Append(c); }
            else { result.Append('\\', slashes); result.Append(c); }
            slashes = 0;
        }
        result.Append('\\', slashes * 2); result.Append('"'); return result.ToString();
    }
}

// All metadata, comparisons and readback belong to a single clipboard lock.
public static class AMCClipboard {
    public sealed class State {
        public uint Sequence;
        public uint[] Formats;
        public bool InventoryComplete, Empty;
        public string Text;
    }
    private static void RequireSTA() {
        if (System.Threading.Thread.CurrentThread.GetApartmentState() != System.Threading.ApartmentState.STA)
            throw new InvalidOperationException("clipboard_requires_sta");
    }
    private static uint[] Inventory() {
        var formats = new List<uint>(); uint current = 0;
        for (;;) {
            Native.ClearError(0);
            current = Native.EnumClipboardFormats(current);
            if (current == 0) {
                if (Native.LastError() != 0) throw new InvalidOperationException("clipboard_enumeration_failed");
                break;
            }
            if (formats.Count >= 256 || formats.Contains(current)) throw new InvalidOperationException("clipboard_inventory_incomplete");
            formats.Add(current);
        }
        formats.Sort(); return formats.ToArray();
    }
    private static string ReadText(uint[] formats) {
        if (Array.IndexOf(formats, 13u) < 0) return "";
        IntPtr handle = Native.GetClipboardData(13);
        if (handle == IntPtr.Zero) throw new InvalidOperationException("clipboard_read_failed");
        ulong size = Native.GlobalSize(handle).ToUInt64();
        if (size < 2) throw new InvalidOperationException("oversized_clipboard");
        IntPtr pointer = Native.GlobalLock(handle);
        if (pointer == IntPtr.Zero) throw new InvalidOperationException("clipboard_read_failed");
        try {
            // GlobalSize is allocation capacity, including possible slack or odd padding.
            int copySize = (int)Math.Min(size, 8194UL); copySize -= copySize % 2;
            byte[] bytes = new byte[copySize]; Marshal.Copy(pointer, bytes, 0, bytes.Length);
            int end = 0; while (end < bytes.Length && (bytes[end] != 0 || bytes[end + 1] != 0)) end += 2;
            if (end == bytes.Length) throw new InvalidOperationException("malformed_clipboard_text");
            return new UnicodeEncoding(false, false, true).GetString(bytes, 0, end);
        } finally { Native.GlobalUnlock(handle); }
    }
    private static State ReadLocked() {
        uint[] formats = Inventory(); string text = ReadText(formats);
        // Delayed rendering can introduce formats; capture final metadata after text.
        formats = Inventory();
        return new State { Sequence = Native.GetClipboardSequenceNumber(), Formats = formats,
            InventoryComplete = true, Empty = formats.Length == 0, Text = text };
    }
    // Enumerate only: requesting a format's data can trigger private delayed rendering.
    public static State InventorySnapshot() {
        RequireSTA();
        if (!Native.OpenClipboard(IntPtr.Zero)) throw new InvalidOperationException("clipboard_unavailable");
        try {
            uint[] formats = Inventory();
            return new State { Sequence = Native.GetClipboardSequenceNumber(), Formats = formats,
                InventoryComplete = true, Empty = formats.Length == 0 };
        } finally { Native.CloseClipboard(); }
    }
    public static State Snapshot() {
        RequireSTA();
        if (!Native.OpenClipboard(IntPtr.Zero)) throw new InvalidOperationException("clipboard_unavailable");
        try { return ReadLocked(); }
        finally { Native.CloseClipboard(); }
    }
    private static byte[] LegacyText(string text, uint page) {
        // NLS may return CP_ACP/CP_OEMCP (0/1); retain those documented defaults.
        // Explicit length includes the terminator; null fallback pointers also support UTF-8.
        int size = Native.WideCharToMultiByte(page, 0, text, text.Length, null, 0, IntPtr.Zero, IntPtr.Zero);
        if (size <= 0 || size > 16388) throw new InvalidOperationException("clipboard_encoding_failed");
        byte[] bytes = new byte[size];
        if (Native.WideCharToMultiByte(page, 0, text, text.Length, bytes, bytes.Length, IntPtr.Zero, IntPtr.Zero) != size)
            throw new InvalidOperationException("clipboard_encoding_failed");
        return bytes;
    }
    private static byte[][] TextPayloads(string text, byte[] unicode) {
        // CF_LOCALE explicitly identifies the thread's Standards and Formats locale,
        // not the input language. Both legacy code pages come from that same LCID.
        uint locale = Native.GetThreadLocale(), ansi, oem;
        const uint numericDefaults = 0xa0000000; // RETURN_NUMBER | NOUSEROVERRIDE.
        if (locale == 0 || Native.GetLocaleInfo(locale, numericDefaults | 0x1004, out ansi, 2) != 2 ||
            Native.GetLocaleInfo(locale, numericDefaults | 0x000b, out oem, 2) != 2)
            throw new InvalidOperationException("clipboard_encoding_failed");
        return new byte[][] { unicode, LegacyText(text + "\0", ansi), LegacyText(text + "\0", oem), BitConverter.GetBytes(locale) };
    }
    public static State Write(string text, uint expected, uint[] inventory, DateTime deadline) {
        RequireSTA(); text = text ?? "";
        if (text.Length > 4096 || text.IndexOf('\0') >= 0 || inventory == null || inventory.Length > 256)
            throw new InvalidOperationException("invalid_clipboard_guard");
        for (int i = 0; i < inventory.Length; i++)
            if (inventory[i] == 0 || (i > 0 && inventory[i] <= inventory[i - 1])) throw new InvalidOperationException("invalid_clipboard_guard");
        byte[] payload = new UnicodeEncoding(false, false, true).GetBytes(text + "\0");
        byte[][] payloads = text.Length == 0 ? new byte[0][] : TextPayloads(text, payload);
        uint[] formats = new uint[] { 13, 1, 7, 16 };
        IntPtr[] memory = new IntPtr[payloads.Length];
        IntPtr owner = IntPtr.Zero; bool opened = false, effectPossible = false;
        try {
            // Prepare every format before opening; allocation/conversion cannot clear old data.
            for (int i = 0; i < payloads.Length; i++) {
                memory[i] = Native.GlobalAlloc(0x42, new UIntPtr((uint)payloads[i].Length));
                if (memory[i] == IntPtr.Zero) throw new InvalidOperationException("clipboard_allocation_failed");
                IntPtr pointer = Native.GlobalLock(memory[i]);
                if (pointer == IntPtr.Zero) throw new InvalidOperationException("clipboard_allocation_failed");
                try { Marshal.Copy(payloads[i], 0, pointer, payloads[i].Length); }
                finally { Native.GlobalUnlock(memory[i]); }
            }
            // Built-in STATIC class, task-owned message-only HWND; never NULL owner.
            owner = Native.CreateWindowEx(0, "STATIC", "", 0, 0, 0, 0, 0, new IntPtr(-3), IntPtr.Zero, IntPtr.Zero, IntPtr.Zero);
            if (owner == IntPtr.Zero) throw new InvalidOperationException("clipboard_owner_failed");
            if (!Native.OpenClipboard(owner)) throw new InvalidOperationException("clipboard_unavailable");
            opened = true;
            uint[] actual = Inventory();
            if (Native.GetClipboardSequenceNumber() != expected || actual.Length != inventory.Length)
                throw new InvalidOperationException("clipboard_conflict");
            for (int i = 0; i < actual.Length; i++)
                if (actual[i] != inventory[i]) throw new InvalidOperationException("clipboard_conflict");
            if (DateTime.UtcNow >= deadline) throw new InvalidOperationException("expired_request");
            effectPossible = true;
            if (!Native.EmptyClipboard()) throw new InvalidOperationException("clipboard_clear_failed");
            // Supply every text alias and locale under the original guard/lock, so
            // CloseClipboard need not publish missing text formats after token capture.
            for (int i = 0; i < memory.Length; i++) {
                if (Native.SetClipboardData(formats[i], memory[i]) == IntPtr.Zero)
                    throw new InvalidOperationException("clipboard_possibly_cleared");
                memory[i] = IntPtr.Zero; // Only a successful transfer gives the system ownership.
            }
            State state = ReadLocked();
            if (state.Text != text || (text.Length == 0 && !state.Empty)) throw new InvalidOperationException("clipboard_possibly_cleared");
            if (!Native.CloseClipboard()) throw new InvalidOperationException("clipboard_possibly_cleared");
            opened = false;
            if (!Native.DestroyWindow(owner)) throw new InvalidOperationException("clipboard_possibly_cleared");
            owner = IntPtr.Zero;
            // Observe stability only. Never adopt a post-close sequence or foreign payload.
            if (Native.GetClipboardSequenceNumber() != state.Sequence)
                throw new InvalidOperationException("clipboard_possibly_cleared");
            return state;
        } catch {
            if (effectPossible) throw new InvalidOperationException("clipboard_possibly_cleared");
            throw;
        } finally {
            // One cleanup failure must not skip the other task-owned resources.
            bool cleanupFailed = false;
            try { if (opened && !Native.CloseClipboard()) cleanupFailed = true; }
            catch { cleanupFailed = true; }
            try { if (owner != IntPtr.Zero && !Native.DestroyWindow(owner)) cleanupFailed = true; }
            catch { cleanupFailed = true; }
            for (int i = 0; i < memory.Length; i++) {
                try { if (memory[i] != IntPtr.Zero && Native.GlobalFree(memory[i]) != IntPtr.Zero) cleanupFailed = true; }
                catch { cleanupFailed = true; }
            }
            if (cleanupFailed) throw new InvalidOperationException("clipboard_possibly_cleared");
        }
    }
    // Native boundary replaced in the data-only regression fixture; never called there.
    private static class Native {
        public static int LastError() { return Marshal.GetLastWin32Error(); }
        [DllImport("kernel32.dll", EntryPoint="SetLastError")] public static extern void ClearError(uint error);
        [DllImport("user32.dll", SetLastError=true)] public static extern bool OpenClipboard(IntPtr owner);
        [DllImport("user32.dll")] public static extern bool CloseClipboard();
        [DllImport("user32.dll", SetLastError=true)] public static extern uint EnumClipboardFormats(uint previous);
        [DllImport("user32.dll")] public static extern uint GetClipboardSequenceNumber();
        [DllImport("user32.dll")] public static extern IntPtr GetClipboardData(uint format);
        [DllImport("user32.dll")] public static extern bool EmptyClipboard();
        [DllImport("user32.dll")] public static extern IntPtr SetClipboardData(uint format, IntPtr memory);
        [DllImport("kernel32.dll")] public static extern IntPtr GlobalAlloc(uint flags, UIntPtr size);
        [DllImport("kernel32.dll")] public static extern UIntPtr GlobalSize(IntPtr memory);
        [DllImport("kernel32.dll")] public static extern IntPtr GlobalLock(IntPtr memory);
        [DllImport("kernel32.dll")] public static extern bool GlobalUnlock(IntPtr memory);
        [DllImport("kernel32.dll")] public static extern IntPtr GlobalFree(IntPtr memory);
        [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr CreateWindowEx(int exStyle, string name, string title, int style, int x, int y, int width, int height, IntPtr parent, IntPtr menu, IntPtr instance, IntPtr parameter);
        [DllImport("user32.dll")] public static extern bool DestroyWindow(IntPtr owner);
        [DllImport("kernel32.dll")] public static extern uint GetThreadLocale();
        [DllImport("kernel32.dll", EntryPoint="GetLocaleInfoW", SetLastError=true)] public static extern int GetLocaleInfo(uint locale, uint kind, out uint value, int characters);
        [DllImport("kernel32.dll", CharSet=CharSet.Unicode, ExactSpelling=true, SetLastError=true)] public static extern int WideCharToMultiByte(uint page, uint flags, string text, int characters, byte[] bytes, int capacity, IntPtr fallback, IntPtr used);
    }
}
