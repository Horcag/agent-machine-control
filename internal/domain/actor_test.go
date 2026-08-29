package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestActorID_Validate(t *testing.T) {
	tests := []struct {
		name    string
		actorID domain.ActorID
		wantErr bool
	}{
		{
			name:    "valid user actor",
			actorID: domain.ActorID("user:alice"),
			wantErr: false,
		},
		{
			name:    "valid agent actor",
			actorID: domain.ActorID("agent:subagent-1"),
			wantErr: false,
		},
		{
			name:    "empty actor",
			actorID: domain.ActorID(""),
			wantErr: true,
		},
		{
			name:    "too long actor",
			actorID: domain.ActorID(strings.Repeat("a", 257)),
			wantErr: true,
		},
		{
			name:    "leading whitespace",
			actorID: domain.ActorID(" alice"),
			wantErr: true,
		},
		{
			name:    "trailing whitespace",
			actorID: domain.ActorID("alice "),
			wantErr: true,
		},
		{
			name:    "invalid utf-8 bytes",
			actorID: domain.ActorID("alice\xff\xfe"),
			wantErr: true,
		},
		{
			name:    "control character newline",
			actorID: domain.ActorID("alice\n"),
			wantErr: true,
		},
		{
			name:    "control character null",
			actorID: domain.ActorID("alice\x00"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.actorID.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("ActorID.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, domain.ErrInvalidActorID) {
				t.Errorf("ActorID.Validate() error = %v, want ErrInvalidActorID", err)
			}
		})
	}
}

func TestActorID_String(t *testing.T) {
	id := domain.ActorID("user:bob")
	if id.String() != "user:bob" {
		t.Errorf("ActorID.String() = %q, want %q", id.String(), "user:bob")
	}
}

func TestScopeSet_Basic(t *testing.T) {
	s := domain.NewScopeSet("machine:read", "machine:write")
	if !s.Has("machine:read") {
		t.Errorf("expected machine:read to be present")
	}
	if !s.Has("machine:write") {
		t.Errorf("expected machine:write to be present")
	}
	if s.Has("machine:delete") {
		t.Errorf("expected machine:delete to be absent")
	}

	slice := s.Slice()
	if len(slice) != 2 || slice[0] != "machine:read" || slice[1] != "machine:write" {
		t.Errorf("ScopeSet.Slice() unexpected: %v", slice)
	}
}

func TestScopeSet_SubsetsAndNil(t *testing.T) {
	s := domain.NewScopeSet("machine:read", "machine:write")
	var nilSet domain.ScopeSet
	if nilSet.Has("any") {
		t.Errorf("nil ScopeSet.Has should return false")
	}
	if nilSet.Slice() != nil {
		t.Errorf("nil ScopeSet.Slice should return nil")
	}
	if nilSet.Clone() == nil {
		t.Errorf("nil ScopeSet.Clone should return non-nil empty set")
	}

	sub := domain.NewScopeSet("machine:read")
	if !sub.IsSubsetOf(s) {
		t.Errorf("expected sub to be subset of s")
	}
	if s.IsSubsetOf(sub) {
		t.Errorf("expected s to not be subset of sub")
	}
	if s.IsSubsetOf(nil) {
		t.Errorf("expected s to not be subset of nil")
	}
	if domain.NewScopeSet().IsSubsetOf(s) != true {
		t.Errorf("empty set should be subset of s")
	}

	// Scope validation rejects invalid scopes
	invalidScopeSet := domain.NewScopeSet(" leading_space")
	if err := invalidScopeSet.Validate(); err == nil || !errors.Is(err, domain.ErrInvalidScope) {
		t.Errorf("expected ErrInvalidScope for leading space in scope, got %v", err)
	}
}

