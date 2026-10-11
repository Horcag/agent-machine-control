package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type guestTestResolver struct{ resolution TargetResolution }

func (r guestTestResolver) ResolveTarget(context.Context, string) (TargetResolution, error) {
	return r.resolution, nil
}

type guestTestConfig struct {
	root       stream.RootIdentity
	generation string
	calls      int
	change     func(*GuestConfiguration, int)
}

func (c *guestTestConfig) Current(_ context.Context, _ domain.MachineRef, caller domain.ActorContext) (GuestConfiguration, error) {
	c.calls++
	out := GuestConfiguration{Generation: c.generation, Caller: caller.Clone(), WorkspaceIdentity: c.root}
	if c.change != nil {
		c.change(&out, c.calls)
	}
	return out, nil
}

type guestTestSafety struct {
	destructive bool
	classes     []domain.OperationClass
	calls       int
}

func (s *guestTestSafety) ResolveSafety(context.Context, domain.MachineRef) (SafetyResolution, error) {
	s.calls++
	if len(s.classes) > 0 {
		index := s.calls - 1
		if index >= len(s.classes) {
			index = len(s.classes) - 1
		}
		if s.classes[index] != domain.ClassReversibleMutation {
			return SafetyResolution{Classification: s.classes[index]}, nil
		}
	}
	if s.destructive {
		return SafetyResolution{Classification: domain.ClassDestructivePrivileged}, nil
	}
	return SafetyResolution{Classification: domain.ClassReversibleMutation, Contained: true, RollbackRef: "fixture-checkpoint", RollbackState: policy.RollbackState{Available: true, Verified: true, CheckpointID: "fixture-checkpoint"}}, nil
}

type guestTestTransport struct {
	store                       *GuestStore
	worker                      stream.WorkerIdentity
	prepared, executed, retired int
	prepareHook                 func()
	executeHook                 func(context.Context, stream.Request, io.Reader, io.Writer) (stream.Result, error)
	retireErr                   error
	fail                        bool
	alter                       func(*stream.Result)
	files                       map[string]stream.FileIdentity
}

func (f *guestTestTransport) Prepare(context.Context, domain.MachineRef, string, string) (stream.WorkerIdentity, error) {
	f.prepared++
	if f.prepareHook != nil {
		f.prepareHook()
	}
	return f.worker, nil
}
func (f *guestTestTransport) Retire(domain.MachineRef, string, string) error {
	f.retired++
	return f.retireErr
}
func (f *guestTestTransport) Execute(ctx context.Context, _ domain.MachineRef, _ string, _ string, request stream.Request, input io.Reader, output io.Writer) (stream.Result, error) {
	f.executed++
	if f.executeHook != nil {
		return f.executeHook(ctx, request, input, output)
	}
	entries, err := os.ReadDir(f.store.directory)
	if err != nil {
		return stream.Result{}, err
	}
	found := false
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".worker") {
			var record guestWorkerRecord
			if err := f.store.read(ctx, strings.TrimSuffix(entry.Name(), ".worker"), ".worker", &record); err != nil {
				return stream.Result{}, err
			}
			if record.Identity == f.worker {
				found = true
			}
		}
	}
	if !found {
		return stream.Result{}, errors.New("worker was not durably recorded before execution")
	}
	if f.fail {
		return stream.Result{}, errors.New("lost result")
	}
	result := stream.Result{ID: request.ID, Status: "ok", Worker: f.worker, SHA256: guestDigest("")}
	switch request.Operation {
	case "observe":
		root := fixtureGuestRoot()
		result.Root = &root
	case "put":
		data, err := io.ReadAll(input)
		if err != nil || int64(len(data)) != request.Size {
			return result, errors.New("input size")
		}
		root := *request.ExpectedRoot
		file := stream.FileIdentity{Path: request.Path, Volume: root.Volume, FileID: strings.Repeat("b", 32), CreationTime: 200, Owner: root.Owner, SDDLHash: root.SDDLHash, Size: request.Size, SHA256: request.SHA256, RootPath: root.Path, RootVolume: root.Volume, RootFileID: root.FileID, RootCreationTime: root.CreationTime, RootOwner: root.Owner, RootSDDLHash: root.SDDLHash}
		f.files[request.Path] = file
		result.Files = []stream.FileIdentity{file}
	case "get":
		file := f.files[request.Path]
		result.Files = []stream.FileIdentity{file}
		result.Size = file.Size
		result.SHA256 = file.SHA256
		_, _ = output.Write([]byte("data"))
	case "exec":
		_, _ = io.Copy(io.Discard, input)
		result.Size = 2
		result.SHA256 = guestSHA("ok")
		_, _ = output.Write([]byte("ok"))
	case "remove-owned":
		for _, file := range request.Owned {
			result.Removed = append(result.Removed, stream.Removal{Path: file.Path, Status: "deleted", Absent: true})
		}
	}
	if f.alter != nil {
		f.alter(&result)
	}
	return result, nil
}

