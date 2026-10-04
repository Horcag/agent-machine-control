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
	for _, name := range []string{"unicode_crlf", "empty", "sequence_conflict", "format_conflict", "same_text_aba", "busy", "access_denied", "enumeration_error", "oversized", "malformed", "partial_clear", "snapshot_zero", "wrap", "read_maximum", "read_padded", "read_odd_capacity", "read_missing_terminator", "read_supplementary", "read_huge_capacity_bounded", "read_malformed_surrogate", "read_aligned_terminator", "clear_false_uncertain", "inventory_unicode", "inventory_custom", "inventory_image", "inventory_mixed_malformed", "inventory_empty_zero", "inventory_busy", "inventory_enumeration_error", "inventory_duplicate", "inventory_oversize", "inventory_mta", "aliases_prepared", "locale_failure", "conversion_failure", "allocation_failure", "lock_failure", "partial_alias_transfer", "close_synthesis_stable", "postclose_foreign_conflict", "expiry_before_clear", "close_failure", "owner_destroy_failure", "postdestroy_drift", "locale_fallback_codepages", "second_conversion_failure", "conversion_bound", "locale_numeric_size", "close_cleanup_exception", "owner_cleanup_exception", "free_cleanup_exception"} {
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
        public static bool CloseClipboard() { ClipboardCases.CheckLock();ClipboardCases.Closes++;if(ClipboardCases.Closes==ClipboardCases.CloseFailAt)return false;ClipboardCases.Locked=false;ClipboardCases.AfterClose();if(ClipboardCases.Closes==ClipboardCases.CloseThrowAt)throw new Exception("synthetic_close_cleanup");return true; }
        public static uint EnumClipboardFormats(uint previous) {
            ClipboardCases.CheckLock();
            if(ClipboardCases.EnumFail){ClipboardCases.Error=5;return 0;}
            if(ClipboardCases.EnumDuplicate)return 13;
            if(previous==0)ClipboardCases.EnumIndex=0;
            if(ClipboardCases.EnumIndex<ClipboardCases.Formats.Length)return ClipboardCases.Formats[ClipboardCases.EnumIndex++];return 0;
        }
        public static uint GetClipboardSequenceNumber(){return ClipboardCases.Sequence;}
        public static IntPtr GetClipboardData(uint format){ClipboardCases.CheckLock();ClipboardCases.Reads++;return ClipboardCases.Data;}
        public static bool EmptyClipboard(){ClipboardCases.CheckLock();ClipboardCases.Writes++;ClipboardCases.Formats=new uint[0];ClipboardCases.Sequence=unchecked(ClipboardCases.Sequence+1);return !ClipboardCases.ClearFail;}
        public static IntPtr SetClipboardData(uint format,IntPtr memory){ClipboardCases.CheckLock();ClipboardCases.Sets++;if(ClipboardCases.SetFail || ClipboardCases.Sets==ClipboardCases.SetFailAt)return IntPtr.Zero;ClipboardCases.Transfer(format,memory);ClipboardCases.Sequence=unchecked(ClipboardCases.Sequence+1);return memory;}
        public static IntPtr GlobalAlloc(uint flags,UIntPtr size){ClipboardCases.CheckPreparation();ClipboardCases.Allocs++;if(ClipboardCases.Allocs==ClipboardCases.AllocFailAt)return IntPtr.Zero;return ClipboardCases.Allocate((int)size.ToUInt32());}
        public static UIntPtr GlobalSize(IntPtr memory){return ClipboardCases.DataSize;}
        public static IntPtr GlobalLock(IntPtr memory){ClipboardCases.Locks++;if(ClipboardCases.Locks==ClipboardCases.LockFailAt)return IntPtr.Zero;return memory;}
        public static bool GlobalUnlock(IntPtr memory){return true;}
        public static IntPtr GlobalFree(IntPtr memory){ClipboardCases.Free(memory);ClipboardCases.Frees++;if(ClipboardCases.Frees==ClipboardCases.FreeThrowAt)throw new Exception("synthetic_free_cleanup");return IntPtr.Zero;}
        public static IntPtr CreateWindowEx(int exStyle,string name,string title,int style,int x,int y,int width,int height,IntPtr parent,IntPtr menu,IntPtr instance,IntPtr parameter){ClipboardCases.Owners++;return new IntPtr(1);}
        public static bool DestroyWindow(IntPtr owner){ClipboardCases.Destroys++;if(ClipboardCases.Destroys==ClipboardCases.DestroyFailAt)return false;ClipboardCases.Owners--;if(ClipboardCases.Destroys==ClipboardCases.DestroyThrowAt)throw new Exception("synthetic_destroy_cleanup");if(ClipboardCases.DestroyDrift)ClipboardCases.Sequence=unchecked(ClipboardCases.Sequence+1);return true;}
        public static uint GetThreadLocale(){ClipboardCases.CheckPreparation();ClipboardCases.Locales++;return 0x0419;}
        public static int GetLocaleInfo(uint locale,uint kind,out uint value,int count){ClipboardCases.CheckPreparation();ClipboardCases.VerifyLocale(locale,kind,count);value=ClipboardCases.FallbackPages?((kind & 0xffff)==0x1004?0u:1u):((kind & 0xffff)==0x1004?1251u:866u);return ClipboardCases.LocaleFail?0:(ClipboardCases.LocaleSizeInvalid?1:2);}
        public static int WideCharToMultiByte(uint page,uint flags,string text,int length,byte[] bytes,int capacity,IntPtr fallback,IntPtr used){ClipboardCases.CheckPreparation();ClipboardCases.Conversions++;if(ClipboardCases.ConvertFail || ClipboardCases.Conversions==ClipboardCases.ConvertFailAt)return 0;if(ClipboardCases.ConvertOversize)return 16389;ClipboardCases.VerifyConversion(page,flags,text,length,fallback,used);byte[] data=Encoding.GetEncoding(page==0?1251:(page==1?866:(int)page)).GetBytes(text);if(bytes!=null)Array.Copy(data,bytes,data.Length);return data.Length;}

    }
