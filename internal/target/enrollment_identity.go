package target

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

// EnrollmentIdentity binds grants to this protected enrollment publication.
// Identical logical enrollment after clear is a different filesystem publication.
func (s *Store) EnrollmentIdentity(ctx context.Context) (Default, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repairPending(ctx); err != nil {
		return Default{}, "", err
	}
	if err := s.validateDirectory(ctx); err != nil {
		return Default{}, "", err
	}
	if err := s.operations.SyncDir(s.dir); err != nil {
		return Default{}, "", err
	}
	if err := s.security.ValidateFile(ctx, s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default{}, "", ErrNoDefault
		}
		return Default{}, "", ErrInsecureState
	}
	f, err := openNoFollow(s.path)
	if err != nil {
		return Default{}, "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxDocumentBytes+1))
	if err != nil {
		return Default{}, "", err
	}
	if len(data) > MaxDocumentBytes {
		return Default{}, "", ErrInvalidDocument
	}
	value, err := decode(data)
	if err != nil {
		return Default{}, "", err
	}
	identity, err := enrollmentFileIdentity(f)
	if err != nil {
		return Default{}, "", err
	}
	info, err := f.Stat()
	if err != nil {
		return Default{}, "", err
	}
	current, err := os.Lstat(s.path)
	if err != nil || !os.SameFile(info, current) {
		return Default{}, "", ErrInsecureState
	}
	if err := s.security.ValidateFile(ctx, s.path); err != nil {
		return Default{}, "", ErrInsecureState
	}
	hash := sha256.Sum256([]byte(identity + "\x00" + StateDigest(&value)))
	return value, hex.EncodeToString(hash[:]), nil
}