func fixtureGuestRoot() stream.RootIdentity {
	return stream.RootIdentity{Path: `C:\AMC\fixture`, Volume: 7, FileID: strings.Repeat("a", 32), CreationTime: 100, Owner: "S-1-5-21-fixture", SDDLHash: strings.Repeat("d", 64)}
}

type guestHarness struct {
	service   *GuestService
	transport *guestTestTransport
	config    *guestTestConfig
	safety    *guestTestSafety
	actor     domain.ActorContext
}

func newGuestHarness(t *testing.T) *guestHarness {
	t.Helper()
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	safety := &guestTestSafety{}
	coordinator := NewSessionService(nil, safety, lease.NewManager(sd.LeasesDir()), audit.NewStore(sd.AuditDir()), receipt.NewStore(sd.ReceiptsDir()), approval.NewStore(sd.ApprovalsDir()), WithSessionMutationJournal(sessions.NewMutationJournal(sd.SessionsDir())))
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	resolver := guestTestResolver{TargetResolution{Locator: domain.MachineLocator{HostID: domain.LocalHostID, VMID: targetID}, ProviderVMID: targetID}}
	config := &guestTestConfig{root: fixtureGuestRoot(), generation: "fixture-generation"}
	store := NewGuestStore(filepath.Join(sd.Root(), "guest"))
	transport := &guestTestTransport{store: store, worker: stream.WorkerIdentity{PID: 400, BirthTicks: 133900000000000001, Image: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, SID: "S-1-5-21-fixture"}, files: map[string]stream.FileIdentity{}}
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionWrite, domain.ScopeEvidenceCapture)
	actor, err := domain.NewActorContext("fixture-actor", "fixture-actor", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return &guestHarness{service: NewGuestService(coordinator, resolver, config, transport, store), transport: transport, config: config, safety: safety, actor: actor}
}
func (h *guestHarness) params(operation, key string) GuestOperationParams {
	return GuestOperationParams{Target: "fixture-machine", Caller: h.actor, Operation: operation, Reason: "synthetic guest test", IdempotencyKey: key, Deadline: time.Now().UTC().Add(time.Minute)}
}
func (h *guestHarness) prepare(t *testing.T) {
	t.Helper()
	out, err := h.service.Execute(context.Background(), h.params("prepare", "prepare"), nil, nil)
	if err != nil || out.Receipt == nil {
		t.Fatalf("prepare: %+v %v", out, err)
	}
}
func (h *guestHarness) put(t *testing.T, key string) GuestArtifact {
	t.Helper()
	p := h.params("put", key)
	p.Size = 4
	p.SHA256 = guestSHA("data")
	out, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("data")), nil)
	if err != nil || len(out.Artifacts) != 1 {
		t.Fatalf("put: %+v %v", out, err)
	}
	return out.Artifacts[0]
}

