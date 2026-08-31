//go:build !unix && !windows

package target

import (
	"context"
	"errors"
)

type unsupportedSecurity struct{}

func newPlatformSecurity() Security { return unsupportedSecurity{} }

func (unsupportedSecurity) ValidateDir(context.Context, string) error {
	return errors.New("target: protected state is unsupported on this platform")
}

func (unsupportedSecurity) ProtectDir(context.Context, string) error {
	return errors.New("target: protected state is unsupported on this platform")
}

func (unsupportedSecurity) ValidateInheritedFile(context.Context, string) error {
	return errors.New("target: protected state is unsupported on this platform")
}

func (unsupportedSecurity) ValidateFile(context.Context, string) error {
	return errors.New("target: protected state is unsupported on this platform")
}

func (unsupportedSecurity) ProtectFile(context.Context, string) error {
	return errors.New("target: protected state is unsupported on this platform")
}
