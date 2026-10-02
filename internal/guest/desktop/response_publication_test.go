package desktop

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestTransportReadPreservesWorkerResponseBeforeExit(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for publication interleaving fixture")
	}
	server, _ := scripts.ReadFile("server.ps1")
	transport, _ := scripts.ReadFile("transport.ps1")
	data, err := json.Marshal(map[string]string{"server": string(server), "transport": string(transport)})
	if err != nil {
		t.Fatal(err)
	}
	fixture := strings.Replace(workerResultFixture, "$source=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))", "$source=$bundle.server", 1)
	// Pause the fake child inside WaitForExit, after publishing its success but
	// before the dispatcher sees its exit. Execute the production read/finally AST.
	fixture = strings.Replace(fixture, "$script:waits++", "$script:waits++;if($script:mode-eq 'valid'){Invoke-TransportRead}", 1)
	fixture = publicationReadPrelude + fixture + "\nif($script:reads-ne 1){throw 'transport_read_not_executed'}"
	if output, err := runParserCheck(path, fixture, data); err != nil {
		t.Fatalf("response read/worker-exit interleaving: %v %s", err, output)
	}
}

const publicationReadPrelude = `
$ErrorActionPreference='Stop'
$bundle=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))|ConvertFrom-Json
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseInput($bundle.transport,[ref]$tokens,[ref]$errors)
if($errors.Count){throw 'transport_parse_failed'}
$inner=$ast.Find({param($node) $node -is [Management.Automation.Language.TryStatementAst] -and $null-ne $node.Finally},$false)
# The last three body statements validate, bound and read the complete response.
# Include the actual finally block, so premature deletion changes fixture behavior.
$readBody=($inner.Body.Statements|Select-Object -Last 3|ForEach-Object {$_.Extent.Text})-join "` + "`n" + `"
$script:reader=[ScriptBlock]::Create('try {'+$readBody+'} finally '+$inner.Finally.Extent.Text)
$script:reads=0
function Invoke-TransportRead {
 $outputPath=$script:expectedResult;$ownsInput=$true;$ownsTemp=$false;$tempPath=$inputPath+'.tmp'
 $prior=[Console]::Out;$captured=[IO.StringWriter]::new()
 try {
  [Console]::SetOut($captured)
  . $script:reader
  $observed=$captured.ToString()|ConvertFrom-Json
  if(-not $observed.success -or $observed.text-ne 'synthetic valid result'){throw 'transport_read_changed_response'}
  $script:reads++
 } finally {[Console]::SetOut($prior);$captured.Dispose()}
}
`
