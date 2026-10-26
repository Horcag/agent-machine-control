package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

const maxGuestDocument = 256 * 1024

// GuestStore keeps immutable, private worker and ownership records. Its directory
// must be dedicated server state, never caller-selected storage.
type GuestStore struct{ directory string }

func NewGuestStore(directory string) *GuestStore { return &GuestStore{directory: directory} }

func (s *GuestStore) write(ctx context.Context, id, suffix string, value any) error {
	if s == nil || !validLabDigest(id) {
		return ErrGuestOwnership
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > maxGuestDocument {
		return ErrGuestOwnership
	}
	if err := statedir.EnsurePrivateDirectory(s.directory); err != nil {
		return err
	}
	security := target.NewPrivatePathSecurity()
	if err := security.ValidateDir(ctx, s.directory); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	name := id + suffix
	file, info, err := reserveProtectedLabFile(ctx, root, s.directory, name, security)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			removeLabReservation(root, name, info)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	committed = true
	return statedir.SyncDir(s.directory)
}

func (s *GuestStore) read(ctx context.Context, id, suffix string, out any) error {
	if s == nil || !validLabDigest(id) {
		return ErrGuestOwnership
	}
	security := target.NewPrivatePathSecurity()
	if err := security.ValidateDir(ctx, s.directory); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	name := id + suffix
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxGuestDocument {
		return ErrGuestOwnership
	}
	if err = security.ValidateFile(ctx, filepath.Join(s.directory, name)); err != nil {
		return err
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return ErrGuestOwnership
	}
	data, err := io.ReadAll(io.LimitReader(file, maxGuestDocument+1))
	if err != nil || len(data) > maxGuestDocument {
		return ErrGuestOwnership
	}
	if err = decodeLabDocument(data, out); err != nil {
		return errors.Join(ErrGuestOwnership, err)
	}
	return ctx.Err()
}
