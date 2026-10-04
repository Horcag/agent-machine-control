package hyperv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"os/exec"
	"slices"
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
		{"double", "click", 2, 0, true, 2, 1, 0},
		{"drag", "drag", 0, 0, true, 1, 21, 0},
		{"short_drag", "drag", 0, 0, true, 1, 21, 20},
		{"fractional_step_drag", "drag", 0, 0, true, 1, 21, 401},
		{"long_drag", "drag", 0, 0, true, 1, 21, 5000},
		{"failed_drag", "drag", 0, 3, false, 1, 3, 5000},
	} {
		t.Run(test.name, func(t *testing.T) { runSyntheticConsoleGesture(t, path, test) })
	}
}

type consoleGestureCase struct {
	name, kind     string
	count, failAt  int
	success        bool
	presses, moves int
	duration       int
}

func runSyntheticConsoleGesture(t *testing.T, path string, test consoleGestureCase) {
	t.Helper()

	fixture := `class SyntheticDevice {
    static [Collections.Generic.List[string]] $Events=[Collections.Generic.List[string]]::new()
    static [Collections.Generic.List[int]] $Sleeps=[Collections.Generic.List[int]]::new()
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
   function Start-Sleep([int]$Milliseconds) { [SyntheticDevice]::Sleeps.Add($Milliseconds) }
   [Console]::Out.WriteLine('amc-gesture-ready')
   $result=& {
   ` + strings.TrimPrefix(ScriptConsoleInput, scriptConsolePrelude) + `
   }
   @{result=($result|ConvertFrom-Json);events=@([SyntheticDevice]::Events);sleeps=@([SyntheticDevice]::Sleeps)}|ConvertTo-Json -Compress -Depth 5`
	data, _ := json.Marshal(map[string]any{"vm_id": consoleTestID, "keys": []int{17, 16}, "fail_at": test.failAt, "input": map[string]any{"kind": test.kind, "x": 10, "y": 20, "to_x": 110, "to_y": 120, "button": "left", "count": test.count, "duration_ms": test.duration}})
	output := runReadyConsoleGesture(t, path, fixture, data)
	var result struct {
		Result struct {
			Success bool `json:"success"`
		} `json:"result"`
		Events []string `json:"events"`
		Sleeps []int    `json:"sleeps"`
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
	if test.kind == "drag" && test.success {
		if !slices.Equal(result.Sleeps, expectedDragDelays(test.duration)) || result.Events[len(result.Events)-4] != "move:110,120" {
			t.Fatalf("duration/end mismatch: delays=%v events=%v", result.Sleeps, result.Events)
		}
	}
}

func expectedDragDelays(duration int) []int {
	if duration == 0 {
		duration = 400
	}
	delays := make([]int, 20)
	for step := range delays {
		delays[step] = duration*(step+1)/20 - duration*step/20
	}
	return delays
}

// runReadyConsoleGesture bounds cold startup separately from synthetic execution.
func runReadyConsoleGesture(t *testing.T, path, fixture string, data []byte) []byte {
	t.Helper()
	codes := utf16.Encode([]rune(fixture))
	encoded := make([]byte, 2*len(codes))
	for i, c := range codes {
		binary.LittleEndian.PutUint16(encoded[2*i:], c)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// Cold PowerShell startup/module loading has its own budget; the gesture
	// still has only 20 seconds after the fixture has consumed its input.
	timer := time.AfterFunc(60*time.Second, cancel)
	defer timer.Stop()
	// #nosec G204 -- fixed local synthetic device program, never invokes host UI APIs.
	command := exec.CommandContext(ctx, path, "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
	command.Stdin = strings.NewReader(string(data))
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	ready, readyErr := reader.ReadString('\n')
	startup := time.Since(started)
	if readyErr != nil || strings.TrimSpace(ready) != "amc-gesture-ready" {
		cancel()
		waitErr := command.Wait()
		t.Fatalf("synthetic readiness: %v; process: %v; stdout: %q; stderr: %s", readyErr, waitErr, ready, &stderr)
	}
	timer.Reset(20 * time.Second)
	output, readErr := io.ReadAll(reader)
	if err := command.Wait(); err != nil || readErr != nil {
		t.Fatalf("synthetic script: %v; read: %v; context: %v; stdout: %s; stderr: %s", err, readErr, ctx.Err(), output, &stderr)
	}
	t.Logf("PowerShell fixture ready after %s; gesture completed in %s", startup, time.Since(started)-startup)
	return output
}
