package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"strconv"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

// MaxGuestSpoolBytes caps both one output and aggregate concurrent private
// staging. The operation deadline is its expiry; every return removes its inode.
const MaxGuestSpoolBytes int64 = 1 << 30

type guestSpool struct {
	service *GuestService
	root    *os.Root
	file    *os.File
	info    os.FileInfo
	name    string
	limit   int64
	size    int64
	digest  hash.Hash
}

func (s *GuestService) spool(ctx context.Context, p GuestOperationParams) (*guestSpool, error) {
	if p.MaxOutput <= 0 || p.MaxOutput > MaxGuestSpoolBytes {
		return nil, domain.ErrNonCanonicalParameter
	}
	s.mu.Lock()
	if s.spoolBytes+p.MaxOutput > MaxGuestSpoolBytes {
		s.mu.Unlock()
		return nil, ErrGuestAuthority
	}
	s.spoolBytes += p.MaxOutput
	s.mu.Unlock()
	spool := &guestSpool{service: s, limit: p.MaxOutput, digest: sha256.New()}
	fail := func(err error) (*guestSpool, error) { return nil, errors.Join(err, spool.cleanup()) }
	if err := statedir.EnsurePrivateDirectory(s.store.directory); err != nil {
		return fail(err)
	}
	security := target.NewPrivatePathSecurity()
	if err := security.ValidateDir(ctx, s.store.directory); err != nil {
		return fail(err)
	}
	root, err := os.OpenRoot(s.store.directory)
	if err != nil {
		return fail(err)
	}
	spool.root = root
	var nonce [16]byte
	_, err = rand.Read(nonce[:])
	id := hex.EncodeToString(nonce[:])
	if err != nil {
		return fail(err)
	}
	spool.name = "guest-spool-" + string(id) + "-" + strconv.FormatInt(p.Deadline.UnixNano(), 10)
	spool.file, spool.info, err = reserveProtectedLabFile(ctx, root, s.store.directory, spool.name, security)
	if err != nil {
		return fail(err)
	}
	return spool, nil
}
func (s *guestSpool) Write(p []byte) (int, error) { return s.WriteContext(context.Background(), p) }
func (s *guestSpool) WriteContext(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > s.limit-s.size {
		return 0, domain.ErrNonCanonicalParameter
	}
	n, err := stream.OwnedOutput(s.file).WriteContext(ctx, p)
	if n > 0 {
		_, _ = s.digest.Write(p[:n])
		s.size += int64(n)
	}
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}
func (s *guestSpool) matches(out GuestOperationResult) bool {
	return s.size == out.Size && hex.EncodeToString(s.digest.Sum(nil)) == out.SHA256
}
func (s *guestSpool) publish(ctx context.Context, output stream.ContextWriter) error {
	if err := s.file.Sync(); err != nil {
		return err
	}
	if err := s.file.Close(); err != nil {
		return err
	}
	file, err := s.root.Open(s.name)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !os.SameFile(info, s.info) || info.Size() != s.size {
		return ErrGuestOwnership
	}
	buffer := make([]byte, stream.BufferSize)
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			written, writeErr := output.WriteContext(ctx, buffer[:n])
			if writeErr != nil {
				return writeErr
			}
			if written != n {
				return io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
func (s *guestSpool) cleanup() error {
	s.service.mu.Lock()
	s.service.spoolBytes -= s.limit
	s.service.mu.Unlock()
	if s.file != nil {
		_ = s.file.Close()
	}
	if s.root == nil {
		return nil
	}
	defer s.root.Close()
	if s.info == nil {
		return nil
	}
	current, err := s.root.Lstat(s.name)
	if err != nil {
		return err
	}
	if !os.SameFile(current, s.info) {
		return ErrGuestOwnership
	}
	return s.root.Remove(s.name)
}

type guestCheckedInput struct {
	source   stream.ContextReader
	size     int64
	count    int64
	expected string
	digest   hash.Hash
	eof      bool
}

func (r *guestCheckedInput) Read(p []byte) (int, error) {
	return r.ReadContext(context.Background(), p)
}
func (r *guestCheckedInput) ReadContext(ctx context.Context, p []byte) (int, error) {
	n, err := r.source.ReadContext(ctx, p)
	if n < 0 || n > len(p) || int64(n) > r.size-r.count {
		return 0, stream.ErrIntegrity
	}
	if n > 0 {
		_, _ = r.digest.Write(p[:n])
		r.count += int64(n)
	}
	if err == io.EOF {
		r.eof = true
	}
	return n, err
}
func (r *guestCheckedInput) valid() bool {
	return r.eof && r.count == r.size && hex.EncodeToString(r.digest.Sum(nil)) == r.expected
}
