package stream

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Execute the production mode decisions and opener with synthetic native query
// results. These checks deliberately do not claim Windows handle acceptance.
func TestStartupPipeModeDecisionsAndRefusals(t *testing.T) {
	definitions := startupPipeDefinitions(t, "function Read-AMCSource(")
	script := `$ErrorActionPreference='Stop';Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class SyntheticBootstrapNative {
 public static long Handle=123;
 public static uint FileType=3;
 public static int Status=0,IOStatus=0,Mode=0,Queries=0;
 public static long Information=4;
 public static IntPtr GetStdHandle(int id){return new IntPtr(Handle);}
 public static uint GetFileType(IntPtr handle){return FileType;}
 public static int NtQueryInformationFile(IntPtr handle,IntPtr iosb,IntPtr mode,uint length,int type){
  if(length!=4||type!=16)throw new Exception("wrong query contract");
  for(int i=0;i<2*IntPtr.Size;i++)if(Marshal.ReadByte(iosb,i)!=0)throw new Exception("uninitialized status");
  if(Marshal.ReadInt32(mode)!=0)throw new Exception("uninitialized mode");
  Queries++;Marshal.WriteInt32(iosb,IOStatus);Marshal.WriteIntPtr(iosb,IntPtr.Size,new IntPtr(Information));Marshal.WriteInt32(mode,Mode);return Status;
 }
}

'@;$Native=[SyntheticBootstrapNative];` + definitions + `
foreach($mode in @(0,1,2,4,8,64)){
 if(!(Get-AMCAsyncMode 0 0 4 $mode)){throw 'async mode rejected'};
};
foreach($mode in @(0x10,0x20,0x11,0x22)){
 if(Get-AMCAsyncMode 0 0 4 $mode){throw 'sync mode accepted as async'};
};
$cases=@(
 @(0,3,0,0,4,0,'211',0),@(-1,3,0,0,4,0,'211',0),
 @(123,1,0,0,4,0,'211',0),@(123,3,-1,0,4,0,'211',1),
 @(123,3,1,0,4,0,'211',1),@(123,3,0x103,0,4,0,'214',1),
 @(123,3,0,-1,4,0,'211',1),@(123,3,0,0x103,4,0,'211',1),
 @(123,3,0,0,3,0,'212',1),@(123,3,0,0,5,0,'212',1),
 @(123,3,0,0,4,0x30,'211',1)
);
foreach($case in $cases){
 [SyntheticBootstrapNative]::Handle=$case[0];[SyntheticBootstrapNative]::FileType=$case[1];
 [SyntheticBootstrapNative]::Status=$case[2];[SyntheticBootstrapNative]::IOStatus=$case[3];
 [SyntheticBootstrapNative]::Information=$case[4];[SyntheticBootstrapNative]::Mode=$case[5];
 [SyntheticBootstrapNative]::Queries=0;
 try{$stream=Open-AMCStandardStream -10 ([IO.FileAccess]::Read);$stream.Dispose();throw 'invalid query returned a stream'}
 catch{if($_.Exception.Message-ne$case[6]){throw}};
 if([SyntheticBootstrapNative]::Queries-ne$case[7]){throw 'invalid handle queried or missing native query'};
};
[Console]::Out.WriteLine('mode-decisions:10 refusals:11');`
	runStartupPowerShell(t, script, "mode-decisions:10 refusals:11")
}

func TestStartupSourceReaderRetainsQueuedFrames(t *testing.T) {
	definitions := startupPipeDefinitions(t, "$i=Open-AMCStandardStream -10 ([IO.FileAccess]::Read);")
	script := `$ErrorActionPreference='Stop';` + definitions + `
$i=[IO.MemoryStream]::new([byte[]]@(0,0,0,3,255,0,128,0,0,0,1,65,0,0,0,1,66));
try{
 $b=Read-AMCSource $i;
 if($b-isnot[byte[]]-or$b.Length-ne3-or$b[0]-ne255-or$b[1]-ne0-or$b[2]-ne128){throw 'source bytes changed'};
 if(!$i.CanRead-or$i.Position-ne7){throw 'source reader closed or consumed queued frames'};
 $remaining=New-Object byte[] 10;
 if($i.Read($remaining,0,10)-ne10-or[BitConverter]::ToString($remaining)-ne'00-00-00-01-41-00-00-00-01-42'){throw 'queued frames lost or replayed'};
 [Console]::Out.WriteLine('retained-stream queued-frames:2');
}finally{$i.Dispose()};`
	runStartupPowerShell(t, script, "retained-stream queued-frames:2")
}

func startupPipeDefinitions(t *testing.T, end string) string {
	t.Helper()
	_, definitions, ok := strings.Cut(StartupCommand, "$Native=$type.CreateType();")
	if !ok {
		t.Fatal("missing native declaration boundary")
	}
	definitions, _, ok = strings.Cut(definitions, end)
	if !ok {
		t.Fatal("missing startup fixture boundary", end)
	}
	return definitions
}

func runStartupPowerShell(t *testing.T, script, expected string) {
	t.Helper()
	path, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("Linux PowerShell unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// #nosec G204 -- fixed production bootstrap definitions and synthetic fixtures, never guest commands.
	command := exec.CommandContext(ctx, path, "-NoProfile", "-NonInteractive", "-Command", script)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), expected) {
		t.Fatalf("bootstrap contract %q: %v %s", expected, err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
}
