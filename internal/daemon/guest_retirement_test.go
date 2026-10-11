package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

type retirementFakeRunner struct {
	run   func([]byte) ([]byte, error)
	calls int
	t     *testing.T
}

func (r *retirementFakeRunner) RunCommand(_ context.Context, target domain.MachineRef, command string, input []byte, limit int) ([]byte, error) {
	r.calls++
	if target != "local:11111111-1111-4111-8111-111111111111" || !strings.HasPrefix(command, "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand ") || limit != 16*1024 {
		r.t.Fatal("observation escaped original target or fixed bounded command")
	}
	return r.run(input)
}
func TestGuestRetirementExactNativeObservation(t *testing.T) {
	original := stream.WorkerIdentity{PID: 1234, BirthTicks: 638000000000000001, Image: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, SID: "S-1-5-21-1000"}
	for _, status := range []string{"live", "missing", "reused", "exited", "denied", "malformed", "mismatch"} {
		t.Run(status, func(t *testing.T) {
			runner := &retirementFakeRunner{t: t, run: func(input []byte) ([]byte, error) { return retirementFixtureResponse(t, input, original, status) }}

			provider := guestConfigurationProvider{retirementRunner: runner, retirementCurrent: func(context.Context, domain.MachineRef, domain.ActorContext) (app.GuestConfiguration, error) {
				return app.GuestConfiguration{Generation: "original-generation"}, nil
			}}
			absent, err := provider.ConfirmGuestRetirement(t.Context(), "local:11111111-1111-4111-8111-111111111111", "operator:synthetic", "original-generation", original)
			expected := status == "missing" || status == "reused" || status == "exited"
			if absent != expected || (expected && err != nil) || runner.calls != 1 {
				t.Fatalf("absent=%v error=%v calls=%d", absent, err, runner.calls)
			}
		})
	}
}
func TestGuestRetirementRejectsGenerationDrift(t *testing.T) {
	for _, changeAt := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(changeAt), func(t *testing.T) {
			calls := 0
			original := stream.WorkerIdentity{PID: 12, BirthTicks: 100, Image: "synthetic-image", SID: "synthetic-sid"}
			runner := &retirementFakeRunner{t: t, run: func([]byte) ([]byte, error) {
				return json.Marshal(guestRetirementObservation{Original: original, Status: "missing", Observed: stream.WorkerIdentity{PID: original.PID}})
			}}
			provider := guestConfigurationProvider{retirementRunner: runner, retirementCurrent: func(context.Context, domain.MachineRef, domain.ActorContext) (app.GuestConfiguration, error) {
				calls++
				generation := "original"
				if calls >= changeAt {
					generation = "changed"
				}
				return app.GuestConfiguration{Generation: generation}, nil
			}}
			absent, err := provider.ConfirmGuestRetirement(t.Context(), "local:11111111-1111-4111-8111-111111111111", "operator:synthetic", "original", original)
			if absent || !errors.Is(err, app.ErrGuestAuthority) {
				t.Fatalf("generation drift accepted: %v %v", absent, err)
			}
			if changeAt < 3 && runner.calls != 0 {
				t.Fatal("dispatched after authority drift")
			}
		})
	}
}

func retirementFixtureResponse(t *testing.T, input []byte, original stream.WorkerIdentity, status string) ([]byte, error) {
	t.Helper()

	var expected stream.WorkerIdentity
	if json.Unmarshal(input, &expected) != nil || expected != original {
		t.Fatal("original process binding missing")
	}
	if status == "denied" {
		return nil, errors.New("denied")
	}
	if status == "malformed" {
		return []byte("not-json"), nil
	}
	observed := original
	nativeStatus := status
	switch status {
	case "missing":
		observed = stream.WorkerIdentity{PID: original.PID}
	case "reused":
		observed.BirthTicks++
	case "mismatch":
		nativeStatus = "exited"
		observed.SID = "S-1-5-21-2000"
	}
	return json.Marshal(guestRetirementObservation{Original: original, Status: nativeStatus, Observed: observed})

}
