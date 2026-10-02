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
