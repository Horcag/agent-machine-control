package desktop

import (
	"os/exec"
	"testing"
)

func TestGuestWorkerFailuresAndNullableElements(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for isolated worker fixtures")
	}
	fixtures := []struct{ name, script, source string }{
		{"worker result and cleanup", workerResultFixture, "server.ps1"},
		{"nullable UIA properties and password redaction", nullableElementFixture, "actions.ps1"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			data, _ := scripts.ReadFile(fixture.source)
			if output, err := runParserCheck(path, fixture.script, data); err != nil {
				t.Fatalf("isolated behavior fixture: %v %s", err, output)
			}
		})
	}
}

// The production dispatcher runs one iteration against owned synthetic files and
// a fake child object. It never starts a worker or invokes a desktop/guest API.
const workerResultFixture = `
$ErrorActionPreference='Stop'
$source=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
# The sole excluded statement loads production queue/security APIs. Every server
# decision below it executes unchanged with fixture-owned queue/process methods.
$source=$source.Substring($source.IndexOf("` + "`n" + `")+1)
$fixtureRoot=[IO.Path]::Combine([IO.Path]::GetTempPath(),'amc-worker-fault-'+[Guid]::NewGuid().ToString('N'))
if(Test-Path -LiteralPath $fixtureRoot){throw 'foreign_fixture_root'}
[IO.Directory]::CreateDirectory($fixtureRoot)|Out-Null
function Assert-Installed {}
function Assert-ConsoleSession {}
function Remove-ExpiredQueueFiles {}
function Assert-PrivatePath {param($path,$directory)
 if(-not $path.StartsWith($fixtureRoot+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)){throw 'foreign_fixture_path'}
 if((Get-Item -LiteralPath $path).PSIsContainer-ne $directory){throw 'invalid_fixture_kind'}
}
function Assert-Request {param($request) return [DateTimeOffset]::UtcNow.AddSeconds(5)}
function Get-WorkerArguments {param($id,$action)
 if($action-cne 'uia.tree'){throw 'dispatcher_lost_action'}
 return 'synthetic-worker-'+$id
}
function Write-PrivateFile {param($path,$data)
 if(-not $path.StartsWith($fixtureRoot+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)){throw 'foreign_fixture_output'}
 if(-not $path.EndsWith('.json.tmp')){throw 'fallback_not_staged'}
 $file=[IO.File]::Open($path,[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::Read)
 try{
  $half=[int]($data.Length/2);$file.Write($data,0,$half);$file.Flush()
  if(Test-Path -LiteralPath $script:expectedResult){throw 'partial_final_response_visible'}
  $file.Write($data,$half,$data.Length-$half)
 }finally{$file.Dispose()}
 if($script:mode-eq 'publication_conflict'){
  [IO.File]::WriteAllText($script:expectedResult,'{"request_id":"'+$script:id+'","success":true,"text":"synthetic valid result"}')
 }
}
function Start-Sleep {param($Milliseconds) $script:expiry=[DateTimeOffset]::MinValue}
function Start-Process {param($FilePath,$ArgumentList,$WindowStyle,[switch]$PassThru)
 if($FilePath-ne 'synthetic-executable' -or $ArgumentList-ne ('synthetic-worker-'+$script:id)){throw 'wrong_child_identity'}
 $process=[pscustomobject]@{HasExited=($script:mode-ne 'timeout')}
 $process|Add-Member ScriptMethod WaitForExit {
  param($milliseconds)
  if($milliseconds){
   if($milliseconds-lt 1 -or $milliseconds-gt 30000){throw 'unbounded_wait'}
   $script:waits++
   return $script:mode-ne 'timeout'
  }
  $script:reaps++;return $true
 }
 $process|Add-Member ScriptMethod Kill {$script:kills++;$this.HasExited=$true}
 $process|Add-Member ScriptMethod Dispose {$script:disposed++}
 if($script:mode-eq 'valid'){
  [IO.File]::WriteAllText($script:expectedResult,'{"request_id":"'+$script:id+'","success":true,"text":"synthetic valid result"}')
 }
 return $process
}
try {
 foreach($script:mode in @('early_exit','timeout','valid','publication_conflict')){
  $root=Join-Path $fixtureRoot $script:mode
  [IO.Directory]::CreateDirectory((Join-Path $root 'requests'))|Out-Null
  [IO.Directory]::CreateDirectory((Join-Path $root 'results'))|Out-Null
  $script:id='a'*32;$powerShell='synthetic-executable'
  $script:expectedResult=Join-Path (Join-Path $root 'results') ($script:id+'.json')
  $inputPath=Join-Path (Join-Path $root 'requests') ($script:id+'.json')
  [IO.File]::WriteAllText($inputPath,('{"request_id":"'+$script:id+'","action":"uia.tree"}'))
  $script:kills=0;$script:disposed=0;$script:waits=0;$script:reaps=0
  $timer=[Diagnostics.Stopwatch]::StartNew()
  . ([ScriptBlock]::Create($source))
  $timer.Stop()
  if($timer.Elapsed.TotalSeconds-ge 2){throw 'failure_response_not_prompt'}
  if(-not (Test-Path -LiteralPath $script:expectedResult)){throw 'missing_result'}
  $response=[IO.File]::ReadAllText($script:expectedResult)|ConvertFrom-Json
  if($response.request_id-ne $script:id -or $script:disposed-ne 1 -or $script:waits-ne 1){throw 'response_identity_or_cleanup'}
  if(Test-Path -LiteralPath ($inputPath+'.work')){throw 'abandoned_work_file'}
  if($script:mode-in @('valid','publication_conflict')){
   if(-not $response.success -or $response.text-ne 'synthetic valid result' -or $script:kills-ne 0){throw 'valid_result_changed'}
  }else{
   $expected='desktop_failed';if($script:mode-eq 'timeout'){$expected='desktop_timeout'}
   if($response.success -or $response.error-ne $expected){throw 'wrong_failure_category'}
   if($script:mode-eq 'timeout' -and ($script:kills-ne 1 -or $script:reaps-ne 1)){throw 'timeout_child_not_reaped'}
   if($script:mode-eq 'early_exit' -and $script:kills-ne 0){throw 'exited_child_killed'}
  }
 }
}finally{[IO.Directory]::Delete($fixtureRoot,$true)}
`

