package domain

// Guest operation capabilities reuse session authority while identifying whole
// guest operations independently from interactive terminal chunks.
const CapabilityGuestOperation = "guest.operation"

func IsGuestOperation(kind OperationKind) bool {
	switch kind {
	case "guest.prepare", "guest.put", "guest.get", "guest.exec", "guest.remove-owned":
		return true
	default:
		return false
	}
}

func GuestOperationScopes(kind OperationKind) []string {
	switch kind {
	case "guest.prepare":
		return []string{ScopeSessionOpen}
	case "guest.exec":
		return []string{ScopeSessionWrite, ScopeSessionRead, ScopeEvidenceCapture}
	case "guest.get":
		return []string{ScopeSessionRead, ScopeEvidenceCapture}
	default:
		return []string{ScopeSessionWrite}
	}
}

func validateGuestOperationParams(params map[string]any) error {
	if len(params) != 6 || !canonicalHex(params["configuration_sha256"], 64) || !canonicalHex(params["payload_sha256"], 64) {
		return ErrNonCanonicalParameter
	}
	size, err := guestParameterInt(params["size"])
	if err != nil || size < 0 || int64(size) > 1<<40 {
		return ErrNonCanonicalParameter
	}
	limit, err := guestParameterInt(params["max_output"])
	if err != nil || limit < 0 || int64(limit) > 1<<40 {
		return ErrNonCanonicalParameter
	}
	ids, ok := params["artifact_ids"].([]string)
	if !ok || len(ids) > 128 {
		return ErrNonCanonicalParameter
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !canonicalHex(id, 32) || seen[id] {
			return ErrNonCanonicalParameter
		}
		seen[id] = true
	}
	op, ok := params["operation"].(string)
	if !ok || !IsGuestOperation(OperationKind("guest."+op)) {
		return ErrNonCanonicalParameter
	}
	switch op {
	case "prepare":
		if size != 0 || limit != 0 || len(ids) != 0 {
			return ErrNonCanonicalParameter
		}
	case "put":
		if limit != 0 || len(ids) != 0 {
			return ErrNonCanonicalParameter
		}
	case "exec":
		if size > 16*1024*1024 || limit == 0 || limit > 16*1024*1024 || len(ids) != 0 {
			return ErrNonCanonicalParameter
		}
	case "get":
		if size != 0 || limit == 0 || len(ids) != 1 {
			return ErrNonCanonicalParameter
		}
	case "remove-owned":
		if size != 0 || limit != 0 || len(ids) == 0 {
			return ErrNonCanonicalParameter
		}
	}
	return nil
}

func guestParameterInt(value any) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	default:
		return 0, ErrNonCanonicalParameter
	}
}