func TestActorContext_DirectAndSameActorInvariant(t *testing.T) {
	callerPerms := domain.NewScopeSet("machine:read", "machine:write")
	effectivePermsInvalid := domain.NewScopeSet("machine:read", "machine:admin")

	// Direct execution (same caller and effective) with identical permissions
	directCtx, err := domain.NewActorContext("user:admin", "user:admin", callerPerms, callerPerms)
	if err != nil {
		t.Fatalf("unexpected error for direct context: %v", err)
	}
	if directCtx.IsDelegated() {
		t.Errorf("direct context should not be delegated")
	}
	if !directCtx.HasScope("machine:read") {
		t.Errorf("direct context should have machine:read scope")
	}

	// Regression Test for Mandatory Repair #1: Same-actor escalation attempt
	// Authenticated caller and effective actor are the same, but effective permissions exceed caller permissions.
	_, err = domain.NewActorContext("user:operator", "user:operator", callerPerms, effectivePermsInvalid)
	if err == nil {
		t.Fatalf("expected ErrDelegationExceedsAuthority for same-actor escalation, got nil")
	}
	if !errors.Is(err, domain.ErrDelegationExceedsAuthority) {
		t.Errorf("expected ErrDelegationExceedsAuthority, got %v", err)
	}
}

func TestActorContext_DelegationAndInvalidActors(t *testing.T) {
	callerPerms := domain.NewScopeSet("machine:read", "machine:write")
	effectivePermsValid := domain.NewScopeSet("machine:read")
	effectivePermsInvalid := domain.NewScopeSet("machine:read", "machine:admin")

	// Valid delegation (effective permissions are subset of caller permissions)
	delegatedCtx, err := domain.NewActorContext("user:admin", "agent:runner", callerPerms, effectivePermsValid)
	if err != nil {
		t.Fatalf("unexpected error for valid delegated context: %v", err)
	}
	if !delegatedCtx.IsDelegated() {
		t.Errorf("delegated context should be delegated")
	}

	// Invalid delegation (effective permissions exceed caller permissions)
	_, err = domain.NewActorContext("user:operator", "agent:runner", callerPerms, effectivePermsInvalid)
	if err == nil {
		t.Fatalf("expected ErrDelegationExceedsAuthority, got nil")
	}
	if !errors.Is(err, domain.ErrDelegationExceedsAuthority) {
		t.Errorf("expected ErrDelegationExceedsAuthority, got %v", err)
	}

	// Invalid caller ID
	_, err = domain.NewActorContext("", "user:bob", callerPerms, effectivePermsValid)
	if err == nil || !errors.Is(err, domain.ErrInvalidActorID) {
		t.Errorf("expected ErrInvalidActorID for invalid caller, got %v", err)
	}

	// Invalid effective ID
	_, err = domain.NewActorContext("user:bob", "", callerPerms, effectivePermsValid)
	if err == nil || !errors.Is(err, domain.ErrInvalidActorID) {
		t.Errorf("expected ErrInvalidActorID for invalid effective actor, got %v", err)
	}

	// Invalid scope in caller permissions
	badCallerPerms := domain.NewScopeSet("bad scope with\ncontrol")
	_, err = domain.NewActorContext("user:alice", "user:alice", badCallerPerms, effectivePermsValid)
	if err == nil || !errors.Is(err, domain.ErrInvalidScope) {
		t.Errorf("expected ErrInvalidScope for bad caller perms, got %v", err)
	}
}

func TestActorContext_CloneImmutability(t *testing.T) {
	callerPerms := domain.NewScopeSet("machine:read", "machine:write")
	effectivePerms := domain.NewScopeSet("machine:read")

	ctx, err := domain.NewActorContext("user:alice", "agent:bot", callerPerms, effectivePerms)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Clone context
	cloned := ctx.Clone()

	// Mutate original callerPerms map
	callerPerms["machine:admin"] = struct{}{}
	if cloned.CallerPermissions.Has("machine:admin") {
		t.Errorf("cloned context mutated when original map was modified")
	}
}
