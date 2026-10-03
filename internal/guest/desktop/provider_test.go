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
	calls    int
	deadline time.Time
	command  string
	input    []byte
	output   []byte
	err      error
}

func (r *runnerFake) RunCommand(ctx context.Context, _ domain.MachineRef, command string, input []byte, limit int) ([]byte, error) {
	r.calls++
	r.deadline, _ = ctx.Deadline()
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
	response := Response{RequestID: req.RequestID, Success: true, SessionID: 1, Elevated: true}
	if req.Action == "clipboard.set.guarded" || req.Action == "clipboard.get" {
		response.Text = req.Text
		formats := []uint32{}
		if req.Text != "" {
			formats = []uint32{13}
		}
		response.Clipboard = &domain.DesktopClipboard{Formats: formats, InventoryComplete: true, Empty: len(formats) == 0}
	}
	data, err := json.Marshal(response)
	if response.Clipboard != nil && req.Text == "" {
		data = append([]byte(`{"text":"",`), data[1:]...)
	}
	return data, err
}

func request(action string) Request {
	return Request{RequestID: strings.Repeat("a", 32), Action: action, Deadline: time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)}
}

func TestExecuteCarriesGuestDataOnlyOnStdin(t *testing.T) {
	runner := &runnerFake{}
	req := request("clipboard.set")
	req.Text = "synthetic secret; $(do-not-evaluate) 世界\nnext line"
	if _, err := New(runner).Execute(context.Background(), "local:aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", req); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(runner.command, req.Text) || !strings.Contains(string(runner.input), "synthetic secret") {
		t.Fatal("input interpolated or lost")
	}
	if !strings.HasSuffix(string(runner.input), "\n") || strings.Count(string(runner.input), "\n") != 1 {
		t.Fatal("request is not one terminated compact JSON line")
	}
	var carried Request
	if err := json.Unmarshal(runner.input, &carried); err != nil {
		t.Fatal(err)
	}
	if carried.Text != req.Text {
		t.Fatal("line framing changed guest text")
	}
	deadline, _ := time.Parse(time.RFC3339Nano, carried.Deadline)
	if deadline.After(time.Now().Add(25 * time.Second)) {
		t.Fatal("unbounded guest deadline")
	}
}

func TestExchangePreservesBoundedEffectiveDeadline(t *testing.T) {
	now := time.Now()
	parent, cancel := context.WithDeadline(t.Context(), now.Add(5*time.Second))
	defer cancel()
	for _, test := range []struct {
		name                      string
		requestDeadline, expected time.Time
		parent                    context.Context
	}{
		{"transport cap", now.Add(time.Minute), time.Time{}, t.Context()},
		{"earlier request", now.Add(10 * time.Second), now.Add(10 * time.Second), t.Context()},
		{"earlier parent", now.Add(time.Minute), now.Add(5 * time.Second), parent},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &runnerFake{}

			req := request("status")
			req.Deadline = test.requestDeadline.UTC().Format(time.RFC3339Nano)
			before := time.Now()
			if _, err := New(runner).Execute(test.parent, "local:aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", req); err != nil {
				t.Fatal(err)
			}
			after := time.Now()
			var carried Request
			if err := json.Unmarshal(runner.input, &carried); err != nil {
				t.Fatal(err)
			}
			actual, err := time.Parse(time.RFC3339Nano, carried.Deadline)
			if err != nil || !actual.Equal(runner.deadline) {
				t.Fatal("runner and guest received different deadlines")
			}
			if test.expected.IsZero() {
				if actual.Before(before.Add(25*time.Second)) || actual.After(after.Add(25*time.Second)) {
					t.Fatal("transport did not retain the 25-second budget")
				}
			} else if !actual.Equal(test.expected) {
				t.Fatal("transport changed an earlier deadline")
			}
		})
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
