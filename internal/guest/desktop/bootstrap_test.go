package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestBootstrapCompletesWithoutStdinEOF(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for exact bootstrap framing fixture")
	}
	for _, text := range []string{"synthetic nonce text", "Привет 世界", "first\nsecond\r\nthird", "'; $(throw 'must remain data'); & | < > ` \""} {
		t.Run(text, func(t *testing.T) {
			data, err := json.Marshal(map[string]string{"program": base64.StdEncoding.EncodeToString([]byte(bootstrapFixtureProgram)), "text": text})
			if err != nil {
				t.Fatal(err)
			}
			output, err := runBootstrapWithPipe(t, path, append(data, '\n'), false)
			if err != nil {
				t.Fatalf("exact bootstrap failed: %v", err)
			}
			var response struct{ Nonce, Text string }
			if json.Unmarshal(output, &response) != nil || response.Nonce != "amc-bootstrap-framing" || response.Text != text {
				t.Fatal("bootstrap changed structured data or failed to return fixed nonce")
			}
		})
	}
}

func TestBootstrapRejectsMalformedLineAndTruncatedEOF(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for exact bootstrap framing fixture")
	}
	for _, test := range []struct {
		name, input string
		closeInput  bool
	}{
		{"malformed terminated line", "{invalid JSON}\n", false},
		{"truncated EOF", "{\"program\":", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := runBootstrapWithPipe(t, path, []byte(test.input), test.closeInput)
			if err == nil || bytes.Contains(output, []byte("amc-bootstrap-framing")) {
				t.Fatal("invalid framed input executed the synthetic program")
			}
		})
	}
}

// Only a fixed synthetic data echo executes. No queue, filesystem or UI API loads.
const bootstrapFixtureProgram = `param($request)
[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false)
[Console]::Out.Write((@{nonce='amc-bootstrap-framing';text=[string]$request.text}|ConvertTo-Json -Compress))`

func runBootstrapWithPipe(t *testing.T, path string, data []byte, closeInput bool) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// #nosec G204 -- exact fixed bootstrap; the envelope executes only a fixed synthetic program.
	command := exec.CommandContext(ctx, path, strings.Fields(transportCommand())[1:]...)
	command.WaitDelay = time.Second
	var output, stderr bytes.Buffer
	command.Stdout, command.Stderr = &output, &stderr
	pipe, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	// command.Process is the sole owned process; always reap it before returning.
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	if _, err := pipe.Write(data); err != nil {
		pipe.Close()
		cancel()
		<-done
		t.Fatalf("write synthetic frame: %v", err)
	}
	if closeInput {
		pipe.Close()
	}
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		return output.Bytes(), err
	case <-timer.C:
		// Release EOF before cancellation to let the original whole-stream bootstrap
		// exit normally too. This avoids leaving a Windows interop child behind.
		pipe.Close()
		select {
		case <-done:
		case <-ctx.Done():
			cancel()
			<-done
		}
		t.Fatal("bootstrap did not exit within the framing bound while stdin stayed open")
		return nil, errors.New("unreachable")
	}
}
