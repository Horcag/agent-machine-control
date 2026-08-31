package ssh

import (
	"errors"
	"testing"
)

func TestVerifiedDarwinVarAlias(t *testing.T) {
	t.Run("accepts canonical target", func(t *testing.T) {
		var evaluated string
		allowed, err := verifiedDarwinVarAlias("/var/.", func(path string) (string, error) {
			evaluated = path
			return "/private/var", nil
		})
		if err != nil || !allowed {
			t.Fatalf("verifiedDarwinVarAlias() = %t, %v", allowed, err)
		}
		if evaluated != "/var" {
			t.Fatalf("EvalSymlinks path = %q, want /var", evaluated)
		}
	})

	t.Run("rejects relative target", func(t *testing.T) {
		allowed, err := verifiedDarwinVarAlias("/var", func(string) (string, error) { return "private/var", nil })
		if err == nil || allowed {
			t.Fatalf("verifiedDarwinVarAlias() = %t, %v", allowed, err)
		}
	})

	t.Run("rejects unexpected target", func(t *testing.T) {
		allowed, err := verifiedDarwinVarAlias("/var", func(string) (string, error) { return "/tmp/var", nil })
		if err == nil || allowed {
			t.Fatalf("verifiedDarwinVarAlias() = %t, %v", allowed, err)
		}
	})

	t.Run("returns resolution failure", func(t *testing.T) {
		want := errors.New("synthetic resolution failure")
		allowed, err := verifiedDarwinVarAlias("/var", func(string) (string, error) { return "", want })
		if !errors.Is(err, want) || allowed {
			t.Fatalf("verifiedDarwinVarAlias() = %t, %v", allowed, err)
		}
	})
}
