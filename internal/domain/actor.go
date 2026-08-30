package domain

import (
	"fmt"
	"sort"
)

const (
	// MaxActorIDLength is the maximum allowed length of an actor identifier.
	MaxActorIDLength = 256
	// MinActorIDLength is the minimum allowed length of an actor identifier.
	MinActorIDLength = 1

	// Scopes for machine, audit, operation, and persistent sessions.
	ScopeMachineRead     = "machine:read"
	ScopeMachineWrite    = "machine:write"
	ScopeAuditRead       = "audit:read"
	ScopeOperationCancel = "operation:cancel"
	ScopeSessionRead     = "session:read"
	ScopeSessionWrite    = "session:write"
	ScopeSessionOpen     = "session:open"
	ScopeSessionClose    = "session:close"
	ScopeSessionAdmin    = "session:admin"
	ScopeEvidenceCapture = "evidence:sensitive:capture"
)

// ActorID is a strongly typed, validated principal identifier.
type ActorID string

// String returns the string representation of the ActorID.
func (a ActorID) String() string {
	return string(a)
}

// Validate checks if the ActorID is non-empty, bounded, and contains only valid characters.
func (a ActorID) Validate() error {
	return ValidateBoundedString(string(a), MinActorIDLength, MaxActorIDLength, ErrInvalidActorID)
}

// ScopeSet represents a set of granted authorization scopes/capabilities.
type ScopeSet map[string]struct{}

// NewScopeSet creates a ScopeSet from a list of scope strings.
func NewScopeSet(scopes ...string) ScopeSet {
	set := make(ScopeSet, len(scopes))
	for _, sc := range scopes {
		if sc != "" {
			set[sc] = struct{}{}
		}
	}
	return set
}

// Validate checks that every scope in the set is a valid canonical identifier.
func (s ScopeSet) Validate() error {
	for sc := range s {
		if err := ValidateScope(sc); err != nil {
			return err
		}
	}
	return nil
}

// Has returns true if the scope is present in the set.
func (s ScopeSet) Has(scope string) bool {
	if s == nil {
		return false
	}
	_, ok := s[scope]
	return ok
}

// IsSubsetOf returns true if every scope in s is present in other.
func (s ScopeSet) IsSubsetOf(other ScopeSet) bool {
	if len(s) == 0 {
		return true
	}
	if len(other) == 0 {
		return false
	}
	for k := range s {
		if !other.Has(k) {
			return false
		}
	}
	return true
}

// Clone returns a deep copy of the ScopeSet.
func (s ScopeSet) Clone() ScopeSet {
	if s == nil {
		return make(ScopeSet)
	}
	clone := make(ScopeSet, len(s))
	for k := range s {
		clone[k] = struct{}{}
	}
	return clone
}

// Slice returns a sorted slice of scopes in the set.
func (s ScopeSet) Slice() []string {
	if len(s) == 0 {
		return nil
	}
	res := make([]string, 0, len(s))
	for k := range s {
		res = append(res, k)
	}
	sort.Strings(res)
	return res
}

// ActorContext encapsulates the verified identity of the direct transport caller
// and the effective actor on whose behalf the operation is executed.
type ActorContext struct {
	AuthenticatedCaller  ActorID
	EffectiveActor       ActorID
	CallerPermissions    ScopeSet
	EffectivePermissions ScopeSet
}

// NewActorContext constructs a validated ActorContext.
func NewActorContext(caller, effective ActorID, callerPerms, effectivePerms ScopeSet) (ActorContext, error) {
	ctx := ActorContext{
		AuthenticatedCaller:  caller,
		EffectiveActor:       effective,
		CallerPermissions:    callerPerms.Clone(),
		EffectivePermissions: effectivePerms.Clone(),
	}
	if err := ctx.Validate(); err != nil {
		return ActorContext{}, err
	}
	return ctx, nil
}

// Clone returns a deep copy of the ActorContext.
func (ac ActorContext) Clone() ActorContext {
	return ActorContext{
		AuthenticatedCaller:  ac.AuthenticatedCaller,
		EffectiveActor:       ac.EffectiveActor,
		CallerPermissions:    ac.CallerPermissions.Clone(),
		EffectivePermissions: ac.EffectivePermissions.Clone(),
	}
}

// IsDelegated returns true if the effective actor differs from the authenticated caller.
func (ac ActorContext) IsDelegated() bool {
	return ac.AuthenticatedCaller != ac.EffectiveActor
}

// HasScope checks if the effective actor holds the requested scope.
func (ac ActorContext) HasScope(scope string) bool {
	return ac.EffectivePermissions.Has(scope)
}

// Validate ensures caller and effective identities are valid and effective permissions
// never exceed authenticated caller authority, regardless of delegation status.
func (ac ActorContext) Validate() error {
	if err := ac.AuthenticatedCaller.Validate(); err != nil {
		return fmt.Errorf("%w for authenticated caller: %v", ErrInvalidActorID, err)
	}
	if err := ac.EffectiveActor.Validate(); err != nil {
		return fmt.Errorf("%w for effective actor: %v", ErrInvalidActorID, err)
	}
	if err := ac.CallerPermissions.Validate(); err != nil {
		return fmt.Errorf("%w for caller permissions: %v", ErrInvalidScope, err)
	}
	if err := ac.EffectivePermissions.Validate(); err != nil {
		return fmt.Errorf("%w for effective permissions: %v", ErrInvalidScope, err)
	}
	// Scope authority invariant: effective permissions must always be a subset
	// of authenticated caller permissions, even when caller == effective actor.
	if !ac.EffectivePermissions.IsSubsetOf(ac.CallerPermissions) {
		return ErrDelegationExceedsAuthority
	}
	return nil
}
