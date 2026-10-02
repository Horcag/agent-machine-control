package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	for index, text := range []string{"synthetic nonce text", "Привет 世界 😀", strings.Repeat("Привет 😀", 8192), "first\nsecond\r\nthird", "'; $(throw 'must remain data'); & | < > ` \""} {
		t.Run([]string{"ASCII", "Unicode", "large fragmented frame", "escaped newlines", "shell metacharacters"}[index], func(t *testing.T) {
			data, err := json.Marshal(bootstrapEnvelope(text))
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
		name       string
		input      []byte
		closeInput bool
	}{
		{"malformed terminated line", []byte("{invalid JSON}\n"), false},
		{"empty line", []byte("\n"), false},
		{"invalid UTF8", append(bytes.Replace(mustBootstrapFrame(t, bootstrapEnvelope("valid")), []byte("valid"), []byte{0xff}, 1), '\n'), false},
		{"truncated EOF", []byte("{\"program\":"), true},
		{"missing LF", mustBootstrapFrame(t, bootstrapEnvelope("valid")), true},
		{"missing LF held open", mustBootstrapFrame(t, bootstrapEnvelope("valid")), false},
		{"over cap including LF", paddedBootstrapFrame(t, 524289), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := runBootstrapWithPipe(t, path, test.input, test.closeInput)
			if err == nil || bytes.Contains(output, []byte("amc-bootstrap-framing")) {
				t.Fatal("invalid framed input executed the synthetic program")
			}
		})
	}
}

func bootstrapEnvelope(text string) map[string]string {
	return map[string]string{"program": base64.StdEncoding.EncodeToString([]byte(bootstrapFixtureProgram)), "text": text,
		"request_id": strings.Repeat("a", 32), "deadline": time.Now().Add(20 * time.Second).UTC().Format(time.RFC3339Nano)}
}

func mustBootstrapFrame(t *testing.T, envelope map[string]string) []byte {
	t.Helper()
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func paddedBootstrapFrame(t *testing.T, size int) []byte {
	t.Helper()
	data := mustBootstrapFrame(t, bootstrapEnvelope("boundary"))
	data = append(data, bytes.Repeat([]byte{' '}, size-len(data)-1)...)
	return append(data, '\n')
}

func TestBootstrapAcceptsFrameAtByteCap(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable")
	}
	output, err := runBootstrapWithPipe(t, path, paddedBootstrapFrame(t, 524288), false)
	if err != nil || !bytes.Contains(output, []byte("amc-bootstrap-framing")) {
		t.Fatalf("maximum allowed frame rejected: %v", err)
	}
}

func TestBootstrapRejectsIdentityAndExpiryBeforeProgram(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable")
	}
	for _, test := range []struct{ name, key, value string }{
		{"invalid ID", "request_id", "INVALID"},
		{"expired", "deadline", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)},
		{"too far", "deadline", time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)},
		{"invalid deadline", "deadline", "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			envelope := bootstrapEnvelope("must not execute")
			envelope[test.key] = test.value
			output, err := runBootstrapWithPipe(t, path, append(mustBootstrapFrame(t, envelope), '\n'), false)
			if err == nil || bytes.Contains(output, []byte("amc-bootstrap-framing")) {
				t.Fatal("invalid envelope executed embedded prelude")
			}
		})
	}
	if len(transportCommand()) >= 32768 {
		t.Fatal("bootstrap exceeds shell command limit")
	}
}

// Only a fixed synthetic data echo executes. No queue, filesystem or UI API loads.
const bootstrapFixtureProgram = `param($request)
[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false)
[Console]::Out.Write((@{nonce='amc-bootstrap-framing';text=[string]$request.text}|ConvertTo-Json -Compress))`

