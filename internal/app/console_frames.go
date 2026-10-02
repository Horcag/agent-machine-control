package app

import (
	"context"
	"encoding/hex"
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

const consoleFrameTTL = 2 * time.Minute

// Frame metadata is private; framebuffer bytes are never retained by the service.
func (s *ConsoleService) saveFrame(ctx context.Context, frame domain.ConsoleFrame) (resultErr error) {
	lock, err := s.recovery.leaseManager.Acquire(ctx, "console-frame-metadata", "console.screenshot", "metadata", 10*time.Second)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, s.recovery.leaseManager.Release(cleanup, lock))
	}()
	if err := statedir.EnsurePrivateDirectory(s.framesDir); err != nil {
		return err
	}
	security := target.NewPrivatePathSecurity()
	if err := security.ValidateDir(ctx, s.framesDir); err != nil {
		return err
	}

	root, err := os.OpenRoot(s.framesDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := s.pruneFrames(root); err != nil {
		return err
	}
	file, err := root.OpenFile(frame.FrameID, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := security.ProtectNewFile(ctx, filepath.Join(s.framesDir, frame.FrameID)); err != nil {
		_ = file.Close()
		_ = root.Remove(frame.FrameID)
		return err
	}
	frame.Data = nil
	err = json.NewEncoder(file).Encode(frame)
	closeErr := file.Close()
	return errors.Join(err, closeErr)
}

func validFrameID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && filepath.Base(id) == id
}

func (s *ConsoleService) loadFrame(ctx context.Context, id string) (domain.ConsoleFrame, error) {
	if !validFrameID(id) {
		return domain.ConsoleFrame{}, errors.New("app: invalid console frame ID")
	}
	root, err := os.OpenRoot(s.framesDir)
	if err != nil {
		return domain.ConsoleFrame{}, err
	}
	defer root.Close()
	info, err := root.Lstat(id)
	if err != nil {
		return domain.ConsoleFrame{}, err
	}
	if !info.Mode().IsRegular() {
		return domain.ConsoleFrame{}, errors.New("app: insecure console frame metadata")
	}
	if err := target.NewPrivatePathSecurity().ValidateFile(ctx, filepath.Join(s.framesDir, id)); err != nil {
		return domain.ConsoleFrame{}, err
	}
	file, err := root.Open(id)
	if err != nil {
		return domain.ConsoleFrame{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return domain.ConsoleFrame{}, errors.New("app: console frame metadata changed")
	}
	return decodeConsoleFrame(file, info.Size(), id, s.recovery.now())
}

func decodeConsoleFrame(file io.Reader, size int64, id string, now time.Time) (domain.ConsoleFrame, error) {
	var frame domain.ConsoleFrame
	decoder := json.NewDecoder(io.LimitReader(file, 4096))
	decoder.DisallowUnknownFields()
	if size > 4096 {
		return domain.ConsoleFrame{}, errors.New("app: oversized console metadata")
	}
	if err := decoder.Decode(&frame); err != nil {
		return domain.ConsoleFrame{}, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return domain.ConsoleFrame{}, errors.New("app: trailing console metadata")
	}
	if frame.FrameID != id || len(frame.Data) != 0 || frame.ObservedAt.After(now) || now.Sub(frame.ObservedAt) > consoleFrameTTL {
		return domain.ConsoleFrame{}, errors.New("app: stale or invalid console frame metadata")
	}
	return frame, nil
}

func (s *ConsoleService) resolveFrameInput(ctx context.Context, canonical, providerID string, input domain.ConsoleInput) (domain.ConsoleInput, error) {
	switch input.Kind {
	case "key", "type":
		return input, nil
	}
	frame, err := s.loadFrame(ctx, input.FrameID)
	if err != nil {
		return input, err
	}
	if err := validatePointerFrame(frame, canonical, input); err != nil {
		return input, err
	}
	current, err := s.provider.CaptureConsole(ctx, providerID, 16, 16)
	if err != nil {
		return input, safeConsoleProviderError(err)
	}
	if err := validateCapturedFrame(current, providerID, 16, 16); err != nil {
		return input, err
	}
	if current.NativeWidth != frame.NativeWidth || current.NativeHeight != frame.NativeHeight {
		return input, errors.New("app: console display mode changed; capture a fresh frame")
	}
	input.X = input.X * frame.NativeWidth / frame.Width
	input.Y = input.Y * frame.NativeHeight / frame.Height
	if input.Kind == "drag" {
		input.ToX = input.ToX * frame.NativeWidth / frame.Width
		input.ToY = input.ToY * frame.NativeHeight / frame.Height
	}
	return input, nil
}

func (s *ConsoleService) pruneFrames(root *os.Root) error {
	names, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := names.ReadDir(-1)
	_ = names.Close()
	if err != nil {
		return err
	}
	// Limit retained metadata; remove only expired regular frame files we own.
	count := 0
	for _, entry := range entries {
		if !validFrameID(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("app: unsafe console frame entry")
		}
		if s.recovery.now().Sub(info.ModTime()) > consoleFrameTTL {
			if err := root.Remove(entry.Name()); err != nil {
				return err
			}
		} else {
			count++
		}
	}
	if count >= 512 {
		return errors.New("app: console frame metadata capacity reached")
	}
	return nil
}

func validatePointerFrame(frame domain.ConsoleFrame, canonical string, input domain.ConsoleInput) error {
	if frame.VMID != canonical || domain.ValidateConsoleDimensions(frame.Width, frame.Height) != nil || frame.NativeWidth < 1 || frame.NativeHeight < 1 || frame.NativeWidth > 65535 || frame.NativeHeight > 65535 {
		return errors.New("app: console frame target or dimensions invalid")
	}
	if input.X >= frame.Width || input.Y >= frame.Height {
		return errors.New("app: pointer coordinates exceed captured frame")
	}
	if input.Kind == "drag" && (input.ToX >= frame.Width || input.ToY >= frame.Height) {
		return errors.New("app: drag coordinates exceed captured frame")
	}
	return nil
}
