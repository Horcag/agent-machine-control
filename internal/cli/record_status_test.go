package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type recordingStatusCLIStub struct {
	consoleRecordStub
	status      app.ConsoleRecordStatus
	statusReq   app.ConsoleRecordStatusRequest
	statusActor domain.ActorContext
}

func (s *recordingStatusCLIStub) RecordStatus(_ context.Context, actor domain.ActorContext, req app.ConsoleRecordStatusRequest) (app.ConsoleRecordStatus, error) {
	s.statusReq = req
	s.statusActor = actor
	return s.status, s.err
}

func TestRecordingStatusCLIIDAndMetadataOnlyReadback(t *testing.T) {
	id := "a123456789abcdef0123456789abcdef"
	stub := &recordingStatusCLIStub{consoleRecordStub: consoleRecordStub{recording: app.ConsoleRecording{Data: createSyntheticGIFBytes(t, 8, 4), MIMEType: "image/gif"}}, status: app.ConsoleRecordStatus{SchemaVersion: "1", RecordingID: id, VMID: "local:synthetic", RequestedFrames: 2, Terminal: true, TerminalReason: "completed"}}
	cli := NewApp(nil, WithConsoleService(stub))
	output := filepath.Join(privateConsoleTestDir(t), "output.gif")
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"--direct", "console", "record", "default", "--recording-id", id, "--width", "8", "--height", "4", "--frames", "2", "--interval-ms", "100", "--output", output}, &stdout, &stderr)
	if code != ExitSuccess || stub.req.RecordingID != id {
		t.Fatalf("record ID %d %+v %s", code, stub.req, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{"--direct", "console", "record-status", "default", "--recording-id", id, "--json"}, &stdout, &stderr)
	var got app.ConsoleRecordStatus
	if code != ExitSuccess || json.Unmarshal(stdout.Bytes(), &got) != nil || got != stub.status || stub.statusReq != (app.ConsoleRecordStatusRequest{Target: "default", RecordingID: id}) || stub.calls != 1 {
		t.Fatalf("status %d %s %s %+v", code, stdout.String(), stderr.String(), stub.statusReq)
	}
}
