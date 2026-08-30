package domain_test

import (
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestValidationSession_IDsAndKeys(t *testing.T) {
	// SessionID
	sessID := "sess-0123456789abcdef0123456789abcdef"
	if err := domain.ValidateSessionID(sessID); err != nil {
		t.Errorf("expected valid session ID, got: %v", err)
	}
	if err := domain.ValidateSessionID("invalid"); err == nil {
		t.Errorf("expected error on invalid session ID")
	}

	// ControlKeys
	keys := []string{"ctrl-c", "ctrl-d", "enter", "tab", "backspace", "escape", "up", "down", "left", "right"}
	for _, k := range keys {
		if err := domain.ValidateControlKey(k); err != nil {
			t.Errorf("expected valid key %s, got: %v", k, err)
		}
		norm, err := domain.NormalizeControlKey(k)
		if err != nil || string(norm) != k {
			t.Errorf("unexpected normalized key for %s: %s", k, norm)
		}
	}

	if err := domain.ValidateControlKey("invalid-key"); err == nil {
		t.Errorf("expected error on invalid control key")
	}
	if _, err := domain.NormalizeControlKey("invalid-key"); err == nil {
		t.Errorf("expected error on normalize invalid control key")
	}
}

func TestValidationSession_Operations(t *testing.T) {
	// 1. session.open validation
	openOp := domain.Operation{
		Kind:                "session.open",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               makeTestActorContext(),
		Reason:              "open valid session",
		Deadline:            time.Now().Add(time.Hour),
		IdempotencyKey:      "idem-1",
		RequiredCapability:  domain.CapabilitySessionOpen,
		RequiredScopes:      []string{domain.ScopeSessionOpen},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"cols": 80,
			"rows": 24,
			"term": "xterm-256color",
		},
	}
	if err := domain.ValidateOperationParameters(openOp.Kind, openOp.Parameters); err != nil {
		t.Fatalf("expected valid session.open op parameters, got: %v", err)
	}

	// Unexpected overrides (key_alias/user should now be rejected)
	badAliasOp := openOp
	badAliasOp.Parameters = map[string]any{"key_alias": "default"}
	if err := domain.ValidateOperationParameters(badAliasOp.Kind, badAliasOp.Parameters); err == nil {
		t.Errorf("expected error on key_alias override in open parameters")
	}

	badUserOp := openOp
	badUserOp.Parameters = map[string]any{"user": "admin"}
	if err := domain.ValidateOperationParameters(badUserOp.Kind, badUserOp.Parameters); err == nil {
		t.Errorf("expected error on user override in open parameters")
	}

	// Invalid dimensions
	badColsOp := openOp
	badColsOp.Parameters = map[string]any{"cols": 5}
	if err := domain.ValidateOperationParameters(badColsOp.Kind, badColsOp.Parameters); err == nil {
		t.Errorf("expected error on cols < 20")
	}

	badRowsOp := openOp
	badRowsOp.Parameters = map[string]any{"rows": 500}
	if err := domain.ValidateOperationParameters(badRowsOp.Kind, badRowsOp.Parameters); err == nil {
		t.Errorf("expected error on rows > 200")
	}

	// Unexpected param
	badParamOp := openOp
	badParamOp.Parameters = map[string]any{"unexpected": "val"}
	if err := domain.ValidateOperationParameters(badParamOp.Kind, badParamOp.Parameters); err == nil {
		t.Errorf("expected error on unexpected open parameter")
	}

	// 2. session.write validation
	sessID := "sess-0123456789abcdef0123456789abcdef"
	writeOp := openOp
	writeOp.Kind = "session.write"
	writeOp.RequiredCapability = domain.CapabilitySessionWrite
	writeOp.RequiredScopes = []string{domain.ScopeSessionWrite}
	writeOp.Parameters = map[string]any{
		"session_id": sessID,
		"data":       "ls\n",
	}
	if err := domain.ValidateOperationParameters(writeOp.Kind, writeOp.Parameters); err != nil {
		t.Fatalf("expected valid session.write op parameters, got: %v", err)
	}

	// Digested write parameters
	digestWriteOp := openOp
	digestWriteOp.Kind = "session.write"
	digestWriteOp.RequiredCapability = domain.CapabilitySessionWrite
	digestWriteOp.RequiredScopes = []string{domain.ScopeSessionWrite}
	digestWriteOp.Parameters = map[string]any{
		"session_id":  sessID,
		"data_sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"data_length": 10,
	}
	if err := domain.ValidateOperationParameters(digestWriteOp.Kind, digestWriteOp.Parameters); err != nil {
		t.Fatalf("expected valid session.write digested parameters, got: %v", err)
	}

	badWriteOp := writeOp
	badWriteOp.Parameters = map[string]any{
		"session_id": sessID,
		"data":       string([]byte{0xff, 0xfe}), // Invalid UTF-8
	}
	if err := domain.ValidateOperationParameters(badWriteOp.Kind, badWriteOp.Parameters); err == nil {
		t.Errorf("expected error on invalid UTF-8 bytes in session.write")
	}

	// 3. session.control validation
	ctrlOp := openOp
	ctrlOp.Kind = "session.control"
	ctrlOp.RequiredCapability = domain.CapabilitySessionControl
	ctrlOp.RequiredScopes = []string{domain.ScopeSessionWrite}
	ctrlOp.Parameters = map[string]any{
		"session_id": sessID,
		"key":        "ctrl-c",
	}
	if err := domain.ValidateOperationParameters(ctrlOp.Kind, ctrlOp.Parameters); err != nil {
		t.Fatalf("expected valid session.control op parameters, got: %v", err)
	}

	// 4. session.close validation
	closeOp := openOp
	closeOp.Kind = "session.close"
	closeOp.RequiredCapability = domain.CapabilitySessionClose
	closeOp.RequiredScopes = []string{domain.ScopeSessionClose}
	closeOp.Parameters = map[string]any{
		"session_id": sessID,
	}
	if err := domain.ValidateOperationParameters(closeOp.Kind, closeOp.Parameters); err != nil {
		t.Fatalf("expected valid session.close op parameters, got: %v", err)
	}
}

