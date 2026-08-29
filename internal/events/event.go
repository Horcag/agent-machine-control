package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// Event is an alias to domain.Event.
type Event = domain.Event

// FormatSSE formats an Event according to the Server-Sent Events specification.
func FormatSSE(e Event) ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("events: failed to marshal SSE event data: %w", err)
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "id: %d\n", e.Sequence)
	if e.EventType != "" {
		fmt.Fprintf(&buf, "event: %s\n", e.EventType)
	}
	fmt.Fprintf(&buf, "data: %s\n\n", string(data))

	return buf.Bytes(), nil
}

// ParseSSE decodes an SSE data payload into a domain.Event.
func ParseSSE(data []byte) (Event, error) {
	var ev Event
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ev); err != nil {
		return Event{}, fmt.Errorf("events: failed to decode event: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return Event{}, fmt.Errorf("events: trailing data in event payload")
	}
	return ev, nil
}
