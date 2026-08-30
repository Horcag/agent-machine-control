package ssh

import (
	"errors"
	"path/filepath"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

const maxKeyAliasLength = 64

func validateKeyAlias(alias string) error {
	if len(alias) == 0 || len(alias) > maxKeyAliasLength {
		return domain.ErrNonCanonicalParameter
	}
	for i := range len(alias) {
		char := alias[i]
		alphaNumeric := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
		if alphaNumeric {
			continue
		}
		if i > 0 && i < len(alias)-1 && (char == '-' || char == '_') {
			continue
		}
		return domain.ErrNonCanonicalParameter
	}
	return nil
}

func keyMaterialPath(keysDir, alias, extension string) (string, error) {
	if err := validateKeyAlias(alias); err != nil {
		return "", err
	}
	root, err := filepath.Abs(filepath.Clean(keysDir))
	if err != nil {
		return "", errors.New("ssh: key store path is invalid")
	}
	candidate, err := filepath.Abs(filepath.Join(root, alias+extension))
	if err != nil {
		return "", errors.New("ssh: key path is invalid")
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == "." || filepath.Dir(rel) != "." || filepath.IsAbs(rel) {
		return "", errors.New("ssh: key path escapes the key store")
	}
	return candidate, nil
}
