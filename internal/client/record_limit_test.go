package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func TestRecordingResponseAllowsBoundedGIFWithoutExpandingOtherResponses(t *testing.T) {
	artifact := bytes.Repeat([]byte{42}, 1200*1024)
	data, err := json.Marshal(app.ConsoleRecording{MIMEType: "image/gif", Data: artifact})
	if err != nil {
		t.Fatal(err)
	}
	var recording app.ConsoleRecording
	if err = decodeHTTPResponse(t.Context(), bytes.NewReader(data), &recording); err != nil || !bytes.Equal(recording.Data, artifact) {
		t.Fatalf("valid recording rejected: %v", err)
	}
	var ordinary map[string]any
	if err = decodeHTTPResponse(t.Context(), bytes.NewReader(data), &ordinary); !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("ordinary limit widened: %v", err)
	}
}