`

const clipboardMockCases = `
public static class ClipboardCases {
 public static bool Locked,Busy,EnumFail,EnumDuplicate,SetFail,ClearFail,LocaleFail,ConvertFail,AutoSynthesis,ForeignAfterClose,DestroyDrift,FallbackPages,ConvertOversize,LocaleSizeInvalid; public static int Writes,Sets,Closes,Opens,Owners,Error,CopiedBytes,Reads,Locks,Copies,EnumIndex;
 public static int Allocs,AllocFailAt,LockFailAt,SetFailAt,CloseFailAt,DestroyFailAt,Destroys,Conversions,ConvertFailAt,Locales,CloseThrowAt,DestroyThrowAt,FreeThrowAt,Frees;
 public static uint Sequence;public static uint[] Formats;public static IntPtr Data;public static UIntPtr DataSize;
 private static List<IntPtr> allocations=new List<IntPtr>();
 private static Dictionary<IntPtr,int> sizes=new Dictionary<IntPtr,int>();
 private static Dictionary<uint,IntPtr> transferred=new Dictionary<uint,IntPtr>();
 public static void CheckPreparation(){if(Locked)throw new Exception("preparation_inside_lock");}
 public static void VerifyLocale(uint locale,uint kind,int count){Verify(locale==0x0419 && (kind & 0xa0000000)==0xa0000000 && ((kind & 0xffff)==0x1004 || (kind & 0xffff)==0x000b) && count==2);}
 public static void VerifyConversion(uint page,uint flags,string text,int length,IntPtr fallback,IntPtr used){Verify((page==1251 || page==866 || page==0 || page==1) && flags==0 && text.Length==length && text.EndsWith("\0") && fallback==IntPtr.Zero && used==IntPtr.Zero);}
 public static void Transfer(uint format,IntPtr memory){transferred.Add(format,memory);Formats=new uint[transferred.Count];transferred.Keys.CopyTo(Formats,0);if(format==13){Data=memory;DataSize=new UIntPtr((uint)sizes[memory]);}}
 public static void AfterClose(){if(AutoSynthesis && Writes>0 && Formats.Length>0 && Formats.Length<4){Formats=new uint[]{1,7,13,16};Sequence=unchecked(Sequence+3);}if(ForeignAfterClose){ForeignAfterClose=false;Formats=new uint[]{49321};Sequence=unchecked(Sequence+1);}}
 private static void VerifyPayload(uint format,byte[] expected){byte[] bytes=new byte[sizes[transferred[format]]];Marshal.Copy(transferred[format],bytes,0,bytes.Length);Verify(bytes.Length==expected.Length);for(int i=0;i<bytes.Length;i++)Verify(bytes[i]==expected[i]);}
 private static void VerifyOwnedOnly(){Verify(allocations.Count==transferred.Count);foreach(IntPtr p in transferred.Values)Verify(allocations.Contains(p));}

