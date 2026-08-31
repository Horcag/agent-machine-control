package target

import "testing"

const testOwnerSID = "S-1-5-21-1000"

func exactWindowsACLProof(kind PathKind) windowsACLProof {
	return exactWindowsACLProofForCurrent(kind, testOwnerSID)
}

func exactWindowsACLProofForCurrent(kind PathKind, currentSID string) windowsACLProof {
	flags := uint8(0)
	protected := true
	if kind == PathDirectory {
		flags = windowsACEObjectInherit | windowsACEContainerInherit
	}
	if kind == PathInheritedFile {
		flags = windowsACEInherited
		protected = false
	}
	entries := make([]windowsACEProof, 0, 3)
	for _, sid := range windowsAllowedTrusteeSIDs(currentSID) {
		entries = append(entries, windowsACEProof{
			Type: windowsACEAllow, Flags: flags, Mask: windowsFullControl, SID: sid,
		})
	}
	return windowsACLProof{
		Owner:       currentSID,
		CurrentUser: currentSID,
		Protected:   protected,
		Kind:        kind,
		Entries:     entries,
	}
}

func TestValidateWindowsACLProofAcceptsOnlyExactDirectoryAndFileForms(t *testing.T) {
	for _, kind := range []PathKind{PathDirectory, PathFile, PathInheritedFile} {
		t.Run(string(kind), func(t *testing.T) {
			if err := validateWindowsACLProof(exactWindowsACLProof(kind)); err != nil {
				t.Fatalf("exact proof rejected: %v", err)
			}
		})
	}

	mutations := map[string]func(*windowsACLProof){
		"foreign owner": func(proof *windowsACLProof) { proof.Owner = "S-1-5-21-2000" },
		"unprotected":   func(proof *windowsACLProof) { proof.Protected = false },
		"deny":          func(proof *windowsACLProof) { proof.Entries[0].Type = windowsACEDeny },
		"unsupported":   func(proof *windowsACLProof) { proof.Entries[0].Type = 5 },
		"inherited":     func(proof *windowsACLProof) { proof.Entries[0].Flags |= windowsACEInherited },
		"extra": func(proof *windowsACLProof) {
			proof.Entries = append(proof.Entries, windowsACEProof{Type: windowsACEAllow, Mask: windowsFullControl, SID: "S-1-1-0"})
		},
		"weak":              func(proof *windowsACLProof) { proof.Entries[0].Mask-- },
		"duplicate":         func(proof *windowsACLProof) { proof.Entries[1].SID = proof.Entries[0].SID },
		"missing":           func(proof *windowsACLProof) { proof.Entries = proof.Entries[:2] },
		"wrong inheritance": func(proof *windowsACLProof) { proof.Entries[0].Flags = windowsACEObjectInherit },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			proof := exactWindowsACLProof(PathDirectory)
			mutate(&proof)
			if err := validateWindowsACLProof(proof); err == nil {
				t.Fatalf("mutated proof unexpectedly accepted: %+v", proof)
			}
		})
	}

	wrongShapes := map[string]windowsACLProof{
		"unsupported path kind": func() windowsACLProof {
			proof := exactWindowsACLProof(PathFile)
			proof.Kind = PathKind("device")
			return proof
		}(),
		"file inheritable": func() windowsACLProof {
			proof := exactWindowsACLProof(PathFile)
			proof.Entries[0].Flags = windowsACEObjectInherit | windowsACEContainerInherit
			return proof
		}(),
		"inherited file missing inherited flag": func() windowsACLProof {
			proof := exactWindowsACLProof(PathInheritedFile)
			proof.Entries[0].Flags = 0
			return proof
		}(),
		"inherited file protected": func() windowsACLProof {
			proof := exactWindowsACLProof(PathInheritedFile)
			proof.Protected = true
			return proof
		}(),
	}
	for name, proof := range wrongShapes {
		t.Run(name, func(t *testing.T) {
			if err := validateWindowsACLProof(proof); err == nil {
				t.Fatalf("wrong ACL shape unexpectedly accepted: %+v", proof)
			}
		})
	}
}

func TestValidateWindowsACLProofAcceptsExactLocalSystemForms(t *testing.T) {
	for _, kind := range []PathKind{PathDirectory, PathFile, PathInheritedFile} {
		t.Run(string(kind), func(t *testing.T) {
			proof := exactWindowsACLProofForCurrent(kind, windowsLocalSystemSID)
			if len(proof.Entries) != 2 {
				t.Fatalf("LocalSystem entries = %d, want exactly 2", len(proof.Entries))
			}
			if proof.Entries[0].SID != windowsLocalSystemSID || proof.Entries[1].SID != windowsAdministratorsSID {
				t.Fatalf("LocalSystem trustees = %+v, want ordered SYSTEM and Administrators", proof.Entries)
			}
			if err := validateWindowsACLProof(proof); err != nil {
				t.Fatalf("exact LocalSystem proof rejected: %v", err)
			}
		})
	}
}

func TestValidateWindowsACLProofRejectsDuplicateAndExtraLocalSystemEntries(t *testing.T) {
	tests := map[string]windowsACLProof{
		"duplicate": func() windowsACLProof {
			proof := exactWindowsACLProofForCurrent(PathFile, windowsLocalSystemSID)
			proof.Entries[1] = proof.Entries[0]
			return proof
		}(),
		"extra": func() windowsACLProof {
			proof := exactWindowsACLProofForCurrent(PathFile, windowsLocalSystemSID)
			proof.Entries[1].SID = "S-1-1-0"
			return proof
		}(),
	}
	for name, proof := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateWindowsACLProof(proof); err == nil {
				t.Fatalf("invalid LocalSystem proof unexpectedly accepted: %+v", proof)
			}
		})
	}
}
