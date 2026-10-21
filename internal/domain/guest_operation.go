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
	if err != nil {
		return ErrNonCanonicalParameter
	}
	limit, err := guestParameterInt(params["max_output"])
	if err != nil {
		return ErrNonCanonicalParameter
	}
	ids, ok := params["artifact_ids"].([]string)
	if !ok {
		return ErrNonCanonicalParameter
	}
	op, ok := params["operation"].(string)
	if !ok || !IsGuestOperation(OperationKind("guest."+op)) {
		return ErrNonCanonicalParameter
	}
	return ValidateGuestOperationShape(op, size, limit, ids)
}

func validateGuestArtifactIDs(ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if !canonicalHex(id, 32) || seen[id] {
			return ErrNonCanonicalParameter
		}
		seen[id] = true
	}
	return nil
}

// ValidateGuestOperationShape applies the shared bounds and artifact contract before I/O.
func ValidateGuestOperationShape(op string, size, limit int64, ids []string) error {
	if size < 0 || size > 1<<40 || limit < 0 || limit > 1<<40 || len(ids) > 128 {
		return ErrNonCanonicalParameter
	}
	if err := validateGuestArtifactIDs(ids); err != nil {
		return err
	}
	return validateGuestShape(op, size, limit, len(ids))
}

func validateGuestShape(op string, size, limit int64, number int) error {
	valid := false
	switch op {
	case "prepare":
		valid = size == 0 && limit == 0 && number == 0
	case "put":
		valid = limit == 0 && number == 0
	case "exec":
		valid = validGuestCommandShape(size, limit, number)
	case "get":
		valid = size == 0 && limit > 0 && number == 1
	case "remove-owned":
		valid = size == 0 && limit == 0 && number > 0
	}
	if !valid {
		return ErrNonCanonicalParameter
	}
	return nil
}
func validGuestCommandShape(size, limit int64, number int) bool {
	return size <= 16*1024*1024 && limit > 0 && limit <= 16*1024*1024 && number == 0
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
