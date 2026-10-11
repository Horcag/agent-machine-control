//go:build !linux && !windows

package client

import (
	"context"
	"errors"
	"os"
)

// Publication requires a native held-handle create-new primitive. Do not
// substitute a staging pathname on unsupported platforms.
func publishGuestFileHandle(context.Context, *os.File, string) error {
	return errors.New("held-handle guest file publication is unsupported on this platform")
}
func createGuestStagingFile(string) (*os.File, error) {
	return nil, errors.New("guest file staging is unsupported on this platform")
}

func guestPublicationDestination(string) (string, error) {
	return "", errors.New("guest file staging is unsupported on this platform")
}
