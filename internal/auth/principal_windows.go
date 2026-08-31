//go:build windows

package auth

import (
	"errors"
	"fmt"
	"os/user"
	"strings"
)

func defaultPrincipalResolver() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("auth: failed to resolve Windows user: %w", err)
	}
	if current == nil || strings.TrimSpace(current.Username) == "" {
		return "", errors.New("auth: Windows username is empty")
	}
	return fmt.Sprintf("operator:%s", sanitizePrincipal(current.Username)), nil
}
