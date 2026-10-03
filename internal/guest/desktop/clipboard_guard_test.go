package desktop

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestClipboardGuardWireRefusesLegacyHelper(t *testing.T) {
	runner := &runnerFake{}
	req := request("clipboard.set")
	zero := uint32(0)
	formats := []uint32{}
	req.ExpectedSequence = &zero
	req.ExpectedInventory = &formats
	if _, err := New(runner).Execute(t.Context(), "local:aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", req); err != nil {
		t.Fatal(err)
	}
	var wire Request
	if err := json.Unmarshal(runner.input, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Action != "clipboard.set.guarded" || wire.ExpectedSequence == nil || *wire.ExpectedSequence != 0 {
		t.Fatal("guard downgrade")
	}
	if _, ok := requestRule(wire.Action); ok {
		t.Fatal("guard wire action admitted as external action")
	}
	data := []byte(`{"request_id":"` + req.RequestID + `","success":false,"error":"clipboard_possibly_cleared"}`)
	if _, err := decodeResponse(data, req.RequestID, "execute"); !errors.Is(err, ErrClipboardUncertain) {
		t.Fatal("partial clear lost", err)
	}
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for data-only clipboard fixture")
	}
	queue, _ := scripts.ReadFile("queue.ps1")
	// Simulate the installed previous allowlist, extracting only the pure dispatcher.
	legacy := strings.ReplaceAll(string(queue), ", 'clipboard.set.guarded'", "")
	input, _ := json.Marshal(map[string]string{"queue": legacy})
	output, err := runParserCheck(path, `$d=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))|ConvertFrom-Json;$a=[Management.Automation.Language.Parser]::ParseInput($d.queue,[ref]$null,[ref]$null);$f=$a.Find({param($n)$n -is [Management.Automation.Language.FunctionDefinitionAst] -and $n.Name -eq 'Get-WorkerArguments'},$false);. ([ScriptBlock]::Create($f.Extent.Text));try{Get-WorkerArguments ('a'*32) 'clipboard.set.guarded';throw 'downgrade'}catch{if($_.Exception.Message -ne 'unsupported_action'){throw}};exit 0`, input)
	if err != nil {
		t.Fatalf("old helper refusal: %v %s", err, output)
	}
}

func TestNativeClipboardGuardMockedBoundary(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for data-only clipboard fixture")
	}
	native, _ := scripts.ReadFile("native.cs")
	source := string(native)
	offset := strings.Index(source, "    // Native boundary replaced")
	if offset < 0 {
		t.Fatal("native boundary missing")
	}
	source = strings.ReplaceAll(source[:offset], "Marshal.Copy(", "ClipboardCases.Copy(") + clipboardMockNative + "\n}\n" + clipboardMockCases
	output, err := runParserCheck(path, `Add-Type -TypeDefinition ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))) -ErrorAction Stop;[ClipboardCases]::Run()`, []byte(source))
	if err != nil {
		t.Fatalf("mocked native clipboard: %v %s", err, output)
	}
	for _, name := range []string{"unicode_crlf", "empty", "sequence_conflict", "format_conflict", "same_text_aba", "busy", "access_denied", "enumeration_error", "oversized", "malformed", "partial_clear", "snapshot_zero", "wrap", "read_maximum", "read_padded", "read_odd_capacity", "read_missing_terminator", "read_supplementary", "read_huge_capacity_bounded", "read_malformed_surrogate", "read_aligned_terminator", "clear_false_uncertain", "inventory_unicode", "inventory_custom", "inventory_image", "inventory_mixed_malformed", "inventory_empty_zero", "inventory_busy", "inventory_enumeration_error", "inventory_duplicate", "inventory_oversize", "inventory_mta"} {
		if strings.Count(string(output), "passed:"+name+"\r\n") != 1 {
			t.Fatalf("missing case %s: %s", name, output)
		}
		t.Log("native case passed:", name)
	}
}

