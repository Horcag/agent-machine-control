package desktop

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestClipboardSnapshotHelperRouteAndLegacyRefusal(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for data-only snapshot action fixture")
	}
	queue, _ := scripts.ReadFile("queue.ps1")
	actions, _ := scripts.ReadFile("actions.ps1")
	data, _ := json.Marshal(map[string]string{"queue": string(queue), "actions": string(actions)})
	output, err := runParserCheck(path, clipboardSnapshotActionFixture, data)
	if err != nil {
		t.Fatalf("snapshot helper fixture: %v %s", err, output)
	}
	for _, name := range []string{"snapshot_metadata_only", "legacy_snapshot_refused"} {
		if strings.Count(string(output), "passed:"+name+"\r\n") != 1 {
			t.Fatalf("missing helper case %s: %s", name, output)
		}
		t.Log("helper case passed:", name)
	}
}

// Extract only the action/dispatcher; replace native types with data-only fakes.
const clipboardSnapshotActionFixture = `
$ErrorActionPreference='Stop'
$data=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))|ConvertFrom-Json
Add-Type -TypeDefinition @'
public static class AMCDesktop {public static bool InteractiveDesktop(){return true;}}
public static class AMCClipboard {
 public sealed class State {public uint Sequence=0;public uint[] Formats=new uint[0];public bool InventoryComplete=true,Empty=true;}
 public static int Calls;
 public static State InventorySnapshot(){Calls++;return new State();}
 public static State Snapshot(){throw new System.Exception("private_payload_read");}
}
'@
function Load-Function($source,$name){
 $ast=[Management.Automation.Language.Parser]::ParseInput($source,[ref]$null,[ref]$null)
 $f=$ast.Find({param($node)$node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name-eq $name},$false)
 if(-not $f){throw 'missing_function'}
 return [ScriptBlock]::Create($f.Extent.Text)
}
function Assert-ConsoleSession {}
$identity=[Security.Principal.WindowsIdentity]::GetCurrent()
try{
 . (Load-Function $data.actions 'Invoke-DesktopAction')
 $response=Invoke-DesktopAction @{action='clipboard.snapshot'}
 if([AMCClipboard]::Calls-ne 1 -or $response.ContainsKey('text') -or $response.Count-ne 3){throw 'payload_or_route_error'}
 $meta=$response.clipboard
 if($meta.Count-ne 4 -or $meta.sequence-ne 0 -or @($meta.formats).Count-ne 0 -or -not $meta.inventory_complete -or -not $meta.empty){throw 'metadata_lost'}
 [Console]::WriteLine('passed:snapshot_metadata_only')
 $root='C:\synthetic-unopened-root'
 . (Load-Function $data.queue 'Get-WorkerArguments')
 if((Get-WorkerArguments ('a'*32) 'clipboard.snapshot')-notmatch ' -STA '){throw 'wrong_apartment'}
 . (Load-Function ($data.queue.Replace(", 'clipboard.snapshot'",'')) 'Get-WorkerArguments')
 try{Get-WorkerArguments ('a'*32) 'clipboard.snapshot';throw 'legacy_accepted'}catch{if($_.Exception.Message-ne 'unsupported_action'){throw}}
 [Console]::WriteLine('passed:legacy_snapshot_refused')
}finally{$identity.Dispose()}
`
