package sessions

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type sanitizerTestChannel struct {
	chunks [][]byte
	next   int
}

func (c *sanitizerTestChannel) Read(dst []byte) (int, error) {
	if c.next >= len(c.chunks) {
		return 0, io.EOF
	}
	n := copy(dst, c.chunks[c.next])
	c.next++
	return n, nil
}

func (*sanitizerTestChannel) Write(context.Context, []byte) (int, error) { return 0, nil }
func (*sanitizerTestChannel) SendControl(context.Context, domain.ControlKey) (guestssh.ControlResult, error) {
	return guestssh.ControlResult{}, nil
}
func (*sanitizerTestChannel) Resize(uint16, uint16) error { return nil }
func (*sanitizerTestChannel) Close(context.Context) error { return nil }
func (*sanitizerTestChannel) LastCloseOutcome() guestssh.CloseOutcome {
	return guestssh.CloseOutcome{Complete: true}
}
func (*sanitizerTestChannel) Wait() (int, error) { return 0, nil }

func TestPumpChannelOutputNeverBuffersSplitTerminalControls(t *testing.T) {
	channel := &sanitizerTestChannel{chunks: [][]byte{
		[]byte("safe\x1b]2;forged"),
		[]byte(" title\a\x1b[?1049htext\x1bPpayload"),
		[]byte(" continues\x1b\\tail"),
	}}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	manager := NewManager(t.TempDir(), nil, func() time.Time { return now })
	session := &Session{
		channel:   channel,
		buffer:    NewRingBuffer(DefaultRingBufferCapBytes),
		sanitizer: guestssh.NewStreamSanitizer(guestssh.SanitizerConfig{}),
	}

	manager.pumpChannelOutput(session)
	chunks, _, _, _ := session.buffer.ReadAfter(0, DefaultMaxReadLimitBytes)
	var output strings.Builder
	for _, chunk := range chunks {
		output.WriteString(chunk.Data)
	}
	if got := output.String(); got != "safetexttail" {
		t.Fatalf("buffered output = %q, want safetexttail", got)
	}
	if strings.ContainsRune(output.String(), '\x1b') {
		t.Fatal("buffered output contains ESC")
	}
}
