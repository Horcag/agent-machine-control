package desktop

import (
	"encoding/json"
	"os/exec"
	"testing"
)

func TestTransportStopsBeforeNextExpiredMutation(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable")
	}
	source, _ := scripts.ReadFile("transport.ps1")
	queue, _ := scripts.ReadFile("queue.ps1")
	data, err := json.Marshal(map[string]string{"transport": string(source), "queue": string(queue)})
	if err != nil {
		t.Fatal(err)
	}
	fixture := transportExpiryFixture + transportExpiryRun
	if output, err := runParserCheck(path, fixture, data); err != nil {
		t.Fatalf("expiry fixtures: %v %s", err, output)
	}
}

// Only authentication is replaced with a synthetic principal. All transport
// decisions and mutation guards execute unchanged; every external operation is
// a recording stub. Advancing the deadline avoids wall-clock timing races.
const transportExpiryFixture = `
$ErrorActionPreference='Stop'
$fixture=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))|ConvertFrom-Json
$source=$fixture.transport
$source=$source.Replace('$principal = [Security.Principal.WindowsPrincipal]::new($identity)', '$principal = [pscustomobject]@{}; $principal | Add-Member ScriptMethod IsInRole {return $true}')
function Assert-Request {param($request) return [DateTimeOffset]::UtcNow.AddSeconds(30)}
function Expire { $script:deadline=[DateTimeOffset]::MinValue }
function Record {param($operation)
 $script:operations.Add($operation)
 if($script:operations.Count-eq $script:expireAfter){Expire}
}
function Assert-Installed {if($script:expireAfter-eq 0){Expire}}
function Test-Path {param($LiteralPath) return $false}
function Get-ScheduledTask {param($TaskName,$TaskPath,$ErrorAction)
 if($script:mode-eq 'provision'){if($script:expireAfter-eq 0){Expire};return}
 return [pscustomobject]@{State='Ready'}
}
function New-PrivateDirectory {param($path) Record 'directory'}
function Write-PrivateFile {param($path,$data) Record 'write'}
function New-ScheduledTaskAction {param($Execute,$Argument) return 'synthetic'}
function New-ScheduledTaskPrincipal {param($UserId,$LogonType,$RunLevel) return 'synthetic'}
function New-ScheduledTaskSettingsSet {param($MultipleInstances,$ExecutionTimeLimit,[switch]$AllowStartIfOnBatteries,[switch]$DontStopIfGoingOnBatteries) return 'synthetic'}
function New-ScheduledTaskTrigger {param($AtLogOn,$User) return 'synthetic'}
function Register-ScheduledTask {param($TaskName,$TaskPath,$Action,$Principal,$Settings,$Trigger) Record 'register'}
function Stop-ScheduledTask {param($TaskName,$TaskPath) Record 'stop'}
function Unregister-ScheduledTask {param($TaskName,$TaskPath,$Confirm) Record 'unregister'}
function Remove-Item {param($LiteralPath,[switch]$Recurse,[switch]$Force) Record 'remove'}
function Remove-ExpiredQueueFiles {Record 'prune'}
function Start-ScheduledTask {param($TaskName,$TaskPath) Record 'start'}
function Get-ChildItem {param($LiteralPath,[switch]$Force)}
function Write-Response {param($response) $script:response=$response}
`

const transportExpiryRun = `
$queueSource=$fixture.queue
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseInput($queueSource,[ref]$tokens,[ref]$errors)
$guard=$ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name-eq 'Assert-Deadline'},$false)
. ([ScriptBlock]::Create($guard.Extent.Text))
$root='C:\amc-synthetic';$sid='synthetic';$taskName='amc-synthetic';$powerShell='synthetic';$taskArguments='synthetic'
foreach($case in @(
 @{mode='provision';counts=0..9},
 @{mode='remove';counts=0..2},
 @{mode='execute';counts=0..3}
)) {
 foreach($script:expireAfter in $case.counts){
  $script:mode=$case.mode
  $script:operations=[Collections.Generic.List[string]]::new();$script:response=$null
  $files=@{};foreach($name in @('queue.ps1','server.ps1','worker.ps1','actions.ps1','native.cs')){$files[$name]='YQ=='}
  $request=[pscustomobject]@{mode=$script:mode;request_id=('a'*32);files=[pscustomobject]$files;hashes=@{};action='status'}
  . ([ScriptBlock]::Create($source))
  if($script:operations.Count-ne $script:expireAfter){throw ('mutation_after_expiry:'+ $script:mode+':'+$script:expireAfter+':'+($script:operations -join ','))}
  if($null-eq $script:response -or $script:response.success -or $script:response.error-ne 'desktop_unavailable'){throw 'missing_private_failure_receipt'}
 }
}
`

