//go:build unix

package target

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestVerifiedDarwinVarAlias(t *testing.T) {
	t.Run("accepts only cleaned var resolving to private var", func(t *testing.T) {
		var evaluated string
		canonical, allowed, err := verifiedDarwinVarAlias("/var/.", func(path string) (string, error) {
			evaluated = path
			return "/private/var/.", nil
		})
		if err != nil || !allowed || canonical != "/private/var" {
			t.Fatalf("verifiedDarwinVarAlias = %q, %t, %v", canonical, allowed, err)
		}
		if evaluated != "/var" {
			t.Fatalf("EvalSymlinks path = %q, want /var", evaluated)
		}
	})

	t.Run("rejects other paths without evaluation", func(t *testing.T) {
		canonical, allowed, err := verifiedDarwinVarAlias("/tmp/var", func(string) (string, error) {
			t.Fatal("unexpected EvalSymlinks call")
			return "", nil
		})
		if err != nil || allowed || canonical != "" {
			t.Fatalf("verifiedDarwinVarAlias = %q, %t, %v", canonical, allowed, err)
		}
	})

	t.Run("rejects unexpected target", func(t *testing.T) {
		_, allowed, err := verifiedDarwinVarAlias("/var", func(string) (string, error) { return "/tmp/var", nil })
		if err == nil || allowed || !strings.Contains(err.Error(), "unexpected Darwin /var target") {
			t.Fatalf("verifiedDarwinVarAlias error = %v, allowed = %t", err, allowed)
		}
	})

	t.Run("returns evaluation error", func(t *testing.T) {
		want := errors.New("synthetic evaluation failure")
		_, allowed, err := verifiedDarwinVarAlias("/var", func(string) (string, error) { return "", want })
		if !errors.Is(err, want) || allowed {
			t.Fatalf("verifiedDarwinVarAlias error = %v, allowed = %t", err, allowed)
		}
	})
}

func TestValidateNoSymlinkComponentsContinuesFromVerifiedAlias(t *testing.T) {
	t.Run("continues from canonical alias target", func(t *testing.T) {
		seen := make([]string, 0, 3)
		modes := map[string]os.FileMode{
			"/var":                         os.ModeSymlink,
			"/private/var/folders":         os.ModeDir,
			"/private/var/folders/session": os.ModeDir,
		}
		err := validateNoSymlinkComponentsWith("/var/folders/session", func(path string) (os.FileMode, error) {
			seen = append(seen, path)
			return modes[path], nil
		}, func(path string) (string, bool, error) {
			if path != "/var" {
				t.Fatalf("alias path = %q, want /var", path)
			}
			return "/private/var", true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := strings.Join(seen, ","), "/var,/private/var/folders,/private/var/folders/session"; got != want {
			t.Fatalf("walked %q, want %q", got, want)
		}
	})

	t.Run("rejects later symlink after canonical alias", func(t *testing.T) {
		var seen []string
		err := validateNoSymlinkComponentsWith("/var/folders/attacker", func(path string) (os.FileMode, error) {
			seen = append(seen, path)
			if path == "/var" || path == "/private/var/folders/attacker" {
				return os.ModeSymlink, nil
			}
			return os.ModeDir, nil
		}, func(path string) (string, bool, error) {
			if path == "/var" {
				return "/private/var", true, nil
			}
			return "", false, nil
		})
		if err == nil || !strings.Contains(err.Error(), "/private/var/folders/attacker") {
			t.Fatalf("walk error = %v", err)
		}
		if got := seen[len(seen)-1]; got != "/private/var/folders/attacker" {
			t.Fatalf("last checked component = %q", got)
		}
	})

	t.Run("rejects every unapproved symlink", func(t *testing.T) {
		err := validateNoSymlinkComponentsWith("/tmp/link", func(string) (os.FileMode, error) {
			return os.ModeSymlink, nil
		}, func(string) (string, bool, error) {
			return "", false, nil
		})
		if err == nil || !strings.Contains(err.Error(), "is a symlink") {
			t.Fatalf("walk error = %v", err)
		}
	})
}
