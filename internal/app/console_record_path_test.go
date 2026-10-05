package app

import (
	"encoding/json"
	"errors"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
	"os"
	"path/filepath"
	"testing"
)

func recordingPathDecoy(t *testing.T, id string) (*ConsoleService, recordingStatusDocument, string, []byte) {
	t.Helper()
	s, doc, _ := recordingStoreFixture(t)
	doc.Status.RecordingID = id
	path := filepath.Join(s.recordingDirectory(), id)
	if err := statedir.EnsurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(path, "00.json")
	if err := os.WriteFile(snapshot, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := target.NewPrivatePathSecurity().ProtectNewFile(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	return s, doc, path, data
}

func TestRecordingPersistenceRejectsNoncanonicalPathBeforeAccess(t *testing.T) {
	for _, id := range []string{"../outside", "a123456789abcdef0123456789abcdeF"} {
		t.Run(id, func(t *testing.T) {
			s, doc, path, data := recordingPathDecoy(t, id)
			if _, err := s.readRecordingStatus(t.Context(), id); !errors.Is(err, ErrRecordingStatusInconclusive) {
				t.Fatalf("read invalid ID: %v", err)
			}
			doc.Sequence = 1
			if err := s.writeRecordingStatus(t.Context(), doc); !errors.Is(err, ErrRecordingStatusInconclusive) {
				t.Fatalf("write invalid ID: %v", err)
			}
			if _, err := os.Stat(filepath.Join(path, "01.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid path written: %v", err)
			}
			parent, err := os.OpenRoot(s.recordingDirectory())
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			if err := s.removeExpiredRecording(t.Context(), parent, id, 0); !errors.Is(err, ErrRecordingStatusInconclusive) {
				t.Fatalf("cleanup invalid ID: %v", err)
			}
			actual, err := os.ReadFile(filepath.Join(path, "00.json"))
			if err != nil || string(actual) != string(data) {
				t.Fatalf("invalid path changed: %v", err)
			}
		})
	}
}