func TestWorkerChecksExpiryAfterAssemblyPreludeAndPublishesFailure(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable")
	}
	worker, _ := scripts.ReadFile("worker.ps1")
	queue, _ := scripts.ReadFile("queue.ps1")
	data, err := json.Marshal(map[string]string{"worker": string(worker), "queue": string(queue)})
	if err != nil {
		t.Fatal(err)
	}
	if output, err := runParserCheck(path, workerExpiryFixture, data); err != nil {
		t.Fatalf("worker expiry fixture: %v %s", err, output)
	}
}

// Fixed source substitutions remove the guest security/module boundary only.
// Assembly and action stubs do not load native code or inspect the desktop.
// The actual post-prelude guard and receipt publication execute unchanged.
const workerExpiryFixture = `
$ErrorActionPreference='Stop'
$fixture=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))|ConvertFrom-Json
# Use literal strings so fixture scope cannot interpolate the production paths.
$source=$fixture.worker.Replace('. (Join-Path $PSScriptRoot ''queue.ps1'')','')
$source=$source.Replace('. (Join-Path $root ''actions.ps1'')','if($script:expired){$deadline=[DateTimeOffset]::MinValue}')
$source=$source.Replace(' -or [Diagnostics.Process]::GetCurrentProcess().SessionId -eq 0','')
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseInput($fixture.queue,[ref]$tokens,[ref]$errors)
$guard=$ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name-eq 'Assert-Deadline'},$false)
. ([ScriptBlock]::Create($guard.Extent.Text))
function Assert-Installed {}
function Assert-PrivatePath {}
function Assert-ConsoleSession {}
function Assert-Request {param($request) return [DateTimeOffset]::UtcNow.AddSeconds(30)}
function Add-Type {param($Path,$AssemblyName) $script:loads++}
function Invoke-DesktopAction {param($request) $script:actions++;return @{text='synthetic'}}
$root=[IO.Path]::Combine([IO.Path]::GetTempPath(),'amc-expiry-'+[Guid]::NewGuid().ToString('N'))
if(Test-Path -LiteralPath $root){throw 'foreign_fixture_root'}
[IO.Directory]::CreateDirectory((Join-Path $root 'requests'))|Out-Null
[IO.Directory]::CreateDirectory((Join-Path $root 'results'))|Out-Null
function Write-PrivateFile {param($path,$data)
 if(-not $path.StartsWith($root+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)){throw 'foreign_fixture_write'}
 [IO.File]::WriteAllBytes($path,$data)
}
try {
 foreach($script:expired in @($false,$true)){
  $id='a'*32;if($script:expired){$id='b'*32}
  [IO.File]::WriteAllText((Join-Path (Join-Path $root 'requests') ($id+'.json.work')),('{"request_id":"'+$id+'"}'))
  $script:loads=0;$script:actions=0
  & ([ScriptBlock]::Create($source)) -RequestID $id
  $result=Join-Path (Join-Path $root 'results') ($id+'.json')
  $response=[IO.File]::ReadAllText($result)|ConvertFrom-Json
  if($script:loads-ne 4 -or $response.request_id-ne $id -or (Test-Path -LiteralPath ($result+'.tmp'))){throw 'prelude_or_receipt_cleanup'}
  if($script:expired){
   if($script:actions-ne 0 -or $response.success -or $response.error-ne 'desktop_failed'){throw 'expired_desktop_action_executed'}
  }elseif($script:actions-ne 1 -or -not $response.success){throw 'valid_action_rejected'}
 }
}finally{[IO.Directory]::Delete($root,$true)}
`
