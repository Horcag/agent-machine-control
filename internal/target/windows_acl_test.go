package target

import "testing"

const testOwnerSID = "S-1-5-21-1000"

func exactWindowsACLProof(kind PathKind) windowsACLProof {
	flags := uint8(0)
	protected := true
	if kind == PathDirectory {
		flags = windowsACEObjectInherit | windowsACEContainerInherit
	}
	if kind == PathInheritedFile {
		flags = windowsACEInherited
		protected = false
	}
	return windowsACLProof{
		Owner:       testOwnerSID,
		CurrentUser: testOwnerSID,
		Protected:   protected,
		Kind:        kind,
		Entries: []windowsACEProof{
			{Type: windowsACEAllow, Flags: flags, Mask: windowsFullControl, SID: testOwnerSID},
			{Type: windowsACEAllow, Flags: flags, Mask: windowsFullControl, SID: windowsLocalSystemSID},
			{Type: windowsACEAllow, Flags: flags, Mask: windowsFullControl, SID: windowsAdministratorsSID},
		},
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