func TestGuestPrepareDurableWorkerBeforeExecute(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	if h.transport.prepared != 1 || h.transport.executed != 1 {
		t.Fatal("whole prepare did not execute exactly once")
	}
}
func TestGuestRequiresExplicitPreparation(t *testing.T) {
	h := newGuestHarness(t)
	p := h.params("exec", "not-prepared")
	p.Size = 1
	p.SHA256 = guestSHA("x")
	p.MaxOutput = 10
	_, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(new(bytes.Buffer)))
	if !errors.Is(err, ErrGuestNotPrepared) || h.transport.executed != 0 {
		t.Fatalf("%v executions %d", err, h.transport.executed)
	}
}
func TestGuestRestartDoesNotRestoreWorkerAuthority(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	h.service = NewGuestService(h.service.session, h.service.resolver, h.config, h.transport, h.service.store)
	p := h.params("exec", "restart")
	p.Size = 1
	p.SHA256 = guestSHA("x")
	p.MaxOutput = 10
	_, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(new(bytes.Buffer)))
	if !errors.Is(err, ErrGuestUncertain) || h.transport.executed != 1 {
		t.Fatalf("%v executions %d", err, h.transport.executed)
	}
}
func TestGuestPersistentHelloFailureBlocksExecute(t *testing.T) {
	h := newGuestHarness(t)
	h.transport.prepareHook = func() {
		entries, err := os.ReadDir(h.service.store.directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".launch") {
				continue
			}
			var launch guestWorkerRecord
			if err := h.service.store.read(context.Background(), strings.TrimSuffix(entry.Name(), ".launch"), ".launch", &launch); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(h.service.store.directory, launch.RecordID+".worker"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err := h.service.Execute(context.Background(), h.params("prepare", "prepare"), nil, nil)
	if err == nil || h.transport.executed != 0 || h.transport.retired != 1 {
		t.Fatalf("%v executions %d retired %d", err, h.transport.executed, h.transport.retired)
	}
}
func TestGuestWholePutIdempotencyAndConflict(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	p := h.params("put", "put")
	p.Size = 4
	p.SHA256 = guestSHA("data")
	first, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("data")), nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("data")), nil)
	if err != nil || !again.CachedReceipt || again.Artifacts[0] != first.Artifacts[0] || h.transport.executed != 2 {
		t.Fatalf("retry %+v %v", again, err)
	}
	p.Size = 5
	_, err = h.service.Execute(context.Background(), p, stream.BytesInput([]byte("other")), nil)
	if err == nil || h.transport.executed != 2 {
		t.Fatalf("collision %v", err)
	}
}
func TestGuestUncertainOperationNeverReplays(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	h.transport.fail = true
	p := h.params("exec", "uncertain")
	p.Size = 1
	p.SHA256 = guestSHA("x")
	p.MaxOutput = 10
	_, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(new(bytes.Buffer)))
	if !errors.Is(err, ErrGuestUncertain) {
		t.Fatal(err)
	}
	h.transport.fail = false
	_, err = h.service.Execute(context.Background(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(new(bytes.Buffer)))
	if !errors.Is(err, ErrGuestUncertain) || h.transport.executed != 2 {
		t.Fatalf("uncertain replay %v executions %d", err, h.transport.executed)
	}
}
func TestGuestExpiredDeadlineAndPolicyFailClosed(t *testing.T) {
	for _, expired := range []bool{true, false} {
		h := newGuestHarness(t)
		p := h.params("prepare", "deny")
		if expired {
			p.Deadline = time.Now().Add(-time.Second)
		} else {
			h.safety.destructive = true
		}
		_, err := h.service.Execute(context.Background(), p, nil, nil)
		if err == nil || h.transport.prepared != 0 || h.transport.executed != 0 {
			t.Fatalf("expired=%v %v", expired, err)
		}
	}
}
func TestGuestFreshAuthorityBeforePrepare(t *testing.T) {
	for _, change := range []string{"actor", "generation", "root"} {
		t.Run(change, func(t *testing.T) {
			h := newGuestHarness(t)
			h.config.change = func(c *GuestConfiguration, call int) {
				if call < 2 {
					return
				}
				switch change {
				case "actor":
					delete(c.Caller.EffectivePermissions, domain.ScopeSessionOpen)
				case "generation":
					c.Generation = "replaced"
				case "root":
					c.WorkspaceIdentity.FileID = strings.Repeat("e", 32)
				}
			}
			_, err := h.service.Execute(context.Background(), h.params("prepare", "fresh"), nil, nil)
			if !errors.Is(err, ErrGuestAuthority) || h.transport.prepared != 0 {
				t.Fatalf("%v prepared %d", err, h.transport.prepared)
			}
		})
	}
}
func TestGuestFreshAuthorityAfterPersistentHello(t *testing.T) {
	h := newGuestHarness(t)
	h.config.change = func(c *GuestConfiguration, call int) {
		if call >= 3 {
			c.Generation = "replaced"
		}
	}
	_, err := h.service.Execute(context.Background(), h.params("prepare", "fresh"), nil, nil)
	if !errors.Is(err, ErrGuestAuthority) || h.transport.executed != 0 {
		t.Fatalf("%v executions %d", err, h.transport.executed)
	}
}
func TestGuestActorAndGenerationCannotUseWorker(t *testing.T) {
	for _, change := range []string{"actor", "generation"} {
		h := newGuestHarness(t)
		h.prepare(t)
		p := h.params("exec", "isolation")
		if change == "actor" {
			p.Caller.AuthenticatedCaller = "other"
			p.Caller.EffectiveActor = "other"
		} else {
			h.config.generation = "new"
		}
		p.Size = 1
		p.SHA256 = guestSHA("x")
		p.MaxOutput = 10
		_, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(new(bytes.Buffer)))
		if (!errors.Is(err, ErrGuestNotPrepared) && !errors.Is(err, ErrGuestUncertain)) || h.transport.executed != 1 {
			t.Fatalf("%s: %v", change, err)
		}
	}
}
func TestGuestOwnedDownloadAndRemoval(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	artifact := h.put(t, "put")
	p := h.params("get", "get")
	p.ArtifactIDs = []string{artifact.ID}
	p.MaxOutput = 10
	var output bytes.Buffer
	out, err := h.service.Execute(context.Background(), p, nil, stream.BufferOutput(&output))
	if err != nil || out.SHA256 != artifact.SHA256 || output.String() != "data" {
		t.Fatalf("get %+v %v", out, err)
	}
	p = h.params("remove-owned", "remove")
	p.ArtifactIDs = []string{artifact.ID}
	out, err = h.service.Execute(context.Background(), p, nil, nil)
	if err != nil || len(out.Removed) != 1 || !out.Removed[0].Attempted || !out.Removed[0].Accepted || !out.Removed[0].Absent {
		t.Fatalf("remove %+v %v", out, err)
	}
}
func TestGuestUnknownArtifactPreserved(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	p := h.params("remove-owned", "unknown")
	p.ArtifactIDs = []string{strings.Repeat("f", 32)}
	_, err := h.service.Execute(context.Background(), p, nil, nil)
	if !errors.Is(err, ErrGuestOwnership) || h.transport.executed != 1 {
		t.Fatalf("%v executions %d", err, h.transport.executed)
	}
}
func TestGuestForeignArtifactRejected(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	artifact := h.put(t, "put")
	foreign := h.actor.Clone()
	foreign.AuthenticatedCaller = "other"
	foreign.EffectiveActor = "other"
	p := h.params("prepare", "other-prepare")
	p.Caller = foreign
	if _, err := h.service.Execute(context.Background(), p, nil, nil); err != nil {
		t.Fatal(err)
	}
	p = h.params("remove-owned", "foreign")
	p.Caller = foreign
	p.ArtifactIDs = []string{artifact.ID}
	_, err := h.service.Execute(context.Background(), p, nil, nil)
	if !errors.Is(err, ErrGuestOwnership) || h.transport.executed != 3 {
		t.Fatalf("%v executions %d", err, h.transport.executed)
	}
}
func TestGuestReplacedRootAndFileMetadataRejected(t *testing.T) {
	for _, field := range []string{"root", "file", "size", "digest", "worker"} {
		t.Run(field, func(t *testing.T) {
			h := newGuestHarness(t)
			h.prepare(t)
			h.transport.alter = func(result *stream.Result) {
				switch field {
				case "root":
					result.Files[0].RootFileID = strings.Repeat("e", 32)
				case "file":
					result.Files[0].FileID = ""
				case "size":
					result.Files[0].Size++
				case "digest":
					result.Files[0].SHA256 = strings.Repeat("e", 64)
				case "worker":
					result.Worker.BirthTicks++
				}
			}
			p := h.params("put", "metadata")
			p.Size = 4
			p.SHA256 = guestSHA("data")
			out, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("data")), nil)
			if !errors.Is(err, ErrGuestOwnership) || len(out.Artifacts) != 0 {
				t.Fatalf("%+v %v", out, err)
			}
		})
	}
}
func TestGuestRemovalPerItemTruth(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	first := h.put(t, "first")
	second := h.put(t, "second")
	h.transport.alter = func(result *stream.Result) { result.Removed[1].Status = "preserved"; result.Removed[1].Absent = false }
	p := h.params("remove-owned", "manifest")
	p.ArtifactIDs = []string{first.ID, second.ID}
	out, err := h.service.Execute(context.Background(), p, nil, nil)
	if err != nil || len(out.Removed) != 2 || !out.Removed[0].Accepted || out.Removed[1].Accepted || out.Removed[1].Absent {
		t.Fatalf("%+v %v", out, err)
	}
}

func guestSHA(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}
