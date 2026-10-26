package stream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Execute the production byte reader, stopping before C# compilation or native
// worker execution. Linux PowerShell proves framing only, not Windows delivery.
func TestStartupBinaryReadWithoutEOF(t *testing.T) {
	path, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("Linux PowerShell unavailable")
	}
	prefix := startupReadPrefix(t)
	script := prefix + `$sha=[Security.Cryptography.SHA256]::Create();$digest=[BitConverter]::ToString($sha.ComputeHash($b)).Replace('-','').ToLowerInvariant();$sha.Dispose();[Console]::Out.WriteLine(('SOURCE:{0}:{1}' -f $b.Length,$digest));[Console]::Out.Flush()`
	for _, tc := range []struct {
		name  string
		data  []byte
		chunk int
	}{
		{"embedded_source_32768_boundary", sourceBytes(), 32768},
		{"binary_arbitrary_boundary", bytes.Repeat([]byte{0, 255, 13, 10, 128, 1}, 101), 7},
		{"maximum_length", bytes.Repeat([]byte{0, 255}, MaxControl/2), 32768},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkStartupBinaryReceipt(t, path, script, tc.data, tc.chunk)
		})
	}
}

func checkStartupBinaryReceipt(t *testing.T, path, script string, data []byte, chunk int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// #nosec G204 -- fixed production reader plus synthetic receipt; no guest input becomes executable code.
	cmd := exec.CommandContext(ctx, path, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bootstrapOutput
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		cancel()
		_ = stdin.Close()
		if !waited {
			_ = cmd.Wait()
		}
	}()
	output := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(io.LimitReader(stdout, 256)).ReadString('\n')
		output <- strings.TrimSpace(line)
	}()
	header := writeBootstrapFrame(t, stdin, data, chunk)
	// Keep the pipe open until receipt: ReadToEnd would block here.
	select {
	case got := <-output:
		want := fmt.Sprintf("SOURCE:%d:%s", len(data), hash(data))
		if got != want {
			t.Fatalf("receipt = %q, want %q", got, want)
		}
		t.Logf("header=%x receipt=%s (stdin open)", header, got)
	case <-ctx.Done():
		t.Fatal("source receipt did not arrive while stdin remained open")
	}
	_ = stdin.Close()
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("PowerShell: %v: %s", err, &stderr)
	}
	if !strings.Contains(stderr.String(), "AMC_STARTUP_V1:3:before_compile") {
		t.Fatal("source read did not reach compile boundary")
	}
}

func writeBootstrapFrame(t *testing.T, stdin io.Writer, data []byte, chunk int) [4]byte {
	t.Helper()
	if len(data) > MaxControl || chunk <= 0 {
		t.Fatal("invalid bootstrap fixture bounds")
	}
	var header [4]byte
	// #nosec G115 -- fixture length is bounded by MaxControl above.
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	for _, part := range [][]byte{header[:1], header[1:]} {
		if err := writeAll(stdin, part); err != nil {
			t.Fatal(err)
		}
	}
	for offset := 0; offset < len(data); offset += chunk {
		end := min(offset+chunk, len(data))
		if err := writeAll(stdin, data[offset:end]); err != nil {
			t.Fatal(err)
		}
	}
	return header
}

func startupReadPrefix(t *testing.T) string {
	t.Helper()
	_, command, ok := strings.Cut(StartupCommand, `-Command "`)
	if !ok || !strings.HasSuffix(command, `"`) {
		t.Fatal("unexpected startup command envelope")
	}
	prefix, _, ok := strings.Cut(strings.TrimSuffix(command, `"`), "Add-Type -TypeDefinition")
	if !ok || len(prefix) > 8191 {
		t.Fatal("missing or oversized startup read prefix")
	}
	// Linux runs the exact framing code with a portable stream opener.
	// Reflection.Emit and Windows handle mode acceptance require native canaries.
	_, prefix, ok = strings.Cut(prefix, "$Native=$type.CreateType();")
	if !ok {
		t.Fatal("missing fixed native declarations")
	}
	definitions, _, ok := strings.Cut(prefix, "$i=Open-AMCStandardStream -10 ([IO.FileAccess]::Read);")
	if !ok {
		t.Fatal("missing standalone source reader")
	}
	return definitions + `function Open-AMCStandardStream {return [Console]::OpenStandardInput()};$i=Open-AMCStandardStream -10 ([IO.FileAccess]::Read);$b=Read-AMCSource $i;`
}

