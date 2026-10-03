package desktop

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDesktopMutationRevalidatesPreparedIdentity(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for data-only identity fixture")
	}
	actions, _ := scripts.ReadFile("actions.ps1")
	output, err := runParserCheck(path, identityRevalidationFixture, actions)
	if err != nil {
		t.Fatalf("identity fixture: %v %s", err, output)
	}
	t.Logf("%s", output)
	for _, name := range []string{"valid-virtual", "same-pid-new-birth", "foreign-session", "ancestry-replacement", "disabled-preparation", "password-preparation", "valid-invoke", "valid-select", "valid-toggle", "valid-expand", "valid-collapse", "enumeration-replacement", "pattern-replacement", "readonly-replacement", "foreign-element", "foreign-root", "detached-element", "cyclic-parent", "expired-preparation", "protected-preparation", "focus-replacement", "focus-moved-bounds", "focus-stolen", "valid-wheel", "repeated-scroll-replacement", "valid-repeated-scroll"} {
		if !strings.Contains(string(output), "passed:"+name+"\n") && !strings.Contains(string(output), "passed:"+name+"\r\n") {
			t.Errorf("missing decision %s: %s", name, output)
		}
	}
}

// All process, window, cursor and UIA APIs are synthetic. Production functions
// are AST-extracted; only the process type is substituted to simulate lifetimes.
const identityRevalidationFixture = `
$ErrorActionPreference='Stop';$ProgressPreference='SilentlyContinue'
$source=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
Add-Type -TypeDefinition @'
using System;
public static class AMCDesktop {
 public static uint PID=11; public static bool Interactive=true; public static long Foreground=1; public static int Effects=0; public static string Mode=""; public static int Left=0; public static int ForegroundReads=0;
 public static bool IsWindow(IntPtr h){return true;}
 public static uint GetWindowThreadProcessId(IntPtr h,out uint p){p=PID;return 1;}
 public static bool InteractiveDesktop(){return Interactive;}
 public static bool IsPasswordControl(bool p,IntPtr h){return p;}
 public static bool IsIconic(IntPtr h){return false;}
 public static bool ShowWindowAsync(IntPtr h,int state){return true;}
 public static bool SetForegroundWindow(IntPtr h){if(Mode=="focus-replacement")PID=12;if(Mode=="focus-moved-bounds")Left=1000;return true;}
 public static IntPtr GetForegroundWindow(){ForegroundReads++;return new IntPtr(Mode=="focus-stolen" && ForegroundReads>1 ? 2 : Foreground);}
 public static bool SetCursorPos(int x,int y){Effects++;return true;}
 public static bool Wheel(int delta,bool horizontal){Effects++;return true;}
}
namespace Windows.Automation {
 public static class AutomationElement {public static object Root;public static object FromHandle(IntPtr h){return Root;}}
 public static class TreeWalker {public static object ControlViewWalker;}
 public static class InvokePattern {public static object Pattern=new object();}
 public static class SelectionItemPattern {public static object Pattern=new object();}
 public static class TogglePattern {public static object Pattern=new object();}
 public static class ExpandCollapsePattern {public static object Pattern=new object();}
 public static class ValuePattern {public static object Pattern=new object();}
 public static class ScrollPattern {public static object Pattern=new object();}
 public enum ScrollAmount {NoAmount,SmallIncrement,SmallDecrement}
}
'@
class BindingProcess {
 static [long]$Birth=100
 static [int]$Session=1
 [int]$SessionId=1
 [DateTime]$StartTime
 BindingProcess([int]$id){$this.SessionId=[BindingProcess]::Session;$this.StartTime=[DateTime]::new([BindingProcess]::Birth,[DateTimeKind]::Utc);if($id-ne 11){$this.StartTime=[DateTime]::new(200,[DateTimeKind]::Utc)}}
 static [BindingProcess] GetProcessById([int]$id){return [BindingProcess]::new($id)}
 static [BindingProcess] GetCurrentProcess(){$p=[BindingProcess]::new(11);$p.SessionId=1;return $p}
 [void] Dispose(){}
}
$tokens=$null;$errors=$null;$ast=[Management.Automation.Language.Parser]::ParseInput($source,[ref]$tokens,[ref]$errors)
if($errors.Count){throw 'production_parse_failed'}
foreach($name in @('Get-WindowHandle','Focus-Window','Get-Bounds','Get-Elements','Assert-ElementBinding','Invoke-DesktopAction','Invoke-ElementPattern')){
 $f=$ast.Find({param($n) $n-is [Management.Automation.Language.FunctionDefinitionAst]-and $n.Name-eq $name},$false)
 if($f){. ([ScriptBlock]::Create($f.Extent.Text.Replace('[Diagnostics.Process]','[BindingProcess]')))}
}
function Assert-ConsoleSession {}
function Get-WindowInfo {param($h) return @{bounds=@{left=[AMCDesktop]::Left;top=0;width=100;height=100}}}
function New-Element($id,$parent,$elementPID){
 $e=[pscustomobject]@{ID=$id;Parent=$parent;Child=$null;Sibling=$null;Current=[pscustomobject]@{ProcessId=$elementPID;Name='synthetic';AutomationId='synthetic';NativeWindowHandle=0;IsPassword=$false;IsEnabled=$true;IsOffscreen=$false;ControlType=[pscustomobject]@{ProgrammaticName='synthetic'};BoundingRectangle=[pscustomobject]@{IsEmpty=$true}}}
 $e|Add-Member ScriptMethod GetRuntimeId {return @($this.ID)}
 $e|Add-Member ScriptMethod GetSupportedPatterns {return @()}
 $e|Add-Member ScriptMethod GetCurrentPattern {param($p)
  if($script:mode-eq 'pattern-replacement'){[AMCDesktop]::PID=12}
  if($script:mode-eq 'same-pid-new-birth'){[BindingProcess]::Birth=200}
  if($script:mode-eq 'foreign-session'){[BindingProcess]::Session=2}
  if($script:mode-eq 'disabled-preparation'){$this.Current.IsEnabled=$false}
  if($script:mode-eq 'password-preparation'){$this.Current.IsPassword=$true}
  if($script:mode-eq 'expired-preparation'){$script:deadline=[DateTimeOffset]::UtcNow.AddSeconds(-1)}
  if($script:mode-eq 'protected-preparation'){[AMCDesktop]::Interactive=$false}
  return $script:pattern
 }
 return $e
}
$identity=[Security.Principal.WindowsIdentity]::GetCurrent()
try{
 foreach($case in @(
  @('valid-virtual','uia.setvalue','',1),
  @('same-pid-new-birth','uia.setvalue','stale_window',0),
  @('foreign-session','uia.setvalue','foreign_session',0),
  @('ancestry-replacement','uia.setvalue','stale_window',0),
  @('disabled-preparation','uia.setvalue','element_unavailable',0),
  @('password-preparation','uia.setvalue','element_unavailable',0),
  @('valid-invoke','uia.invoke','',1),
  @('valid-select','uia.select','',1),
  @('valid-toggle','uia.toggle','',1),
  @('valid-expand','uia.expand','',1),
  @('valid-collapse','uia.collapse','',1),
  @('enumeration-replacement','uia.setvalue','stale_window',0),
  @('pattern-replacement','uia.setvalue','stale_window',0),
  @('readonly-replacement','uia.setvalue','stale_window',0),
  @('foreign-element','uia.setvalue','stale_element',0),
  @('foreign-root','uia.setvalue','stale_element',0),
  @('detached-element','uia.setvalue','stale_element',0),
  @('cyclic-parent','uia.setvalue','stale_element',0),
  @('expired-preparation','uia.setvalue','expired_request',0),
  @('protected-preparation','uia.setvalue','protected_desktop',0),
  @('focus-replacement','scroll','stale_window',0),
  @('focus-moved-bounds','scroll','invalid_pointer',0),
  @('focus-stolen','scroll','foreground_denied',0),
  @('valid-wheel','scroll','',2),
  @('repeated-scroll-replacement','uia.scroll','stale_window',1),
  @('valid-repeated-scroll','uia.scroll','',3)
 )){
  $script:mode=$case[0];[BindingProcess]::Birth=100;[BindingProcess]::Session=1;[AMCDesktop]::PID=11;[AMCDesktop]::Interactive=$true;[AMCDesktop]::Foreground=1;[AMCDesktop]::Effects=0;[AMCDesktop]::Mode=$mode;[AMCDesktop]::Left=0;[AMCDesktop]::ForegroundReads=0;$script:deadline=[DateTimeOffset]::UtcNow.AddSeconds(10)
  $root=New-Element 1 $null 11;$child=New-Element 2 $root 11;$root.Child=$child
  if($mode-eq 'foreign-element'){$child.Current.ProcessId=12}
  if($mode-eq 'foreign-root'){$root.Current.ProcessId=12}
  if($mode-eq 'detached-element'){$child.Parent=$null}
  if($mode-eq 'cyclic-parent'){$child.Parent=$child}
  $walker=[pscustomobject]@{}
  $walker|Add-Member ScriptMethod GetFirstChild {param($e)
   if($script:mode-eq 'enumeration-replacement'){[AMCDesktop]::PID=12};return $e.Child
  }
  $walker|Add-Member ScriptMethod GetNextSibling {param($e) return $e.Sibling}
  $walker|Add-Member ScriptMethod GetParent {param($e) if($script:mode-eq 'ancestry-replacement'){[AMCDesktop]::PID=12};return $e.Parent}
  [Windows.Automation.TreeWalker]::ControlViewWalker=$walker;[Windows.Automation.AutomationElement]::Root=$root
  $script:pattern=[pscustomobject]@{}
  $pattern|Add-Member ScriptProperty Current {if($script:mode-eq 'readonly-replacement'){[AMCDesktop]::PID=12};return [pscustomobject]@{IsReadOnly=$false}}
  foreach($method in @('Invoke','Select','Toggle','Expand','Collapse')){$pattern|Add-Member ScriptMethod $method {[AMCDesktop]::Effects++}}
  $pattern|Add-Member ScriptMethod SetValue {param($v) [AMCDesktop]::Effects++}
  $pattern|Add-Member ScriptMethod Scroll {param($x,$y) [AMCDesktop]::Effects++;if($script:mode-eq 'repeated-scroll-replacement'){[AMCDesktop]::PID=12}}
  $request=[pscustomobject]@{action=$case[1];window_id='1';window_identity='1:11:100';element_id='2';text='synthetic';delta=3;x=10;y=10;axis='vertical'}
  $failure='';try{Invoke-DesktopAction $request|Out-Null}catch{$failure=$_.Exception.Message}
  if($failure-cne $case[2] -or [AMCDesktop]::Effects-ne $case[3]){throw ($mode+': failure='+$failure+' effects='+[AMCDesktop]::Effects)}
  [Console]::Out.WriteLine('passed:'+$mode)
 }
}finally{$identity.Dispose()}
`
