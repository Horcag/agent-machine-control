package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

const desktopVMID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
const desktopSecret = "synthetic-private-desktop-text"

type desktopProviderFake struct {
	targets  []domain.MachineRef
	actions  []string
	requests []domain.DesktopRequest
	response domain.DesktopResponse
	err      error
}

func (p *desktopProviderFake) Provision(_ context.Context, target domain.MachineRef) (domain.DesktopResponse, error) {
	p.targets = append(p.targets, target)
	p.actions = append(p.actions, "provision")
	return p.response, p.err
}
func (p *desktopProviderFake) Execute(ctx context.Context, target domain.MachineRef, req domain.DesktopRequest) (domain.DesktopResponse, error) {
	p.targets = append(p.targets, target)
	p.actions = append(p.actions, req.Action)
	p.requests = append(p.requests, req)
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Format(time.RFC3339Nano) != req.Deadline {
		return domain.DesktopResponse{}, errors.New("missing request execution deadline")
	}
	return p.response, p.err
}
func (p *desktopProviderFake) Remove(_ context.Context, target domain.MachineRef) error {
	p.targets = append(p.targets, target)
	p.actions = append(p.actions, "remove")
	return p.err
}
func desktopRequest(f consoleFixture, action string) app.DesktopActionRequest {
	return app.DesktopActionRequest{Target: "default", Reason: "test disposable guest desktop", IdempotencyKey: "desktop-action-test", Request: domain.DesktopRequest{RequestID: strings.Repeat("a", 32), Action: action, Deadline: f.now.Add(time.Minute).Format(time.RFC3339Nano)}}
}
func desktopProvider(req app.DesktopActionRequest) *desktopProviderFake {
	return &desktopProviderFake{response: domain.DesktopResponse{RequestID: req.Request.RequestID, Success: true, Text: desktopSecret}}
}
func approveDesktop(t *testing.T, f consoleFixture, req app.DesktopActionRequest) app.DesktopActionRequest {
	t.Helper()
	grant, _, err := f.recovery.IssueOperationApproval(context.Background(), app.OperationApprovalIssueParams{Kind: "desktop.action", Caller: f.actor, Target: req.Target, Reason: req.Reason, IdempotencyKey: req.IdempotencyKey, ValidFor: time.Minute, Parameters: domain.DesktopActionParameters(req.Request)})
	if err != nil {
		t.Fatal(err)
	}
	req.ApprovalID = grant.ApprovalID
	req.Request.Deadline = grant.Deadline.Format(time.RFC3339Nano)
	return req
}
func assertDesktopRedacted(t *testing.T, root string, result app.DesktopActionResult, actionErr error) {
	t.Helper()
	data, err := json.Marshal(result.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), desktopSecret) || (actionErr != nil && strings.Contains(actionErr.Error(), desktopSecret)) {
		t.Fatal("sensitive desktop payload exposed in receipt/error")
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), desktopSecret) {
			return errors.New("sensitive desktop payload persisted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDesktopObservationUsesProviderUUIDAndSensitiveAuthority(t *testing.T) {
	for _, action := range []string{"status", "cursor", "windows", "uia.tree", "clipboard.get"} {
		t.Run(action, func(t *testing.T) {
			f := newConsoleFixture(t)
			req := desktopRequest(f, action)
			p := desktopProvider(req)
			result, err := app.NewDesktopService(p, f.service).Action(context.Background(), f.actor, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.targets) != 1 || p.targets[0] != domain.MachineRef(desktopVMID) || len(p.requests) != 1 || !reflect.DeepEqual(p.requests[0], req.Request) {
				t.Fatalf("provider dispatch = %+v, %+v", p.targets, p.requests)
			}
			if result.Response.Text != desktopSecret || result.Receipt != nil || result.CachedReceipt {
				t.Fatalf("observation result = %+v", result)
			}
		})
	}
}

func TestDesktopAdmissionRejectsBeforeDispatch(t *testing.T) {
	for _, variant := range []string{"invalid actor", "missing read", "missing evidence", "missing write", "invalid envelope", "expired", "unbounded", "foreign target", "derived approval", "mixed authority", "missing approval"} {
		t.Run(variant, func(t *testing.T) {
			f := newConsoleFixture(t)
			actor := f.actor
			req := desktopRequest(f, "status")
			var want error
			switch variant {
			case "invalid actor":
				actor = domain.ActorContext{}
			case "missing read", "missing evidence", "missing write":
				scopes := domain.NewScopeSet(domain.ScopeMachineRead)
				if variant == "missing read" {
					scopes = domain.NewScopeSet(domain.ScopeEvidenceCapture)
				}
				if variant == "missing write" {
					req.Request.Action = "clipboard.set"
				}
				var err error
				actor, err = domain.NewActorContext("agent:test", "agent:test", scopes, scopes)
				if err != nil {
					t.Fatal(err)
				}
			case "invalid envelope":
				req.Request.RequestID = "invalid"
				want = domain.ErrInvalidDesktopRequest
			case "expired":
				req.Request.Deadline = f.now.Format(time.RFC3339Nano)
				want = domain.ErrMissingDeadline
			case "unbounded":
				req.Request.Deadline = f.now.Add(time.Minute + time.Second).Format(time.RFC3339Nano)
				want = domain.ErrMissingDeadline
			case "foreign target":
				req.Target = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
			case "derived approval":
				req.ApprovalID = app.ConsoleLabApprovalPrefix + strings.Repeat("a", 32)
				want = app.ErrInvalidConsoleLabGrant
			case "mixed authority":
				req.ApprovalID = "approval-test"
				req.LabGrantID = "grant-test"
				want = app.ErrInvalidConsoleLabGrant
			case "missing approval":
				req.Request.Action = "clipboard.set"
			}
			p := desktopProvider(req)
			_, err := app.NewDesktopService(p, f.service).Action(context.Background(), actor, req)
			if err == nil || (want != nil && !errors.Is(err, want)) || len(p.targets) != 0 {
				t.Fatalf("admission = %v, dispatches = %v", err, p.targets)
			}
		})
	}
}

func TestDesktopMissingDependenciesFailClosed(t *testing.T) {
	f := newConsoleFixture(t)
	req := desktopRequest(f, "status")
	p := desktopProvider(req)
	for _, service := range []*app.DesktopService{nil, app.NewDesktopService(nil, f.service), app.NewDesktopService(p, nil), app.NewDesktopService(p, app.NewConsoleService(nil, nil, nil, ""))} {
		if _, err := service.Action(context.Background(), f.actor, req); !errors.Is(err, app.ErrMissingBackend) {
			t.Fatalf("missing dependencies = %v", err)
		}
	}
	if len(p.targets) != 0 {
		t.Fatal("missing dependencies dispatched")
	}
}

func TestDesktopProviderFailuresAreGeneric(t *testing.T) {
	for _, action := range []string{"status", "clipboard.set"} {
		for _, variant := range []string{"provider error", "wrong request", "unsuccessful", "canceled", "deadline"} {
			t.Run(action+"/"+variant, func(t *testing.T) {
				f := newConsoleFixture(t)
				req := desktopRequest(f, action)
				p := desktopProvider(req)
				want := "app: invalid guest desktop observation"
				actor := f.actor
				if action == "clipboard.set" {
					configureLab(f)
					req.LabGrantID = issueLab(t, f).GrantID
					actor = labActor(t)
					want = "app: guest desktop action failed"
				}
				switch variant {
				case "provider error":
					p.err = errors.New(desktopSecret)
					want = "app: console provider failed"
				case "wrong request":
					p.response.RequestID = strings.Repeat("b", 32)
				case "unsuccessful":
					p.response.Success = false
					p.response.Error = desktopSecret
				case "canceled":
					p.err = context.Canceled
					want = context.Canceled.Error()
				case "deadline":
					p.err = context.DeadlineExceeded
					want = context.DeadlineExceeded.Error()
				}
				result, err := app.NewDesktopService(p, f.service).Action(context.Background(), actor, req)
				if err == nil || err.Error() != want || len(p.targets) != 1 {
					t.Fatalf("provider failure = %+v, %v, calls %v", result, err, p.targets)
				}
				if !reflect.DeepEqual(result.Response, domain.DesktopResponse{}) {
					t.Fatal("malformed response escaped")
				}
				assertDesktopRedacted(t, f.root, result, err)
			})
		}
	}
}

func TestDesktopNormalApprovedFailedRetryReturnsErrorWithoutRedispatch(t *testing.T) {
	f := newConsoleFixture(t)
	req := desktopRequest(f, "clipboard.set")
	req.Request.Text = desktopSecret
	req = approveDesktop(t, f, req)
	p := desktopProvider(req)
	p.err = errors.New(desktopSecret)
	service := app.NewDesktopService(p, f.service)
	var receiptID domain.ReceiptID
	for attempt := range 2 {
		result, err := service.Action(context.Background(), f.actor, req)
		if err == nil || result.Receipt == nil || result.Receipt.Outcome.Status != domain.OutcomeFailed || result.CachedReceipt {
			t.Fatalf("failed retry %d = %+v, %v", attempt, result, err)
		}
		if attempt == 0 {
			receiptID = result.Receipt.ReceiptID
		} else if result.Receipt.ReceiptID != receiptID || err.Error() != "app: guest desktop action previously failed" {
			t.Fatalf("failed retry changed receipt/error: %+v, %v", result, err)
		}
		assertDesktopRedacted(t, f.root, result, err)
	}
	if len(p.targets) != 1 || p.targets[0] != domain.MachineRef(desktopVMID) {
		t.Fatalf("failed retry dispatch = %v", p.targets)
	}
}

func TestDesktopSuccessRetryCachesOnlyReceipt(t *testing.T) {
	for _, authority := range []string{"normal", "lab"} {
		t.Run(authority, func(t *testing.T) {
			f := newConsoleFixture(t)
			req := desktopRequest(f, "clipboard.set")
			req.Request.Text = desktopSecret
			actor := f.actor
			if authority == "normal" {
				req = approveDesktop(t, f, req)
			} else {
				configureLab(f)
				req.LabGrantID = issueLab(t, f).GrantID
				actor = labActor(t)
			}
			p := desktopProvider(req)
			service := app.NewDesktopService(p, f.service)
			first, err := service.Action(context.Background(), actor, req)
			if err != nil || first.Receipt == nil || first.Receipt.Outcome.Status != domain.OutcomeSuccess || first.CachedReceipt || first.Response.Text != desktopSecret {
				t.Fatalf("first = %+v, %v", first, err)
			}
			retry, err := service.Action(context.Background(), actor, req)
			if err != nil || retry.Receipt == nil || retry.Receipt.ReceiptID != first.Receipt.ReceiptID || !retry.CachedReceipt || !reflect.DeepEqual(retry.Response, domain.DesktopResponse{}) {
				t.Fatalf("retry = %+v, %v", retry, err)
			}
			if len(p.targets) != 1 || p.targets[0] != domain.MachineRef(desktopVMID) {
				t.Fatalf("retry dispatch = %v", p.targets)
			}
			assertDesktopRedacted(t, f.root, first, nil)
			req.Request.Text = "changed payload"
			if _, err := service.Action(context.Background(), actor, req); err == nil || len(p.targets) != 1 {
				t.Fatalf("changed payload accepted: %v", err)
			}
		})
	}
}

func TestDesktopLabStaleEpochAndRevocationDenyDispatch(t *testing.T) {
	for _, variant := range []string{"epoch", "revoke"} {
		t.Run(variant, func(t *testing.T) {
			f := newConsoleFixture(t)
			_, epoch := configureLab(f)
			grant := issueLab(t, f)
			req := desktopRequest(f, "clipboard.set")
			req.Request.Text = desktopSecret
			req.LabGrantID = grant.GrantID
			if variant == "epoch" {
				*epoch = strings.Repeat("b", 64)
			} else {
				_, err := f.service.RevokeLabGrant(context.Background(), f.actor, app.ConsoleLabGrantRevokeRequest{GrantID: grant.GrantID, Reason: "end desktop lab", IdempotencyKey: "desktop-revoke", Deadline: req.Request.Deadline})
				if err != nil {
					t.Fatal(err)
				}
			}
			p := desktopProvider(req)
			result, err := app.NewDesktopService(p, f.service).Action(context.Background(), labActor(t), req)
			if err == nil || len(p.targets) != 0 {
				t.Fatalf("inactive lab dispatched: %+v, %v", result, err)
			}
			assertDesktopRedacted(t, f.root, result, err)
		})
	}
}

func TestDesktopProvisionNormalizesRequestIDAndRemoveSynthesizesResponse(t *testing.T) {
	for _, action := range []string{"provision", "remove"} {
		for _, fail := range []bool{false, true} {
			t.Run(action+"/"+map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
				f := newConsoleFixture(t)
				configureLab(f)
				req := desktopRequest(f, action)
				req.LabGrantID = issueLab(t, f).GrantID
				p := desktopProvider(req)
				p.response.RequestID = "provider-provision-id"
				if fail {
					p.err = errors.New(desktopSecret)
				}
				result, err := app.NewDesktopService(p, f.service).Action(context.Background(), labActor(t), req)
				if len(p.actions) != 1 || p.actions[0] != action || p.targets[0] != domain.MachineRef(desktopVMID) || len(p.requests) != 0 {
					t.Fatalf("wrong dispatch: %+v", p)
				}
				if fail {
					if err == nil || err.Error() != "app: console provider failed" || result.Receipt == nil || result.Receipt.Outcome.Status != domain.OutcomeFailed {
						t.Fatalf("failed dispatch: %+v, %v", result, err)
					}
				} else if err != nil || !result.Response.Success || result.Response.RequestID != req.Request.RequestID || result.Receipt == nil || result.Receipt.Outcome.Status != domain.OutcomeSuccess {
					t.Fatalf("successful dispatch: %+v, %v", result, err)
				}
				assertDesktopRedacted(t, f.root, result, err)
			})
		}
	}
}

