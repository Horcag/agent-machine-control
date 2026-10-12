package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/statedir"
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

func TestGuestDirectRecoveryIndependent(t *testing.T) {
	var out, err bytes.Buffer
	code := runGuest(context.Background(), nil, true, "missing", []string{"prepare"}, &out, &err)
	if code != ExitBackendUnavailable || out.Len() != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), err.String())
	}
}
func TestGuestRejectsUnknownOperationBeforeDiscovery(t *testing.T) {
	var out, err bytes.Buffer
	code := runGuest(context.Background(), nil, false, "missing", []string{"adopt-arbitrary-root"}, &out, &err)
	if code != ExitUsage {
		t.Fatalf("code=%d err=%s", code, err.String())
	}
}

func TestGuestExecExitCodePreservesVerifiedResult(t *testing.T) {
	for _, code := range []int{0, 37, -1, 256} {
		var out bytes.Buffer
		result := app.GuestOperationResult{ExitCode: code}
		got := finishGuestCLI(&out, "exec", result)
		want := code
		if code < 0 || code > 255 {
			want = 1
		}
		if got != want {
			t.Fatalf("native code=%d CLI code=%d want=%d", code, got, want)
		}
		var serialized app.GuestOperationResult
		if json.Unmarshal(out.Bytes(), &serialized) != nil || serialized.ExitCode != code {
			t.Fatal("verified execution result was relabeled")
		}
	}
}

func TestGuestCLIDiscoveryStreamsExactAuthorityAndMapsFailure(t *testing.T) {
	var captured []app.GuestOperationParams
	server := httptest.NewServer(guestCLIFailureHandler(t, &captured))
	defer server.Close()
	state := guestCLIState(t, server.URL)
	directory := t.TempDir()
	source := filepath.Join(directory, "script.ps1")
	if err := os.WriteFile(source, []byte("Write-Output 'synthetic'"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"prepare", "put", "get", "exec", "remove-owned"} {
		deadline := time.Now().Add(time.Minute).Format(time.RFC3339Nano)
		args := []string{operation, "--target", "synthetic-target", "--reason", "contract", "--approval-id", "synthetic-grant", "--idempotency-key", "request-" + operation, "--deadline", deadline, "--max-output", "64"}
		if operation == "put" || operation == "exec" {
			args = append(args, "--source", source)
		}
		if operation == "get" || operation == "exec" {
			args = append(args, "--destination", filepath.Join(directory, operation+".bin"))
		}
		var out, stderr bytes.Buffer
		if code := runGuest(t.Context(), nil, false, state, args, &out, &stderr); code == ExitSuccess || out.Len() != 0 {
			t.Fatalf("%s returned success or output after failure: %d %s", operation, code, out.String())
		}
	}
	expectedDispatches := 5
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		expectedDispatches = 3
	}
	if len(captured) != expectedDispatches {
		t.Fatalf("whole CLI dispatches=%d", len(captured))
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("failed downloads leaked outputs or stages: %v %v", entries, err)
	}
}
func guestCLIFailureHandler(t *testing.T, captured *[]app.GuestOperationParams) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		p, _, err := daemon.DecodeGuestMetadata(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if p.Target != "synthetic-target" || p.Reason != "contract" || p.ApprovalID != "synthetic-grant" || strings.Contains(fmt.Sprintf("%+v", p), "script.ps1") {
			t.Error("exact authority or filename privacy violated")
		}
		if p.Operation == "put" || p.Operation == "exec" {
			sum := sha256.Sum256(data)
			if p.Size != int64(len(data)) || p.SHA256 != hex.EncodeToString(sum[:]) {
				t.Error("source binding lost")
			}
		}
		*captured = append(*captured, p)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Trailer", daemon.GuestResultTrailer)
		w.WriteHeader(200)
		data, _ = json.Marshal(daemon.GuestFinal{Error: "synthetic denial"})
		w.Header().Set(daemon.GuestResultTrailer, base64.RawStdEncoding.EncodeToString(data))
	}
}

func guestCLIState(t *testing.T, endpoint string) string {
	t.Helper()
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if _, err = auth.LoadOrCreate(sd.AuthDir(), auth.WithPrincipalResolver(func() (string, error) { return "operator:synthetic", nil })); err != nil {
		t.Fatal(err)
	}
	if err = daemon.WriteEndpointFile(sd.DaemonDir(), daemon.EndpointRecord{SchemaVersion: "1", PID: os.Getpid(), RuntimeID: "synthetic-runtime", StartedAt: time.Now(), Endpoint: endpoint}); err != nil {
		t.Fatal(err)
	}
	return sd.Root()
}

type guestConfirmPrompt bool

func (p guestConfirmPrompt) PromptConfirmation(string) bool { return bool(p) }
func TestGuestCLIApprovalConfirmationAndExactBeneficiary(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/guest/approvals" {
			t.Error("wrong approval route")
		}
		var req daemon.GuestApprovalRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Beneficiary != "self" || req.ValidForMillis != 30000 || req.Params.Operation != "prepare" || req.Params.Reason != "contract" || req.Params.IdempotencyKey != "request-approval" {
			t.Error("approval arguments changed")
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(daemon.GuestApprovalResponse{}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	state := guestCLIState(t, server.URL)
	args := []string{"approve", "prepare", "--target", "synthetic-target", "--reason", "contract", "--idempotency-key", "request-approval", "--valid-for", "30s", "--beneficiary", "self"}
	var out, stderr bytes.Buffer
	if code := runGuest(t.Context(), guestConfirmPrompt(false), false, state, args, &out, &stderr); code != ExitDenied || calls != 0 {
		t.Fatalf("confirmation bypass: %d %d", code, calls)
	}
	if code := runGuest(t.Context(), guestConfirmPrompt(true), false, state, args, &out, &stderr); code != ExitSuccess || calls != 1 {
		t.Fatalf("confirmed approval: %d %d %s", code, calls, stderr.String())
	}
}
func TestGuestCLIInvalidContracts(t *testing.T) {
	for _, args := range [][]string{nil, {"approve"}, {"prepare", "--unknown"}, {"prepare", "--deadline", "invalid"}, {"prepare", "unexpected"}} {
		var out, stderr bytes.Buffer
		if code := runGuest(t.Context(), nil, false, "missing", args, &out, &stderr); code != ExitUsage {
			t.Fatalf("%v code=%d", args, code)
		}
	}
	cl := client.New("http://127.0.0.1:1", "synthetic")
	for _, tc := range []struct{ operation, destination string }{{"get", ""}, {"prepare", "unexpected"}} {
		if _, err := executeGuestCLI(t.Context(), cl, app.GuestOperationParams{Operation: tc.operation}, nil, tc.destination); err == nil {
			t.Fatal("invalid local destination accepted")
		}
	}
	var out, stderr bytes.Buffer
	if code := approveGuestCLI(t.Context(), cl, guestConfirmPrompt(true), app.GuestOperationParams{}, "invalid", "self", &out, &stderr); code != ExitUsage {
		t.Fatal("invalid approval dispatched")
	}
}
