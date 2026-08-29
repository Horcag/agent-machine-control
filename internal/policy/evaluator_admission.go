package policy

import (
	"errors"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func validateOperationAndActor(input EvaluationInput) (domain.Fingerprint, Decision, bool) {
	if input.Operation.Deadline.IsZero() {
		return "", Decision{
			Type:          DecisionDeny,
			DenialReason:  DenialInvalidOperation,
			DenialMessage: "missing or zero operation deadline",
		}, false
	}
	if !input.Now.Before(input.Operation.Deadline) {
		return "", Decision{
			Type:          DecisionDeny,
			DenialReason:  DenialDeadlinePassed,
			DenialMessage: "operation deadline has passed prior to admission",
		}, false
	}
	if input.Operation.Actor.AuthenticatedCaller == "" {
		return "", Decision{
			Type:          DecisionDeny,
			DenialReason:  DenialUnauthenticated,
			DenialMessage: "unauthenticated caller",
		}, false
	}
	if err := input.Operation.Actor.Validate(); err != nil {
		if errors.Is(err, domain.ErrDelegationExceedsAuthority) {
			return "", Decision{
				Type:          DecisionDeny,
				DenialReason:  DenialDelegationExceeded,
				DenialMessage: "effective actor permissions exceed authenticated caller authority",
			}, false
		}
		return "", Decision{
			Type:          DecisionDeny,
			DenialReason:  DenialInvalidActor,
			DenialMessage: "invalid actor identifier or context",
		}, false
	}
	if err := input.Operation.Validate(); err != nil {
		return "", handleOperationStructuralError(input.Operation.Classification, err), false
	}
	fp, err := input.Operation.Fingerprint()
	if err != nil {
		return "", Decision{
			Type:          DecisionDeny,
			DenialReason:  DenialInvalidOperation,
			DenialMessage: "failed to compute canonical operation fingerprint",
		}, false
	}
	return fp, Decision{}, true
}

func handleOperationStructuralError(class domain.OperationClass, err error) Decision {
	switch {
	case errors.Is(err, domain.ErrMissingIdempotencyKey):
		return Decision{
			Type:           DecisionDeny,
			EffectiveClass: class,
			DenialReason:   DenialMissingIdempotencyKey,
			DenialMessage:  "missing or invalid idempotency key for mutating operation",
		}
	case errors.Is(err, domain.ErrMissingReason):
		return Decision{
			Type:           DecisionDeny,
			EffectiveClass: class,
			DenialReason:   DenialMissingReason,
			DenialMessage:  "missing actor reason for mutating operation",
		}
	default:
		return Decision{
			Type:          DecisionDeny,
			DenialReason:  DenialInvalidOperation,
			DenialMessage: "operation failed structural validation",
		}
	}
}

func checkCapabilityRequirements(input EvaluationInput) (Decision, bool) {
	reqCap := input.Operation.RequiredCapability
	if reqCap == "" {
		return Decision{}, true
	}

	if err := domain.ValidateCapability(reqCap); err != nil {
		return Decision{
			Type:          DecisionDeny,
			DenialReason:  DenialInvalidOperation,
			DenialMessage: "invalid capability identifier",
		}, false
	}

	if input.AvailableCapabilities != nil {
		if err := input.AvailableCapabilities.Validate(); err != nil {
			return Decision{
				Type:          DecisionDeny,
				DenialReason:  DenialInvalidOperation,
				DenialMessage: "invalid available capability in policy input",
			}, false
		}
	}

	if !input.AvailableCapabilities.Has(reqCap) {
		return Decision{
			Type:          DecisionDeny,
			DenialReason:  DenialMissingCapability,
			DenialMessage: "target backend lacks required capability",
		}, false
	}

	return Decision{}, true
}

func isSensitiveScope(scope string, sensitiveSet domain.ScopeSet) bool {
	if scope == DefaultSensitiveEvidenceScope {
		return true
	}
	if sensitiveSet != nil && sensitiveSet.Has(scope) {
		return true
	}
	return false
}

func hasSensitiveScope(actor domain.ActorContext, sensitiveSet domain.ScopeSet) bool {
	if actor.HasScope(DefaultSensitiveEvidenceScope) {
		return true
	}
	for sc := range sensitiveSet {
		if actor.HasScope(sc) {
			return true
		}
	}
	return false
}

func checkScopeRequirements(input EvaluationInput) (Decision, bool) {
	// Check each explicitly required scope
	for _, sc := range input.Operation.RequiredScopes {
		if !input.Operation.Actor.HasScope(sc) {
			if isSensitiveScope(sc, input.SensitiveEvidenceScopes) {
				return Decision{
					Type:          DecisionDeny,
					DenialReason:  DenialMissingSensitiveEvidenceScope,
					DenialMessage: "effective actor lacks required sensitive-evidence scope",
				}, false
			}
			return Decision{
				Type:          DecisionDeny,
				DenialReason:  DenialMissingScope,
				DenialMessage: "effective actor lacks required authorization scope",
			}, false
		}
	}

	// Check explicit EvidenceSensitivity metadata
	if input.Operation.EvidenceSensitivity.IsSensitive() && !hasSensitiveScope(input.Operation.Actor, input.SensitiveEvidenceScopes) {
		return Decision{
			Type:          DecisionDeny,
			DenialReason:  DenialMissingSensitiveEvidenceScope,
			DenialMessage: "effective actor lacks required sensitive-evidence capture scope",
		}, false
	}

	return Decision{}, true
}

func evaluateMutationPrerequisites(input EvaluationInput) (Decision, bool) {
	if !input.AuditWritable {
		return Decision{
			Type:           DecisionDeny,
			EffectiveClass: input.Operation.Classification,
			DenialReason:   DenialAuditUnwritable,
			DenialMessage:  "cannot admit mutating operation when audit storage is unwritable",
		}, false
	}
	return Decision{}, true
}
