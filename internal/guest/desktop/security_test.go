package desktop

import (
	"os/exec"
	"testing"
)

func TestGuestSecurityFixtures(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for owned security fixtures")
	}
	queue, _ := scripts.ReadFile("queue.ps1")
	fixtures := map[string]string{
		"native mandatory labels and exclusive creation": privateSecurityFixture,
		"disconnected and foreign console sessions":      consoleSessionFixture,
	}
	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			if output, err := runParserCheck(path, fixture, queue); err != nil {
				t.Fatalf("security fixture: %v %s", err, output)
			}
		})
	}
}

const privateSecurityFixture = `
$ErrorActionPreference='Stop'
$source=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
# Standard host tokens test ME; elevated guest/Windows tokens test the production HI policy.
$principal=[Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
$level='ME';if($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)){$level='HI'}
$source=$source.Replace('HI',$level)
# Loading queue definitions compiles filesystem-only APIs and performs no UI/guest action.
. ([ScriptBlock]::Create($source))
$root=[IO.Path]::Combine([IO.Path]::GetTempPath(),'amc-security-fault-'+[Guid]::NewGuid().ToString('N'))
if(Test-Path -LiteralPath $root){throw 'foreign_test_root'}
$created=$false
try {
 $descriptor='O:'+$sid+'G:'+$sid+'D:P(A;OICI;FA;;;'+$sid+')(A;OICI;FA;;;SY)S:(ML;OICI;NW;;;'+$level+')'
 [AMCDesktopSecurity]::NewDirectory($root,$descriptor);$created=$true
 Assert-PrivatePath $root $true
 $child=Join-Path $root 'child';New-PrivateDirectory $child
 $path=Join-Path $child 'synthetic.json'
 Write-PrivateFile $path ([Text.Encoding]::UTF8.GetBytes('synthetic payload'))
 foreach($entry in @($root,$child,$path)) {
  $label=[AMCDesktopSecurity]::Label($entry)
  if($label-notmatch ('\(ML;[^;]*;NW;;;'+$level+'\)')){throw 'native label missing'}
  Assert-PrivatePath $entry ($entry-ne $path)
 }
 if([IO.File]::ReadAllText($path)-ne 'synthetic payload'){throw 'file content changed'}
 $failed=$false
 try{Write-PrivateFile $path ([Text.Encoding]::UTF8.GetBytes('overwrite'))}catch{$failed=$true}
 if(-not $failed -or [IO.File]::ReadAllText($path)-ne 'synthetic payload'){throw 'exclusive creation failed'}
 # A protected DACL with no mandatory label must still be rejected.
 $unlabeled=Join-Path $root 'unlabeled'
 $acl=[Security.AccessControl.DirectorySecurity]::new()
 $acl.SetSecurityDescriptorSddlForm('O:'+$sid+'G:'+$sid+'D:P(A;;FA;;;'+$sid+')(A;;FA;;;SY)')
 [IO.Directory]::CreateDirectory($unlabeled,$acl)|Out-Null
 # Explicitly remove inherited MIC from this exact synthetic fixture only.
 $tool=Join-Path $env:SystemRoot 'System32\icacls.exe'
 & $tool $unlabeled '/setintegritylevel' 'L' | Out-Null
 if($LASTEXITCODE-ne 0){throw 'fixture label change failed'}
 $failed=$false
 try{Assert-PrivatePath $unlabeled $true}catch{$failed=$true}
 if(-not $failed){throw 'lower label accepted'}
 [Console]::Out.Write('native '+$level+' labels verified for directories and file')
} finally {
 if($created){[IO.Directory]::Delete($root,$true)}
}
`

const consoleSessionFixture = `
$ErrorActionPreference='Stop'
$source=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseInput($source,[ref]$tokens,[ref]$errors)
$function=$ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name-eq 'Assert-ConsoleSession'},$false)
$guard=$function.Extent.Text.Replace('[Diagnostics.Process]::GetCurrentProcess().SessionId','$script:Session')
. ([ScriptBlock]::Create($guard))
# Only a fake managed console identity is consulted; no host UI API is called.
Add-Type -TypeDefinition 'public static class AMCDesktopSecurity { public static uint Console; public static uint WTSGetActiveConsoleSessionId(){return Console;} }'
$script:Session=2;[AMCDesktopSecurity]::Console=2;Assert-ConsoleSession
foreach($case in @(@(0,0),@(2,3),@(2,[uint32]::MaxValue))) {
 $script:Session=$case[0];[AMCDesktopSecurity]::Console=$case[1];$failed=$false
 try{Assert-ConsoleSession}catch{$failed=$true}
 if(-not $failed){throw 'invalid console accepted'}
}
`
