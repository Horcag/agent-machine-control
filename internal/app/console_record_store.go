package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

// Immutable snapshots are published by rename to a fresh name, so readers never
// observe partial JSON and Windows does not need replacement of an open file.
func (s *ConsoleService) writeRecordingStatus(ctx context.Context, doc recordingStatusDocument) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.recordingPath(doc.Status.RecordingID)
	if err != nil {
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
	name := fmt.Sprintf("%02d.json", doc.Sequence)
	if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return ErrRecordingStatusInconclusive
	}
	data, err := json.Marshal(doc)
	if err != nil || len(data) > 4096 || doc.Sequence > 61 {
		return ErrRecordingStatusInconclusive
	}
	marker, markerInfo, err := reserveProtectedLabFile(ctx, root, dir, "publishing", security)
	if err != nil {
		return err
	}
	if err := marker.Close(); err != nil {
		return err
	}
	file, info, err := reserveProtectedLabFile(ctx, root, dir, "pending", security)
	if err != nil {
		return err
	}
	defer removeLabReservation(root, "pending", info)
	defer file.Close()
	return publishRecordingSnapshot(root, file, name, data, info, markerInfo)
}

func publishRecordingSnapshot(root *os.Root, file *os.File, name string, data []byte, pendingInfo, markerInfo os.FileInfo) error {
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	current, err := root.Lstat("pending")
	if err != nil || !os.SameFile(pendingInfo, current) {
		return ErrRecordingStatusInconclusive
	}
	if err := root.Rename("pending", name); err != nil {
		return err
	}
	if err := statedir.SyncRoot(root); err != nil {
		return err
	}
	// Leave the marker on every write failure. Readers treat publication uncertainty
	// as inconclusive, including failures after the namespace commit.
	current, err = root.Lstat("publishing")
	if err != nil || !os.SameFile(markerInfo, current) {
		return ErrRecordingStatusInconclusive
	}
	return root.Remove("publishing")
}

func (s *ConsoleService) reserveRecording(ctx context.Context, doc recordingStatusDocument) (resultErr error) {
	child, err := s.recordingPath(doc.Status.RecordingID)
	if err != nil {
		return err
	}
	lock, err := s.recovery.leaseManager.Acquire(ctx, "console-recording-metadata", "console.record", "metadata", 10*time.Second)
	if err != nil {
		return ErrRecordingStatusInconclusive
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, s.recovery.leaseManager.Release(cleanup, lock))
	}()
	dir := s.recordingDirectory()
	if statedir.EnsurePrivateDirectory(dir) != nil || target.NewPrivatePathSecurity().ValidateDir(ctx, dir) != nil {
		return ErrRecordingStatusInconclusive
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ErrRecordingStatusInconclusive
	}
	defer root.Close()
	if _, err := root.Lstat(doc.Status.RecordingID); !errors.Is(err, os.ErrNotExist) {
		return ErrRecordingIDUnavailable
	}
	listing, err := root.Open(".")
	if err != nil {
		return ErrRecordingStatusInconclusive
	}
	entries, err := listing.ReadDir(recordingStatusCapacity + 1)
	_ = listing.Close()
	if err != nil && err != io.EOF || len(entries) > recordingStatusCapacity {
		return ErrRecordingIDUnavailable
	}
	count := s.pruneRecordingStatuses(ctx, root, doc, entries)
	if count >= recordingStatusCapacity {
		return ErrRecordingIDUnavailable
	}
	if err := root.Mkdir(doc.Status.RecordingID, 0700); err != nil {
		return ErrRecordingIDUnavailable
	}
	if target.NewPrivatePathSecurity().ProtectNewDir(ctx, child) != nil {
		return ErrRecordingStatusInconclusive
	}
	if s.writeRecordingStatus(ctx, doc) != nil {
		return ErrRecordingStatusInconclusive
	}
	return nil
}

func (s *ConsoleService) removeExpiredRecording(ctx context.Context, parent *os.Root, id string, sequence int) error {
	dir, err := s.recordingPath(id)
	if err != nil {
		return err
	}
	if err := target.NewPrivatePathSecurity().ValidateDir(ctx, dir); err != nil {
		return err
	}
	root, err := parent.OpenRoot(id)
	if err != nil {
		return err
	}
	defer root.Close()
	listing, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := listing.ReadDir(64)
	_ = listing.Close()
	if err != nil && err != io.EOF || len(entries) != sequence+1 {
		return ErrRecordingStatusInconclusive
	}
	for _, entry := range entries {
		if err := validateRecordingCleanupEntry(ctx, dir, entry, sequence); err != nil {
			return err
		}
	}
	for _, entry := range entries {
		if err := root.Remove(entry.Name()); err != nil {
			return err
		}
	}
	_ = root.Close()
	return parent.Remove(id)
}

func (s *ConsoleService) pruneRecordingStatuses(ctx context.Context, root *os.Root, doc recordingStatusDocument, entries []os.DirEntry) int {
	count := len(entries)
	for _, entry := range entries {
		if !validRecordingID(entry.Name()) {
			continue
		}
		old, err := s.readRecordingStatus(ctx, entry.Name())
		if err != nil || !old.Status.Terminal || old.Caller != doc.Caller || old.Actor != doc.Actor || old.Status.VMID != doc.Status.VMID || s.recovery.now().Before(old.Status.ExpiresAt) {
			continue
		}
		if s.removeExpiredRecording(ctx, root, entry.Name(), old.Sequence) == nil {
			count--
		}
	}
	return count
}

func validateRecordingCleanupEntry(ctx context.Context, dir string, entry os.DirEntry, sequence int) error {
	info, err := entry.Info()
	if err != nil || !info.Mode().IsRegular() || !validRecordingSnapshot(entry.Name(), sequence) {
		return ErrRecordingStatusInconclusive
	}
	return target.NewPrivatePathSecurity().ValidateFile(ctx, filepath.Join(dir, entry.Name()))
}