func runBootstrapWithPipe(t *testing.T, path string, data []byte, closeInput bool) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 46*time.Second)
	defer cancel()
	// #nosec G204 -- exact fixed bootstrap; the envelope executes only a fixed synthetic program.
	command := exec.CommandContext(ctx, path, readyBootstrapArguments(t)...)
	command.WaitDelay = time.Second
	output := &bootstrapReadyOutput{ready: make(chan struct{})}
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = output, &stderr
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
	startup := time.NewTimer(30 * time.Second)
	defer startup.Stop()
	select {
	case <-output.ready:
	case err := <-done:
		t.Fatalf("bootstrap exited before readiness: %v", err)
	case <-startup.C:
		pipe.Close()
		cancel()
		<-done
		t.Fatal("PowerShell did not reach bootstrap readiness within startup bound")
	}
	if err := writeBootstrapInput(t, pipe, data, closeInput, done); err != nil {
		pipe.Close()
		cancel()
		<-done
		t.Fatalf("write synthetic frame: %v", err)
	}

	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			err = fmt.Errorf("%w: %s", err, stderr.String())
		}
		return bytes.TrimPrefix(output.Bytes(), []byte(bootstrapReadyMarker)), err
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

func writeBootstrapInput(t *testing.T, pipe io.WriteCloser, data []byte, closeInput bool, done <-chan error) error {
	t.Helper()
	// Fragment the frame, including UTF-8 sequence boundaries; keep stdin open.
	for start := 0; start < len(data); {
		end := min(start+997, len(data))
		if start < 64 {
			end = start + 1
		}
		if _, err := pipe.Write(data[start:end]); err != nil {
			return err
		}
		start = end
	}
	if !closeInput && len(data) > 0 && data[len(data)-1] != '\n' {
		// An unterminated valid JSON envelope cannot execute while more bytes
		// may arrive. Release EOF only after proving it stays blocked.
		select {
		case err := <-done:
			t.Fatalf("unterminated frame exited before EOF: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		closeInput = true
	}
	if closeInput {
		pipe.Close()
	}
	return nil
}

// The wrapper emits a fixed marker before the byte-for-byte production body. It
// isolates inherited Core module paths without payload interpolation or UI APIs.
const bootstrapReadyMarker = "amc-bootstrap-ready\n"

func readyBootstrapArguments(t *testing.T) []string {
	t.Helper()
	args := strings.Fields(transportCommand())[1:]
	body, err := base64.StdEncoding.DecodeString(args[len(args)-1])
	if err != nil || len(body)%2 != 0 {
		t.Fatal("production bootstrap encoding invalid")
	}
	const prefix = "$env:PSModulePath=Join-Path $PSHOME 'Modules';[Console]::Out.WriteLine('amc-bootstrap-ready');[Console]::Out.Flush();"
	wrapped := make([]byte, len(prefix)*2, len(prefix)*2+len(body))
	for i := range len(prefix) {
		binary.LittleEndian.PutUint16(wrapped[i*2:], uint16(prefix[i]))
	}
	wrapped = append(wrapped, body...)
	args[len(args)-1] = base64.StdEncoding.EncodeToString(wrapped)
	decoded, err := base64.StdEncoding.DecodeString(args[len(args)-1])
	if err != nil || !bytes.Equal(decoded[len(prefix)*2:], body) {
		t.Fatal("readiness wrapper changed production bootstrap body")
	}
	return args
}

type bootstrapReadyOutput struct {
	buffer   bytes.Buffer
	ready    chan struct{}
	signaled bool
}

func (output *bootstrapReadyOutput) Write(data []byte) (int, error) {
	n, err := output.buffer.Write(data)
	// WriteLine uses CRLF on Windows. Normalize only the fixed readiness line.
	marker := []byte("amc-bootstrap-ready\r\n")
	if !output.signaled && bytes.HasPrefix(output.Bytes(), marker) {
		rest := bytes.Clone(output.Bytes()[len(marker):])
		output.buffer.Reset()
		output.buffer.WriteString(bootstrapReadyMarker)
		output.buffer.Write(rest)
		output.signaled = true
		close(output.ready)
	}
	return n, err
}

func (output *bootstrapReadyOutput) Bytes() []byte { return output.buffer.Bytes() }
