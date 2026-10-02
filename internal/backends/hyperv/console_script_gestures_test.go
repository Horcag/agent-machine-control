package hyperv

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestConsoleGestureScriptsApplyAndReleaseSyntheticDevices(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("native PowerShell unavailable")
	}
	for _, test := range []consoleGestureCase{
		{"double", "click", 2, 0, true, 2, 1},
		{"drag", "drag", 0, 0, true, 1, 21},
		{"failed_drag", "drag", 0, 3, false, 1, 3},
	} {
		t.Run(test.name, func(t *testing.T) { runSyntheticConsoleGesture(t, path, test) })
	}
}

type consoleGestureCase struct {
	name, kind     string
	count, failAt  int
	success        bool
	presses, moves int
}

func runSyntheticConsoleGesture(t *testing.T, path string, test consoleGestureCase) {
	t.Helper()

	fixture := `class SyntheticDevice {
    static [Collections.Generic.List[string]] $Events=[Collections.Generic.List[string]]::new()
    static [int] $Moves=0
    static [int] $FailAt=0
    [object] PressKey([uint32]$key) { [SyntheticDevice]::Events.Add('press:'+$key);return @{ReturnValue=0} }
    [object] ReleaseKey([uint32]$key) { [SyntheticDevice]::Events.Add('release:'+$key);return @{ReturnValue=0} }
    [object] SetButtonState([uint32]$button,[bool]$held) { [SyntheticDevice]::Events.Add('button:'+$held);return @{ReturnValue=0} }
    [object] SetAbsolutePosition([int]$x,[int]$y) {
     [SyntheticDevice]::Moves++;[SyntheticDevice]::Events.Add('move:'+$x+','+$y)
     if([SyntheticDevice]::Moves -eq [SyntheticDevice]::FailAt){return @{ReturnValue=1}};return @{ReturnValue=0}
    }
   }
   $ProgressPreference='SilentlyContinue';$r=([Console]::In.ReadToEnd()|ConvertFrom-Json);$id=$r.vm_id;[SyntheticDevice]::FailAt=$r.fail_at
   function GuestDevice($kind) { if($kind -eq 'Msvm_VideoHead'){return @{CurrentHorizontalResolution=200;CurrentVerticalResolution=200}};return [SyntheticDevice]::new() }
   function RequireSuccess($result){if($result.ReturnValue -ne 0){throw 'synthetic failure'}}
   $result=& {
   ` + strings.TrimPrefix(ScriptConsoleInput, scriptConsolePrelude) + `
   }
   @{result=($result|ConvertFrom-Json);events=@([SyntheticDevice]::Events)}|ConvertTo-Json -Compress -Depth 5`
	data, _ := json.Marshal(map[string]any{"vm_id": consoleTestID, "keys": []int{17, 16}, "fail_at": test.failAt, "input": map[string]any{"kind": test.kind, "x": 10, "y": 20, "to_x": 110, "to_y": 120, "button": "left", "count": test.count}})
	codes := utf16.Encode([]rune(fixture))
	encoded := make([]byte, 2*len(codes))
	for i, c := range codes {
		binary.LittleEndian.PutUint16(encoded[2*i:], c)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	// #nosec G204 -- fixed local synthetic device program, never invokes host UI APIs.
	command := exec.CommandContext(ctx, path, "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
	command.Stdin = strings.NewReader(string(data))
	output, err := command.Output()
	if err != nil {
		t.Fatalf("synthetic script: %v %s", err, output)
	}
	var result struct {
		Result struct {
			Success bool `json:"success"`
		} `json:"result"`
		Events []string `json:"events"`
	}
	if json.Unmarshal(output, &result) != nil || result.Result.Success != test.success {
		t.Fatalf("unexpected result %s", output)
	}
	presses, moves := 0, 0
	for _, event := range result.Events {
		if event == "button:True" {
			presses++
		}
		if strings.HasPrefix(event, "move:") {
			moves++
		}
	}
	if presses != test.presses || moves != test.moves {
		t.Fatalf("gesture path %v", result.Events)
	}
	tail := strings.Join(result.Events[len(result.Events)-3:], "|")
	if tail != "release:16|release:17|button:False" {
		t.Fatalf("held input after gesture: %v", result.Events)
	}
	if test.name == "drag" && result.Events[len(result.Events)-4] != "move:110,120" {
		t.Fatal("drag missed destination")
	}
}