func TestStartupBinaryReadRejectsIncompleteAndOversizedFrames(t *testing.T) {
	path, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("Linux PowerShell unavailable")
	}
	var oversized [4]byte
	binary.BigEndian.PutUint32(oversized[:], MaxControl+1)
	for _, tc := range []struct {
		name        string
		input       []byte
		sourceStage bool
	}{
		{"truncated_header", []byte{0, 0, 0}, false},
		{"truncated_source", []byte{0, 0, 0, 3, 1, 2}, true},
		{"oversized_length", oversized[:], false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// #nosec G204 -- fixed production reader plus synthetic receipt; no guest input becomes executable code.
			cmd := exec.CommandContext(ctx, path, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", startupReadPrefix(t)+`[Console]::Out.WriteLine('UNEXPECTED_SOURCE_ACCEPTED')`)
			cmd.Stdin = bytes.NewReader(tc.input)
			var capture bootstrapOutput
			cmd.Stdout, cmd.Stderr = &capture, &capture
			err := cmd.Run()
			output := capture.Bytes()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || ctx.Err() != nil {
				t.Fatalf("expected prompt exit 1, got %v: %s", err, output)
			}
			if strings.Contains(string(output), "before_compile") || strings.Contains(string(output), "UNEXPECTED_SOURCE_ACCEPTED") {
				t.Fatalf("accepted invalid frame: %s", output)
			}
			if strings.Contains(string(output), "before_source_read") != tc.sourceStage {
				t.Fatalf("unexpected source stage: %s", output)
			}
			t.Logf("rejected frame with exit 1: %s", strings.TrimSpace(string(output)))
		})
	}
}

type bootstrapWriteStep struct {
	n   int
	err error
}

// The startup probe has only a fixed receipt and three fixed progress markers.
// A changed command cannot turn diagnostics into an unbounded test allocation.
type bootstrapOutput struct{ bytes.Buffer }

func (w *bootstrapOutput) Write(p []byte) (int, error) {
	if len(p) > 1024-w.Len() {
		return 0, io.ErrShortWrite
	}
	return w.Buffer.Write(p)
}

type bootstrapStepWriter struct {
	steps  []bootstrapWriteStep
	output bytes.Buffer
	calls  int
}

func (w *bootstrapStepWriter) Write(p []byte) (int, error) {
	if w.calls == len(w.steps) {
		return 0, io.ErrUnexpectedEOF
	}
	step := w.steps[w.calls]
	w.calls++
	if step.n >= 0 && step.n <= len(p) {
		_, _ = w.output.Write(p[:step.n])
	}
	return step.n, step.err
}

func TestBootstrapWriteAllFailureAndPartialProgress(t *testing.T) {
	failure := errors.New("synthetic write failure")
	for _, tc := range []struct {
		name  string
		steps []bootstrapWriteStep
		want  []byte
		err   error
	}{
		{"partial_then_complete", []bootstrapWriteStep{{1, nil}, {2, nil}}, []byte{1, 2, 3}, nil},
		{"partial_then_error", []bootstrapWriteStep{{1, nil}, {1, failure}}, []byte{1, 2}, failure},
		{"zero_progress", []bootstrapWriteStep{{0, nil}}, nil, io.ErrShortWrite},
		{"negative_count", []bootstrapWriteStep{{-1, nil}}, nil, ErrProtocol},
		{"oversized_count", []bootstrapWriteStep{{4, nil}}, nil, ErrProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &bootstrapStepWriter{steps: tc.steps}
			if err := writeAll(w, []byte{1, 2, 3}); !errors.Is(err, tc.err) {
				t.Fatalf("write error = %v, want %v", err, tc.err)
			}
			if !bytes.Equal(w.output.Bytes(), tc.want) || w.calls != len(tc.steps) {
				t.Fatalf("output=%x calls=%d", w.output.Bytes(), w.calls)
			}
		})
	}
}
