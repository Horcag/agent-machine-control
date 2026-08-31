package app

import "github.com/Horcag/agent-machine-control/internal/domain"

// targetMutationPlan is the non-public Stage B handoff to a future backend adapter.
// Operation and both fingerprints are constructed from canonical identity before ProviderVMID is exposed.
type targetMutationPlan struct {
	Operation              domain.Operation
	Fingerprint            domain.Fingerprint
	IdempotencyFingerprint domain.Fingerprint
	ProviderVMID           string
}

func buildTargetMutationPlan(resolution TargetResolution, operation domain.Operation) (targetMutationPlan, error) {
	if err := resolution.Validate(); err != nil {
		return targetMutationPlan{}, err
	}
	operation.Target = domain.MachineRef(resolution.Locator.String())
	if err := operation.Validate(); err != nil {
		return targetMutationPlan{}, err
	}
	fingerprint, err := operation.Fingerprint()
	if err != nil {
		return targetMutationPlan{}, err
	}
	idempotencyFingerprint, err := domain.ComputeIdempotencyFingerprint(operation)
	if err != nil {
		return targetMutationPlan{}, err
	}
	return targetMutationPlan{
		Operation:              operation,
		Fingerprint:            fingerprint,
		IdempotencyFingerprint: idempotencyFingerprint,
		ProviderVMID:           resolution.ProviderVMID,
	}, nil
}
