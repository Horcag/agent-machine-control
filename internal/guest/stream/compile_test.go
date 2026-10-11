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
 public static class IdentityRegression {
  public static void Check(){
   var a=new System.Collections.Generic.Dictionary<string,object>();var b=new System.Collections.Generic.Dictionary<string,object>();
   string[] keys={"path","volume","fileId","creationTime","owner","sddlHash","size","sha256","rootPath","rootVolume","rootFileId","rootCreationTime","rootOwner","rootSDDLHash"};
   foreach(var key in keys){a[key]="original";b[key]="original";}
   if(!Native.Same(a,b))throw new System.Exception("original rejected");
   foreach(var key in keys){b[key]="changed";if(Native.Same(a,b))throw new System.Exception("changed identity accepted "+key);b[key]="original";}
   b.Remove("owner");if(Native.Same(a,b))throw new System.Exception("incomplete identity accepted");
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
