package mcpadapter

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Use a child process because stdin's blocked decoder belongs to the process
// lifetime. The parent deliberately retains its pipe writer until the child exits.
func TestRunStdioIdlePeerShutdown(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"signal", "context", "pre-canceled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestRunStdioShutdownChild$")
			cmd.Env = append(os.Environ(), "AMC_TEST_STDIO_SHUTDOWN="+mode)
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if mode != "pre-canceled" {
				_, err = io.WriteString(stdin, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{},\"clientInfo\":{\"name\":\"synthetic\",\"version\":\"1\"}}}\n{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n")
			}
			waitErr := cmd.Wait() // Reaps the exact child, including deadline failures.
			if err != nil || waitErr != nil || ctx.Err() != nil {
				t.Fatalf("shutdown with peer writer open: write=%v wait=%v context=%v\n%s", err, waitErr, ctx.Err(), output.String())
			}
		})
	}
}

func TestRunStdioShutdownChild(t *testing.T) {
	mode := os.Getenv("AMC_TEST_STDIO_SHUTDOWN")
	if mode == "" {
		return
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	signals := make(chan os.Signal, 1)
	server := NewAdapter("").BuildServer()
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, request)
			if method == "notifications/initialized" {
				if mode == "signal" {
					signals <- syscall.SIGINT
				} else {
					cancel()
				}
			}
			return result, err
		}
	})
	if mode == "pre-canceled" {
		cancel()
	}
	code := runStdio(ctx, server, signals, cancel, io.Discard)
	if code != 0 && (mode != "pre-canceled" || code != 2) {
		t.Fatalf("unexpected shutdown code %d", code)
	}
}
