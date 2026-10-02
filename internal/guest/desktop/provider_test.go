package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

type runnerFake struct {
	calls   int
	command string
	input   []byte
	output  []byte
	err     error
}

func (r *runnerFake) RunCommand(_ context.Context, _ domain.MachineRef, command string, input []byte, limit int) ([]byte, error) {
	r.calls++
	r.command, r.input = command, input
	if limit != 512*1024 {
		return nil, errors.New("incorrect output limit")
	}
	if r.output != nil || r.err != nil {
		return r.output, r.err
	}
	var req Request
	if err := json.Unmarshal(input, &req); err != nil {
		return nil, err
	}
	return json.Marshal(Response{RequestID: req.RequestID, Success: true, SessionID: 1, Elevated: true})
}

func request(action string) Request {
	return Request{RequestID: strings.Repeat("a", 32), Action: action, Deadline: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
}

func TestExecuteCarriesGuestDataOnlyOnStdin(t *testing.T) {
	runner := &runnerFake{}
	req := request("clipboard.set")
	req.Text = "synthetic secret; $(do-not-evaluate) 世界"
	if _, err := New(runner).Execute(context.Background(), "local:aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(runner.command, req.Text) || !strings.Contains(string(runner.input), "synthetic secret") {
		t.Fatal("input interpolated or lost")
	}
	var carried Request
	if err := json.Unmarshal(runner.input, &carried); err != nil {
		t.Fatal(err)
	}
	deadline, _ := time.Parse(time.RFC3339Nano, carried.Deadline)
	if deadline.After(time.Now().Add(31 * time.Second)) {
		t.Fatal("unbounded guest deadline")
	}
}

func TestInvalidRequestsNeverReachSSH(t *testing.T) {
	for _, mutate := range []func(*Request){
		func(r *Request) { r.Action = "unknown" }, func(r *Request) { r.RequestID = "../x" }, func(r *Request) { r.Deadline = "invalid" },
		func(r *Request) { r.Deadline = time.Now().Add(-time.Second).Format(time.RFC3339Nano) }, func(r *Request) { r.Text = "secret" },
		func(r *Request) { r.X = 1 }, func(r *Request) { r.Executable = "C:\\Windows\\app.exe" },
	} {
		runner := &runnerFake{}
		req := request("status")
		mutate(&req)
		if _, err := New(runner).Execute(context.Background(), "local:aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", req); err == nil || runner.calls != 0 {
			t.Fatal("invalid input reached SSH")
		}
	}
	runner := &runnerFake{}
	if _, err := New(runner).Execute(context.Background(), "", request("status")); err == nil || runner.calls != 0 {
		t.Fatal("invalid target reached SSH")
	}
}

func TestProvisionAndRemoveUseBoundedPrivateInstallation(t *testing.T) {
	runner := &runnerFake{}
	provider := New(runner)
	target := domain.MachineRef("local:aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa")
	if _, err := provider.Provision(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Program string            `json:"program"`
		Mode    string            `json:"mode"`
		Files   map[string]string `json:"files"`
		Hashes  map[string]string `json:"hashes"`
	}
	if err := json.Unmarshal(runner.input, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Mode != "provision" || len(payload.Files) != 5 || len(payload.Hashes) != 5 || len(runner.command) > 8191 {
		t.Fatal("incomplete/unbounded installation")
	}

	for name, encoded := range payload.Files {
		if data, err := base64.StdEncoding.DecodeString(encoded); err != nil || len(data) == 0 || payload.Hashes[name] == "" {
			t.Fatal("invalid embedded helper")
		}
	}
	if err := provider.Remove(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runner.input), `"mode":"remove"`) {
		t.Fatal("missing exact uninstall")
	}
}

func TestProviderFailuresAndMalformedResponsesDoNotExposeGuestData(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, output := range []string{
		`{"request_id":"` + id + `","success":false,"error":"synthetic secret"}`,
		`{"request_id":"wrong","success":true,"session_id":1,"elevated":true}`,
		`{"request_id":"` + id + `","success":true,"session_id":0,"elevated":true}`,
		`{"request_id":"` + id + `","success":true,"session_id":1,"elevated":false}`,
		`{"request_id":"` + id + `","success":true,"session_id":1,"elevated":true,"unknown":true}`,
		`{"request_id":"` + id + `","success":true,"session_id":1,"elevated":true} {}`,
	} {
		_, err := New(&runnerFake{output: []byte(output)}).Execute(context.Background(), "local:aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", request("status"))
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unsafe response error: %v", err)
		}
	}
	_, err := New(&runnerFake{err: errors.New("synthetic secret")}).Execute(context.Background(), "local:aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", request("status"))
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatal("transport error leaked")
	}
}

func TestProvisionCarriesFixedProgramOutsideWindowsCommandLine(t *testing.T) {
	runner := &runnerFake{}
	if _, err := New(runner).Provision(context.Background(), "synthetic"); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Program string `json:"program"`
	}
	if err := json.Unmarshal(runner.input, &payload); err != nil {
		t.Fatal(err)
	}
	program, err := base64.StdEncoding.DecodeString(payload.Program)
	if err != nil || !strings.HasPrefix(string(program), "param($request)\n") || !strings.Contains(string(program), "Assert-Installed") {
		t.Fatal("missing fixed stdin transport program")
	}
	if len(runner.command) > 8191 || strings.Contains(runner.command, payload.Program) {
		t.Fatal("program exceeds shell limit or moved into command line")
	}
}
