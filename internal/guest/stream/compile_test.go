package stream

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Linux compilation checks C# and PowerShell API usage without executing a
// Windows worker or any native methods. The .NET Framework JSON API is shimmed
// only in this compiler fixture, never in the production source.
func TestWorkerSourceCompilesAndBootstrapParses(t *testing.T) {
	path, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("Linux PowerShell compiler unavailable")
	}
	source := sourceBytes()
	source = append(source, []byte(`
namespace System.Web.Script.Serialization {
 public sealed class JavaScriptSerializer {
  public int MaxJsonLength {get;set;}
  public string Serialize(object value){throw new System.NotSupportedException();}
  public T Deserialize<T>(string value){throw new System.NotSupportedException();}
 }
}
namespace AMCStream {
 public sealed class ThrowingControlStream : System.IO.MemoryStream {
  public override int Read(byte[] bytes,int offset,int count){throw new System.IO.IOException("private failure text");}
 }
 public static class IdentityRegression {
  static void ControlRefusal(System.IO.Stream input,string stage,System.Func<string,System.Collections.Generic.Dictionary<string,object>> decode){
   try{Worker.ReadControl(input,decode);throw new System.Exception("control failure accepted");}
   catch(Worker.ControlFailure failure){if(failure.Stage!=stage)throw new System.Exception("wrong control stage");}
  }

  public static void Check(){
   System.Func<string,System.Collections.Generic.Dictionary<string,object>> decoded=delegate(string text){return new System.Collections.Generic.Dictionary<string,object>{{"id",text}};};
   ControlRefusal(new System.IO.MemoryStream(),"control_eof",decoded);
   ControlRefusal(new ThrowingControlStream(),"control_read",decoded);
   ControlRefusal(new System.IO.MemoryStream(new byte[]{0,0,0,0}),"control_header",decoded);
   ControlRefusal(new System.IO.MemoryStream(new byte[]{0,4,0,1}),"control_header",decoded);
   ControlRefusal(new System.IO.MemoryStream(new byte[]{0,0,0,2,1}),"control_eof",decoded);
   ControlRefusal(new System.IO.MemoryStream(new byte[]{0,0,0,1,255}),"control_decode",decoded);
   ControlRefusal(new System.IO.MemoryStream(new byte[]{0,0,0,1,65}),"control_decode",delegate(string text){throw new System.Exception("private JSON parser text");});
   if((string)Worker.ReadControl(new System.IO.MemoryStream(new byte[]{0,0,0,1,65}),decoded)["id"]!="A")throw new System.Exception("valid control changed");
   var failure=Failure.Metadata(new System.Exception(@"C:\private\secret SDDL token"),"observe");
   if(failure.Count!=2||(string)failure["stage"]!="observe"||(uint)failure["nativeError"]!=0)throw new System.Exception("exception text retained");
   failure=Failure.Metadata(new NativeFailure("security_enable",1300),"observe");
   if(failure.Count!=2||(string)failure["stage"]!="security_enable"||(uint)failure["nativeError"]!=1300)throw new System.Exception("native diagnostic lost");
   var a=new System.Collections.Generic.Dictionary<string,object>();var b=new System.Collections.Generic.Dictionary<string,object>();
   string[] keys={"path","volume","fileId","creationTime","owner","sddlHash","size","sha256","rootPath","rootVolume","rootFileId","rootCreationTime","rootOwner","rootSDDLHash"};
   foreach(var key in keys){a[key]="original";b[key]="original";}
   if(!Native.Same(a,b))throw new System.Exception("original rejected");
   foreach(var key in keys){b[key]="changed";if(Native.Same(a,b))throw new System.Exception("changed identity accepted "+key);b[key]="original";}
   b.Remove("owner");if(Native.Same(a,b))throw new System.Exception("incomplete identity accepted");

   if(Lifetime.CheckedPipeState(0,40,3)!=3)throw new System.Exception("connected pipe rejected");
   foreach(var item in new[]{new object[]{-1,40UL,211u},new object[]{0,39UL,212u},new object[]{0x103,40UL,214u}}){
    try{Lifetime.CheckedPipeState((int)item[0],(ulong)item[1],3);throw new System.Exception("invalid query accepted");}
    catch(Lifetime.PipeFailure pipeFailure){if(pipeFailure.ExitCode!=(uint)item[2])throw new System.Exception("wrong query exit");}
   }
   int kills=0,closes=0;
   var termination=new Lifetime.OwnTermination(delegate(uint code){if(code!=213u)throw new System.Exception("wrong kill marker");kills++;return false;},delegate{closes++;});
   termination.Stop(213);termination.Stop(211);
   if(kills!=1||closes!=1)throw new System.Exception("failed kill did not close exactly once");
   kills=0;closes=0;
   termination=new Lifetime.OwnTermination(delegate(uint code){kills++;throw new System.Exception("native invocation failed");},delegate{closes++;});
   termination.Stop(211);termination.Stop(212);
   if(kills!=1||closes!=1)throw new System.Exception("throwing kill leaked owned job");
   var now=new System.DateTime(2026,1,1,0,0,0,System.DateTimeKind.Utc);
   if(Lifetime.FailureBudget(now.AddSeconds(30),now)!=System.TimeSpan.FromSeconds(1))throw new System.Exception("failure bound exceeded one second");
   if(Lifetime.FailureBudget(now.AddMilliseconds(20),now)!=System.TimeSpan.FromMilliseconds(20))throw new System.Exception("request deadline extended");
   if(Lifetime.FailureBudget(now.AddTicks(-1),now)!=System.TimeSpan.Zero)throw new System.Exception("expired request extended");
   // Output backpressure and terminal input cannot cancel the prearmed bound.
   using(var released=new System.Threading.ManualResetEvent(false)){
    int closures=0;
    using(var timer=Lifetime.ArmFailureBound(delegate(){System.Threading.Interlocked.Increment(ref closures);released.Set();},System.DateTime.UtcNow.AddMilliseconds(100))){
     if(!released.WaitOne(5000)||closures!=1)throw new System.Exception("blocked failure response survived its bound");
    }
   }
   using(var terminal=new System.IO.MemoryStream(System.Text.Encoding.UTF8.GetBytes("another business request"))){
    Lifetime.DrainTerminal(terminal);
    if(terminal.Position!=terminal.Length)throw new System.Exception("terminal input was reused");
   }
   using(var stopped=new System.Threading.ManualResetEvent(false)){
    int observations=0,disconnects=0;
    // A disconnected pipe with unread payload must still terminate the worker.
    Lifetime.Observe(stopped,delegate(){observations++;return 4u;},delegate(){disconnects++;});
    if(observations!=1||disconnects!=1)throw new System.Exception("EOF observer missed unread backlog");
    stopped.Set();Lifetime.Observe(stopped,delegate(){throw new System.Exception("observer survived stop");},delegate(){throw new System.Exception("stop closed job twice");});
   }
  }
 }
}`)...)
	script := `$ErrorActionPreference='Stop';$source=[Console]::In.ReadToEnd();Add-Type -TypeDefinition $source -IgnoreWarnings -WarningAction SilentlyContinue;[AMCStream.IdentityRegression]::Check();Write-Output 'compiled'`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, "-NoProfile", "-NonInteractive", "-Command", script)
	command.Stdin = bytes.NewReader(source)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "compiled") {
		t.Fatalf("C# declarations: %v %s", err, output)
	}
	if len(StartupCommand) > 8192 || len(source) > MaxControl {
		t.Fatal("startup/source bounds exceeded")
	}
	bootstrap := strings.TrimSuffix(strings.SplitN(StartupCommand, `-Command "`, 2)[1], `"`)
	script = `$tokens=$null;$errors=$null;[Management.Automation.Language.Parser]::ParseInput([Console]::In.ReadToEnd(),[ref]$tokens,[ref]$errors)|Out-Null;if($errors.Count){$errors|ForEach-Object{$_.Message};exit 1}`
	command = exec.CommandContext(ctx, path, "-NoProfile", "-NonInteractive", "-Command", script)
	command.Stdin = strings.NewReader(bootstrap)
	if output, err = command.CombinedOutput(); err != nil {
		t.Fatalf("bootstrap parse: %v %s", err, output)
	}
}
