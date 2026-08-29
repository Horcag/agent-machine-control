package events

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

const (
	// MaxReplayBuffer is the maximum number of recent events retained in-memory and on disk per operation.
	MaxReplayBuffer = 256

	// SubscriberBufferSize is the queue capacity for live events for each subscriber.
	SubscriberBufferSize = 64

	// TotalSubscriberCapacity is the total channel buffer size allowing all retained events plus live events.
	TotalSubscriberCapacity = MaxReplayBuffer + SubscriberBufferSize

	// MaxEventBytes is the maximum allowed byte size of a single serialized JSON event line.
	MaxEventBytes = 64 * 1024 // 64 KiB

	// MaxEventsFileSize is the maximum allowed size of an events file on disk.
	MaxEventsFileSize = 16 * 1024 * 1024 // 16 MiB
)

func validateEventsFile(eventsPath string) error {
	fi, err := os.Lstat(eventsPath)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("events: events file %s is a symlink", eventsPath)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("events: events file %s is not a regular file", eventsPath)
	}
	if fi.Size() > MaxEventsFileSize {
		return fmt.Errorf("events: events file %s exceeds size bound (%d bytes)", eventsPath, fi.Size())
	}
	return nil
}

func parseDiskEventLine(line []byte, lineNum int, eventsPath, opID string, lastSeq uint64) (Event, uint64, error) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return Event{}, 0, fmt.Errorf("events: empty or blank line %d in %s", lineNum, eventsPath)
	}
	if len(trimmed) > MaxEventBytes {
		return Event{}, 0, fmt.Errorf("events: line %d in %s exceeds byte bound", lineNum, eventsPath)
	}

	var ev Event
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ev); err != nil {
		return Event{}, 0, fmt.Errorf("events: corrupt event json at line %d in %s: %w", lineNum, eventsPath, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Event{}, 0, fmt.Errorf("events: trailing json data at line %d in %s", lineNum, eventsPath)
	}
	if err := ev.Validate(); err != nil {
		return Event{}, 0, fmt.Errorf("events: invalid event at line %d in %s: %w", lineNum, eventsPath, err)
	}
	if ev.OperationID != opID {
		return Event{}, 0, fmt.Errorf("events: operation id mismatch at line %d in %s (expected %s, got %s)", lineNum, eventsPath, opID, ev.OperationID)
	}

	if lastSeq == 0 {
		lastSeq = ev.Sequence
	} else {
		if ev.Sequence <= lastSeq {
			return Event{}, 0, fmt.Errorf("events: non-monotonic or duplicate sequence %d at line %d in %s (previous %d)", ev.Sequence, lineNum, eventsPath, lastSeq)
		}
		lastSeq = ev.Sequence
	}

	return ev, lastSeq, nil
}

func loadHistoryFromDisk(operationsDir, opID string) ([]Event, error) {
	if operationsDir == "" {
		return nil, nil
	}
	if err := domain.ValidateOperationID(opID); err != nil {
		return nil, err
	}
	eventsPath := filepath.Join(operationsDir, fmt.Sprintf("%s.events.jsonl", opID))
	if err := validateEventsFile(eventsPath); err != nil {
		return nil, err
	}

	f, err := os.Open(eventsPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var evs []Event
	reader := bufio.NewReaderSize(f, MaxEventBytes)
	var lastSeq uint64
	lineNum := 0

	for {
		lineNum++
		line, isPrefix, err := reader.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("events: error reading line %d from %s: %w", lineNum, eventsPath, err)
		}
		if isPrefix {
			return nil, fmt.Errorf("events: line %d in %s exceeds maximum event size of %d bytes", lineNum, eventsPath, MaxEventBytes)
		}

		ev, nextSeq, parseErr := parseDiskEventLine(line, lineNum, eventsPath, opID, lastSeq)
		if parseErr != nil {
			return nil, parseErr
		}
		lastSeq = nextSeq
		evs = append(evs, ev)
	}

	if len(evs) > MaxReplayBuffer {
		evs = evs[len(evs)-MaxReplayBuffer:]
	}
	return evs, nil
}

func persistEventToDisk(operationsDir string, ev Event) error {
	if operationsDir == "" {
		return nil
	}
	if err := domain.ValidateOperationID(ev.OperationID); err != nil {
		return err
	}

	data, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("events: marshal error: %w", err)
	}
	if len(data) > MaxEventBytes {
		return fmt.Errorf("events: serialized event exceeds maximum size of %d bytes", MaxEventBytes)
	}

	eventsPath := filepath.Join(operationsDir, fmt.Sprintf("%s.events.jsonl", ev.OperationID))

	existing, err := loadHistoryFromDisk(operationsDir, ev.OperationID)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("events: failed to load existing history before persistence: %w", err)
	}
	existing = append(existing, ev)

	if len(existing) > MaxReplayBuffer {
		retained := existing[len(existing)-MaxReplayBuffer:]
		if err := compactEventsOnDisk(eventsPath, retained); err != nil {
			return err
		}
		return statedir.SyncDir(operationsDir)
	}

	if err := appendEventToDisk(eventsPath, data); err != nil {
		return err
	}
	return statedir.SyncDir(operationsDir)
}

func compactEventsOnDisk(eventsPath string, retained []Event) error {
	var buf bytes.Buffer
	for _, e := range retained {
		line, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("events: marshal error during compaction: %w", err)
		}
		if len(line) > MaxEventBytes {
			return fmt.Errorf("events: compacted event exceeds maximum size of %d bytes", MaxEventBytes)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	if buf.Len() > MaxEventsFileSize {
		return fmt.Errorf("events: compacted events file exceeds maximum allowed file size")
	}

	tmpPath := fmt.Sprintf("%s.tmp.%d", eventsPath, time.Now().UnixNano())
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("events: failed to open temp events file: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("events: failed to write temp events file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("events: failed to sync temp events file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("events: failed to close temp events file: %w", err)
	}
	if err := os.Rename(tmpPath, eventsPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("events: failed to commit compacted events file: %w", err)
	}
	return nil
}

func appendEventToDisk(eventsPath string, data []byte) error {
	f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("events: failed to open events log: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("events: failed to write event: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("events: failed to sync events log: %w", err)
	}
	return f.Close()
}
