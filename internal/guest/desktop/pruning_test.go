package desktop

import (
	"os/exec"
	"testing"
)

// The fixture runs queue functions on newly owned temporary files. CIM process
// inventory and ACL decisions are mocked; no host UI, process or guest is touched.
func TestQueuePruningRecoversInterruptedWritersAndPreservesLiveWork(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for synthetic queue fault fixture")
	}
	queue, _ := scripts.ReadFile("queue.ps1")
	if output, err := runParserCheck(path, queueFaultFixture, queue); err != nil {
		t.Fatalf("queue fault fixture: %v %s", err, output)
	}
}

const queueFaultFixture = `
$ErrorActionPreference='Stop'
$source=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseInput($source,[ref]$tokens,[ref]$errors)
if($errors.Count){throw 'parser_error'}
$functions=$ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -in @('Get-WorkerArguments','Get-LiveRequestIDs','Remove-ExpiredQueueFiles')},$false)
foreach($function in $functions){. ([ScriptBlock]::Create($function.Extent.Text))}
$root=[IO.Path]::Combine([IO.Path]::GetTempPath(),'amc-queue-fault-'+[Guid]::NewGuid().ToString('N'))
if(Test-Path -LiteralPath $root){throw 'foreign_test_root'}
[IO.Directory]::CreateDirectory($root)|Out-Null
$powerShell='C:\synthetic\powershell.exe';$sid='S-1-5-21-synthetic';$script:Processes=@();$script:DeniedPaths=@();$script:InventoryFailure=$false
function Get-CimInstance { if($script:InventoryFailure){throw 'inventory_failed'};return $script:Processes }
function Invoke-CimMethod {param($InputObject) return @{ReturnValue=0;Sid=$InputObject.OwnerSID}}
function Assert-PrivatePath {param($path,$directory)
 if($script:DeniedPaths -contains $path){throw 'unsafe_fixture_acl'}
 $item=Get-Item -LiteralPath $path -Force
 if(($item.Attributes -band [IO.FileAttributes]::ReparsePoint)-ne 0 -or $item.PSIsContainer-ne $directory){throw 'unsafe_fixture_path'}
 if($path-ne $root -and -not $path.StartsWith($root+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)){throw 'foreign_fixture_path'}
}
function New-QueueFile($kind,$id,$suffix,$stale){
 $path=Join-Path (Join-Path $root $kind) ($id+$suffix)
 [IO.File]::WriteAllText($path,'synthetic queue data')
 if($stale){[IO.File]::SetLastWriteTimeUtc($path,[DateTime]::UtcNow.AddMinutes(-3))}
 return $path
}
function Require-File($path,$exists){if((Test-Path -LiteralPath $path)-ne $exists){throw ('wrong retention '+[IO.Path]::GetFileName($path))}}
$held=$null
try {
 foreach($kind in @('requests','results')){[IO.Directory]::CreateDirectory((Join-Path $root $kind))|Out-Null}
 $expired=@(
  (New-QueueFile 'requests' ('a'*32) '.json.tmp' $true),
  (New-QueueFile 'results' ('b'*32) '.json.tmp' $true),
  (New-QueueFile 'requests' ('c'*32) '.json.work' $true),
  (New-QueueFile 'requests' ('d'*32) '.json' $true))
 $activeID='e'*32
 $active=@((New-QueueFile 'requests' $activeID '.json.work' $true),(New-QueueFile 'results' $activeID '.json.tmp' $true))
 $fresh=New-QueueFile 'requests' ('f'*32) '.json.tmp' $false
 $insecureFile=New-QueueFile 'requests' ('0'*32) '.json.tmp' $true;$script:DeniedPaths=@($insecureFile)
 $busy=New-QueueFile 'requests' ('1'*32) '.json.tmp' $true
 $held=[IO.File]::Open($busy,[IO.FileMode]::Open,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None)
 $foreign=New-QueueFile 'requests' 'foreign-owner' '.json.tmp' $true
 $upper=New-QueueFile 'requests' ('A'*32) '.json.work' $true
 $invalidSuffix=New-QueueFile 'results' ('2'*32) '.json.work' $true
 $script:Processes=@(@{ExecutablePath=$powerShell;CommandLine=('"'+$powerShell+'" '+(Get-WorkerArguments $activeID));OwnerSID=$sid})
 Remove-ExpiredQueueFiles
 foreach($path in $expired){Require-File $path $false}
 foreach($path in @($active)+@($fresh,$insecureFile,$busy,$foreign,$upper,$invalidSuffix)){Require-File $path $true}
 $held.Dispose();$held=$null
 Remove-ExpiredQueueFiles
 Require-File $busy $false
 # Losing process ownership proof fails closed, preserving even expired files.
 $guarded=New-QueueFile 'requests' ('3'*32) '.json.tmp' $true
 $script:Processes[0].OwnerSID='S-1-5-21-foreign';$failed=$false
 try{Remove-ExpiredQueueFiles}catch{$failed=$true}
 if(-not $failed){throw 'foreign worker accepted'};Require-File $guarded $true
 $script:Processes=@();$script:InventoryFailure=$true;$failed=$false
 try{Remove-ExpiredQueueFiles}catch{$failed=$true}
 if(-not $failed){throw 'unknown inventory accepted'};Require-File $guarded $true
 $script:InventoryFailure=$false;$script:DeniedPaths=@($root);$failed=$false
 try{Remove-ExpiredQueueFiles}catch{$failed=$true}
 if(-not $failed){throw 'unsafe root accepted'};Require-File $guarded $true
 $script:DeniedPaths=@($insecureFile)
 # The orphan becomes collectible once its exact worker no longer exists.
 Remove-ExpiredQueueFiles
 foreach($path in $active){Require-File $path $false}
 Require-File $guarded $false
 # One cleanup pass scans at most 256 entries per directory.
 $script:DeniedPaths=@()
 foreach($entry in @(Get-ChildItem -LiteralPath (Join-Path $root 'requests'))){Remove-Item -LiteralPath $entry.FullName -Force}
 for($index=0;$index-lt 257;$index++){New-QueueFile 'requests' ($index.ToString('x32')) '.json.tmp' $true|Out-Null}
 Remove-ExpiredQueueFiles
 $remaining=@(Get-ChildItem -LiteralPath (Join-Path $root 'requests')).Count
 if($remaining-lt 1){throw 'unbounded cleanup pass'}
 Remove-ExpiredQueueFiles
 if(@(Get-ChildItem -LiteralPath (Join-Path $root 'requests')).Count-ne 0){throw 'cleanup did not recover capacity'}
} finally {
 if($held){$held.Dispose()}
 # Only this freshly created, exact GUID-named synthetic resource is removed.
 [IO.Directory]::Delete($root,$true)
}
`

