package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func TestGuestFailureClassificationRedactsAndPrioritizesUncertainty(t *testing.T) {
	for _, tc := range []struct {
		err      error
		category string
	}{
		{fmt.Errorf("private-secret: %w", domain.ErrNonCanonicalParameter), "invalid_argument"},
		{&app.PolicyDeniedError{Reason: policy.DenialApprovalRequired, Message: "private-secret"}, "approval_required"},
		{app.ErrGuestWorkspaceUnprovisioned, "guest_workspace_unprovisioned"},
		{errors.Join(app.ErrGuestUncertain, domain.ErrNonCanonicalParameter), "guest_uncertain"},
		{errors.New("private-secret"), "guest_uncertain"},
	} {
		if got := guestFailureCategory(tc.err); got != tc.category || strings.Contains(got, "private") {
			t.Fatalf("category=%q want=%q", got, tc.category)
		}
	}
}

func TestGuestHTTPMalformedShapesRejectedByRealServiceWithoutDispatch(t *testing.T) {
	server, token, fixture := newGuestHTTPServiceFixture(t)
	for _, operation := range []string{"prepare", "put", "get", "exec", "remove-owned"} {
		t.Run(operation, func(t *testing.T) {
			p, payload := guestHTTPCase(operation, strings.Repeat("a", 32))
			switch operation {
			case "prepare", "put", "remove-owned":
				p.MaxOutput = 64
			case "get":
				p.ArtifactIDs = nil
			case "exec":
				p.MaxOutput = 16*1024*1024 + 1
			}
			for range 2 {
				final, body := invokeGuestHTTPFixture(t, server.URL, token, p, payload)
				if final.Category != "invalid_argument" || final.Error == "" || len(body) != 0 || len(fixture.operations) != 0 {
					t.Fatalf("final=%+v dispatch=%v", final, fixture.operations)
				}
			}
		})
	}
}

func TestGuestHTTPPreDispatchDenialsRemainTyped(t *testing.T) {
	for _, tc := range []struct {
		name, category string
		configure      func(*guestHTTPServiceFixture)
	}{
		{"workspace", "guest_workspace_unprovisioned", func(f *guestHTTPServiceFixture) {
			f.configErr = fmt.Errorf("private-secret: %w", app.ErrGuestWorkspaceUnprovisioned)
		}},
		{"approval", "approval_required", func(f *guestHTTPServiceFixture) { f.privileged = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, token, fixture := newGuestHTTPServiceFixture(t)
			tc.configure(fixture)
			p, payload := guestHTTPCase("prepare", "")
			for range 2 {
				final, body := invokeGuestHTTPFixture(t, server.URL, token, p, payload)
				encoded, _ := json.Marshal(final)
				if final.Category != tc.category || len(body) != 0 || len(fixture.operations) != 0 || strings.Contains(string(encoded), "private-secret") {
					t.Fatalf("final=%s dispatch=%v", encoded, fixture.operations)
				}
			}
		})
	}
}

func TestGuestLegacyHTTPEarlyFailuresUseStandardEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, category string
		status         int
		configure      func(*guestHTTPServiceFixture, *app.GuestOperationParams)
	}{
		{"invalid-prepare", "invalid_argument", 400, func(_ *guestHTTPServiceFixture, p *app.GuestOperationParams) { p.MaxOutput = 64 }},
		{"approval", "approval_required", 403, func(f *guestHTTPServiceFixture, _ *app.GuestOperationParams) { f.privileged = true }},
		{"workspace", "guest_workspace_unprovisioned", 403, func(f *guestHTTPServiceFixture, _ *app.GuestOperationParams) {
			f.configErr = app.ErrGuestWorkspaceUnprovisioned
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, token, fixture := newGuestHTTPServiceFixture(t)
			p, payload := guestHTTPCase("prepare", "")
			tc.configure(fixture, &p)
			response := guestHTTPFixtureResponse(t, server.URL, token, p, payload, false)
			defer response.Body.Close()
			var envelope ErrorEnvelope
			if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != tc.status || envelope.Error.Category != tc.category || response.Header.Get("Trailer") != "" || len(fixture.operations) != 0 {
				t.Fatalf("status=%d envelope=%+v operations=%v", response.StatusCode, envelope, fixture.operations)
			}
		})
	}
}

func TestGuestLegacyHTTPDispatchedFailureRetainsStrictTrailer(t *testing.T) {
	server, token, fixture := newGuestHTTPServiceFixture(t)
	fixture.prepareErr = errors.New("private-secret-transport-error")
	p, payload := guestHTTPCase("prepare", "")
	response := guestHTTPFixtureResponse(t, server.URL, token, p, payload, false)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.RawStdEncoding.DecodeString(response.Trailer.Get(GuestResultTrailer))
	if err != nil {
		t.Fatal(err)
	}
	// The installed legacy client has only these fields and rejects unknown fields.
	var legacy struct {
		Result app.GuestOperationResult `json:"result"`
		Error  string                   `json:"error,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&legacy); err != nil {
		t.Fatalf("legacy decoder: %v data=%s", err, data)
	}
	if response.StatusCode != 200 || len(body) != 0 || legacy.Error == "" || legacy.Result.Receipt == nil || legacy.Result.Receipt.Outcome.Status != domain.OutcomeFailed || strings.Contains(string(data), "private-secret") {
		t.Fatalf("status=%d legacy=%+v body=%q", response.StatusCode, legacy, body)
	}
}

func TestGuestEarlyHTTPErrorNeverRewritesStreamedOrExecutedOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, category string
		started        bool
		receipt        *domain.Receipt
	}{
		{"stream-started", "invalid_argument", true, nil},
		{"uncertain", "guest_uncertain", false, nil},
		{"failed-receipt", "invalid_argument", false, &domain.Receipt{Outcome: domain.ExecutionOutcome{Status: domain.OutcomeFailed}}},
		{"success-receipt", "invalid_argument", false, &domain.Receipt{Outcome: domain.ExecutionOutcome{Status: domain.OutcomeSuccess}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			if writeGuestEarlyError(response, tc.category, app.GuestOperationResult{Receipt: tc.receipt}, tc.started) || response.Body.Len() != 0 {
				t.Fatal("streamed/executed outcome rewritten as early rejection")
			}
		})
	}
}

func TestGuestLegacyRejectionStatusContractClearsStreamHeaders(t *testing.T) {
	for _, tc := range []struct {
		category string
		status   int
	}{
		{"invalid_argument", 400}, {"conflict", 409}, {"timeout", 504}, {"cancelled", 408},
		{"target_unavailable", 503}, {"machine_not_found", 404}, {"approval_required", 403},
	} {
		response := httptest.NewRecorder()
		response.Header().Set("Trailer", GuestResultTrailer)
		response.Header().Set(GuestResultTrailer, "discard-stream-metadata")
		denied := &domain.Receipt{Outcome: domain.ExecutionOutcome{Status: domain.OutcomeDenied}}
		if !writeGuestEarlyError(response, tc.category, app.GuestOperationResult{Receipt: denied}, false) {
			t.Fatal("denied receipt not recognized")
		}
		var envelope ErrorEnvelope
		if json.Unmarshal(response.Body.Bytes(), &envelope) != nil || response.Code != tc.status || envelope.Error.Category != tc.category || response.Header().Get("Trailer") != "" || response.Header().Get(GuestResultTrailer) != "" {
			t.Fatalf("response=%+v", response)
		}
	}
}
