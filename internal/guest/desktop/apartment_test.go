package desktop

import (
	"os/exec"
	"testing"
)

// Only the extracted queue argument/inventory functions and a freshly owned
// script reporting its apartment run. No desktop, clipboard or UIA API is used.
func TestWorkerApartmentsAndExactLiveInventory(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for isolated apartment fixture")
	}
	queue, _ := scripts.ReadFile("queue.ps1")
	if output, err := runParserCheck(path, workerApartmentFixture, queue); err != nil {
		t.Fatalf("apartment fixture: %v %s", err, output)
	}
}

const workerApartmentFixture = `
$ErrorActionPreference='Stop'
$source=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseInput($source,[ref]$tokens,[ref]$errors)
if($errors.Count){throw 'parser_error'}
foreach($name in @('Get-WorkerArguments','Get-LiveRequestIDs')){
 $function=$ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name-eq $name},$false)
 . ([ScriptBlock]::Create($function.Extent.Text))
}
$root=Join-Path ([IO.Path]::GetTempPath()) ('amc-apartment-'+[Guid]::NewGuid().ToString('N'))
if(Test-Path -LiteralPath $root){throw 'foreign_root'}
[IO.Directory]::CreateDirectory($root)|Out-Null
$powerShell=Join-Path $PSHOME 'powershell.exe';$sid='S-1-5-21-synthetic'
$child=$null
function Require($ok,$message){if(-not $ok){throw $message}}
function Get-CimInstance {return $script:Processes}
function Invoke-CimMethod {param($InputObject) return @{ReturnValue=0;Sid=$InputObject.OwnerSID}}
try{
 $id='a'*32;$other='b'*32
 $uia=@('uia.tree','uia.invoke','uia.setvalue','uia.select','uia.toggle','uia.expand','uia.collapse','uia.scroll')
 $sta=@('status','cursor','windows','clipboard.get','clipboard.snapshot','clipboard.set','launch','scroll','window.focus','window.move','window.resize','window.close','window.minimize','window.maximize','window.restore')
 foreach($action in $uia){Require ((Get-WorkerArguments $id $action).Contains(' -MTA ')) ('wrong UIA apartment '+$action)}
 foreach($action in $sta){Require ((Get-WorkerArguments $id $action).Contains(' -STA ')) ('wrong STA apartment '+$action)}
 foreach($action in @('uia.unknown','UIA.tree','uia.tree -STA','')){
  $failed=$false;try{Get-WorkerArguments $id $action|Out-Null}catch{$failed=$true}
  Require $failed 'unknown action accepted'
 }
 foreach($invalidID in @(($id+' -MTA'),($id+[char]10),($id+[char]13+[char]10),('A'*32),('a'*31),('a'*33))){
  $failed=$false;try{Get-WorkerArguments $invalidID 'status'|Out-Null}catch{$failed=$true}
  Require $failed 'invalid request ID accepted'
 }
 $script:Processes=@(
  @{ExecutablePath=$powerShell;CommandLine=('"'+$powerShell+'" '+(Get-WorkerArguments $id 'clipboard.get'));OwnerSID=$sid},
  @{ExecutablePath=$powerShell;CommandLine=('"'+$powerShell+'" '+(Get-WorkerArguments $other 'uia.tree'));OwnerSID=$sid})
 $live=Get-LiveRequestIDs
 Require ($live.Count-eq 2 -and $live.ContainsKey($id) -and $live.ContainsKey($other)) 'one apartment lost live ownership'
 foreach($suffix in @(' -STA',' -MTA',' -NoExit')){
  $script:Processes=@(@{ExecutablePath=$powerShell;CommandLine=((Get-WorkerArguments $id 'uia.tree')+$suffix);OwnerSID=$sid})
  Require ((Get-LiveRequestIDs).Count-eq 0) 'noncanonical suffix accepted'
 }
 $script:Processes=@(@{ExecutablePath=$powerShell;CommandLine=(Get-WorkerArguments $id 'uia.tree').Replace('-MTA','-mta');OwnerSID=$sid})
 Require ((Get-LiveRequestIDs).Count-eq 0) 'noncanonical apartment accepted'
 foreach($action in @('status','uia.tree')){
  $script:Processes=@(@{ExecutablePath='C:\foreign\powershell.exe';CommandLine=(Get-WorkerArguments $id $action);OwnerSID=$sid})
  $failed=$false;try{Get-LiveRequestIDs|Out-Null}catch{$failed=$true};Require $failed 'foreign executable accepted'
  $script:Processes[0].ExecutablePath=$powerShell;$script:Processes[0].OwnerSID='foreign'
  $failed=$false;try{Get-LiveRequestIDs|Out-Null}catch{$failed=$true};Require $failed 'foreign owner accepted'
 }
 # Execute the actual generated fixed arguments with an owned data-only worker.
 $worker=Join-Path $root 'worker.ps1'
 [IO.File]::WriteAllText($worker,'param([string]$RequestID);[Console]::Out.Write(([Threading.Thread]::CurrentThread.GetApartmentState().ToString()+":"+$RequestID))')
 foreach($case in @(@{Action='clipboard.get';Apartment='STA'},@{Action='clipboard.snapshot';Apartment='STA'},@{Action='uia.tree';Apartment='MTA'})){
  $start=[Diagnostics.ProcessStartInfo]::new($powerShell,(Get-WorkerArguments $id $case.Action))
  $start.UseShellExecute=$false;$start.CreateNoWindow=$true;$start.RedirectStandardOutput=$true;$start.RedirectStandardError=$true
  $start.EnvironmentVariables['PSModulePath']=Join-Path $PSHOME 'Modules'
  $child=[Diagnostics.Process]::Start($start)
  if(-not $child.WaitForExit(10000)){throw 'bounded_apartment_child_timeout'}
  Require ($child.ExitCode-eq 0) 'apartment child failed'
  Require ($child.StandardOutput.ReadToEnd()-ceq ($case.Apartment+':'+$id)) 'actual child apartment mismatch'
  $child.Dispose();$child=$null
 }
}finally{
 if($child){if(-not $child.HasExited){$child.Kill();$child.WaitForExit()};$child.Dispose()}
 [IO.Directory]::Delete($root,$true)
}
`
