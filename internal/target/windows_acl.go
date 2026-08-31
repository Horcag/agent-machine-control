package target

import (
	"errors"
	"fmt"
)

const (
	windowsACEAllow            uint8  = 0
	windowsACEDeny             uint8  = 1
	windowsACEObjectInherit    uint8  = 0x01
	windowsACEContainerInherit uint8  = 0x02
	windowsACEInherited        uint8  = 0x10
	windowsFullControl         uint32 = 0x001f01ff

	windowsLocalSystemSID    = "S-1-5-18"
	windowsAdministratorsSID = "S-1-5-32-544"
)

type windowsACLProof struct {
	Owner       string            `json:"owner"`
	CurrentUser string            `json:"current_user"`
	Protected   bool              `json:"protected"`
	Kind        PathKind          `json:"kind"`
	Entries     []windowsACEProof `json:"entries"`
}

type windowsACEProof struct {
	Type  uint8  `json:"type"`
	Flags uint8  `json:"flags"`
	Mask  uint32 `json:"mask"`
	SID   string `json:"sid"`
}

func validateWindowsACLProof(proof windowsACLProof) error {
	if proof.CurrentUser == "" || proof.Owner != proof.CurrentUser {
		return errors.New("target: protected path is not owned by the current Windows SID")
	}
	expectedFlags, expectedProtected, err := expectedWindowsACLShape(proof.Kind)
	if err != nil {
		return err
	}
	if proof.Protected != expectedProtected {
		return errors.New("target: protected path has unexpected DACL inheritance protection")
	}
	allowed := windowsAllowedTrusteeSIDs(proof.CurrentUser)
	if len(proof.Entries) != len(allowed) {
		return fmt.Errorf("target: protected path has %d ACEs, want exactly %d", len(proof.Entries), len(allowed))
	}
	expectedTrustees := make(map[string]struct{}, len(allowed))
	for _, sid := range allowed {
		expectedTrustees[sid] = struct{}{}
	}
	for _, entry := range proof.Entries {
		if entry.Type != windowsACEAllow {
			return fmt.Errorf("target: protected path has unsupported or deny ACE type %d", entry.Type)
		}
		if entry.Flags != expectedFlags {
			return fmt.Errorf("target: protected path ACE for %q has flags %#x, want %#x", entry.SID, entry.Flags, expectedFlags)
		}
		if entry.Mask != windowsFullControl {
			return fmt.Errorf("target: protected path ACE for %q has mask %#x, want FullControl", entry.SID, entry.Mask)
		}
		if _, expected := expectedTrustees[entry.SID]; !expected {
			return fmt.Errorf("target: protected path ACE grants unexpected or duplicate SID %q", entry.SID)
		}
		delete(expectedTrustees, entry.SID)
	}
	return nil
}

func windowsAllowedTrusteeSIDs(currentSID string) []string {
	candidates := [...]string{currentSID, windowsLocalSystemSID, windowsAdministratorsSID}
	allowed := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, sid := range candidates {
		if _, duplicate := seen[sid]; duplicate {
			continue
		}
		seen[sid] = struct{}{}
		allowed = append(allowed, sid)
	}
	return allowed
}

func expectedWindowsACLShape(kind PathKind) (uint8, bool, error) {
	switch kind {
	case PathDirectory:
		return windowsACEObjectInherit | windowsACEContainerInherit, true, nil
	case PathFile:
		return 0, true, nil
	case PathInheritedFile:
		return windowsACEInherited, false, nil
	default:
		return 0, false, fmt.Errorf("target: unsupported protected path kind %q", kind)
	}
}
