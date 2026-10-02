package desktop

import (
	"os/exec"
	"strings"
	"testing"
)

func TestInstalledTaskPrincipalResolvesToAuthenticatedSID(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for native identity resolution")
	}
	queue, _ := scripts.ReadFile("queue.ps1")
	output, err := runParserCheck(path, installedTaskIdentityFixture, queue)
	if err != nil {
		t.Fatalf("installed task identity fixture: %v %s", err, output)
	}
	for _, name := range []string{"matching SID", "matching account", "normalized account identity", "foreign SID", "foreign account", "unresolvable account", "invalid SID", "empty identity", "null identity", "non-interactive logon", "lower run level", "missing action", "extra action", "foreign executable", "changed arguments"} {
		if !strings.Contains(string(output), name+" verified") {
			t.Fatalf("identity fixture did not execute %s", name)
		}
	}
}

// Only Assert-Installed is loaded. Identity translation uses the real Windows
// security API; task and filesystem security lookups are synthetic. File
// hashing reads only owned synthetic files, always removed in finally.
const installedTaskIdentityFixture = `
$ErrorActionPreference='Stop'
$source=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseInput($source,[ref]$tokens,[ref]$errors)
$function=$ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name-eq 'Assert-Installed'},$false)
. ([ScriptBlock]::Create($function.Extent.Text))
$identity=[Security.Principal.WindowsIdentity]::GetCurrent();$sid=$identity.User.Value
$account=$identity.Name
$normalizedAccount=$account.Substring($account.LastIndexOf('\')+1)
# An unqualified name can resolve a different identity on some Windows hosts.
$normalizedMatches=$false
try{$normalizedMatches=([Security.Principal.NTAccount]::new($normalizedAccount)).Translate([Security.Principal.SecurityIdentifier]).Equals($identity.User)}catch{}
$foreignSID='S-1-5-18';if($sid-eq $foreignSID){$foreignSID='S-1-5-19'}
$foreignAccount=([Security.Principal.SecurityIdentifier]::new($foreignSID)).Translate([Security.Principal.NTAccount]).Value
$root=[IO.Path]::Combine([IO.Path]::GetTempPath(),'amc-task-identity-'+[Guid]::NewGuid().ToString('N'))
if(Test-Path -LiteralPath $root){throw 'foreign_fixture_root'}
[IO.Directory]::CreateDirectory($root)|Out-Null
$taskName='synthetic-task';$powerShell='synthetic-executable';$taskArguments='synthetic-fixed-arguments'
function Assert-PrivatePath {param($path,$directory)
 if($path-ne $root -and -not $path.StartsWith($root+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)){throw 'foreign_fixture_path'}
}
function Get-ScheduledTask {param($TaskName,$TaskPath,$ErrorAction)
 if($TaskName-ne 'synthetic-task' -or $TaskPath-ne '\' -or $ErrorAction-ne 'Stop'){throw 'unexpected_task_lookup'}
 return $script:task
}
try {
 $hashes=@{}
 foreach($name in @('queue.ps1','server.ps1','worker.ps1','actions.ps1','native.cs')){
  $path=Join-Path $root $name
  [IO.File]::WriteAllText($path,'synthetic helper fixture')
  $hashes[$name]=(Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
 }
 [IO.File]::WriteAllText((Join-Path $root 'manifest.json'),(@{version=1;sid=$sid;hashes=$hashes}|ConvertTo-Json -Compress))
 $cases=@(
  @{name='matching SID';user=$sid;accepted=$true},
  @{name='matching account';user=$account;accepted=$true},
  @{name='normalized account identity';user=$normalizedAccount;accepted=$normalizedMatches},
  @{name='foreign SID';user=$foreignSID},
  @{name='foreign account';user=$foreignAccount},
  @{name='unresolvable account';user=('amc-nobody-'+[Guid]::NewGuid().ToString('N'))},
  @{name='invalid SID';user='S-1-invalid'},
  @{name='empty identity';user=''},
  @{name='null identity';user=$null},
  @{name='non-interactive logon';user=$sid;logon='S4U'},
  @{name='lower run level';user=$sid;level='Limited'},
  @{name='missing action';user=$sid;actions=0},
  @{name='extra action';user=$sid;actions=2},
  @{name='foreign executable';user=$sid;execute='foreign-executable'},
  @{name='changed arguments';user=$sid;arguments='foreign-arguments'}
 )
 foreach($case in $cases){
  $principal=[pscustomobject]@{UserId=$case.user;LogonType='Interactive';RunLevel='Highest'}
  if($case.ContainsKey('logon')){$principal.LogonType=$case.logon}
  if($case.ContainsKey('level')){$principal.RunLevel=$case.level}
  $action=[pscustomobject]@{Execute=$powerShell;Arguments=$taskArguments}
  if($case.ContainsKey('execute')){$action.Execute=$case.execute}
  if($case.ContainsKey('arguments')){$action.Arguments=$case.arguments}
  $actions=@($action)
  if($case.ContainsKey('actions')){if($case.actions-eq 0){$actions=@()}else{$actions=@($action,$action)}}
  $script:task=[pscustomobject]@{Principal=$principal;Actions=$actions}
  $failure=$null
  try{Assert-Installed}catch{$failure=$_.Exception.Message}
  if($case.accepted){if($null-ne $failure){throw ('matching_identity_rejected:'+ $case.name+':'+$failure)}}
  elseif($failure-ne 'foreign_task'){throw ('tampered_task_accepted_or_wrong_failure:'+ $case.name)}
  [Console]::Out.WriteLine($case.name+' verified')
 }
}finally{[IO.Directory]::Delete($root,$true);$identity.Dispose()}
`
