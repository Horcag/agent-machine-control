package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const CapabilityDesktopAction = "desktop.action"

// DesktopActionParameters records only the action and exact payload digest.
func DesktopActionParameters(req DesktopRequest) map[string]any {
	data, _ := json.Marshal(req)
	digest := sha256.Sum256(data)
	return map[string]any{"action": req.Action, "payload_sha256": hex.EncodeToString(digest[:])}
}

func canonicalHex(value any, size int) bool {
	s, ok := value.(string)
	if !ok || len(s) != size {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validateDesktopActionParams(params map[string]any) error {
	if len(params) != 2 || !canonicalHex(params["payload_sha256"], 64) {
		return ErrNonCanonicalParameter
	}
	switch params["action"] {
	case "provision", "remove", "window.focus", "window.move", "window.resize", "window.close", "window.minimize", "window.maximize", "window.restore", "uia.invoke", "uia.setvalue", "scroll", "clipboard.set", "launch":
		return nil
	default:
		return ErrNonCanonicalParameter
	}
}

func validateConsoleLabIssueParams(params map[string]any) error {
	if len(params) != 4 || !canonicalHex(params["grant_id"], 32) || !canonicalHex(params["enrollment_identity"], 64) {
		return ErrNonCanonicalParameter
	}
	beneficiary, ok := params["beneficiary"].(string)
	if !ok || beneficiary == "" || len(beneficiary) > 256 {
		return ErrNonCanonicalParameter
	}
	expires, ok := params["expires_at"].(string)
	if !ok {
		return ErrNonCanonicalParameter
	}
	t, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || t.Format(time.RFC3339Nano) != expires {
		return ErrNonCanonicalParameter
	}
	return nil
}

func validateConsoleLabRevokeParams(params map[string]any) error {
	if len(params) != 1 || !canonicalHex(params["grant_id"], 32) {
		return ErrNonCanonicalParameter
	}
	return nil
}
