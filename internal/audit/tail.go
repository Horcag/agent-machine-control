package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

const (
	// DefaultTailLimit is the default number of audit events returned.
	DefaultTailLimit = 50

	// MaxTailLimit is the maximum number of audit events that can be queried at once.
	MaxTailLimit = 1000

	// MaxLineBytes is the maximum allowed byte length for a single audit log line.
	MaxLineBytes = 64 * 1024
)

// Tail returns up to limit of the most recent audit events in chronological order.
func (s *Store) Tail(limit int) ([]Event, error) {
	if limit <= 0 {
		limit = DefaultTailLimit
	}
	if limit > MaxTailLimit {
		limit = MaxTailLimit
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	events, err := s.readEventsLocked()
	if err != nil {
		return nil, err
	}
	if len(events) <= limit {
		return events, nil
	}
	return events[len(events)-limit:], nil
}

func (s *Store) readEventsLocked() ([]Event, error) {
	return s.readEventsLockedContext(context.Background())
}

func (s *Store) readEventsLockedContext(ctx context.Context) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(s.logPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []Event{}, nil
		}
		return nil, fmt.Errorf("audit: failed to open audit log: %w", err)
	}
	defer f.Close()

	var events []Event
	reader := bufio.NewReader(f)

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("audit: failed to read audit line: %w", err)
		}
		if isPrefix {
			return nil, fmt.Errorf("audit: log line exceeds maximum size (%d bytes)", MaxLineBytes)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		event, err := decodeAuditEvent(line)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func decodeAuditEvent(line []byte) (Event, error) {
	var event Event
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&event); err != nil {
		return Event{}, fmt.Errorf("audit: malformed audit record: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return Event{}, fmt.Errorf("audit: trailing data in audit record")
	}
	return event, nil
}
