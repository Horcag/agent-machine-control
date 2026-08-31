package daemon

import (
	"context"
	"testing"
)

func TestOperationMachineFilterCanonicalIdentityDoesNotRequireLiveTarget(t *testing.T) {
	const vmID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	server := &Server{}
	for _, reference := range []string{vmID, "local:" + vmID} {
		canonical, err := server.normalizeOperationMachineFilter(context.Background(), reference)
		if err != nil || canonical != "local:"+vmID {
			t.Fatalf("normalize %q = %q, %v", reference, canonical, err)
		}
	}
}
