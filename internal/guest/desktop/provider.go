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
var ErrClipboardUncertain = domain.ErrClipboardUncertain

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
	// A distinct action is rejected by the installed old helper allowlist.
	if req.ExpectedSequence != nil {
		req.Action = "clipboard.set.guarded"
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
	// Leave ten seconds of clock skew headroom under the guest bootstrap
	// 35-second admission bound, while retaining earlier caller deadlines.
	ctx, cancelBound := context.WithTimeout(ctx, 25*time.Second)
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
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	output, err := p.runner.RunCommand(ctx, target, transportCommand(), append(data, '\n'), 512*1024)
	if err != nil {
		if req.Action == "clipboard.set.guarded" {
			return Response{}, ErrClipboardUncertain
		}
		return Response{}, safeError(ctx)
	}
	response, err := decodeResponse(output, req.RequestID, mode, req)
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
	// One compact JSON line avoids waiting for SSH stdin EOF. The fixed bootstrap
	// stays below the shell command limit; program and guest data remain on stdin.
	// Read exactly one byte per call to avoid console text reader buffering.
	// The byte cap includes LF; UTF-8 decoding rejects loss.
	bootstrap := `$ErrorActionPreference='Stop';$stream=[Console]::OpenStandardInput();$buffer=[IO.MemoryStream]::new();$one=New-Object byte[] 1;try{while($true){$read=$stream.Read($one,0,1);if($read -eq 0){throw 'truncated_request'};if($buffer.Length -ge 524288){throw 'oversized_request'};$buffer.Write($one,0,1);if($one[0] -eq 10){break}};if($buffer.Length -eq 1){throw 'empty_request'};$utf8=[Text.UTF8Encoding]::new($false,$true);$envelope=$utf8.GetString($buffer.ToArray(),0,[int]$buffer.Length-1)|ConvertFrom-Json}finally{$buffer.Dispose()};if($envelope.request_id -cnotmatch '^[0-9a-f]{32}$'){throw 'invalid_request'};$deadline=[DateTimeOffset]::Parse($envelope.deadline);$now=[DateTimeOffset]::UtcNow;if($deadline -le $now -or $deadline -gt $now.AddSeconds(35)){throw 'expired_request'};$program=$utf8.GetString([Convert]::FromBase64String($envelope.program));$envelope.PSObject.Properties.Remove('program');& ([ScriptBlock]::Create($program)) $envelope`
	runes := utf16.Encode([]rune(bootstrap))
	data := make([]byte, 2*len(runes))
	for i, code := range runes {
		binary.LittleEndian.PutUint16(data[2*i:], code)
	}
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(data)
}

func decodeResponse(data []byte, id, mode string, requests ...Request) (Response, error) {
	var req Request
	if len(requests) != 0 {
		req = requests[0]
	}
	failure := ErrUnavailable
	if req.Action == "clipboard.set.guarded" {
		failure = ErrClipboardUncertain
	}
	var response Response
	if len(data) > 512*1024 {
		return response, failure
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&response) != nil || decoder.Decode(new(any)) != io.EOF || response.RequestID != id {
		return Response{}, failure
	}
	if !response.Success || response.Error != "" {
		return Response{}, responseFailure(response, failure)
	}
	if !validResponseEnvelope(response, mode) {
		return Response{}, failure
	}
	if !validClipboardResponse(data, response, req) {
		return Response{}, failure
	}
	return response, nil
}

// Validate the session authority and bounded payload shared by all actions.
func validResponseEnvelope(response Response, mode string) bool {
	return (mode == "remove" || (response.SessionID >= 1 && response.Elevated)) &&
		len(response.Windows) <= 128 && len(response.Elements) <= 256 && len(response.Text) <= 16384
}

func responseFailure(response Response, fallback error) error {
	if !response.Success && (response.Error == "clipboard_pre_effect_rejected" || response.Error == "unsupported_action") {
		return ErrUnavailable
	}
	if response.Error == "clipboard_possibly_cleared" {
		return ErrClipboardUncertain
	}
	return fallback
}

func safeError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrUnavailable
}
