package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

type guestStaging struct {
	root          *os.Root
	directory     string
	directoryInfo os.FileInfo
	file          *os.File
	fileInfo      os.FileInfo
}

func newGuestStaging(ctx context.Context, parent string) (*guestStaging, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	directory := filepath.Join(parent, ".amc-guest-"+hex.EncodeToString(nonce[:]))
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		return nil, errors.New("guest staging namespace reservation collision")
	}
	if err := statedir.EnsurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	stage := &guestStaging{directory: directory}
	var err error
	stage.directoryInfo, err = os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if err = stage.open(ctx); err != nil {
		return nil, errors.Join(err, stage.cleanup())
	}
	return stage, nil
}
func (s *guestStaging) open(ctx context.Context) error {
	security := target.NewPrivatePathSecurity()
	if err := security.ValidateDir(ctx, s.directory); err != nil {
		return err
	}
	var err error
	s.root, err = os.OpenRoot(s.directory)
	if err != nil {
		return err
	}
	held, err := s.root.Stat(".")
	if err != nil || !os.SameFile(held, s.directoryInfo) {
		return errors.New("guest staging namespace identity changed")
	}
	path := filepath.Join(s.directory, "pending")
	s.file, err = createGuestStagingFile(path)
	if err != nil {
		return err
	}
	s.fileInfo, err = s.file.Stat()
	if err != nil {
		return err
	}
	if err = security.ValidateInheritedFile(ctx, path); err != nil {
		return err
	}
	if err = security.ProtectNewFile(ctx, path); err != nil {
		return err
	}
	if err = security.ValidateFile(ctx, path); err != nil {
		return err
	}
	named, err := s.root.Lstat("pending")
	if err != nil || !os.SameFile(named, s.fileInfo) {
		return errors.New("guest staging file escaped held private namespace")
	}
	return nil
}
func (s *guestStaging) cleanup() error {
	var result error
	if s.file != nil {
		if err := s.file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			result = errors.Join(result, err)
		}
	}
	if s.root != nil {
		result = errors.Join(result, s.removeOwnedFile(), s.root.Close())
	}
	current, err := os.Lstat(s.directory)
	if os.IsNotExist(err) {
		return result
	}
	if err != nil || s.directoryInfo == nil || !os.SameFile(current, s.directoryInfo) {
		return errors.Join(result, errors.New("guest staging directory cleanup identity changed"))
	}
	return errors.Join(result, os.Remove(s.directory))
}

func (s *guestStaging) removeOwnedFile() error {
	directory, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(32)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) == 32 {
		return errors.New("guest staging cleanup entry budget exceeded")
	}
	var result error
	for _, entry := range entries {
		info, err := s.root.Lstat(entry.Name())
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if s.fileInfo == nil || !os.SameFile(info, s.fileInfo) {
			result = errors.Join(result, errors.New("guest staging cleanup blocked by foreign file identity"))
			continue
		}
		result = errors.Join(result, s.root.Remove(entry.Name()))
	}
	return result
}