func TestWindowObservationIncludesTheSharedCursor(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for pure action fixture")
	}
	actions, _ := scripts.ReadFile("actions.ps1")
	if output, err := runParserCheck(path, cursorObservationFixture, actions); err != nil {
		t.Fatalf("cursor observation fixture: %v %s", err, output)
	}
}

const cursorObservationFixture = `
$ErrorActionPreference='Stop'
$source=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseInput($source,[ref]$tokens,[ref]$errors)
$function=$ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Invoke-DesktopAction'},$false)
. ([ScriptBlock]::Create($function.Extent.Text))
# Fake managed methods only: no native declarations, host GUI or real inventory.
Add-Type -TypeDefinition 'using System; public static class AMCDesktop { public static bool InteractiveDesktop(){return true;} public static IntPtr[] Windows(){return new IntPtr[]{new IntPtr(123)};} }'
$identity=[Security.Principal.WindowsIdentity]::GetCurrent();$script:cursorCalls=0
function Assert-ConsoleSession {}
function Get-GuestCursor {$script:cursorCalls++;return @{x=21;y=31;visible=$true}}
function Get-WindowInfo {param($hwnd) return @{id='123';title='synthetic window'}}
$windows=Invoke-DesktopAction @{action='windows'}
$cursor=Invoke-DesktopAction @{action='cursor'}
if($windows.windows.Count-ne 1 -or $windows.cursor.x-ne 21 -or $windows.cursor.y-ne 31 -or -not $windows.cursor.visible){throw 'window cursor missing'}
if($cursor.cursor.x-ne 21 -or $script:cursorCalls-ne 2){throw 'cursor observation drift'}
`
