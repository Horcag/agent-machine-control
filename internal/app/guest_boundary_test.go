package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func TestGuestAdmittedSafetyCannotBeReplaced(t *testing.T) {
	for _, classes := range [][]domain.OperationClass{
		{domain.ClassDestructivePrivileged, domain.ClassReversibleMutation, domain.ClassDestructivePrivileged},
		{domain.ClassReversibleMutation, domain.ClassDestructivePrivileged},
		{domain.ClassReversibleMutation, domain.ClassReversibleMutation, domain.ClassDestructivePrivileged},
		{domain.ClassObserve},
	} {
		t.Run(strings.Join([]string{string(classes[0]), string(classes[len(classes)-1])}, "-"), func(t *testing.T) {
			h := newGuestHarness(t)
			h.safety.classes = classes
			_, err := h.service.Execute(context.Background(), h.params("prepare", "safety"), nil, nil)
			if err == nil || h.transport.executed != 0 {
				t.Fatalf("admitted unsafe transition: %v executions=%d", err, h.transport.executed)
			}
		})
	}
}

func TestGuestDownloadReceiptAndOutputCommit(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	artifact := h.put(t, "put")
	p := h.params("get", "get")
	p.ArtifactIDs = []string{artifact.ID}
	p.MaxOutput = 10
	var output bytes.Buffer
	observing := guestObservingOutput{buffer: &output, observe: func() {
		op, err := guestOperation(p, domain.MachineRef(h.service.resolver.(guestTestResolver).resolution.Locator.String()), GuestConfiguration{Generation: h.config.generation, Caller: h.actor, WorkspaceIdentity: h.config.root})
		if err != nil {
			t.Fatal(err)
		}
		rcpt, err := h.service.session.receiptStore.LookupIdempotencyContext(context.Background(), op)
		if err != nil || rcpt == nil || rcpt.Validate() != nil || rcpt.Outcome.Status != domain.OutcomeSuccess {
			t.Fatalf("output before durable receipt: %+v %v", rcpt, err)
		}
	}}
	out, err := h.service.Execute(context.Background(), p, nil, observing)
	if err != nil || out.Receipt == nil || out.Receipt.Validate() != nil || out.Receipt.RollbackRef == "" || output.String() != "data" {
		t.Fatalf("get %+v %v", out, err)
	}
	assertGuestSpoolClean(t, h)
}

type guestObservingOutput struct {
	buffer  *bytes.Buffer
	observe func()
}

func (w guestObservingOutput) Write(p []byte) (int, error) {
	return w.WriteContext(context.Background(), p)
}
func (w guestObservingOutput) WriteContext(_ context.Context, p []byte) (int, error) {
	w.observe()
	return w.buffer.Write(p)
}

func TestGuestInvalidOutputNeverEscapes(t *testing.T) {
	for _, field := range []string{"worker", "file", "digest", "short", "overflow"} {
		t.Run(field, func(t *testing.T) {
			h := newGuestHarness(t)
			h.prepare(t)
			artifact := h.put(t, "put")
			p := h.params("get", "invalid")
			p.ArtifactIDs = []string{artifact.ID}
			p.MaxOutput = 4
			if field == "overflow" {
				h.transport.executeHook = func(_ context.Context, _ stream.Request, _ io.Reader, output io.Writer) (stream.Result, error) {
					_, err := output.Write([]byte("overflow"))
					return stream.Result{}, err
				}
			} else {
				h.transport.alter = func(result *stream.Result) {
					switch field {
					case "worker":
						result.Worker.BirthTicks++
					case "file":
						result.Files[0].FileID = strings.Repeat("f", 32)
					case "digest":
						result.SHA256 = strings.Repeat("f", 64)
					case "short":
						result.Size--
					}
				}
			}
			var output bytes.Buffer
			_, err := h.service.Execute(context.Background(), p, nil, stream.BufferOutput(&output))
			if err == nil || output.Len() != 0 {
				t.Fatalf("invalid %s escaped %q: %v", field, output.String(), err)
			}
			assertGuestSpoolClean(t, h)
		})
	}
}

