package stream

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func failureResult(id string, worker WorkerIdentity) Result {
	return Result{ID: id, Worker: worker, Status: "failed", ExitCode: 1, SHA256: emptyDigest, Failure: &WorkerFailure{Stage: "workspace_open", NativeError: 5}}
}
func failureWire(header, completion Result) *bytes.Reader {
	var wire bytes.Buffer
	_ = writeControl(&wire, header)
	_ = writeControl(&wire, completion)
	return bytes.NewReader(wire.Bytes())
}
func TestVerifiedWorkerFailureHasBoundedMetadataAndNoPayload(t *testing.T) {
	worker := WorkerIdentity{PID: 12, BirthTicks: 123, Image: "synthetic-worker", SID: "synthetic-sid"}
	req := Request{ID: "observe", Operation: "observe", Path: `C:\synthetic`, DeadlineUTC: time.Now().Add(time.Minute)}
	result := failureResult(req.ID, worker)
	var sink bytes.Buffer
	got, err := receiveResult(failureWire(result, result), req, &sink, worker)
	var diagnostic *WorkerFailure
	if !errors.As(err, &diagnostic) || !errors.Is(err, ErrWorkerFailure) || diagnostic.Stage != "workspace_open" || diagnostic.NativeError != 5 || got.Failure == nil || sink.Len() != 0 {
		t.Fatalf("failure contract: result=%+v error=%v payload=%d", got, err, sink.Len())
	}
	if diagnostic.Error() != "guest stream: worker failed at workspace_open (native error 5)" {
		t.Fatal(diagnostic.Error())
	}
	diagnostic.Stage = `C:\private\secret SDDL token`
	if strings.Contains(diagnostic.Error(), "private") || diagnostic.Error() != ErrProtocol.Error() {
		t.Fatal("invalid stage leaked")
	}
}
func TestWorkerFailureRejectsIncompleteOrInconsistentFrames(t *testing.T) {
	req := Request{ID: "observe", MaxOutput: 64}
	original := failureResult(req.ID, WorkerIdentity{})
	cases := map[string]func(*Result){
		"unknown stage":      func(r *Result) { r.Failure.Stage = "private exception" },
		"payload":            func(r *Result) { r.Size = 1 },
		"root":               func(r *Result) { r.Root = &RootIdentity{Path: "private"} },
		"files":              func(r *Result) { r.Files = []FileIdentity{{Path: "private"}} },
		"removal":            func(r *Result) { r.Removed = []Removal{{Path: "private"}} },
		"success":            func(r *Result) { r.Status = "ok" },
		"exit zero":          func(r *Result) { r.ExitCode = 0 },
		"missing diagnostic": func(r *Result) { r.Failure = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			changed := original
			copied := *original.Failure
			changed.Failure = &copied
			change(&changed)
			var sink bytes.Buffer
			if _, err := receiveResult(failureWire(changed, changed), req, &sink, WorkerIdentity{}); err == nil || sink.Len() != 0 {
				t.Fatal("accepted invalid failure", err)
			}
		})
	}
	mismatch := original
	mismatch.Failure = &WorkerFailure{Stage: "security_read", NativeError: 1314}
	if _, err := receiveResult(failureWire(original, mismatch), req, nil, WorkerIdentity{}); !errors.Is(err, ErrProtocol) {
		t.Fatal("mismatched stage accepted", err)
	}
	var truncated bytes.Buffer
	_ = writeControl(&truncated, original)
	if _, err := receiveResult(&truncated, req, nil, WorkerIdentity{}); err == nil {
		t.Fatal("failure without completion accepted")
	}
	success := original
	success.Status = "ok"
	success.ExitCode = 0
	success.Failure = nil
	if _, err := receiveResult(failureWire(success, original), req, nil, WorkerIdentity{}); !errors.Is(err, ErrProtocol) {
		t.Fatal("unannounced failure accepted", err)
	}
}
func TestStructuredWorkerFailureEvictsWithoutReplay(t *testing.T) {
	f := &fixtureOpener{handler: func(ch net.Conn, req Request) {
		result := failureResult(req.ID, WorkerIdentity{PID: 12, BirthTicks: 123, Image: "synthetic-worker", SID: "synthetic-sid"})
		_ = writeControl(ch, result)
		_ = writeControl(ch, result)
	}}
	client := fixtureClient(t, f)
	ctx := context.Background()
	if _, err := client.Prepare(ctx, "target", "actor", "generation"); err != nil {
		t.Fatal(err)
	}
	req := Request{ID: "observe", Operation: "observe", Path: `C:\synthetic`, DeadlineUTC: time.Now().Add(time.Minute)}
	if _, err := client.Execute(ctx, "target", "actor", "generation", req, nil, nil); !errors.Is(err, ErrWorkerFailure) {
		t.Fatal(err)
	}
	if _, err := client.Execute(ctx, "target", "actor", "generation", req, nil, nil); !errors.Is(err, ErrRequest) {
		t.Fatal("reused failed worker", err)
	}
	if f.count.Load() != 1 || f.requests.Load() != 1 {
		t.Fatal("replayed or restarted")
	}
}
