//go:build !windows

package auth

import (
	"errors"
	"fmt"
	"os"
)

func defaultPrincipalResolver() (string, error) {
	uid := os.Getuid()
	if uid < 0 {
		return "", errors.New("auth: failed to resolve valid Unix UID")
	}
	return fmt.Sprintf("operator:uid-%d", uid), nil
}
