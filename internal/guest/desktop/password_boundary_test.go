package desktop

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestPasswordBoundaryUsesSharedNativePredicate(t *testing.T) {
	data, _ := scripts.ReadFile("actions.ps1")
	source := string(data)
	if strings.Count(source, "[AMCDesktop]::IsPasswordControl(") != 2 {
		t.Fatal("tree redaction and mutation refusal must both use the native password predicate")
	}
	if strings.Contains(source, "if ($properties.IsPassword)") || strings.Contains(source, "-or $element.Current.IsPassword)") {
		t.Fatal("UIA-only password checks miss legacy password edits")
	}
}

func TestNativePasswordBoundaryWithHiddenOwnedControls(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for native password boundary fixture")
	}
	native, _ := scripts.ReadFile("native.cs")
	actions, _ := scripts.ReadFile("actions.ps1")
	data, err := json.Marshal(map[string]string{"native": string(native), "actions": string(actions)})
	if err != nil {
		t.Fatal(err)
	}
	if output, err := runParserCheck(path, nativePasswordFixture, data); err != nil {
		t.Fatalf("native password boundary fixture: %v %s", err, output)
	}
}

// Hidden fixture-owned controls are never shown or focused. UIA objects are
// synthetic, reproducing a legacy Pane with IsPassword=false and a real HWND.
// Session, window identity and interactive-desktop prerequisites use fixture
// substitutes; production traversal, redaction, selection and mutation refusal
// run unchanged. No real UIA provider or guest installation is queried.
const nativePasswordFixture = `
$ErrorActionPreference='Stop'
$data=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))|ConvertFrom-Json
Add-Type -TypeDefinition $data.native
Add-Type -AssemblyName System.Windows.Forms
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class PasswordFixture {
 [DllImport("user32.dll",CharSet=CharSet.Unicode)] public static extern IntPtr CreateWindowEx(int exStyle,string name,string title,int style,int x,int y,int width,int height,IntPtr parent,IntPtr menu,IntPtr instance,IntPtr parameter);
 [DllImport("user32.dll")] public static extern bool DestroyWindow(IntPtr hwnd);
}
namespace Windows.Automation {
 public static class AutomationElement { public static object Root; public static object FromHandle(IntPtr handle){return Root;} }
 public static class TreeWalker { public static object ControlViewWalker; }
 public static class ValuePattern { public static object Pattern=new object(); }
}
'@
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseInput($data.actions,[ref]$tokens,[ref]$errors)
foreach($name in @('Get-Bounds','Get-Elements','Invoke-DesktopAction','Invoke-ElementPattern')){
 $function=$ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name-eq $name},$false)
 $body=$function.Extent.Text.Replace('[AMCDesktop]::InteractiveDesktop()','$true')
 . ([ScriptBlock]::Create($body))
}
function Assert-ConsoleSession {}
function Get-WindowHandle {param($id,$expected) return $script:regular.Handle}
function New-Element($id,$name,$handle,$password){
 $element=[pscustomobject]@{ID=$id;Child=$null;Sibling=$null;Writes=0;PatternReads=0;Value='';Current=[pscustomobject]@{Name=$name;AutomationId='fixture';NativeWindowHandle=$handle.ToInt32();IsPassword=$password;IsEnabled=$true;IsOffscreen=$false;ControlType=[pscustomobject]@{ProgrammaticName='ControlType.Pane'};BoundingRectangle=[pscustomobject]@{IsEmpty=$true}}}
 $element|Add-Member ScriptMethod GetRuntimeId {return @($this.ID)}
 $element|Add-Member ScriptMethod GetSupportedPatterns {return @()}
 $element|Add-Member ScriptMethod GetCurrentPattern {param($pattern) $this.PatternReads++;return $this}
 $element|Add-Member ScriptMethod SetValue {param($value) $this.Writes++;$this.Value=$value}
 return $element
}
$controls=[Collections.Generic.List[Windows.Forms.Control]]::new()
$nativeHandles=[Collections.Generic.List[IntPtr]]::new()
$identity=[Security.Principal.WindowsIdentity]::GetCurrent()
try{
 $script:regular=[Windows.Forms.TextBox]::new();$controls.Add($regular)
 $regular.Text='synthetic regular text'
 $masked=[Windows.Forms.TextBox]::new();$controls.Add($masked);$masked.PasswordChar='*';$masked.Text='synthetic masked secret'
 $system=[Windows.Forms.TextBox]::new();$controls.Add($system);$system.UseSystemPasswordChar=$true;$system.Text='synthetic system secret'
 $panel=[Windows.Forms.Panel]::new();$controls.Add($panel)
 $edit=[PasswordFixture]::CreateWindowEx(0,'Edit','synthetic native secret',0x20,0,0,100,20,[IntPtr]::Zero,[IntPtr]::Zero,[IntPtr]::Zero,[IntPtr]::Zero)
 if($edit-eq [IntPtr]::Zero){throw 'native_edit_creation_failed'};$nativeHandles.Add($edit)
 $plain=[PasswordFixture]::CreateWindowEx(0,'Edit','synthetic native regular',0,0,0,100,20,[IntPtr]::Zero,[IntPtr]::Zero,[IntPtr]::Zero,[IntPtr]::Zero)
 if($plain-eq [IntPtr]::Zero){throw 'native_regular_creation_failed'};$nativeHandles.Add($plain)
 $button=[PasswordFixture]::CreateWindowEx(0,'Button','synthetic non-edit',0x20,0,0,100,20,[IntPtr]::Zero,[IntPtr]::Zero,[IntPtr]::Zero,[IntPtr]::Zero)
 if($button-eq [IntPtr]::Zero){throw 'native_button_creation_failed'};$nativeHandles.Add($button)
 if(-not [AMCDesktop]::ClassName($masked.Handle).StartsWith('WindowsForms10.EDIT.')){throw 'windows_forms_edit_not_exercised'}
 if([AMCDesktop]::ClassName($edit)-ne 'Edit'){throw 'native_edit_not_exercised'}
 foreach($hwnd in @($masked.Handle,$system.Handle,$edit)){
  if(-not [AMCDesktop]::IsPasswordControl($false,$hwnd)){throw 'legacy_password_not_detected'}
 }
 foreach($hwnd in @([IntPtr]::Zero,$regular.Handle,$panel.Handle,$plain,$button)){
  if([AMCDesktop]::IsPasswordControl($false,$hwnd)){throw 'regular_control_suppressed'}
 }
 $stale=[PasswordFixture]::CreateWindowEx(0,'Edit','synthetic stale',0x20,0,0,100,20,[IntPtr]::Zero,[IntPtr]::Zero,[IntPtr]::Zero,[IntPtr]::Zero)
 if($stale-eq [IntPtr]::Zero){throw 'stale_fixture_creation_failed'};$nativeHandles.Add($stale)
 if(-not [PasswordFixture]::DestroyWindow($stale)){throw 'stale_fixture_cleanup_failed'};$nativeHandles.Remove($stale)|Out-Null
 try{[AMCDesktop]::IsPasswordControl($false,$stale)|Out-Null;throw 'stale_handle_allowed'}catch{if($_.Exception.Message-notmatch 'element_unavailable'){throw}}
 if(-not [AMCDesktop]::IsPasswordControl($true,[IntPtr]::Zero)){throw 'uia_password_flag_lost'}
 $elements=@((New-Element 1 'synthetic regular text' $regular.Handle $false),(New-Element 2 'synthetic masked secret' $masked.Handle $false),(New-Element 3 'synthetic system secret' $system.Handle $false),(New-Element 4 'synthetic native secret' $edit $false),(New-Element 5 'synthetic pane name' $panel.Handle $false),(New-Element 6 'synthetic uia secret' ([IntPtr]::Zero) $true),(New-Element 7 'synthetic virtual name' ([IntPtr]::Zero) $false),(New-Element 8 'synthetic native regular' $plain $false))
 for($i=1;$i-lt $elements.Count;$i++){$elements[$i-1].Child=$elements[$i]}
 $walker=[pscustomobject]@{}
 $walker|Add-Member ScriptMethod GetFirstChild {param($element) return $element.Child}
 $walker|Add-Member ScriptMethod GetNextSibling {param($element) return $element.Sibling}
 [Windows.Automation.AutomationElement]::Root=$elements[0]
 [Windows.Automation.TreeWalker]::ControlViewWalker=$walker
 $deadline=[DateTimeOffset]::UtcNow.AddSeconds(10)
 $tree=Invoke-DesktopAction ([pscustomobject]@{action='uia.tree';window_id='1'})
 if($tree.elements.Count-ne 8){throw 'fixture_cases_missing'}
 foreach($index in @(1,2,3,5)){if($tree.elements[$index].name-ne ''){throw 'password_name_leaked'}}
 foreach($index in @(0,4,6,7)){if($tree.elements[$index].name-ne $elements[$index].Current.Name){throw 'ordinary_name_changed'}}
 $public=$tree|ConvertTo-Json -Depth 8
 if($public.Contains('secret')){throw 'password_in_public_tree'}
 foreach($index in @(1,2,3,5)){
  $request=[pscustomobject]@{action='uia.setvalue';window_id='1';window_identity='synthetic';element_id=([string]($index+1));text='synthetic replacement'}
  try{Invoke-DesktopAction $request|Out-Null;throw 'password_write_allowed'}catch{if($_.Exception.Message-notmatch 'element_unavailable'){throw}}
  if($elements[$index].Writes-ne 0 -or $elements[$index].PatternReads-ne 0){throw 'password_pattern_invoked'}
 }
 foreach($index in @(0,4,6,7)){
  Invoke-DesktopAction ([pscustomobject]@{action='uia.setvalue';window_id='1';window_identity='synthetic';element_id=([string]($index+1));text='synthetic replacement'})|Out-Null
  if($elements[$index].Writes-ne 1 -or $elements[$index].Value-ne 'synthetic replacement'){throw 'ordinary_write_changed'}
 }
}finally{
 $cleanupFailed=$false
 foreach($handle in $nativeHandles){if(-not [PasswordFixture]::DestroyWindow($handle)){$cleanupFailed=$true}}
 foreach($control in $controls){$control.Dispose()}
 $identity.Dispose()
 if($cleanupFailed){throw 'native_fixture_cleanup_failed'}
}
`