 public static void CheckLock(){if(!Locked)throw new Exception("outside_lock");}
 public static IntPtr Allocate(int size){IntPtr p=Marshal.AllocHGlobal(size);allocations.Add(p);sizes.Add(p,size);return p;}
 public static void Free(IntPtr p){if(transferred.ContainsValue(p))throw new Exception("free_transferred");if(!allocations.Remove(p))throw new Exception("double_free");Marshal.FreeHGlobal(p);}
 private static void Reset(){foreach(IntPtr p in allocations)Marshal.FreeHGlobal(p);allocations.Clear();sizes.Clear();transferred.Clear();Locked=Busy=EnumFail=EnumDuplicate=SetFail=ClearFail=LocaleFail=ConvertFail=AutoSynthesis=ForeignAfterClose=DestroyDrift=FallbackPages=ConvertOversize=LocaleSizeInvalid=false;Allocs=AllocFailAt=LockFailAt=SetFailAt=CloseFailAt=DestroyFailAt=Destroys=Conversions=ConvertFailAt=Locales=CloseThrowAt=DestroyThrowAt=FreeThrowAt=Frees=0;Writes=Sets=Closes=Opens=Owners=Error=CopiedBytes=Reads=Locks=Copies=EnumIndex=0;Sequence=0;Formats=new uint[0];Data=IntPtr.Zero;DataSize=UIntPtr.Zero;}
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
  VerifyOwnedOnly();
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
   Reset();var s=AMCClipboard.Write("世界 😀\r\nnext",0,new uint[0],DateTime.UtcNow.AddMinutes(1));Verify(s.Text=="世界 😀\r\nnext" && s.Sequence==5 && s.InventoryComplete && !s.Empty && Closes==1 && !Locked && Owners==0);Console.WriteLine("passed:unicode_crlf");
   Reset();s=AMCClipboard.Write("",0,new uint[0],DateTime.UtcNow.AddMinutes(1));Verify(s.Empty && s.Formats.Length==0 && s.Sequence==1);Console.WriteLine("passed:empty");
   Reset();Sequence=1;Failure("text",0,new uint[0],"clipboard_conflict",0);Console.WriteLine("passed:sequence_conflict");
   Reset();Formats=new uint[]{13,49321};Failure("text",0,new uint[]{13},"clipboard_conflict",0);Console.WriteLine("passed:format_conflict");
   Reset();Sequence=2;Formats=new uint[]{13};Failure("same text",1,new uint[]{13},"clipboard_conflict",0);Console.WriteLine("passed:same_text_aba");
   Reset();Busy=true;Failure("text",0,new uint[0],"clipboard_unavailable",0);Console.WriteLine("passed:busy");
   Reset();Busy=true;try{AMCClipboard.Snapshot();throw new Exception("access_allowed");}catch(InvalidOperationException e){Verify(e.Message=="clipboard_unavailable");}Verify(!Locked);Console.WriteLine("passed:access_denied");
   Reset();EnumFail=true;Failure("text",0,new uint[0],"clipboard_enumeration_failed",0);Console.WriteLine("passed:enumeration_error");
   Reset();Failure(new string('x',4097),0,new uint[0],"invalid_clipboard_guard",0);Console.WriteLine("passed:oversized");
   Reset();Failure("\ud800",0,new uint[0],"malformed",0);Verify(Locales==0 && Conversions==0 && Allocs==0 && Opens==0);Console.WriteLine("passed:malformed");
   Reset();SetFail=true;Failure("text",0,new uint[0],"clipboard_possibly_cleared",1);Console.WriteLine("passed:partial_clear");
   Reset();LocaleFail=true;Failure("text",0,new uint[0],"clipboard_encoding_failed",0);Verify(Opens==0 && Allocs==0);Console.WriteLine("passed:locale_failure");
   Reset();LocaleSizeInvalid=true;Failure("text",0,new uint[0],"clipboard_encoding_failed",0);Verify(Opens==0 && Allocs==0);Console.WriteLine("passed:locale_numeric_size");
   Reset();ConvertFail=true;Failure("text",0,new uint[0],"clipboard_encoding_failed",0);Verify(Opens==0 && Allocs==0);Console.WriteLine("passed:conversion_failure");
   Reset();FallbackPages=true;s=AMCClipboard.Write("Кириллица",0,new uint[0],DateTime.UtcNow.AddMinutes(1));Verify(s.Text=="Кириллица" && Sets==4 && Conversions==4);VerifyPayload(1,Encoding.GetEncoding(1251).GetBytes("Кириллица\0"));VerifyPayload(7,Encoding.GetEncoding(866).GetBytes("Кириллица\0"));Console.WriteLine("passed:locale_fallback_codepages");
   Reset();ConvertFailAt=3;Failure("text",0,new uint[0],"clipboard_encoding_failed",0);Verify(Opens==0 && Allocs==0);Console.WriteLine("passed:second_conversion_failure");
   Reset();ConvertOversize=true;Failure("text",0,new uint[0],"clipboard_encoding_failed",0);Verify(Opens==0 && Allocs==0);Console.WriteLine("passed:conversion_bound");

