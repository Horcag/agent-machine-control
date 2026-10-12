package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type guestHTTPServiceFixture struct {
	root       stream.RootIdentity
	worker     stream.WorkerIdentity
	files      map[string]stream.FileIdentity
	data       map[string][]byte
	operations []string
}

func (f *guestHTTPServiceFixture) ResolveTarget(context.Context, string) (app.TargetResolution, error) {
	guid := "11111111-1111-4111-8111-111111111111"
	return app.TargetResolution{Locator: domain.MachineLocator{HostID: domain.LocalHostID, VMID: guid}, ProviderVMID: guid}, nil
}
func (f *guestHTTPServiceFixture) Current(_ context.Context, _ domain.MachineRef, caller domain.ActorContext) (app.GuestConfiguration, error) {
	return app.GuestConfiguration{Generation: "synthetic-generation", Caller: caller, WorkspaceIdentity: f.root}, nil
}
func (f *guestHTTPServiceFixture) ResolveSafety(context.Context, domain.MachineRef) (app.SafetyResolution, error) {
	return app.SafetyResolution{Classification: domain.ClassReversibleMutation, Contained: true, RollbackRef: "synthetic-checkpoint", RollbackState: policy.RollbackState{Available: true, Verified: true, CheckpointID: "synthetic-checkpoint"}}, nil
}
func (f *guestHTTPServiceFixture) Prepare(context.Context, domain.MachineRef, string, string) (stream.WorkerIdentity, error) {
	return f.worker, nil
}
func (f *guestHTTPServiceFixture) Retire(domain.MachineRef, string, string) error { return nil }
func (f *guestHTTPServiceFixture) Execute(_ context.Context, _ domain.MachineRef, _ string, _ string, p stream.Request, input io.Reader, output io.Writer) (stream.Result, error) {
	f.operations = append(f.operations, p.Operation)
	result := stream.Result{ID: p.ID, Status: "ok", Worker: f.worker}
	switch p.Operation {
	case "observe":
		result.Root = &f.root
	case "put":
		data, err := io.ReadAll(input)
		if err != nil {
			return result, err
		}
		if int64(len(data)) != p.Size {
			return result, io.ErrUnexpectedEOF
		}
		file := stream.FileIdentity{Path: p.Path, Volume: f.root.Volume, FileID: strings.Repeat("b", 32), CreationTime: 200, Owner: f.root.Owner, SDDLHash: f.root.SDDLHash, Size: p.Size, SHA256: p.SHA256, RootPath: f.root.Path, RootVolume: f.root.Volume, RootFileID: f.root.FileID, RootCreationTime: f.root.CreationTime, RootOwner: f.root.Owner, RootSDDLHash: f.root.SDDLHash}
		f.files[p.Path] = file
		f.data[p.Path] = data
		result.Files = []stream.FileIdentity{file}
	case "get":
		file := f.files[p.Path]
		result.Files = []stream.FileIdentity{file}
		result.Size, result.SHA256 = file.Size, file.SHA256
		if _, err := output.Write(f.data[p.Path]); err != nil {
			return result, err
		}
	case "exec":
		if _, err := io.Copy(io.Discard, input); err != nil {
			return result, err
		}
		data := []byte("verified command output")
		result.Size = int64(len(data))
		hash := sha256.Sum256(data)
		result.SHA256 = hex.EncodeToString(hash[:])
		if _, err := output.Write(data); err != nil {
			return result, err
		}
	case "remove-owned":
		for _, file := range p.Owned {
			result.Removed = append(result.Removed, stream.Removal{Path: file.Path, Status: "deleted", Absent: true})
		}
	}
	return result, nil
}
func TestGuestHTTPAllOperationsReachRealSharedService(t *testing.T) {
	httpServer, token, fixture := newGuestHTTPServiceFixture(t)
	var artifactID string
	for _, operation := range []string{"prepare", "put", "get", "exec", "remove-owned"} {
		p, payload := guestHTTPCase(operation, artifactID)
		final, body := invokeGuestHTTPFixture(t, httpServer.URL, token, p, payload)
		if final.Error != "" || final.Result.Receipt == nil || final.Result.Receipt.OperationKind != domain.OperationKind("guest."+operation) {
			t.Fatalf("%s was rejected before shared service: %+v", operation, final)
		}
		if operation == "put" {
			if len(final.Result.Artifacts) != 1 {
				t.Fatal("put did not return opaque ownership")
			}
			artifactID = final.Result.Artifacts[0].ID
		}
		if operation == "get" && !bytes.Equal(body, []byte{0, 255, 128, 1}) {
			t.Fatal("binary get did not preserve source bytes")
		}
		if operation == "exec" && string(body) != "verified command output" {
			t.Fatal("exec output was not published")
		}
	}
	if strings.Join(fixture.operations, ",") != "observe,put,get,exec,remove-owned" {
		t.Fatalf("whole operation decisions were not executed: %v", fixture.operations)
	}
}
func invokeGuestHTTPFixture(t *testing.T, endpoint, token string, p app.GuestOperationParams, payload []byte) (GuestFinal, []byte) {
	t.Helper()
	metadata, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var requestBody bytes.Buffer
	// #nosec G115 -- synthetic bounded metadata is less than 64KiB.
	if err = binary.Write(&requestBody, binary.BigEndian, uint32(len(metadata))); err != nil {
		t.Fatal(err)
	}
	requestBody.Write(metadata)
	requestBody.Write(payload)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, bytes.NewReader(requestBody.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", GuestContentType)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	trailer, err := base64.RawStdEncoding.DecodeString(response.Trailer.Get(GuestResultTrailer))
	if err != nil {
		t.Fatal(err)
	}
	var final GuestFinal
	if err = json.Unmarshal(trailer, &final); err != nil {
		t.Fatalf("final trailer: %v status=%d body=%q", err, response.StatusCode, body)
	}
	return final, body
}

func guestHTTPCase(operation, artifactID string) (app.GuestOperationParams, []byte) {
	p := app.GuestOperationParams{Operation: operation, Reason: "synthetic whole guest operation", IdempotencyKey: "http-" + operation, Deadline: time.Now().Add(time.Minute)}
	var payload []byte
	switch operation {
	case "put":
		payload = []byte{0, 255, 128, 1}
	case "get":
		p.ArtifactIDs = []string{artifactID}
		p.MaxOutput = 100
	case "exec":
		payload = []byte("Write-Output 'synthetic'")
		p.MaxOutput = 100
	case "remove-owned":
		p.ArtifactIDs = []string{artifactID}
	}
	if payload != nil {
		p.Size = int64(len(payload))
		hash := sha256.Sum256(payload)
		p.SHA256 = hex.EncodeToString(hash[:])
	}
	return p, payload
}

func newGuestHTTPServiceFixture(t *testing.T) (*httptest.Server, string, *guestHTTPServiceFixture) {
	t.Helper()
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	store, err := auth.LoadOrCreate(sd.AuthDir(), auth.WithPrincipalResolver(func() (string, error) { return "operator:synthetic", nil }))
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.ReadTokenFile(sd.AuthDir(), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &guestHTTPServiceFixture{root: stream.RootIdentity{Path: `C:\AMC\synthetic`, Volume: 7, FileID: strings.Repeat("a", 32), CreationTime: 100, Owner: "S-1-5-21-synthetic", SDDLHash: strings.Repeat("d", 64)}, worker: stream.WorkerIdentity{PID: 123, BirthTicks: 638000000000000001, Image: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, SID: "S-1-5-21-synthetic"}, files: map[string]stream.FileIdentity{}, data: map[string][]byte{}}
	session := app.NewSessionService(nil, fixture, lease.NewManager(sd.LeasesDir()), audit.NewStore(sd.AuditDir()), receipt.NewStore(sd.ReceiptsDir()), approval.NewStore(sd.ApprovalsDir()), app.WithSessionMutationJournal(sessions.NewMutationJournal(sd.SessionsDir())))
	svc := app.NewGuestService(session, fixture, fixture, fixture, app.NewGuestStore(filepath.Join(sd.Root(), "guest")))
	server := &Server{guestService: svc, authStore: store, semaphore: make(chan struct{}, 8)}
	httpServer := httptest.NewServer(server.authMiddleware(http.HandlerFunc(server.handleGuestOperation)))
	t.Cleanup(httpServer.Close)
	return httpServer, token, fixture
}
