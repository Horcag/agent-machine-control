package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func guardedRequest() Request {
	req := request("clipboard.set")
	sequence := uint32(0)
	inventory := []uint32{}
	req.ExpectedSequence, req.ExpectedInventory = &sequence, &inventory
	req.Text = "requested"
	return req
}

func TestGuardedClipboardLostResultsRequireReconciliationWithoutRetry(t *testing.T) {
	req := guardedRequest()
	for _, test := range []struct {
		name, output string
		failure      error
		ordinary     bool
	}{
		{name: "runner failure", failure: errors.New("private failure")},
		{name: "deadline after dispatch", failure: context.DeadlineExceeded},
		{name: "cancellation after dispatch", failure: context.Canceled},
		{name: "missing result", output: " "},
		{name: "malformed result", output: "{"},
		{name: "mismatched request", output: `{"request_id":"wrong","success":true}`},
		{name: "generic worker failure", output: `{"success":false,"error":"desktop_failed"}`},
		{name: "generic transport failure", output: `{"success":false,"error":"desktop_unavailable"}`},
		{name: "native conflict without marker", output: `{"success":false,"error":"clipboard_conflict"}`},
		{name: "fixed pre-effect rejection", output: `{"success":false,"error":"clipboard_pre_effect_rejected"}`, ordinary: true},
		{name: "contradictory pre-effect result", output: `{"request_id":"` + req.RequestID + `","success":true,"error":"clipboard_pre_effect_rejected"}`},
		{name: "old helper unsupported action", output: `{"success":false,"error":"unsupported_action"}`, ordinary: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			output := test.output
			if strings.Contains(output, `"success":false`) {
				output = strings.Replace(output, "{", `{"request_id":"`+req.RequestID+`",`, 1)
			}
			runner := &runnerFake{output: []byte(output), err: test.failure}
			_, err := New(runner).Execute(t.Context(), "synthetic", req)
			want := ErrClipboardUncertain
			if test.ordinary {
				want = ErrUnavailable
			}
			if !errors.Is(err, want) || runner.calls != 1 {
				t.Fatalf("error=%v calls=%d", err, runner.calls)
			}
			if strings.Contains(err.Error(), "private failure") {
				t.Fatal("private transport failure exposed")
			}
		})
	}
}

