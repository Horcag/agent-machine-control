//go:build !unix && !windows

package target

import (
	"errors"
	"os"
)

func openNoFollow(string) (*os.File, error) {
	return nil, errors.New("target: no-follow reads are unsupported on this platform")
}
