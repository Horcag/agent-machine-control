package desktop

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf16"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

//go:embed *.ps1 native.cs
var scripts embed.FS

var ErrUnavailable = errors.New("desktop: guest interactive driver unavailable")
var ErrInvalidRequest = errors.New("desktop: invalid request")

// CommandRunner must use enrolled SSH credentials and pinned guest host keys.
type CommandRunner interface {
	RunCommand(context.Context, domain.MachineRef, string, []byte, int) ([]byte, error)
}

type Provider struct{ runner CommandRunner }

func New(runner CommandRunner) *Provider { return &Provider{runner: runner} }

// Provision installs only the authenticated user's private interactive guest helper.
// The caller must authorize this mutation before entering this boundary.
func (p *Provider) Provision(ctx context.Context, target domain.MachineRef) (Response, error) {
	req, err := internalRequest()
	if err != nil {
		return Response{}, err
	}
	files, hashes := installationFiles()
	return p.exchange(ctx, target, req, "provision", files, hashes)
}

// Execute never retries an uncertain mutation. Native console is a separate fallback.
func (p *Provider) Execute(ctx context.Context, target domain.MachineRef, req Request) (Response, error) {
	if err := validateRequest(req, time.Now()); err != nil {
		return Response{}, err
	}
	return p.exchange(ctx, target, req, "execute", nil, nil)
}

// Remove refuses foreign or modified task installations rather than deleting them.
func (p *Provider) Remove(ctx context.Context, target domain.MachineRef) error {
	req, err := internalRequest()
	if err != nil {
		return err
	}
	_, err = p.exchange(ctx, target, req, "remove", nil, nil)
	return err
}

func internalRequest() (Request, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Request{}, ErrUnavailable
	}
	return Request{RequestID: hex.EncodeToString(id[:]), Deadline: time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano), Action: "status"}, nil
}

func installationFiles() (map[string]string, map[string]string) {
	files, hashes := map[string]string{}, map[string]string{}
	for _, name := range []string{"queue.ps1", "server.ps1", "worker.ps1", "actions.ps1", "native.cs"} {
		data, _ := scripts.ReadFile(name)
		files[name] = base64.StdEncoding.EncodeToString(data)
		digest := sha256.Sum256(data)
		hashes[name] = hex.EncodeToString(digest[:])
	}
	return files, hashes
}

func (p *Provider) exchange(ctx context.Context, target domain.MachineRef, req Request, mode string, files, hashes map[string]string) (Response, error) {
	if err := target.Validate(); err != nil {
		return Response{}, ErrInvalidRequest
	}
	if p == nil || p.runner == nil {
		return Response{}, ErrUnavailable
	}
	deadline, err := time.Parse(time.RFC3339Nano, req.Deadline)
	if err != nil {
		return Response{}, ErrInvalidRequest
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	ctx, cancelBound := context.WithTimeout(ctx, 30*time.Second)
	defer cancelBound()
	deadline, _ = ctx.Deadline()
	req.Deadline = deadline.UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(struct {
		Request
		Mode    string            `json:"mode"`
		Files   map[string]string `json:"files,omitempty"`
		Hashes  map[string]string `json:"hashes,omitempty"`
		Program string            `json:"program"`
	}{req, mode, files, hashes, transportProgram()})
	if err != nil {
		return Response{}, ErrInvalidRequest
	}
	output, err := p.runner.RunCommand(ctx, target, transportCommand(), data, 512*1024)
	if err != nil {
		return Response{}, safeError(ctx)
	}
	response, err := decodeResponse(output, req.RequestID, mode)
	if err != nil {
		return Response{}, err
	}
	return response, nil
}

func transportProgram() string {
	queue, _ := scripts.ReadFile("queue.ps1")
	transport, _ := scripts.ReadFile("transport.ps1")
	return base64.StdEncoding.EncodeToString(append(append([]byte("param($request)\n"), queue...), append([]byte{'\n'}, transport...)...))
}

func transportCommand() string {
	// Only the fixed embedded program is evaluated. Action/text fields remain JSON data.
	// Stdin keeps both program and data below the Windows default-shell command limit.
	bootstrap := `[Console]::InputEncoding=[Text.UTF8Encoding]::new($false);$envelope=[Console]::In.ReadToEnd()|ConvertFrom-Json;$program=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($envelope.program));$envelope.PSObject.Properties.Remove('program');& ([ScriptBlock]::Create($program)) $envelope`
	runes := utf16.Encode([]rune(bootstrap))
	data := make([]byte, 2*len(runes))
	for i, code := range runes {
		binary.LittleEndian.PutUint16(data[2*i:], code)
	}
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(data)
}

func decodeResponse(data []byte, id, mode string) (Response, error) {
	var response Response
	if len(data) > 512*1024 {
		return response, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&response) != nil || decoder.Decode(new(any)) != io.EOF || response.RequestID != id || !response.Success || response.Error != "" {
		return Response{}, ErrUnavailable
	}
	if mode != "remove" && (response.SessionID < 1 || !response.Elevated) {
		return Response{}, ErrUnavailable
	}
	if len(response.Windows) > 128 || len(response.Elements) > 256 || len(response.Text) > 16384 {
		return Response{}, ErrUnavailable
	}
	return response, nil
}

func safeError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrUnavailable
}