// Native imports are removed before compilation; only synthetic allocated memory is touched.
const clipboardMockNative = `
    private static class Native {
        public static int LastError() { return ClipboardCases.Error; }
        public static void ClearError(uint error) { ClipboardCases.Error=0; }
        public static bool OpenClipboard(IntPtr owner) { if(ClipboardCases.Busy)return false; ClipboardCases.Opens++;ClipboardCases.Locked=true;return true; }
        public static bool CloseClipboard() { ClipboardCases.Locked=false;ClipboardCases.Closes++;return true; }
        public static uint EnumClipboardFormats(uint previous) {
            ClipboardCases.CheckLock();
            if(ClipboardCases.EnumFail){ClipboardCases.Error=5;return 0;}
            if(ClipboardCases.EnumDuplicate)return 13;
            if(previous==0)ClipboardCases.EnumIndex=0;
            if(ClipboardCases.EnumIndex<ClipboardCases.Formats.Length)return ClipboardCases.Formats[ClipboardCases.EnumIndex++];return 0;
        }
        public static uint GetClipboardSequenceNumber(){ClipboardCases.CheckLock();return ClipboardCases.Sequence;}
        public static IntPtr GetClipboardData(uint format){ClipboardCases.CheckLock();ClipboardCases.Reads++;return ClipboardCases.Data;}
        public static bool EmptyClipboard(){ClipboardCases.CheckLock();ClipboardCases.Writes++;ClipboardCases.Formats=new uint[0];ClipboardCases.Sequence=unchecked(ClipboardCases.Sequence+1);return !ClipboardCases.ClearFail;}
        public static IntPtr SetClipboardData(uint format,IntPtr memory){ClipboardCases.CheckLock();ClipboardCases.Sets++;if(ClipboardCases.SetFail)return IntPtr.Zero;ClipboardCases.Data=memory;ClipboardCases.Formats=new uint[]{13};ClipboardCases.Sequence=unchecked(ClipboardCases.Sequence+1);return memory;}
        public static IntPtr GlobalAlloc(uint flags,UIntPtr size){ClipboardCases.DataSize=size;return ClipboardCases.Allocate((int)size.ToUInt32());}
        public static UIntPtr GlobalSize(IntPtr memory){return ClipboardCases.DataSize;}
        public static IntPtr GlobalLock(IntPtr memory){ClipboardCases.Locks++;return memory;}
        public static bool GlobalUnlock(IntPtr memory){return true;}
        public static IntPtr GlobalFree(IntPtr memory){ClipboardCases.Free(memory);return IntPtr.Zero;}
        public static IntPtr CreateWindowEx(int exStyle,string name,string title,int style,int x,int y,int width,int height,IntPtr parent,IntPtr menu,IntPtr instance,IntPtr parameter){ClipboardCases.Owners++;return new IntPtr(1);}
        public static bool DestroyWindow(IntPtr owner){ClipboardCases.Owners--;return true;}
    }
`

