package mcpadapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGuestInvalidDeadlineNoDispatch(t *testing.T) {
	adapter := NewAdapter("missing")
	result, _, err := adapter.GuestPrepare(t.Context(), nil, GuestInput{Deadline: "invalid"})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestGuestMCPStreamsLocalPayloadAndPublishesVerifiedBinary(t *testing.T) {
	payload := []byte{0, 255, 128, 1}
	var operations []string
	server := httptest.NewServer(guestMCPStreamHandler(t, payload, &operations))
	defer server.Close()
	a := &Adapter{client: client.New(server.URL, "synthetic-agent")}
	directory := t.TempDir()
	source := filepath.Join(directory, "payload.bin")
	if err := os.WriteFile(source, []byte("Write-Output 'synthetic'"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"prepare", "put", "get", "exec", "remove-owned"} {
		in := guestMCPCaseInput(operation, source, directory)
		result, receipt, err := callGuestMCP(a, t, in, operation)
		if in.Destination != "" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
			assertGuestMCPUnsupportedPublication(t, in.Destination, result, err)
			continue
		}
		if err != nil || result != nil || receipt.Receipt == nil {
			t.Fatalf("%s: %+v %v", operation, result, err)
		}
		if in.Destination != "" {
			got, err := os.ReadFile(in.Destination)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("verified publication %s: %v", operation, err)
			}
		}
	}
	expected := "prepare,put,get,exec,remove-owned"
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		expected = "prepare,put,remove-owned"
	}
	if strings.Join(operations, ",") != expected {
		t.Fatalf("unexpected dispatch %v", operations)
	}
}

func guestMCPStreamHandler(t *testing.T, payload []byte, operations *[]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-agent" {
			t.Error("agent credential lost")
		}
		p, _, err := daemon.DecodeGuestMetadata(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		input, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if p.Target != "synthetic-target" || p.Reason != "contract" || p.ApprovalID != "synthetic-grant" {
			t.Error("exact authority arguments lost")
		}
		if strings.Contains(fmt.Sprintf("%+v", p), "payload.bin") {
			t.Error("local source name escaped into metadata")
		}
		*operations = append(*operations, p.Operation)
		assertGuestMCPUploadBinding(t, p, input)
		output := []byte(nil)
		if p.Operation == "get" || p.Operation == "exec" {
			output = payload
		}
		writeGuestMCPFinal(t, w, p, output)
	}
}
func assertGuestMCPUploadBinding(t *testing.T, p app.GuestOperationParams, input []byte) {
	t.Helper()
	if p.Operation == "put" || p.Operation == "exec" {
		sum := sha256.Sum256(input)
		if p.Size != int64(len(input)) || p.SHA256 != hex.EncodeToString(sum[:]) {
			t.Error("whole payload binding lost")
		}
	}
}

func guestMCPCaseInput(operation, source, directory string) GuestInput {
	in := GuestInput{Target: "synthetic-target", Reason: "contract", ApprovalID: "synthetic-grant", IdempotencyKey: "request-" + operation, Deadline: time.Now().Add(time.Minute).Format(time.RFC3339Nano), MaxOutput: 64}
	if operation == "put" || operation == "exec" {
		in.Source = source
	}
	if operation == "get" || operation == "exec" {
		in.Destination = filepath.Join(directory, operation+".bin")
	}
	return in
}
func assertGuestMCPUnsupportedPublication(t *testing.T, destination string, result *mcp.CallToolResult, err error) {
	t.Helper()
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("unsupported publication was admitted: %+v %v", result, err)
	}
	if _, err = os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("unsupported publication created output: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil || len(entries) != 1 {
		t.Fatalf("unsupported publication leaked staging: %v %v", entries, err)
	}
}

func callGuestMCP(a *Adapter, t *testing.T, in GuestInput, operation string) (*mcp.CallToolResult, app.GuestOperationResult, error) {
	t.Helper()
	switch operation {
	case "prepare":
		return a.GuestPrepare(t.Context(), nil, in)
	case "put":
		return a.GuestPut(t.Context(), nil, in)
	case "get":
		return a.GuestGet(t.Context(), nil, in)
	case "exec":
		return a.GuestExec(t.Context(), nil, in)
	default:
		return a.GuestRemoveOwned(t.Context(), nil, in)
	}
}
func writeGuestMCPFinal(t *testing.T, w http.ResponseWriter, p app.GuestOperationParams, data []byte) {
	t.Helper()
	now := time.Now()
	sum := sha256.Sum256(data)
	receipt := domain.Receipt{ReceiptID: domain.ReceiptID("rcpt-" + strings.Repeat("a", 32)), Fingerprint: domain.Fingerprint("sha256:" + strings.Repeat("a", 64)), Outcome: domain.ExecutionOutcome{Status: domain.OutcomeSuccess}, OperationKind: domain.OperationKind("guest." + p.Operation), IdempotencyKey: p.IdempotencyKey, Target: "local:11111111-1111-4111-8111-111111111111", Actor: "agent:mcp-local", Class: domain.ClassDestructivePrivileged, EffectiveBackend: "ssh", StartedAt: now, CompletedAt: now, ObservationType: domain.ObservationObserved, RedactionStatus: domain.RedactionNotApplicable}
	result := app.GuestOperationResult{Receipt: &receipt, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	if p.Operation == "put" {
		result.Size = p.Size
		result.SHA256 = p.SHA256
	}
	final, _ := json.Marshal(daemon.GuestFinal{Result: result})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Trailer", daemon.GuestResultTrailer)
	if len(data) > 0 {
		if _, err := w.Write(data); err != nil {
			t.Error(err)
		}
	}
	w.Header().Set(daemon.GuestResultTrailer, base64.RawStdEncoding.EncodeToString(final))
}

func TestGuestMCPRejectsLocalContractFailuresBeforeDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid local arguments dispatched") }))
	defer server.Close()
	a := &Adapter{client: client.New(server.URL, "synthetic-agent")}
	deadline := time.Now().Add(time.Minute).Format(time.RFC3339Nano)
	for _, tc := range []struct{ name, operation, source, destination string }{
		{"get-needs-new-path", "get", "", ""}, {"prepare-no-output", "prepare", "", "unexpected"}, {"remove-no-source", "remove-owned", "unexpected", ""}, {"missing-put", "put", filepath.Join(t.TempDir(), "missing"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, err := callGuestMCP(a, t, GuestInput{Deadline: deadline, Source: tc.source, Destination: tc.destination}, tc.operation)
			if err != nil || r == nil || !r.IsError {
				t.Fatalf("result=%+v error=%v", r, err)
			}
		})
	}
}
