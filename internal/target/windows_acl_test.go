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

func permuteWindowsACLProof(proof windowsACLProof, order []int) windowsACLProof {
	entries := make([]windowsACEProof, len(order))
	for index, sourceIndex := range order {
		entries[index] = proof.Entries[sourceIndex]
	}
	proof.Entries = entries
	return proof
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

func TestValidateWindowsACLProofAcceptsTrustedInheritedFileOwners(t *testing.T) {
	for _, owner := range windowsAllowedTrusteeSIDs(testOwnerSID) {
		t.Run(owner, func(t *testing.T) {
			proof := exactWindowsACLProof(PathInheritedFile)
			proof.Owner = owner
			if err := validateWindowsACLProof(proof); err != nil {
				t.Fatalf("trusted inherited-file owner rejected: %v", err)
			}
		})
	}
}

func TestValidateWindowsACLProofRejectsForeignInheritedFileOwner(t *testing.T) {
	proof := exactWindowsACLProof(PathInheritedFile)
	proof.Owner = "S-1-5-21-2000"
	if err := validateWindowsACLProof(proof); err == nil {
		t.Fatalf("foreign inherited-file owner unexpectedly accepted: %+v", proof)
	}
}

func TestValidateWindowsACLProofRejectsTrustedNonCurrentProtectedOwners(t *testing.T) {
	for _, kind := range []PathKind{PathDirectory, PathFile} {
		for _, owner := range []string{windowsLocalSystemSID, windowsAdministratorsSID} {
			t.Run(string(kind)+"/"+owner, func(t *testing.T) {
				proof := exactWindowsACLProof(kind)
				proof.Owner = owner
				if err := validateWindowsACLProof(proof); err == nil {
					t.Fatalf("protected path accepted non-current trusted owner: %+v", proof)
				}
			})
		}
	}
}

func TestValidateWindowsACLProofAcceptsExactLocalSystemForms(t *testing.T) {
	for _, kind := range []PathKind{PathDirectory, PathFile, PathInheritedFile} {
		t.Run(string(kind), func(t *testing.T) {
			proof := exactWindowsACLProofForCurrent(kind, windowsLocalSystemSID)
			if len(proof.Entries) != 2 {
				t.Fatalf("LocalSystem entries = %d, want exactly 2", len(proof.Entries))
			}
			if err := validateWindowsACLProof(proof); err != nil {
				t.Fatalf("exact LocalSystem proof rejected: %v", err)
			}
		})
	}
}

func TestValidateWindowsACLProofAcceptsACEPermutations(t *testing.T) {
	tests := map[string]struct {
		proof  windowsACLProof
		orders [][]int
	}{
		"ordinary user": {
			proof: exactWindowsACLProof(PathDirectory),
			orders: [][]int{
				{0, 1, 2}, {0, 2, 1}, {1, 0, 2},
				{1, 2, 0}, {2, 0, 1}, {2, 1, 0},
			},
		},
		"LocalSystem": {
			proof:  exactWindowsACLProofForCurrent(PathDirectory, windowsLocalSystemSID),
			orders: [][]int{{0, 1}, {1, 0}},
		},
	}
	for name, test := range tests {
		for index, order := range test.orders {
			t.Run(name, func(t *testing.T) {
				proof := permuteWindowsACLProof(test.proof, order)
				if err := validateWindowsACLProof(proof); err != nil {
					t.Fatalf("ACE permutation %d rejected: %v", index, err)
				}
			})
		}
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
