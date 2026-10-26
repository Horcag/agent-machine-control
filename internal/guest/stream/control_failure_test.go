package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

func controlFatal(stage string, original WorkerIdentity) Result {
	return Result{Status: "failed", ExitCode: 1, SHA256: emptyDigest, Worker: original, ControlFailure: &ControlFailure{Stage: stage}}
}
func TestControlFatalPairOriginalBindingAndNoPayload(t *testing.T) {
	original := WorkerIdentity{PID: 12, BirthTicks: 123, Image: "synthetic-worker", SID: "synthetic-sid"}
	for _, stage := range []string{"control_eof", "control_read", "control_decode", "control_header"} {
		t.Run(stage, func(t *testing.T) {
			fatal := controlFatal(stage, original)
			var output bytes.Buffer
			_, err := receiveResult(failureWire(fatal, fatal), Request{ID: "actual-request"}, &output, original)
			observed, ok := errors.AsType[*ControlFailure](err)
			if !ok || observed.Stage != stage || !errors.Is(err, ErrControlFailure) || errors.Is(err, ErrWorkerFailure) || output.Len() != 0 {
				t.Fatal("fatal provenance lost", err)
			}
		})
	}
}
func TestControlFatalRejectsShapeIdentityMismatchAndTruncation(t *testing.T) {
	original := WorkerIdentity{PID: 12, BirthTicks: 123, Image: "synthetic-worker", SID: "synthetic-sid"}
	for name, alter := range map[string]func(*Result){
		"request_id": func(r *Result) { r.ID = "foreign" }, "identity": func(r *Result) { r.Worker.BirthTicks++ },
		"stage": func(r *Result) { r.ControlFailure.Stage = "private path" }, "payload": func(r *Result) { r.Size = 1 },
		"digest": func(r *Result) { r.SHA256 = hash([]byte("foreign")) }, "root": func(r *Result) { r.Root = &RootIdentity{} },
		"file": func(r *Result) { r.Files = []FileIdentity{{}} }, "removal": func(r *Result) { r.Removed = []Removal{{}} },
		"business_failure": func(r *Result) { r.Failure = &WorkerFailure{Stage: "observe"} }, "status": func(r *Result) { r.Status = "ok" },
		"exit": func(r *Result) { r.ExitCode = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			fatal := controlFatal("control_eof", original)
			alter(&fatal)
			var output bytes.Buffer
			if _, err := receiveResult(failureWire(fatal, fatal), Request{ID: "request"}, &output, original); !errors.Is(err, ErrProtocol) || output.Len() != 0 {
				t.Fatal("invalid fatal accepted", err)
			}
		})
	}
	fatal := controlFatal("control_read", original)
	other := controlFatal("control_decode", original)
	if _, err := receiveResult(failureWire(fatal, other), Request{}, nil, original); !errors.Is(err, ErrProtocol) {
		t.Fatal("mismatched pair accepted", err)
	}
	var changedWire bytes.Buffer
	_ = writeControl(&changedWire, fatal)
	raw, _ := json.Marshal(fatal)
	var changed map[string]any
	_ = json.Unmarshal(raw, &changed)
	changed["files"] = []any{}
	_ = writeControl(&changedWire, changed)
	if _, err := receiveResult(&changedWire, Request{}, nil, original); !errors.Is(err, ErrProtocol) {
		t.Fatal("nonidentical fatal pair accepted", err)
	}
	var partial bytes.Buffer
	_ = writeControl(&partial, fatal)
	if _, err := receiveResult(&partial, Request{}, nil, original); errors.Is(err, ErrControlFailure) || err == nil {
		t.Fatal("partial pair promoted", err)
	}
}
func TestControlFatalEvictsWithoutReplay(t *testing.T) {
	fixture := &fixtureOpener{handler: func(conn net.Conn, _ Request) {
		fatal := controlFatal("control_read", WorkerIdentity{PID: 12, BirthTicks: 123, Image: "synthetic-worker", SID: "synthetic-sid"})
		_ = writeControl(conn, fatal)
		_ = writeControl(conn, fatal)
	}}
	client := fixtureClient(t, fixture)
	if _, err := client.Prepare(t.Context(), "target", "actor", "g"); err != nil {
		t.Fatal(err)
	}
	req := Request{ID: "request", Operation: "observe", Path: `C:\synthetic`, DeadlineUTC: time.Now().Add(time.Second)}
	if _, err := client.Execute(t.Context(), "target", "actor", "g", req, nil, nil); !errors.Is(err, ErrControlFailure) {
		t.Fatal(err)
	}
	if _, err := client.Execute(t.Context(), "target", "actor", "g", req, nil, nil); !errors.Is(err, ErrRequest) || fixture.count.Load() != 1 || fixture.requests.Load() != 1 {
		t.Fatal("fatal replayed", err)
	}
}

func TestControlFatalCannotReplaceOperationCompletion(t *testing.T) {
	original := WorkerIdentity{PID: 12, BirthTicks: 123, Image: "synthetic-worker", SID: "synthetic-sid"}
	header := Result{ID: "request", Worker: original, Status: "ok", SHA256: emptyDigest}
	completion := controlFatal("control_read", original)
	completion.ID = "request"
	if _, err := receiveResult(failureWire(header, completion), Request{ID: "request"}, nil, original); !errors.Is(err, ErrProtocol) || errors.Is(err, ErrControlFailure) {
		t.Fatal("unannounced fatal replaced operation result", err)
	}
}