func TestValidationSession_ReadWaitListShow(t *testing.T) {
	sessID := "sess-0123456789abcdef0123456789abcdef"
	baseOp := domain.Operation{
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               makeTestActorContext(),
		Reason:              "test",
		Deadline:            time.Now().Add(time.Hour),
		IdempotencyKey:      "idem-1",
		RequiredCapability:  domain.CapabilityMachineInspect,
		RequiredScopes:      []string{domain.ScopeMachineRead},
		Classification:      domain.ClassObserve,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	// session.read
	readOp := baseOp
	readOp.Kind = "session.read"
	readOp.Parameters = map[string]any{
		"session_id":  sessID,
		"after_seq":   10,
		"limit_bytes": 1024,
		"timeout_ms":  1000,
	}
	if err := domain.ValidateOperationParameters(readOp.Kind, readOp.Parameters); err != nil {
		t.Errorf("expected valid session.read, got: %v", err)
	}

	// session.wait
	waitOp := baseOp
	waitOp.Kind = "session.wait"
	waitOp.Parameters = map[string]any{
		"session_id":      sessID,
		"settle_ms":       500,
		"regex":           "pattern",
		"after_seq":       10,
		"timeout_seconds": 30,
	}
	if err := domain.ValidateOperationParameters(waitOp.Kind, waitOp.Parameters); err != nil {
		t.Errorf("expected valid session.wait, got: %v", err)
	}

	// session.list
	listOp := baseOp
	listOp.Kind = "session.list"
	listOp.Parameters = map[string]any{
		"machine": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		"limit":   100,
	}
	if err := domain.ValidateOperationParameters(listOp.Kind, listOp.Parameters); err != nil {
		t.Errorf("expected valid session.list, got: %v", err)
	}

	// session.show
	showOp := baseOp
	showOp.Kind = "session.show"
	showOp.Parameters = map[string]any{
		"session_id": sessID,
	}
	if err := domain.ValidateOperationParameters(showOp.Kind, showOp.Parameters); err != nil {
		t.Errorf("expected valid session.show, got: %v", err)
	}
}

func TestValidationSession_MutationErrorBranches(t *testing.T) {
	sessID := "sess-0123456789abcdef0123456789abcdef"

	if err := domain.ValidateOperationParameters("session.write", map[string]any{}); err == nil {
		t.Errorf("expected error on missing session_id in session.write")
	}
	if err := domain.ValidateOperationParameters("session.write", map[string]any{"session_id": sessID}); err == nil {
		t.Errorf("expected error on missing data in session.write")
	}
	if err := domain.ValidateOperationParameters("session.write", map[string]any{"session_id": sessID, "data": 123}); err == nil {
		t.Errorf("expected error on non-string data in session.write")
	}
	if err := domain.ValidateOperationParameters("session.write", map[string]any{"session_id": sessID, "data": "ok", "bad": "x"}); err == nil {
		t.Errorf("expected error on unexpected parameter in session.write")
	}
	if err := domain.ValidateOperationParameters("session.control", map[string]any{"session_id": sessID}); err == nil {
		t.Errorf("expected error on missing key in session.control")
	}
	if err := domain.ValidateOperationParameters("session.control", map[string]any{"session_id": sessID, "key": "bad-key"}); err == nil {
		t.Errorf("expected error on invalid key in session.control")
	}
	if err := domain.ValidateOperationParameters("session.control", map[string]any{"session_id": sessID, "key": "ctrl-c", "extra": 1}); err == nil {
		t.Errorf("expected error on extra param in session.control")
	}
	if err := domain.ValidateOperationParameters("session.close", map[string]any{"session_id": sessID, "force": true}); err == nil {
		t.Errorf("expected removed force parameter to be rejected in session.close")
	}
	if err := domain.ValidateOperationParameters("session.close", map[string]any{"session_id": sessID, "unexpected": "x"}); err == nil {
		t.Errorf("expected error on unexpected param in session.close")
	}
}

func TestValidationSession_ObservationErrorBranches(t *testing.T) {
	sessID := "sess-0123456789abcdef0123456789abcdef"

	if err := domain.ValidateOperationParameters("session.read", nil); err == nil {
		t.Errorf("expected error on nil session.read params")
	}
	if err := domain.ValidateOperationParameters("session.wait", map[string]any{"session_id": sessID, "settle_ms": -5}); err == nil {
		t.Errorf("expected error on negative settle_ms")
	}
	if err := domain.ValidateOperationParameters("session.wait", map[string]any{"session_id": sessID, "timeout_seconds": -5}); err == nil {
		t.Errorf("expected error on negative timeout_seconds")
	}
	if err := domain.ValidateOperationParameters("session.wait", map[string]any{"session_id": sessID, "after_seq": -5}); err == nil {
		t.Errorf("expected error on negative after_seq")
	}
	if err := domain.ValidateOperationParameters("session.list", map[string]any{"machine": "bad"}); err == nil {
		t.Errorf("expected error on invalid machine guid in session.list")
	}
	if err := domain.ValidateOperationParameters("session.list", map[string]any{"unexpected": "x"}); err == nil {
		t.Errorf("expected error on unexpected param in session.list")
	}
	if err := domain.ValidateOperationParameters("session.show", map[string]any{"session_id": sessID, "unexpected": "x"}); err == nil {
		t.Errorf("expected error on unexpected param in session.show")
	}
}

func TestMachineCapabilities_SessionCaps(t *testing.T) {
	caps := domain.SessionCapabilities()
	if !caps.Has(domain.CapabilitySessionOpen) ||
		!caps.Has(domain.CapabilitySessionWrite) ||
		!caps.Has(domain.CapabilitySessionControl) ||
		!caps.Has(domain.CapabilitySessionClose) {
		t.Errorf("missing expected session capabilities in SessionCapabilities()")
	}
}

func TestDomain_GeneratorsAndHelpers(t *testing.T) {
	sID, err := domain.GenerateSessionID()
	if err != nil || sID == "" {
		t.Errorf("failed GenerateSessionID: %v", err)
	}
	rID, err := domain.GenerateReceiptID()
	if err != nil || rID == "" {
		t.Errorf("failed GenerateReceiptID: %v", err)
	}
	idemKey := domain.DeriveApprovalIdempotencyKey("idem-test-key")
	if idemKey == "" {
		t.Errorf("expected non-empty DeriveApprovalIdempotencyKey")
	}

	now := time.Now().UTC()
	appr := domain.Approval{
		ID:         domain.ApprovalID("appr-1234567890abcdef1234567890abcdef"),
		ConsumedAt: &now,
	}
	apprClone := appr.Clone()
	if apprClone.ID != appr.ID || apprClone.ConsumedAt == nil {
		t.Errorf("expected cloned approval to match")
	}

	opRec := domain.OperationRecord{
		ID:         "op-123",
		Parameters: map[string]any{"k": "v"},
	}
	opClone := opRec.Clone()
	if opClone.ID != opRec.ID || opClone.Parameters["k"] != "v" {
		t.Errorf("expected cloned operation record to match")
	}
}

func TestDomain_ParameterValidations(t *testing.T) {
	sID := "sess-0123456789abcdef0123456789abcdef"

	err := domain.ValidateOperationParameters("session.open", map[string]any{
		"cols": float64(80),
		"rows": float64(24),
		"term": "xterm-256color",
	})
	if err != nil {
		t.Errorf("expected valid float64 cols/rows, got: %v", err)
	}

	err = domain.ValidateOperationParameters("session.read", map[string]any{
		"session_id":  sID,
		"after_seq":   float64(10),
		"limit_bytes": float64(1024),
	})
	if err != nil {
		t.Errorf("expected valid float64 after_seq/limit_bytes, got: %v", err)
	}

	err = domain.ValidateOperationParameters("session.wait", map[string]any{
		"session_id":      sID,
		"settle_ms":       float64(200),
		"timeout_seconds": float64(10),
		"after_seq":       float64(5),
		"regex":           "test-prompt",
	})
	if err != nil {
		t.Errorf("expected valid session.wait params, got: %v", err)
	}

	err = domain.ValidateOperationParameters("session.control", map[string]any{
		"session_id": sID,
		"key":        "ctrl-c",
	})
	if err != nil {
		t.Errorf("expected valid session.control params, got: %v", err)
	}

	err = domain.ValidateOperationParameters("session.show", map[string]any{
		"session_id": sID,
	})
	if err != nil {
		t.Errorf("expected valid session.show params, got: %v", err)
	}

	err = domain.ValidateOperationParameters("session.close", map[string]any{
		"session_id": sID,
	})
	if err != nil {
		t.Errorf("expected valid session.close params, got: %v", err)
	}
}

func makeTestActorContext() domain.ActorContext {
	perms := domain.NewScopeSet(
		domain.ScopeSessionOpen,
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
		domain.ScopeSessionClose,
		domain.ScopeMachineRead,
		"evidence:sensitive",
	)
	return domain.ActorContext{
		AuthenticatedCaller:  "test-caller",
		EffectiveActor:       "test-caller",
		CallerPermissions:    perms,
		EffectivePermissions: perms,
	}
}
