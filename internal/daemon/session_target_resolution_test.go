package daemon_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/daemon"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestDaemonSessionOpenCanonicalizesEquivalentEnrolledReferences(t *testing.T) {
	srv, endpoint, token, _, stateRoot, fakeSSH := setupTestDaemonWithSSHConfig(t, guestssh.SanitizerConfig{})
	defer func() { _ = srv.Shutdown(context.Background()) }()
	defer fakeSSH.Close()

	const key = "canonical-enrolled-open"
	var first daemon.SessionOpenResponse
	for _, reference := range []string{"", "default", "primary", daemonTestVMID, "local:" + daemonTestVMID} {
		status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
			Target: reference, Reason: "open enrolled authority", IdempotencyKey: key,
		})
		if status != http.StatusOK {
			t.Fatalf("open %q status=%d body=%s", reference, status, data)
		}
		var opened daemon.SessionOpenResponse
		if err := json.Unmarshal(data, &opened); err != nil {
			t.Fatal(err)
		}
		if opened.Session.Target != "local:"+daemonTestVMID {
			t.Fatalf("open %q target=%q", reference, opened.Session.Target)
		}
		if first.Session.SessionID == "" {
			first = opened
			continue
		}
		if opened.Session.SessionID != first.Session.SessionID || opened.Receipt == nil || first.Receipt == nil || opened.Receipt.ReceiptID != first.Receipt.ReceiptID {
			t.Fatalf("equivalent retry %q = %+v, first=%+v", reference, opened, first)
		}
	}

	status, _ := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
		Target: "d4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "different authority", IdempotencyKey: "different-enrolled-open",
	})
	if status != http.StatusConflict {
		t.Fatalf("different enrolled target status=%d, want 409", status)
	}

	store, err := target.NewStore(filepath.Join(stateRoot, "targets"))
	if err != nil {
		t.Fatal(err)
	}
	if publication, clearErr := store.Clear(context.Background()); clearErr != nil || !publication.Durable {
		t.Fatalf("clear target fixture: publication=%+v err=%v", publication, clearErr)
	}
	assertSessionLifecycleAfterTargetClear(t, endpoint, token, first.Session.SessionID)
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
		Reason: "reject open after target clear", IdempotencyKey: "cleared-enrolled-open",
	})
	if status != http.StatusConflict {
		t.Fatalf("open after clear status=%d, want 409", status)
	}
}

func assertSessionLifecycleAfterTargetClear(t *testing.T, endpoint, token, sessionID string) {
	t.Helper()
	sessionURL := endpoint + "/v1/sessions/" + sessionID
	requests := []struct {
		method string
		url    string
		body   any
		want   int
	}{
		{method: http.MethodGet, url: sessionURL, want: http.StatusOK},
		{method: http.MethodGet, url: endpoint + "/v1/sessions?machine=" + daemonTestVMID, want: http.StatusOK},
		{method: http.MethodPost, url: sessionURL + "/write", body: daemon.SessionWriteRequest{Data: "after-clear\n", Reason: "write after target clear", IdempotencyKey: "after-clear-write"}, want: http.StatusOK},
		{method: http.MethodPost, url: sessionURL + "/control", body: daemon.SessionControlRequest{Key: "enter", Reason: "control after target clear", IdempotencyKey: "after-clear-control"}, want: http.StatusOK},
		{method: http.MethodPost, url: sessionURL + "/wait", body: daemon.SessionWaitRequest{Regex: "after-clear", TimeoutMillis: 1000}, want: http.StatusOK},
		{method: http.MethodGet, url: sessionURL + "/read", want: http.StatusOK},
		{method: http.MethodPost, url: sessionURL + "/close", body: daemon.SessionCloseRequest{Reason: "close after target clear", IdempotencyKey: "after-clear-close"}, want: http.StatusOK},
	}
	for _, request := range requests {
		status, data := doJSONReq(t, request.method, request.url, token, request.body)
		if status != request.want {
			t.Fatalf("%s %s status=%d want=%d body=%s", request.method, request.url, status, request.want, data)
		}
	}
}
