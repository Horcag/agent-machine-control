package ssh

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyAliasCanonicalGrammar(t *testing.T) {
	t.Parallel()
	valid := []string{"default", "Key1", "a-b", "a_b", "A1_b-2"}
	for _, alias := range valid {
		if err := validateKeyAlias(alias); err != nil {
			t.Errorf("validateKeyAlias(%q) = %v, want nil", alias, err)
		}
	}

	invalid := []string{
		"", "-leading", "trailing-", "_leading", "trailing_", ".", "..", "a.b", "../outside",
		`a/b`, `a\\b`, `/absolute`, `C:\\key`, `C:key`, `\\\\server\\share`, "has space", "trail. ",
		"default.key", "default.dpapi", "dеfault", "control\n", strings.Repeat("a", maxKeyAliasLength+1),
	}
	for _, alias := range invalid {
		if err := validateKeyAlias(alias); err == nil {
			t.Errorf("validateKeyAlias(%q) = nil, want rejection", alias)
		}
	}
}

func TestKeyMaterialPathRemainsDirectlyBeneathStore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path, err := keyMaterialPath(root, "default_key-1", ".key")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Clean(root) {
		t.Fatalf("key path %q is not directly beneath %q", path, root)
	}
	for _, alias := range []string{"../outside", `sub/key`, `sub\\key`, `C:\\outside`, "default.key"} {
		if _, err := keyMaterialPath(root, alias, ".key"); err == nil {
			t.Errorf("keyMaterialPath accepted escaping alias %q", alias)
		}
	}
}