func TestClipboardSuccessRequiresCompleteBoundedMetadataAndReadback(t *testing.T) {
	req := guardedRequest()
	base := `{"request_id":"` + req.RequestID + `","success":true,"session_id":1,"elevated":true,"text":"requested","clipboard":{"sequence":0,"formats":[13],"inventory_complete":true,"empty":false}}`
	for _, test := range []struct {
		name   string
		mutate func(string) string
	}{
		{"missing metadata", func(s string) string {
			return strings.Replace(s, `,"clipboard":{"sequence":0,"formats":[13],"inventory_complete":true,"empty":false}`, "", 1)
		}},
		{"null metadata", func(s string) string {
			return strings.Replace(s, `{"sequence":0,"formats":[13],"inventory_complete":true,"empty":false}`, "null", 1)
		}},
		{"missing sequence", func(s string) string { return strings.Replace(s, `"sequence":0,`, "", 1) }},
		{"missing formats", func(s string) string { return strings.Replace(s, `"formats":[13],`, "", 1) }},
		{"null formats", func(s string) string { return strings.Replace(s, `[13]`, `null`, 1) }},
		{"missing complete", func(s string) string { return strings.Replace(s, `"inventory_complete":true,`, "", 1) }},
		{"incomplete inventory", func(s string) string {
			return strings.Replace(s, `"inventory_complete":true`, `"inventory_complete":false`, 1)
		}},
		{"missing empty", func(s string) string { return strings.Replace(s, `,"empty":false`, "", 1) }},
		{"text format missing", func(s string) string { return strings.Replace(s, `[13]`, `[14]`, 1) }},
		{"zero format", func(s string) string { return strings.Replace(s, `[13]`, `[0]`, 1) }},
		{"duplicate formats", func(s string) string { return strings.Replace(s, `[13]`, `[13,13]`, 1) }},
		{"unsorted formats", func(s string) string { return strings.Replace(s, `[13]`, `[14,13]`, 1) }},
		{"too many formats", func(s string) string {
			formats := make([]uint32, 257)
			for i := range formats {
				formats[i] = uint32(i + 1)
			}
			data, _ := json.Marshal(formats)
			return strings.Replace(s, `[13]`, string(data), 1)
		}},
		{"empty inconsistency", func(s string) string { return strings.Replace(s, `"empty":false`, `"empty":true`, 1) }},
		{"text mismatch", func(s string) string { return strings.Replace(s, `"requested"`, `"other"`, 1) }},
		{"oversized text", func(s string) string { return strings.Replace(s, `"requested"`, `"`+strings.Repeat("x", 4097)+`"`, 1) }},
		{"oversized UTF16", func(s string) string {
			return strings.Replace(s, `"requested"`, `"`+strings.Repeat("😀", 2049)+`"`, 1)
		}},
		{"null text", func(s string) string { return strings.Replace(s, `"requested"`, `null`, 1) }},
		{"lone high surrogate", func(s string) string { return strings.Replace(s, `"requested"`, `"\ud800"`, 1) }},
		{"lone low surrogate", func(s string) string { return strings.Replace(s, `"requested"`, `"\udc00"`, 1) }},
		{"nul text", func(s string) string { return strings.Replace(s, `"requested"`, `"\u0000"`, 1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &runnerFake{output: []byte(test.mutate(base))}
			if _, err := New(runner).Execute(t.Context(), "synthetic", req); !errors.Is(err, ErrClipboardUncertain) || runner.calls != 1 {
				t.Fatalf("error=%v calls=%d", err, runner.calls)
			}
			if _, err := New(runner).Execute(t.Context(), "synthetic", request("clipboard.get")); !errors.Is(err, ErrUnavailable) && test.name != "missing metadata" && test.name != "text mismatch" {
				t.Fatalf("get error=%v", err)
			}
		})
	}
}

func TestClipboardProtocolAcceptsZeroSequenceAndLegacyTextGet(t *testing.T) {
	for _, text := range []string{"", strings.Repeat("x", 4096), strings.Repeat("😀", 2048), "replacement �", `literal \ud800`} {
		req := guardedRequest()
		req.Text = text
		runner := &runnerFake{}
		if _, err := New(runner).Execute(t.Context(), "synthetic", req); err != nil {
			t.Fatal(err)
		}
	}
	runner := &runnerFake{output: []byte(`{"request_id":"` + request("clipboard.get").RequestID + `","success":true,"session_id":1,"elevated":true,"text":"legacy"}`)}
	response, err := New(runner).Execute(t.Context(), "synthetic", request("clipboard.get"))
	if err != nil || response.Text != "legacy" || response.Clipboard != nil {
		t.Fatalf("legacy response=%+v error=%v", response, err)
	}
}

func TestGuardedClipboardPreAdmissionFailureNeverDispatches(t *testing.T) {
	runner := &runnerFake{}
	req := guardedRequest()
	req.ExpectedInventory = nil
	if _, err := New(runner).Execute(t.Context(), "synthetic", req); !errors.Is(err, ErrInvalidRequest) || runner.calls != 0 {
		t.Fatalf("error=%v calls=%d", err, runner.calls)
	}
	if _, err := New(nil).Execute(t.Context(), "synthetic", guardedRequest()); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

// The runner observes cancellation only once it has received the dispatched request.
type cancelledClipboardRunner struct {
	calls  int
	cancel context.CancelFunc
}

func (r *cancelledClipboardRunner) RunCommand(ctx context.Context, _ domain.MachineRef, _ string, _ []byte, _ int) ([]byte, error) {
	r.calls++
	r.cancel()
	return nil, ctx.Err()
}
func TestGuardedClipboardActualCancellationAfterDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runner := &cancelledClipboardRunner{cancel: cancel}
	if _, err := New(runner).Execute(ctx, "synthetic", guardedRequest()); !errors.Is(err, ErrClipboardUncertain) || runner.calls != 1 {
		t.Fatalf("error=%v calls=%d", err, runner.calls)
	}
}

func TestGuardedClipboardCancellationBeforeDispatchIsOrdinaryFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runner := &runnerFake{}
	if _, err := New(runner).Execute(ctx, "synthetic", guardedRequest()); !errors.Is(err, context.Canceled) || runner.calls != 0 {
		t.Fatalf("error=%v calls=%d", err, runner.calls)
	}
}

func TestGuardedClipboardHelperFailureMarkers(t *testing.T) {
	path, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell unavailable for data-only failure marker fixture")
	}
	worker, _ := scripts.ReadFile("worker.ps1")
	transport, _ := scripts.ReadFile("transport.ps1")
	data, _ := json.Marshal(map[string]string{"worker": string(worker), "transport": string(transport)})
	output, err := runParserCheck(path, clipboardFailureMarkerFixture, data)
	if err != nil {
		t.Fatalf("failure marker fixture: %v %s", err, output)
	}
	for _, name := range []string{"worker_before_action", "worker_conflict", "worker_encoding", "worker_nested_conflict", "worker_partial", "worker_generic", "worker_substring", "transport_before_publish", "transport_after_publish"} {
		if !strings.Contains(string(output), "passed:"+name) {
			t.Fatalf("missing case %s: %s", name, output)
		}
	}
}