   Reset();AllocFailAt=3;Failure("text",0,new uint[0],"clipboard_allocation_failed",0);Verify(Opens==0);Console.WriteLine("passed:allocation_failure");
   Reset();LockFailAt=4;Failure("text",0,new uint[0],"clipboard_allocation_failed",0);Verify(Opens==0);Console.WriteLine("passed:lock_failure");
   Reset();CloseFailAt=1;Failure("text",0,new uint[0],"clipboard_possibly_cleared",1);Verify(Closes==2 && transferred.Count==4);Console.WriteLine("passed:close_failure");
   Reset();DestroyFailAt=1;Failure("text",0,new uint[0],"clipboard_possibly_cleared",1);Verify(Destroys==2 && Closes==1 && transferred.Count==4);Console.WriteLine("passed:owner_destroy_failure");
   Reset();DestroyDrift=true;Failure("text",0,new uint[0],"clipboard_possibly_cleared",1);Verify(Sequence==6 && Reads==1);Console.WriteLine("passed:postdestroy_drift");

   Reset();SetFailAt=3;Failure("text",0,new uint[0],"clipboard_possibly_cleared",1);Verify(Sets==3 && transferred.Count==2);Console.WriteLine("passed:partial_alias_transfer");
   Reset();SetFailAt=3;CloseThrowAt=1;Failure("text",0,new uint[0],"clipboard_possibly_cleared",1);Verify(transferred.Count==2 && Frees==2 && Owners==0);Console.WriteLine("passed:close_cleanup_exception");
   Reset();SetFailAt=3;DestroyThrowAt=1;Failure("text",0,new uint[0],"clipboard_possibly_cleared",1);Verify(transferred.Count==2 && Frees==2 && !Locked);Console.WriteLine("passed:owner_cleanup_exception");
   Reset();SetFailAt=3;FreeThrowAt=1;Failure("text",0,new uint[0],"clipboard_possibly_cleared",1);Verify(transferred.Count==2 && Frees==2 && Owners==0 && !Locked);Console.WriteLine("passed:free_cleanup_exception");

   Reset();AutoSynthesis=true;s=AMCClipboard.Write("text",0,new uint[0],DateTime.UtcNow.AddMinutes(1));Verify(Sequence==s.Sequence && s.Formats.Length==4 && Allocs==4 && Sets==4);Console.WriteLine("passed:close_synthesis_stable");
   Reset();string legacy="Кириллица";s=AMCClipboard.Write(legacy,0,new uint[0],DateTime.UtcNow.AddMinutes(1));VerifyPayload(1,Encoding.GetEncoding(1251).GetBytes(legacy+"\0"));VerifyPayload(7,Encoding.GetEncoding(866).GetBytes(legacy+"\0"));VerifyPayload(16,BitConverter.GetBytes(0x0419u));Verify(s.Text==legacy && Locales==1);Console.WriteLine("passed:aliases_prepared");
   Reset();ForeignAfterClose=true;Failure("text",0,new uint[0],"clipboard_possibly_cleared",1);Verify(Sequence==6 && Formats[0]==49321 && Reads==1);int reads=Reads;Failure("next",5,new uint[]{1,7,13,16},"clipboard_conflict",1);Verify(Reads==reads && Formats[0]==49321);Console.WriteLine("passed:postclose_foreign_conflict");
   Reset();try{AMCClipboard.Write("text",0,new uint[0],DateTime.UtcNow.AddMinutes(-1));throw new Exception("expiry_missing");}catch(InvalidOperationException e){Verify(e.Message=="expired_request");}Verify(Writes==0 && Opens==1 && Closes==1 && Owners==0);VerifyOwnedOnly();Console.WriteLine("passed:expiry_before_clear");

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
   Reset();Sequence=uint.MaxValue;s=AMCClipboard.Write("text",uint.MaxValue,new uint[0],DateTime.UtcNow.AddMinutes(1));Verify(s.Sequence==4);Console.WriteLine("passed:wrap");
  }finally{Reset();}
 }
}
`
