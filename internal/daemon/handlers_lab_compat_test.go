package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

// Matches the installed client's strict schema before persistent authority.
type legacyLabGrant struct {
	AcknowledgeExternalEffects bool              `json:"acknowledge_external_effects"`
	SchemaVersion              int               `json:"schema_version"`
	GrantID                    string            `json:"grant_id"`
	Target                     domain.MachineRef `json:"target"`
	EnrollmentIdentity         string            `json:"enrollment_identity"`
	Issuer                     domain.ActorID    `json:"issuer"`
	Beneficiary                domain.ActorID    `json:"beneficiary"`
	Reason                     string            `json:"reason"`
	IdempotencyKey             string            `json:"idempotency_key"`
	IssuedAt                   time.Time         `json:"issued_at"`
	ExpiresAt                  time.Time         `json:"expires_at"`
}

func TestLabGrantModeWireNegotiationPreservesLegacyStrictClients(t *testing.T) {
	endpoint, operator, agent := setupConsoleDaemon(t, &consoleDaemonBackend{mockDaemonBackend: &mockDaemonBackend{}})
	request := app.ConsoleLabGrantIssueRequest{Mode: app.ConsoleLabModePersistentVM, Target: "default", Reason: "persistent compatibility fixture", IdempotencyKey: "wire-compatibility", Beneficiary: "agent:mcp-local", AcknowledgeExternalEffects: true}
	status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/desktop/lab/issue", operator, request)
	var issued struct {
		Grant   legacyLabGrant `json:"grant"`
		Receipt domain.Receipt `json:"receipt"`
	}
	strictLabDecode(t, status, body, &issued)
	if issued.Grant.GrantID == "" || !issued.Grant.ExpiresAt.IsZero() {
		t.Fatalf("legacy issue: %+v", issued.Grant)
	}
	for _, route := range []string{"status", "active"} {
		payload := map[string]string{"grant_id": issued.Grant.GrantID}
		if route == "active" {
			payload = map[string]string{"target": "default"}
		}
		status, body = doJSONReq(t, http.MethodPost, endpoint+"/v1/desktop/lab/"+route, agent, payload)
		var legacy struct {
			Grant legacyLabGrant `json:"grant"`
			State string         `json:"state"`
		}
		strictLabDecode(t, status, body, &legacy)
		if legacy.State != "active" || legacy.Grant.GrantID != issued.Grant.GrantID {
			t.Fatalf("legacy %s: %+v", route, legacy)
		}
	}
	modern := client.New(endpoint, agent)
	active, err := modern.ActiveConsoleLabGrant(t.Context(), "default")
	if err != nil || active.Grant.Mode != app.ConsoleLabModePersistentVM || active.Grant.GrantID != issued.Grant.GrantID {
		t.Fatalf("modern active: %+v %v", active, err)
	}
	grant, err := modern.ConsoleLabGrantStatus(t.Context(), issued.Grant.GrantID)
	if err != nil || grant.Grant.Mode != app.ConsoleLabModePersistentVM {
		t.Fatalf("modern status: %+v %v", grant, err)
	}
	replay, err := client.New(endpoint, operator).IssueConsoleLabGrant(t.Context(), request)
	if err != nil || replay.Grant.Mode != app.ConsoleLabModePersistentVM || replay.Grant.GrantID != issued.Grant.GrantID {
		t.Fatalf("modern issue replay: %+v %v", replay, err)
	}
}

func strictLabDecode(t *testing.T, status int, body []byte, out any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil || status != http.StatusOK {
		t.Fatalf("strict legacy status=%d body=%s error=%v", status, body, err)
	}
}
