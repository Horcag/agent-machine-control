package desktop

import (
	"os/exec"
	"strings"
	"testing"
)

// This fixture calls only NLS conversion/preparation, never a clipboard/window API.
func TestNativeClipboardEncodingInterop(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for trusted text encoding ABI fixture")
	}
	native, err := scripts.ReadFile("native.cs")
	if err != nil {
		t.Fatal(err)
	}
	source := append(append([]byte{}, native...), []byte(clipboardEncodingCases)...)
	output, err := runParserCheck(path, `Add-Type -TypeDefinition ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))) -ErrorAction Stop;[ClipboardEncodingCases]::Run()`, source)
	if err != nil {
		t.Fatalf("trusted NLS encoding ABI: %v %s", err, output)
	}
	for _, name := range []string{"locale_numeric_abi", "ascii_aliases", "unicode_primary", "utf8_alias", "default_codepages"} {
		if strings.Count(string(output), "passed:"+name+"\r\n") != 1 {
			t.Fatalf("missing encoding ABI case %s: %s", name, output)
		}
		t.Log("encoding ABI case passed:", name)
	}
}

const clipboardEncodingCases = `
public static class ClipboardEncodingCases {
 private static readonly System.Reflection.BindingFlags Hidden=System.Reflection.BindingFlags.NonPublic|System.Reflection.BindingFlags.Static;
 private static byte[][] Payloads(string text){return (byte[][])typeof(AMCClipboard).GetMethod("TextPayloads",Hidden).Invoke(null,new object[]{text,new UnicodeEncoding(false,false,true).GetBytes(text+"\0")});}
 private static byte[] Legacy(string text,uint page){return (byte[])typeof(AMCClipboard).GetMethod("LegacyText",Hidden).Invoke(null,new object[]{text+"\0",page});}
 private static void Equal(byte[] actual,byte[] expected){if(actual.Length!=expected.Length)throw new Exception("encoding_size");for(int i=0;i<actual.Length;i++)if(actual[i]!=expected[i])throw new Exception("encoding_bytes");}
 public static void Run(){
  var native=typeof(AMCClipboard).GetNestedType("Native",System.Reflection.BindingFlags.NonPublic);
  uint locale=(uint)native.GetMethod("GetThreadLocale").Invoke(null,null);
  foreach(uint kind in new uint[]{0x1004,0x000b}){
   object[] args=new object[]{locale,0xa0000000u|kind,0u,2};
   if((int)native.GetMethod("GetLocaleInfo").Invoke(null,args)!=2 || !(args[2] is uint))throw new Exception("locale_numeric_abi");
  }
  Console.WriteLine("passed:locale_numeric_abi");
  byte[] ascii=Encoding.ASCII.GetBytes("ASCII\r\nnext\0");var payloads=Payloads("ASCII\r\nnext");
  if(payloads.Length!=4)throw new Exception("format_count");Equal(payloads[1],ascii);Equal(payloads[2],ascii);Equal(payloads[3],BitConverter.GetBytes(locale));
  Console.WriteLine("passed:ascii_aliases");
  string sample="Кириллица 漢字 e\u0301 😀";payloads=Payloads(sample);Equal(payloads[0],new UnicodeEncoding(false,false,true).GetBytes(sample+"\0"));
  if(payloads[1].Length<1 || payloads[2].Length<1 || payloads[1][payloads[1].Length-1]!=0 || payloads[2][payloads[2].Length-1]!=0)throw new Exception("alias_terminator");
  Console.WriteLine("passed:unicode_primary");
  Equal(Legacy(sample,65001),new UTF8Encoding(false,true).GetBytes(sample+"\0"));Console.WriteLine("passed:utf8_alias");
  Equal(Legacy("ASCII\r\nnext",0),ascii);Equal(Legacy("ASCII\r\nnext",1),ascii);Console.WriteLine("passed:default_codepages");
 }
}
`
