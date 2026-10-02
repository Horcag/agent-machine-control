package target

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestEnrollmentIdentityRejectsCorruptedProtectedDocuments(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "invalid schema", payload: []byte(`{"schema_version":999}`)},
		{name: "oversized", payload: make([]byte, MaxDocumentBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := testDirectory(t)
			path := prepareProtectedDocumentFixture(t, directory)
			if err := os.WriteFile(path, test.payload, 0600); err != nil {
				t.Fatal(err)
			}
			value, identity, err := testStore(t, directory).EnrollmentIdentity(context.Background())
			if !errors.Is(err, ErrInvalidDocument) || identity != "" || value.Locator.HostID != "" || value.Locator.VMID != "" {
				t.Fatalf("invalid authority returned: %+v, %q, %v", value, identity, err)
			}
		})
	}
}
