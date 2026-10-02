package desktop

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestGuestScriptsEnforceInteractivePrivateBoundedExecution(t *testing.T) {
	checks := map[string][]string{
		"queue.ps1":     {"Remove-ExpiredQueueFiles", "GetOwnerSid", "Get-WorkerArguments", "-First 129", "-First 256", "FileShare]::None", "GetNamedSecurityInfo", "0x10", "WTSGetActiveConsoleSessionId", "AreAccessRulesProtected", "unsafe_integrity", "S:(ML;OICI;NW;;;HI)", "foreign_task", "Get-FileHash", "Interactive", "Highest"},
		"transport.ps1": {"-LogonType Interactive -RunLevel Highest", "-MultipleInstances IgnoreNew", "Assert-Installed", "Assert-Request", "'.tmp'", "deadline", "Stop-ScheduledTask"},
		"server.ps1":    {"Remove-ExpiredQueueFiles", "AddSeconds(5)", "Assert-ConsoleSession", "WaitForExit", "$child.Kill()", "$child.Dispose()", "AddMinutes(20)", "-First 64"},
		"worker.ps1":    {"SessionId -eq 0", "Add-Type -Path", "desktop_failed", "Assert-PrivatePath", "524288"},
		"actions.ps1":   {"Assert-ConsoleSession", "Get-GuestCursor", "$response.cursor = Get-GuestCursor", "IsPassword", "GetRuntimeId", "elements.Count -lt 256", "item.depth -ge 8", "foreground_denied", "stale_window", "StartTime.ToUniversalTime().Ticks", "missing_window_identity", "InteractiveDesktop", "UseShellExecute = $false", "clipboard.get", "scroll", "uia.invoke", "uia.setvalue", "SelectionItemPattern", "TogglePattern", "ExpandCollapsePattern", "ScrollPattern", "horizontal"},
	}
	for name, markers := range checks {
		data, _ := scripts.ReadFile(name)
		source := string(data)
		for _, marker := range markers {
			if !strings.Contains(source, marker) {
				t.Errorf("%s lacks %s", name, marker)
			}
		}
		for _, forbidden := range []string{"Invoke-Expression", "TcpListener", "HttpListener", "-LogonType S4U", "VMConnect", "SendKeys"} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s contains %s", name, forbidden)
			}
		}
	}
}

func TestGuestScriptsParseAndNativeDeclarationsCompile(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for read-only parser/compiler check")
	}
	// Only parse script text and compile declarations. Never run guest UI functions on the host.
	for _, name := range []string{"queue.ps1", "transport.ps1", "server.ps1", "worker.ps1", "actions.ps1"} {
		data, _ := scripts.ReadFile(name)
		check := `$s=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()));$tokens=$null;$errors=$null;[Management.Automation.Language.Parser]::ParseInput($s,[ref]$tokens,[ref]$errors)|Out-Null;if($errors.Count){$errors|ForEach-Object{$_.Message};exit 1}`
		if output, err := runParserCheck(path, check, data); err != nil {
			t.Fatalf("%s parser: %v %s", name, err, output)
		}
	}
	data, _ := scripts.ReadFile("native.cs")
	check := `Add-Type -TypeDefinition ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))) -ErrorAction Stop;[AMCDesktop]::Quote('synthetic argument')|Out-Null`
	if output, err := runParserCheck(path, check, data); err != nil {
		t.Fatalf("native declarations: %v %s", err, output)
	}
}

func runParserCheck(path, check string, data []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// A pwsh-launched Go test can inherit Core modules incompatible with Windows
	// PowerShell. Restrict only this synthetic child to its own built-in modules.
	check = `$env:PSModulePath=Join-Path $PSHOME 'Modules';` + check
	// #nosec G204 -- fixed parser/compiler programs; executable resolved from local PATH, no guest input executed.
	command := exec.CommandContext(ctx, path, "-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShell(check))
	command.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString(data))
	return command.CombinedOutput()
}

func encodePowerShell(script string) string {
	codes := utf16.Encode([]rune(script))
	data := make([]byte, len(codes)*2)
	for index, code := range codes {
		binary.LittleEndian.PutUint16(data[index*2:], code)
	}
	return base64.StdEncoding.EncodeToString(data)
}