func TestDesktopBackendComposesCapabilityWithoutMutatingUnderlyingSet(t *testing.T) {
	for _, hasInput := range []bool{false, true} {
		caps := domain.NewCapabilitySet(domain.CapabilityConsoleScreenshot)
		if hasInput {
			caps[domain.CapabilityConsoleInput] = struct{}{}
		}
		backend := &mockBackend{capabilitiesFn: func(_ context.Context, target string) (domain.CapabilitySet, error) {
			if target != desktopVMID {
				t.Errorf("capability target = %q", target)
			}
			return caps, nil
		}}
		got, err := (app.DesktopBackend{Backend: backend}).Capabilities(context.Background(), desktopVMID)
		if err != nil || got.Has(domain.CapabilityDesktopAction) != hasInput || !got.Has(domain.CapabilityConsoleScreenshot) || caps.Has(domain.CapabilityDesktopAction) {
			t.Fatalf("composed capabilities = %v, underlying = %v, error = %v", got, caps, err)
		}
		delete(got, domain.CapabilityConsoleScreenshot)
		if !caps.Has(domain.CapabilityConsoleScreenshot) {
			t.Fatal("composed set aliases underlying capabilities")
		}
	}
	sentinel := errors.New("synthetic capability failure")
	backend := &mockBackend{capabilitiesFn: func(context.Context, string) (domain.CapabilitySet, error) {
		return domain.NewCapabilitySet(domain.CapabilityConsoleInput), sentinel
	}}
	caps, err := (app.DesktopBackend{Backend: backend}).Capabilities(context.Background(), desktopVMID)
	if !errors.Is(err, sentinel) || caps != nil {
		t.Fatalf("capability failure = %v, %v", caps, err)
	}
}