// Only production catch bodies run. There is no worker, queue, native API or file.
const clipboardFailureMarkerFixture = `
$ErrorActionPreference='Stop'
$data=[Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))|ConvertFrom-Json
$worker=[Management.Automation.Language.Parser]::ParseInput($data.worker,[ref]$null,[ref]$null)
$workerTry=@($worker.EndBlock.Statements|Where-Object {$_ -is [Management.Automation.Language.TryStatementAst]})[0]
$body=$workerTry.CatchClauses[0].Body.Extent.Text
$handler=[ScriptBlock]::Create('try { throw $failureToThrow } catch '+$body)
$request=[pscustomobject]@{action='clipboard.set.guarded';request_id=('a'*32)}
foreach($case in @(
 @{Name='worker_before_action';Started=$false;Message='private failure';Expected='clipboard_pre_effect_rejected'},
 @{Name='worker_conflict';Started=$true;Message='clipboard_conflict';Expected='clipboard_pre_effect_rejected'},
 @{Name='worker_encoding';Started=$true;Message='clipboard_encoding_failed';Expected='clipboard_pre_effect_rejected'},
 @{Name='worker_nested_conflict';Started=$true;Message='clipboard_conflict';Nested=$true;Expected='clipboard_pre_effect_rejected'},
 @{Name='worker_partial';Started=$true;Message='clipboard_possibly_cleared';Expected='clipboard_possibly_cleared'},
 @{Name='worker_generic';Started=$true;Message='private failure';Expected='clipboard_possibly_cleared'},
 @{Name='worker_substring';Started=$true;Message='private clipboard_conflict suffix';Expected='clipboard_possibly_cleared'}
)) {
 $actionStarted=$case.Started
 $response=@{success=$false;error='desktop_failed'}
 $failureToThrow=[InvalidOperationException]::new($case.Message)
 if($case.Nested){$failureToThrow=[Exception]::new('private wrapper',$failureToThrow)}
 . $handler
 if($response.success -or $response.error-cne $case.Expected){throw ('wrong_failure_marker:'+$case.Name)}
 Write-Output ('passed:'+$case.Name)
}
$transport=[Management.Automation.Language.Parser]::ParseInput($data.transport,[ref]$null,[ref]$null)
$transportTry=@($transport.EndBlock.Statements|Where-Object {$_ -is [Management.Automation.Language.TryStatementAst]})[0]
$body=$transportTry.CatchClauses[0].Body.Extent.Text
$handler=[ScriptBlock]::Create('try { throw ''private transport failure'' } catch '+$body)
function Write-Response($response){$script:actual=$response}
foreach($dispatchPossible in @($false,$true)){
 . $handler
 $name='transport_before_publish';$expected='clipboard_pre_effect_rejected'
 if($dispatchPossible){$name='transport_after_publish';$expected='desktop_unavailable'}
 if($script:actual.success -or $script:actual.error-cne $expected -or $script:actual.request_id-cne $request.request_id){throw ('wrong_failure_marker:'+$name)}
 Write-Output ('passed:'+$name)
}
`

func TestGuardedClipboardEmptyClearRequiresExplicitReadbackAndEmptyInventory(t *testing.T) {
	req := guardedRequest()
	req.Text = ""
	for _, test := range []struct {
		name, text, formats string
		empty               bool
	}{
		{"missing readback", "", "[]", true},
		{"null readback", `,"text":null`, "[]", true},
		{"nonempty inventory", `,"text":""`, "[13]", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := `{"request_id":"` + req.RequestID + `","success":true,"session_id":1,"elevated":true` + test.text + `,"clipboard":{"sequence":0,"formats":` + test.formats + `,"inventory_complete":true,"empty":`
			if test.empty {
				data += "true"
			} else {
				data += "false"
			}
			data += "}}"
			runner := &runnerFake{output: []byte(data)}
			if _, err := New(runner).Execute(t.Context(), "synthetic", req); !errors.Is(err, ErrClipboardUncertain) || runner.calls != 1 {
				t.Fatalf("error=%v calls=%d", err, runner.calls)
			}
		})
	}
}