const clipboardMockCases = `
public static class ClipboardCases {
 public static bool Locked,Busy,EnumFail,EnumDuplicate,SetFail,ClearFail; public static int Writes,Sets,Closes,Opens,Owners,Error,CopiedBytes,Reads,Locks,Copies,EnumIndex;
 public static uint Sequence;public static uint[] Formats;public static IntPtr Data;public static UIntPtr DataSize;
 private static List<IntPtr> allocations=new List<IntPtr>();
 public static void CheckLock(){if(!Locked)throw new Exception("outside_lock");}
 public static IntPtr Allocate(int size){IntPtr p=Marshal.AllocHGlobal(size);allocations.Add(p);return p;}
 public static void Free(IntPtr p){if(!allocations.Remove(p))throw new Exception("double_free");Marshal.FreeHGlobal(p);}
 private static void Reset(){foreach(IntPtr p in allocations)Marshal.FreeHGlobal(p);allocations.Clear();Locked=Busy=EnumFail=EnumDuplicate=SetFail=ClearFail=false;Writes=Sets=Closes=Opens=Owners=Error=CopiedBytes=Reads=Locks=Copies=EnumIndex=0;Sequence=0;Formats=new uint[0];Data=IntPtr.Zero;DataSize=UIntPtr.Zero;}
 public static void Copy(IntPtr source,byte[] destination,int offset,int length){Copies++;Verify(length<=8194);CopiedBytes=length;Marshal.Copy(source,destination,offset,length);}
 public static void Copy(byte[] source,int offset,IntPtr destination,int length){Copies++;Marshal.Copy(source,offset,destination,length);}
 private static void ReadData(byte[] payload,int capacity,ulong reported){
  byte[] bytes=new byte[capacity];for(int i=0;i<bytes.Length;i++)bytes[i]=120;Array.Copy(payload,bytes,payload.Length);
  Data=Allocate(capacity);Marshal.Copy(bytes,0,Data,bytes.Length);DataSize=new UIntPtr(reported);Formats=new uint[]{13};
 }
 private static void ReadText(string text,int capacity,ulong reported){ReadData(new UnicodeEncoding(false,false,true).GetBytes(text+"\0"),capacity,reported);}
 private static void ReadFailure(string error){
  try{AMCClipboard.Snapshot();throw new Exception("read_failure_missing");}
  catch(InvalidOperationException e){Verify(e.Message==error);}
  catch(DecoderFallbackException){Verify(error=="malformed");}
  Verify(!Locked && Closes==1);
 }
 private static void Verify(bool condition){if(!condition)throw new Exception("assertion_failed");}
 private static void Failure(string text,uint expected,uint[] formats,string error,int writes){
  try{AMCClipboard.Write(text,expected,formats,DateTime.UtcNow.AddMinutes(1));throw new Exception("failure_missing");}
  catch(InvalidOperationException e){Verify(e.Message==error);}
  catch(EncoderFallbackException){Verify(error=="malformed");}
  Verify(Writes==writes && !Locked && Owners==0);
  Verify(allocations.Count==0);
 }
 private static void InventoryCase(string name,uint[] formats,uint sequence){
  Reset();Formats=formats;Sequence=sequence;
  var state=AMCClipboard.InventorySnapshot();var expected=(uint[])formats.Clone();Array.Sort(expected);
  Verify(state.Sequence==sequence && state.InventoryComplete && state.Empty==(formats.Length==0) && state.Text==null);
  Verify(state.Formats.Length==expected.Length);for(int i=0;i<expected.Length;i++)Verify(state.Formats[i]==expected[i]);
  Verify(Opens==1 && Closes==1 && !Locked && Owners==0 && Writes==0 && Sets==0 && Reads==0 && Locks==0 && Copies==0);
  Console.WriteLine("passed:"+name);
 }
 private static void InventoryFailure(string name,string error,int opens){
  try{AMCClipboard.InventorySnapshot();throw new Exception("inventory_failure_missing");}
  catch(InvalidOperationException e){Verify(e.Message==error);}
  Verify(Opens==opens && Closes==opens && !Locked && Owners==0 && Writes==0 && Sets==0 && Reads==0 && Locks==0 && Copies==0);
  Console.WriteLine("passed:"+name);
 }
 public static void Run(){
  try {
   InventoryCase("inventory_unicode",new uint[]{13},uint.MaxValue);
   InventoryCase("inventory_custom",new uint[]{49321},42);
   InventoryCase("inventory_image",new uint[]{2,8},2);
   // Unicode advertises a null/malformed payload; an accidental read fails this fixture.
   InventoryCase("inventory_mixed_malformed",new uint[]{49321,13,2},10);
   InventoryCase("inventory_empty_zero",new uint[0],0);
   Reset();Busy=true;InventoryFailure("inventory_busy","clipboard_unavailable",0);
   Reset();EnumFail=true;InventoryFailure("inventory_enumeration_error","clipboard_enumeration_failed",1);
   Reset();EnumDuplicate=true;InventoryFailure("inventory_duplicate","clipboard_inventory_incomplete",1);
   Reset();Formats=new uint[257];for(int i=0;i<Formats.Length;i++)Formats[i]=(uint)i+1;InventoryFailure("inventory_oversize","clipboard_inventory_incomplete",1);
   Reset();Exception threadError=null;var thread=new System.Threading.Thread(delegate(){try{InventoryFailure("inventory_mta","clipboard_requires_sta",0);}catch(Exception e){threadError=e;}});
   thread.SetApartmentState(System.Threading.ApartmentState.MTA);thread.Start();thread.Join();if(threadError!=null)throw threadError;
   Reset();var s=AMCClipboard.Write("世界 😀\r\nnext",0,new uint[0],DateTime.UtcNow.AddMinutes(1));Verify(s.Text=="世界 😀\r\nnext" && s.Sequence==2 && s.InventoryComplete && !s.Empty && Closes==1 && !Locked && Owners==0);Console.WriteLine("passed:unicode_crlf");
   Reset();s=AMCClipboard.Write("",0,new uint[0],DateTime.UtcNow.AddMinutes(1));Verify(s.Empty && s.Formats.Length==0 && s.Sequence==1);Console.WriteLine("passed:empty");
   Reset();Sequence=1;Failure("text",0,new uint[0],"clipboard_conflict",0);Console.WriteLine("passed:sequence_conflict");
   Reset();Formats=new uint[]{13,49321};Failure("text",0,new uint[]{13},"clipboard_conflict",0);Console.WriteLine("passed:format_conflict");
   Reset();Sequence=2;Formats=new uint[]{13};Failure("same text",1,new uint[]{13},"clipboard_conflict",0);Console.WriteLine("passed:same_text_aba");
   Reset();Busy=true;Failure("text",0,new uint[0],"clipboard_unavailable",0);Console.WriteLine("passed:busy");
   Reset();Busy=true;try{AMCClipboard.Snapshot();throw new Exception("access_allowed");}catch(InvalidOperationException e){Verify(e.Message=="clipboard_unavailable");}Verify(!Locked);Console.WriteLine("passed:access_denied");
   Reset();EnumFail=true;Failure("text",0,new uint[0],"clipboard_enumeration_failed",0);Console.WriteLine("passed:enumeration_error");
   Reset();Failure(new string('x',4097),0,new uint[0],"invalid_clipboard_guard",0);Console.WriteLine("passed:oversized");
   Reset();Failure("\ud800",0,new uint[0],"malformed",0);Console.WriteLine("passed:malformed");
   Reset();SetFail=true;Failure("text",0,new uint[0],"clipboard_possibly_cleared",1);Console.WriteLine("passed:partial_clear");
   Reset();ClearFail=true;Failure("text",0,new uint[0],"clipboard_possibly_cleared",1);Console.WriteLine("passed:clear_false_uncertain");
   Reset();string maximum=new string('x',4096);ReadText(maximum,8194,8194);s=AMCClipboard.Snapshot();Verify(s.Text==maximum && CopiedBytes==8194);Console.WriteLine("passed:read_maximum");
   Reset();ReadText("padded",16384,16384);s=AMCClipboard.Snapshot();Verify(s.Text=="padded" && CopiedBytes==8194);Console.WriteLine("passed:read_padded");
   Reset();ReadText("odd",9,9);s=AMCClipboard.Snapshot();Verify(s.Text=="odd" && CopiedBytes==8);Console.WriteLine("passed:read_odd_capacity");
   Reset();ReadData(new byte[0],8194,8194);ReadFailure("malformed_clipboard_text");Console.WriteLine("passed:read_missing_terminator");
   Reset();ReadText("😀",6,6);s=AMCClipboard.Snapshot();Verify(s.Text=="😀");Console.WriteLine("passed:read_supplementary");
   Reset();ReadText("bounded",8194,1UL<<34);s=AMCClipboard.Snapshot();Verify(s.Text=="bounded" && CopiedBytes==8194);Console.WriteLine("passed:read_huge_capacity_bounded");
   Reset();ReadData(new byte[]{0,216,0,0},4,4);ReadFailure("malformed");Console.WriteLine("passed:read_malformed_surrogate");
   Reset();ReadData(new byte[]{65,0,0,66,0,0},6,6);s=AMCClipboard.Snapshot();Verify(s.Text=="A\u4200");Console.WriteLine("passed:read_aligned_terminator");
   Reset();s=AMCClipboard.Snapshot();Verify(s.Sequence==0 && s.Empty && s.InventoryComplete && Closes==1);Console.WriteLine("passed:snapshot_zero");
   Reset();Sequence=uint.MaxValue;s=AMCClipboard.Write("text",uint.MaxValue,new uint[0],DateTime.UtcNow.AddMinutes(1));Verify(s.Sequence==1);Console.WriteLine("passed:wrap");
  }finally{Reset();}
 }
}
`
