//go:build linux

package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func guestPublicationFixture(t *testing.T) (*os.File, string) {
	t.Helper()
	directory := t.TempDir()
	file, err := createGuestStagingFile(filepath.Join(directory, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	if _, err := file.WriteString("verified payload"); err != nil {
		t.Fatal(err)
	}
	return file, directory
}

func TestGuestPublicationHeldObjectSurvivesPathReplacement(t *testing.T) {
	file, directory := guestPublicationFixture(t)
	original, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	// Keep a link to the original object: Linux cannot relink a fully unlinked
	// inode through procfs. A substituted staging name must never be published.
	if err := os.Rename(file.Name(), filepath.Join(directory, "held")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.Name(), []byte("substituted"), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "download")
	if err := publishGuestFileHandle(context.Background(), file, destination); err != nil {
		t.Fatal(err)
	}
	installed, err := os.Stat(destination)
	if err != nil || !os.SameFile(original, installed) {
		t.Fatalf("published different object: %v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "verified payload" {
		t.Fatalf("published %q: %v", data, err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("caller handle was closed: %v", err)
	}
	substituted, err := os.ReadFile(file.Name())
	if err != nil || string(substituted) != "substituted" {
		t.Fatal("publication modified substituted staging entry")
	}
}

func TestGuestPublicationCreateNewConcurrent(t *testing.T) {
	file, directory := guestPublicationFixture(t)
	destination := filepath.Join(directory, "download")
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Go(func() { results <- publishGuestFileHandle(context.Background(), file, destination) })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("create-new successes = %d", successes)
	}
	if err := os.WriteFile(destination, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishGuestFileHandle(context.Background(), file, destination); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing destination: %v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "existing" {
		t.Fatalf("destination overwritten: %q %v", data, err)
	}
}

type guestExpiredContext struct{ context.Context }

func (guestExpiredContext) Deadline() (time.Time, bool) { return time.Unix(1, 0), true }

func TestGuestPublicationRejectsUnsafeDestinationsAndDeadline(t *testing.T) {
	file, directory := guestPublicationFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(directory, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "victim"), filepath.Join(directory, "existing-link")); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name        string
		ctx         context.Context
		destination string
	}{
		{"symlink-parent", context.Background(), filepath.Join(directory, "alias", "download")},
		{"symlink-leaf", context.Background(), filepath.Join(directory, "existing-link")},
		{"traversal", context.Background(), directory + "/../download"},
		{"dot-segment", context.Background(), directory + "/./download"},
		{"empty-segment", context.Background(), directory + "//download"},
		{"canceled", canceled, filepath.Join(directory, "canceled")},
		{"expired-before-timer", guestExpiredContext{context.Background()}, filepath.Join(directory, "expired")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := publishGuestFileHandle(tc.ctx, file, tc.destination); err == nil {
				t.Fatal("unsafe publication accepted")
			}
		})
	}
	for _, path := range []string{filepath.Join(outside, "download"), filepath.Join(outside, "victim"), filepath.Join(directory, "canceled"), filepath.Join(directory, "expired")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("failure created %s: %v", path, err)
		}
	}
}

func TestGuestPublicationUnlinkedSourceFailsClosed(t *testing.T) {
	file, directory := guestPublicationFixture(t)
	if err := os.Remove(file.Name()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file.Name(), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "download")
	if err := publishGuestFileHandle(context.Background(), file, destination); err == nil {
		t.Fatal("unlinked source unexpectedly published")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("replacement was published: %v", err)
	}
}

func TestGuestStagingCreateNewPrivate(t *testing.T) {
	file, _ := guestPublicationFixture(t)
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("staging permissions: %v %v", info, err)
	}
	if second, err := createGuestStagingFile(file.Name()); !errors.Is(err, os.ErrExist) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("staging did not require create-new: %v", err)
	}
}

func TestGuestPublicationDestinationNormalization(t *testing.T) {
	normalized, err := guestPublicationDestination("download")
	if err != nil || !filepath.IsAbs(normalized) || filepath.Base(normalized) != "download" {
		t.Fatalf("relative destination: %q %v", normalized, err)
	}
	for _, raw := range []string{"", "/", "./download", "../download", "nested/../download", "nested//download", "nested/", "bad\x00name"} {
		if _, err := guestPublicationDestination(raw); err == nil {
			t.Fatalf("malformed destination normalized: %q", raw)
		}
	}
}
