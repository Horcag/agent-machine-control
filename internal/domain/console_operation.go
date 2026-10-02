package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ConsoleInputParameters binds every input field without persisting typed secrets.
func ConsoleInputParameters(input ConsoleInput) map[string]any {
	data, _ := json.Marshal(input)
	digest := sha256.Sum256(data)
	return map[string]any{"kind": input.Kind, "input_sha256": hex.EncodeToString(digest[:])}
}

func validateConsoleInputParams(params map[string]any) error {
	if len(params) != 2 {
		return fmt.Errorf("%w: console input requires kind and input_sha256", ErrNonCanonicalParameter)
	}
	switch params["kind"] {
	case "key", "type", "move", "click", "drag":
	default:
		return ErrNonCanonicalParameter
	}
	value, ok := params["input_sha256"].(string)
	if !ok || len(value) != 64 {
		return ErrNonCanonicalParameter
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ErrNonCanonicalParameter
		}
	}
	return nil
}