// Fake managed UIA declarations and objects expose only synthetic properties.
// Get-Elements itself executes unchanged; no native UIA or host window is queried.
const nullableElementFixture = `
$ErrorActionPreference='Stop'
$source=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseInput($source,[ref]$tokens,[ref]$errors)
foreach($name in @('Get-Bounds','Get-Elements')){
 $function=$ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name-eq $name},$false)
 . ([ScriptBlock]::Create($function.Extent.Text))
}
Add-Type -TypeDefinition 'public static class AMCDesktop { public static bool IsPasswordControl(bool password, System.IntPtr handle){return password;} } namespace Windows.Automation { public static class AutomationElement { public static object Root; public static object FromHandle(System.IntPtr handle){return Root;} } public static class TreeWalker { public static object ControlViewWalker; } }'
function New-Element($id,$name,$automationID,$password){
 $element=[pscustomobject]@{ID=$id;Child=$null;Sibling=$null;Current=[pscustomobject]@{Name=$name;AutomationId=$automationID;IsPassword=$password;NativeWindowHandle=0;IsEnabled=$true;IsOffscreen=$false;ControlType=[pscustomobject]@{ProgrammaticName='synthetic.control'};BoundingRectangle=[pscustomobject]@{IsEmpty=$true}}}
 $element|Add-Member ScriptMethod GetRuntimeId {return @($this.ID)}
 $element|Add-Member ScriptMethod GetSupportedPatterns {return @()}
 return $element
}
$first=New-Element 1 $null $null $false
$password=New-Element 2 'synthetic-password-secret' $null $true
$long=New-Element 3 ('n'*300) ('i'*300) $false
$first.Child=$password;$password.Sibling=$long
$walker=[pscustomobject]@{}
$walker|Add-Member ScriptMethod GetFirstChild {param($element) return $element.Child}
$walker|Add-Member ScriptMethod GetNextSibling {param($element) return $element.Sibling}
[Windows.Automation.AutomationElement]::Root=$first
[Windows.Automation.TreeWalker]::ControlViewWalker=$walker
$deadline=[DateTimeOffset]::UtcNow.AddSeconds(5)
$result=@(Get-Elements ([IntPtr]::Zero))
if($result.Count-ne 3){throw 'tree_traversal_failed'}
if($result[0].name-ne '' -or $result[0].automation_id-ne ''){throw 'null_property_not_normalized'}
if($result[1].name-ne '' -or $result[1].automation_id-ne ''){throw 'password_name_leaked'}
if($result[2].name.Length-ne 256 -or $result[2].automation_id.Length-ne 256){throw 'property_bounds_changed'}
# Provider output excludes internal object references, as the production action does.
$public=@($result|ForEach-Object { $_.Remove('reference');$_ })|ConvertTo-Json -Depth 8
if($public.Contains('synthetic-password-secret')){throw 'password_secret_in_public_tree'}
`
