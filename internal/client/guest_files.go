package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

// GuestSource holds the exact opened local file through hashing and upload.
// File names never enter guest metadata.
type GuestSource struct {
	file     *os.File
	original os.FileInfo
	Size     int64
	SHA256   string
}

func OpenGuestSource(ctx context.Context, path string) (*GuestSource, error) {
	return openGuestSourceBounded(ctx, path, stream.MaxTransferSize)
}
func openGuestSourceBounded(ctx context.Context, path string, maximumBytes int64) (*GuestSource, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	source := &GuestSource{file: file}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()
	source.original, err = file.Stat()
	if err != nil || !source.original.Mode().IsRegular() || source.original.Size() > maximumBytes {
		return nil, fmt.Errorf("guest source must be a regular file")
	}
	hash := sha256.New()
	reader := guestRequestReader{ctx, stream.OwnedInput(file)}
	size, err := io.CopyBuffer(hash, reader, make([]byte, stream.BufferSize))
	if err != nil {
		return nil, err
	}
	source.Size = size
	source.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if err = source.Verify(); err != nil {
		return nil, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	ok = true
	return source, nil
}
func (s *GuestSource) Close() error                { return s.file.Close() }
func (s *GuestSource) Input() stream.ContextReader { return stream.OwnedInput(s.file) }
func (s *GuestSource) Verify() error {
	current, err := s.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(s.original, current) || current.Size() != s.original.Size() || !current.ModTime().Equal(s.original.ModTime()) || s.Size != current.Size() {
		return errors.New("guest source changed during transfer")
	}
	return nil
}

// GuestOperationToFile stages only in the destination directory and publishes
// with link's atomic create-new semantics after complete protocol verification.
func (c *Client) GuestOperationToFile(ctx context.Context, p app.GuestOperationParams, input stream.ContextReader, destination string) (app.GuestOperationResult, error) {
	var zero app.GuestOperationResult
	if _, err := os.Lstat(destination); err == nil {
		return zero, os.ErrExist
	} else if !os.IsNotExist(err) {
		return zero, err
	}
	directory := filepath.Dir(destination)
	file, err := os.CreateTemp(directory, ".amc-guest-*")
	if err != nil {
		return zero, err
	}
	name := file.Name()
	original, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return zero, err
	}
	defer func() {
		_ = file.Close()
		if current, err := os.Lstat(name); err == nil && os.SameFile(original, current) {
			_ = os.Remove(name)
		}
	}()
	result, err := c.GuestOperation(ctx, p, input, stream.OwnedOutput(file))
	if err != nil {
		return result, err
	}
	if err = publishGuestFile(ctx, file, name, original, destination, result); err != nil {
		return result, err
	}

	return result, nil
}

// ValidateScript validates bounded UTF-8 before sending any execution request.
func (s *GuestSource) ValidateScript() error {
	if s.Size > stream.MaxScriptSize {
		return stream.ErrRequest
	}
	data, err := io.ReadAll(io.LimitReader(s.file, stream.MaxScriptSize+1))
	if err != nil {
		return err
	}
	if int64(len(data)) != s.Size || !utf8.Valid(data) {
		return stream.ErrRequest
	}
	_, err = s.file.Seek(0, io.SeekStart)
	return err
}

// OpenGuestPayload opens only the explicit local payload accepted by an operation.
func OpenGuestPayload(ctx context.Context, operation, path string) (*GuestSource, error) {
	if operation != "put" && operation != "exec" {
		if path != "" {
			return nil, stream.ErrRequest
		}
		return nil, nil
	}
	maximum := stream.MaxTransferSize
	if operation == "exec" {
		maximum = stream.MaxScriptSize
	}
	source, err := openGuestSourceBounded(ctx, path, maximum)
	if err != nil {
		return nil, err
	}
	if operation == "exec" {
		if err = source.ValidateScript(); err != nil {
			_ = source.Close()
			return nil, err
		}
	}
	return source, nil
}

func publishGuestFile(ctx context.Context, file *os.File, name string, original os.FileInfo, destination string, result app.GuestOperationResult) error {
	var err error
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = verifyGuestFileContent(ctx, file, result); err != nil {
		return err
	}
	current, err := os.Lstat(name)
	if err != nil || !os.SameFile(original, current) {
		return errors.New("guest staging identity changed")
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Link(name, destination); err != nil {
		return err
	}
	installed, err := os.Lstat(destination)
	if err != nil || !os.SameFile(original, installed) {
		return errors.New("guest destination identity mismatch")
	}
	if err = statedir.SyncDir(filepath.Dir(destination)); err != nil {
		return err
	}
	return nil
}

func verifyGuestFileContent(ctx context.Context, file *os.File, result app.GuestOperationResult) error {
	var err error
	if err = file.Sync(); err != nil {
		return err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	size, err := io.CopyBuffer(hash, guestRequestReader{ctx, stream.OwnedInput(file)}, make([]byte, stream.BufferSize))
	if err != nil {
		return err
	}
	if size != result.Size || (size > 0 && hex.EncodeToString(hash.Sum(nil)) != result.SHA256) {
		return stream.ErrIntegrity
	}

	return nil
}
