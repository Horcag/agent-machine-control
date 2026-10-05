package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func (s *ConsoleService) readRecordingStatus(ctx context.Context, id string) (recordingStatusDocument, error) {
	var doc recordingStatusDocument
	if err := ctx.Err(); err != nil {
		return doc, ErrRecordingStatusInconclusive
	}
	dir, err := s.recordingPath(id)
	if err != nil {
		return doc, err
	}
	security := target.NewPrivatePathSecurity()
	if security.ValidateDir(ctx, dir) != nil {
		return doc, ErrRecordingStatusInconclusive
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return doc, ErrRecordingStatusInconclusive
	}
	defer root.Close()
	if _, err := root.Lstat("publishing"); !errors.Is(err, os.ErrNotExist) {
		return doc, ErrRecordingStatusInconclusive
	}
	listing, err := root.Open(".")
	if err != nil {
		return doc, ErrRecordingStatusInconclusive
	}
	entries, err := listing.ReadDir(64)
	_ = listing.Close()
	if err != nil && err != io.EOF || len(entries) > 63 {
		return doc, ErrRecordingStatusInconclusive
	}
	name, err := latestRecordingSnapshot(entries)
	if err != nil {
		return doc, err
	}
	return readRecordingSnapshot(ctx, root, dir, name, id, security)
}

func readRecordingSnapshot(ctx context.Context, root *os.Root, dir, name, id string, security target.Security) (recordingStatusDocument, error) {
	var doc recordingStatusDocument
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || security.ValidateFile(ctx, filepath.Join(dir, name)) != nil {
		return doc, ErrRecordingStatusInconclusive
	}
	file, err := root.Open(name)
	if err != nil {
		return doc, ErrRecordingStatusInconclusive
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return doc, ErrRecordingStatusInconclusive
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || decodeLabDocument(data, &doc) != nil || name != fmt.Sprintf("%02d.json", doc.Sequence) || !validRecordingDocument(doc, id) {
		return recordingStatusDocument{}, ErrRecordingStatusInconclusive
	}
	// A writer may have started or failed publication after the initial marker
	// check. Recheck on the same directory handle before trusting a new snapshot.
	if _, err := root.Lstat("publishing"); !errors.Is(err, os.ErrNotExist) {
		return recordingStatusDocument{}, ErrRecordingStatusInconclusive
	}
	return doc, nil
}

func validRecordingDocument(doc recordingStatusDocument, id string) bool {
	s := doc.Status
	if doc.SchemaVersion != 1 || s.SchemaVersion != "1" || doc.Caller.Validate() != nil || doc.Actor.Validate() != nil || s.RecordingID != id || !validRecordingLocator(s.VMID) {
		return false
	}
	if !validRecordingCounts(s) || !validRecordingTimes(s) {
		return false
	}
	if !s.Terminal {
		return s.TerminalReason == ""
	}
	if s.CaptureInFlight {
		return false
	}
	switch s.TerminalReason {
	case "completed":
		return s.CompletedCaptures == s.RequestedFrames
	case "failed", "canceled", "deadline_exceeded":
		return true
	default:
		return false
	}
}

func validRecordingCounts(s ConsoleRecordStatus) bool {
	return s.RequestedFrames >= 2 && s.RequestedFrames <= 30 && s.AttemptedCaptures >= 0 && s.AttemptedCaptures <= s.RequestedFrames && s.CompletedCaptures >= 0 && s.CompletedCaptures <= s.AttemptedCaptures && (!s.CaptureInFlight || s.AttemptedCaptures > s.CompletedCaptures)
}

func validRecordingTimes(s ConsoleRecordStatus) bool {
	return !s.StartedAt.IsZero() && !s.UpdatedAt.Before(s.StartedAt) && s.ExpiresAt.Equal(s.StartedAt.Add(recordingStatusTTL))
}

func latestRecordingSnapshot(entries []os.DirEntry) (string, error) {
	name := ""
	for _, entry := range entries {
		if entry.Name() == "pending" {
			continue
		}
		if !validRecordingSnapshot(entry.Name(), 61) {
			return "", ErrRecordingStatusInconclusive
		}
		if entry.Name() > name {
			name = entry.Name()
		}
	}
	if name == "" {
		return "", ErrRecordingStatusInconclusive
	}
	return name, nil
}

func validRecordingSnapshot(name string, maximum int) bool {
	var seq int
	_, err := fmt.Sscanf(name, "%02d.json", &seq)
	return err == nil && seq >= 0 && seq <= maximum && name == fmt.Sprintf("%02d.json", seq)
}

func validRecordingLocator(value string) bool {
	locator, err := domain.ParseMachineLocator(value)
	return err == nil && locator.HostID == domain.LocalHostID && locator.String() == value
}