func TestGuestPartialDownloadKeepsApprovalAndNeverReplays(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	artifact := h.put(t, "put")
	h.safety.destructive = true
	scopes := domain.NewScopeSet(domain.ScopeSessionAdmin)
	operator, err := domain.NewActorContext("fixture-operator", "fixture-operator", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	p := h.params("get", "partial-get")
	p.Caller = operator
	p.ArtifactIDs = []string{artifact.ID}
	p.MaxOutput = 10
	// Issue to the same canonical beneficiary as a separately prepared, owned
	// fixture; the synthetic actor now represents that beneficiary consistently.
	agent, _ := sessionApprovalAgentActor()
	agent.CallerPermissions[domain.ScopeEvidenceCapture] = struct{}{}
	agent.EffectivePermissions[domain.ScopeEvidenceCapture] = struct{}{}
	// Existing ownership remains isolated: use the beneficiary actor from startup.
	h.safety.destructive = false
	h.actor = agent
	h.prepareWithKey(t, "agent-prepare")
	artifact = h.put(t, "agent-put")
	h.safety.destructive = true
	p.ArtifactIDs = []string{artifact.ID}
	grant, _, err := h.service.IssueApproval(context.Background(), p, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p.Caller = agent
	p.Deadline = grant.Deadline
	p.ApprovalID = grant.ApprovalID
	h.transport.executeHook = func(ctx context.Context, _ stream.Request, _ io.Reader, output io.Writer) (stream.Result, error) {
		_, _ = output.(stream.ContextWriter).WriteContext(ctx, []byte("da"))
		return stream.Result{}, context.Canceled
	}
	var output bytes.Buffer
	first, err := h.service.Execute(context.Background(), p, nil, stream.BufferOutput(&output))
	if !errors.Is(err, ErrGuestUncertain) || first.Receipt == nil || output.Len() != 0 {
		t.Fatalf("partial output=%q receipt=%+v err=%v", output.String(), first.Receipt, err)
	}
	consumed, err := h.service.session.approvalStore.IsConsumedContext(context.Background(), grant.ApprovalID)
	if err != nil || !consumed {
		t.Fatalf("dispatched read released approval: %v %v", consumed, err)
	}
	count := h.transport.executed
	again, err := h.service.Execute(context.Background(), p, nil, stream.BufferOutput(&output))
	if !errors.Is(err, ErrGuestUncertain) || !again.CachedReceipt || h.transport.executed != count || output.Len() != 0 {
		t.Fatalf("replayed partial download: %+v %v", again, err)
	}
	assertGuestSpoolClean(t, h)
}

func (h *guestHarness) prepareWithKey(t *testing.T, key string) {
	t.Helper()
	if _, err := h.service.Execute(context.Background(), h.params("prepare", key), nil, nil); err != nil {
		t.Fatal(err)
	}
}
func assertGuestSpoolClean(t *testing.T, h *guestHarness) {
	t.Helper()
	entries, err := os.ReadDir(h.service.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "guest-spool-") {
			t.Fatalf("retained temporary spool %s", entry.Name())
		}
	}
	if h.service.spoolBytes != 0 {
		t.Fatalf("retained spool reservation %d", h.service.spoolBytes)
	}
}

func TestGuestRetirementFailureBlocksPreparationAcrossGenerationAndRestart(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	h.transport.fail = true
	h.transport.retireErr = errors.New("channel close uncertain")
	p := h.params("exec", "failed")
	p.Size = 1
	p.SHA256 = guestSHA("x")
	p.MaxOutput = 10
	_, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("x")), stream.BufferOutput(new(bytes.Buffer)))
	if !errors.Is(err, ErrGuestUncertain) {
		t.Fatal(err)
	}
	h.transport.fail = false
	h.config.generation = "changed-generation"
	_, err = h.service.Execute(context.Background(), h.params("prepare", "other-generation"), nil, nil)
	if !errors.Is(err, ErrGuestUncertain) || h.transport.prepared != 1 {
		t.Fatalf("replaced unresolved worker: %v prepares=%d", err, h.transport.prepared)
	}
	h.service = NewGuestService(h.service.session, h.service.resolver, h.config, h.transport, h.service.store)
	_, err = h.service.Execute(context.Background(), h.params("prepare", "restart"), nil, nil)
	if !errors.Is(err, ErrGuestUncertain) || h.transport.prepared != 1 {
		t.Fatalf("restart forgot native obligation: %v", err)
	}
}

func TestGuestFailedDurableFinalizationDoesNotPublish(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	artifact := h.put(t, "put")
	h.service.session.mutationJournal = sessions.NewMutationJournal(h.service.session.mutationJournalDirForTest(t), sessions.WithMutationJournalHook(func(action string) error {
		if action == "finalize" {
			return errors.New("finalization unavailable")
		}
		return nil
	}))
	p := h.params("get", "finalization-failure")
	p.ArtifactIDs = []string{artifact.ID}
	p.MaxOutput = 10
	var output bytes.Buffer
	_, err := h.service.Execute(context.Background(), p, nil, stream.BufferOutput(&output))
	if err == nil || output.Len() != 0 {
		t.Fatalf("uncommitted output %q %v", output.String(), err)
	}
	assertGuestSpoolClean(t, h)
}
func (s *SessionService) mutationJournalDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGuestInputSizeAndDigestAreCheckedBeforeOwnership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   string
		size   int64
		digest string
	}{{"digest", "data", 4, guestSHA("else")}, {"short", "da", 4, guestSHA("data")}, {"extra", "data-extra", 4, guestSHA("data")}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGuestHarness(t)
			h.prepare(t)
			p := h.params("put", "input-check")
			p.Size = tc.size
			p.SHA256 = tc.digest
			out, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte(tc.data)), nil)
			if err == nil || len(out.Artifacts) != 0 {
				t.Fatalf("invalid input became owned: %+v %v", out, err)
			}
		})
	}
}

func TestGuestSpoolAggregateQuotaAndCleanup(t *testing.T) {
	h := newGuestHarness(t)
	p := h.params("get", "spool")
	p.MaxOutput = MaxGuestSpoolBytes
	first, err := h.service.spool(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		other, err := h.service.spool(context.Background(), p)
		if other != nil {
			_ = other.cleanup()
		}
		done <- err
	}()
	if err := <-done; err == nil {
		t.Fatal("aggregate staging quota exceeded")
	}
	if err := first.cleanup(); err != nil {
		t.Fatal(err)
	}
	assertGuestSpoolClean(t, h)
}

func TestGuestRawIOFailsBeforeDispatch(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	p := h.params("exec", "raw-input")
	p.Size = 1
	p.SHA256 = guestSHA("x")
	p.MaxOutput = 10
	if _, err := h.service.Execute(context.Background(), p, strings.NewReader("x"), stream.BufferOutput(new(bytes.Buffer))); err == nil || h.transport.executed != 1 {
		t.Fatalf("raw source admitted: %v", err)
	}
	if _, err := h.service.Execute(context.Background(), p, stream.BytesInput([]byte("x")), new(bytes.Buffer)); err == nil || h.transport.executed != 1 {
		t.Fatalf("raw sink admitted: %v", err)
	}
}
