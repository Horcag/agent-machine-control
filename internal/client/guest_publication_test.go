package client

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func TestGuestPublicationUsesOriginalHeldObjectAfterStagingReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows exclusive staging handles reject replacement; native platform tests verify sharing")
	}
	original := []byte("verified original output")
	cl, _ := guestFixture(t, original, "valid")
	cl.guestBeforePublication = func(file *os.File) error {
		path := file.Name()
		if err := os.Rename(path, filepath.Join(filepath.Dir(path), "renamed-owned")); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("foreign replacement"), 0600)
	}
	directory := t.TempDir()
	destination := filepath.Join(directory, "result")
	_, err := cl.GuestOperationToFile(t.Context(), app.GuestOperationParams{Operation: "get", IdempotencyKey: "synthetic-request", MaxOutput: 100, Deadline: time.Now().Add(time.Second)}, nil, destination)
	// The simulated foreign pending file is preserved and reported during cleanup.
	if err == nil {
		t.Fatal("foreign staging cleanup was silently claimed")
	}
	actual, readErr := os.ReadFile(destination)
	if readErr != nil || !bytes.Equal(actual, original) {
		t.Fatalf("publication selected foreign source: %q %v", actual, readErr)
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		owned, err := os.ReadDir(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if len(owned) != 1 || owned[0].Name() != "pending" {
			t.Fatalf("owned renamed temp leaked or foreign temp deleted: %v", owned)
		}
	}
}
