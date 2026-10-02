package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func (s *ConsoleService) labDirectory() string {
	return filepath.Join(filepath.Dir(s.framesDir), "console-lab-grants")
}

func (s *ConsoleService) labLock(ctx context.Context, id string) (func() error, error) {
	if !validFrameID(id) {
		return nil, ErrInvalidConsoleLabGrant
	}
	if err := s.validateDependencies(); err != nil {
		return nil, err
	}
	lock, err := s.recovery.leaseManager.Acquire(ctx, "console-lab-"+id, "console.lab", id, 6*time.Minute)
	if err != nil {
		return nil, err
	}
	return func() error {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.recovery.leaseManager.Release(cleanup, lock)
	}, nil
}

func (s *ConsoleService) readLabDocument(ctx context.Context, id, suffix string, out any) error {
	if !validFrameID(id) {
		return ErrInvalidConsoleLabGrant
	}
	dir := s.labDirectory()
	security := target.NewPrivatePathSecurity()
	if err := security.ValidateDir(ctx, dir); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	name := id + suffix
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return ErrInvalidConsoleLabGrant
	}
	if err := security.ValidateFile(ctx, filepath.Join(dir, name)); err != nil {
		return err
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return ErrInvalidConsoleLabGrant
	}
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(data) > 8192 {
		return ErrInvalidConsoleLabGrant
	}
	if err := decodeLabDocument(data, out); err != nil {
		return err
	}
	return ctx.Err()
}

func rejectLabDuplicateFields(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalidConsoleLabGrant
	}
	seen := make(map[string]bool)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return ErrInvalidConsoleLabGrant
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return ErrInvalidConsoleLabGrant
		}
		seen[key] = true
		if err := d.Decode(new(json.RawMessage)); err != nil {
			return ErrInvalidConsoleLabGrant
		}
	}
	return nil
}

func (s *ConsoleService) writeLabDocument(ctx context.Context, id, suffix string, value any) error {
	if !validFrameID(id) {
		return ErrInvalidConsoleLabGrant
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > 8192 {
		return ErrInvalidConsoleLabGrant
	}
	dir := s.labDirectory()
	if err := statedir.EnsurePrivateDirectory(dir); err != nil {
		return err
	}
	security := target.NewPrivatePathSecurity()
	if err := security.ValidateDir(ctx, dir); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	name := id + suffix
	file, info, err := reserveProtectedLabFile(ctx, root, dir, name, security)
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
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	committed = true
	return statedir.SyncDir(dir)
}

func (s *ConsoleService) loadLabGrant(ctx context.Context, id string) (ConsoleLabGrant, error) {
	var grant ConsoleLabGrant
	if err := s.readLabDocument(ctx, id, ".json", &grant); err != nil {
		return grant, err
	}
	if grant.SchemaVersion != 1 || grant.GrantID != id || !validLabDigest(grant.EnrollmentIdentity) ||
		!validLabGrantAuthority(grant) ||
		grant.IssuedAt.IsZero() || !grant.ExpiresAt.After(grant.IssuedAt) || grant.ExpiresAt.Sub(grant.IssuedAt) > MaxConsoleLabGrantValidity {
		return ConsoleLabGrant{}, ErrInvalidConsoleLabGrant
	}
	return grant, nil
}

func (s *ConsoleService) labRevoked(ctx context.Context, id string) (bool, error) {
	var record consoleLabRevocation
	err := s.readLabDocument(ctx, id, ".revoked", &record)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if record.GrantID != id || record.Actor.Validate() != nil || record.RevokedAt.IsZero() || domain.ValidateReason(record.Reason) != nil || domain.ValidateIdempotencyKey(record.IdempotencyKey) != nil || !record.Deadline.After(record.RevokedAt) {
		return false, ErrInvalidConsoleLabGrant
	}
	return true, nil
}

func validLabDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func decodeLabDocument(data []byte, out any) error {
	if err := rejectLabDuplicateFields(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return ErrInvalidConsoleLabGrant
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrInvalidConsoleLabGrant
	}
	return nil
}

func validLabGrantAuthority(grant ConsoleLabGrant) bool {
	return grant.Target.Validate() == nil && grant.Issuer.Validate() == nil && grant.Beneficiary.Validate() == nil &&
		domain.ValidateReason(grant.Reason) == nil && domain.ValidateIdempotencyKey(grant.IdempotencyKey) == nil && grant.GrantID == labGrantID(grant.Issuer, grant.IdempotencyKey) &&
		(grant.Beneficiary == grant.Issuer || grant.Beneficiary == operationApprovalMCPBeneficiary)
}

func removeLabReservation(root *os.Root, name string, info os.FileInfo) {
	if info == nil {
		return
	}
	current, err := root.Lstat(name)
	if err == nil && os.SameFile(info, current) {
		_ = root.Remove(name)
	}
}

func reserveProtectedLabFile(ctx context.Context, root *os.Root, dir, name string, security target.Security) (_ *os.File, _ os.FileInfo, resultErr error) {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	defer func() {
		if resultErr != nil {
			if file != nil {
				_ = file.Close()
			}
			removeLabReservation(root, name, info)
		}
	}()
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return nil, nil, errors.Join(err, closeErr)
	}
	// Windows ACL protection operates on a closed reservation; reopen and check its identity.
	if err := security.ProtectNewFile(ctx, filepath.Join(dir, name)); err != nil {
		return nil, nil, err
	}
	file, err = root.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, nil, ErrInvalidConsoleLabGrant
	}
	return file, info, nil
}
