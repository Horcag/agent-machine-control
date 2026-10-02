package app

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/target"
)

type labReservationSecurity struct {
	target.Security
	protect func() error
}

func (s labReservationSecurity) ProtectNewFile(context.Context, string) error { return s.protect() }

func TestConsoleLabReservationProtectionFailureRemovesOnlyOwnedFile(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "replacement"}[replaced], func(t *testing.T) {
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			security := labReservationSecurity{protect: failedLabProtection(root, replaced)}
			if file, _, err := reserveProtectedLabFile(context.Background(), root, dir, "grant.json", security); err == nil || file != nil {
				t.Fatalf("unprotected reservation accepted: %v", err)
			}
			if replaced {
				assertLabReplacement(t, root)
			} else if _, err := root.Lstat("grant.json"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed reservation retained: %v", err)
			}
		})
	}
}

func TestConsoleLabReservationReplacementAfterProtectionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	security := labReservationSecurity{protect: func() error {
		return replaceLabReservation(root)
	}}
	if file, _, err := reserveProtectedLabFile(context.Background(), root, dir, "grant.json", security); !errors.Is(err, ErrInvalidConsoleLabGrant) || file != nil {
		t.Fatalf("replaced reservation accepted: %v", err)
	}
	assertLabReplacement(t, root)
}

func replaceLabReservation(root *os.Root) error {
	if err := root.Rename("grant.json", "owned.json"); err != nil {
		return err
	}
	return root.WriteFile("grant.json", []byte("replacement"), 0600)
}

func assertLabReplacement(t *testing.T, root *os.Root) {
	t.Helper()
	data, err := root.ReadFile("grant.json")
	if err != nil || string(data) != "replacement" {
		t.Fatalf("replacement removed or changed: %q, %v", data, err)
	}
}

func failedLabProtection(root *os.Root, replaced bool) func() error {
	return func() error {
		if replaced {
			if err := replaceLabReservation(root); err != nil {
				return err
			}
		}
		return errors.New("ACL proof denied")
	}
}
