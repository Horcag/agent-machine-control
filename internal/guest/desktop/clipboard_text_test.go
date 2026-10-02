package desktop

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestClipboardSetSerializedTextAndUTF16Limits(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for data-only clipboard fixture")
	}
	actions, err := scripts.ReadFile("actions.ps1")
	if err != nil {
		t.Fatal(err)
	}
	type clipboardCase struct {
		Name    string          `json:"name"`
		Request json.RawMessage `json:"request"`
		Text    string          `json:"text"`
		Call    string          `json:"call"`
		Error   string          `json:"error"`
	}
	cases := []clipboardCase{}
	for _, test := range []struct {
		name, text, call, failure string
	}{
		{"go_omitted_empty", "", "clear", ""},
		{"unicode", "synthetic 世界 😀; $(do-not-evaluate)\nnext line", "set", ""},
		{"bmp_limit", strings.Repeat("a", 4096), "set", ""},
		{"utf16_limit", strings.Repeat("😀", 2048), "set", ""},
		{"bmp_oversized", strings.Repeat("a", 4097), "", "oversized_clipboard"},
		{"utf16_oversized", strings.Repeat("😀", 2048) + "a", "", "oversized_clipboard"},
	} {
		req := request("clipboard.set")
		req.Text = test.text
		serialized, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, clipboardCase{test.name, serialized, test.text, test.call, test.failure})
	}
	cases = append(cases,
		clipboardCase{Name: "explicit_empty", Request: json.RawMessage(`{"action":"clipboard.set","text":""}`), Call: "clear"},
		clipboardCase{Name: "explicit_null", Request: json.RawMessage(`{"action":"clipboard.set","text":null}`), Call: "clear"},
	)
	data, err := json.Marshal(map[string]any{"actions": string(actions), "cases": cases})
	if err != nil {
		t.Fatal(err)
	}
	output, err := runParserCheck(path, clipboardTextFixture, data)
	if err != nil {
		t.Fatalf("clipboard text fixture: %v %s", err, output)
	}
	for _, test := range cases {
		if strings.Count(string(output), "passed:"+test.Name+"\r\n") != 1 {
			t.Fatalf("case %s did not execute exactly once: %s", test.Name, output)
		}
		t.Logf("native case passed: %s", test.Name)
	}
}

// Run only the AST-extracted production clipboard.set branch. The substitute
// type records calls and rejects empty SetText just like Windows Forms; neither
// the real Clipboard assembly nor any desktop/session/VM functions are loaded.
const clipboardTextFixture = `
$ErrorActionPreference='Stop'
$data=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))|ConvertFrom-Json
if($PSVersionTable.PSVersion.Major-ne 5 -or $PSVersionTable.PSVersion.Minor-ne 1){throw 'expected_windows_powershell_5_1'}
$tokens=$null;$errors=$null
$ast=[Management.Automation.Language.Parser]::ParseInput($data.actions,[ref]$tokens,[ref]$errors)
if($errors.Count){throw 'parser_error'}
$branches=@($ast.FindAll({param($node)
 $node -is [Management.Automation.Language.IfStatementAst] -and
 $node.Clauses[0].Item1.Extent.Text -ceq '$request.action -eq ''clipboard.set'''
},$true))
if($branches.Count-ne 1){throw 'clipboard_branch_not_unique'}
$branchText=$branches[0].Clauses[0].Item2.Extent.Text
$body=[ScriptBlock]::Create($branchText.Substring(1,$branchText.Length-2))
Add-Type -TypeDefinition @'
namespace Windows.Forms {
 public static class Clipboard {
  public static string Call = "";
  public static string Text = "";
  public static void Clear() { Call += "clear"; }
  public static void SetText(string text) {
   Call += "set";
   if (string.IsNullOrEmpty(text)) throw new System.ArgumentException("empty_set_text");
   Text = text;
  }
 }
}
'@
foreach($case in $data.cases){
 $request=$case.request;$response=@{session_id=1;elevated=$false}
 [Windows.Forms.Clipboard]::Call='';[Windows.Forms.Clipboard]::Text=''
 $failure='';$result=$null
 try{$result=& $body}catch{$failure=$_.Exception.Message}
 if($failure-cne $case.error){throw ($case.name+': unexpected error '+$failure)}
 if([Windows.Forms.Clipboard]::Call-cne $case.call){throw ($case.name+': unexpected clipboard calls')}
 if($case.call-eq 'set' -and [Windows.Forms.Clipboard]::Text-cne $case.text){throw ($case.name+': text changed')}
 if(-not $case.error -and (-not [Object]::ReferenceEquals($result,$response))){throw ($case.name+': response changed')}
 [Console]::Out.WriteLine('passed:'+$case.name)
}
`
