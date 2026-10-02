package target

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/lease"
)

func TestEnrollmentFenceBlocksPublicationAcrossProcesses(t *testing.T) {
	dir := testDirectory(t)
	store := testStore(t, dir)
	publication, err := store.Save(context.Background(), testDefault(t, vmA))
	requireDurablePublication(t, "save", publication, err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestEnrollmentFenceProcessHelper$")
	command.Env = append(os.Environ(), "AMC_TEST_ENROLLMENT_FENCE="+dir)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); cancel(); _ = command.Wait() }()
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "locked\n" {
		t.Fatalf("child fence readiness = %q, %v", ready, err)
	}
	clearCtx, clearCancel := context.WithTimeout(ctx, 3*time.Second)
	publication, err = store.Clear(clearCtx)
	clearCancel()
	if publication.Committed || errors.Is(err, ErrInsecureState) || (!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, lease.ErrLeaseConflict)) {
		t.Fatalf("publication crossed process fence: %+v, %v", publication, err)
	}
	if _, err := stdin.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	publication, err = store.Clear(ctx)
	requireDurablePublication(t, "clear after child dispatch", publication, err)
}

func TestEnrollmentFenceProcessHelper(t *testing.T) {
	dir := os.Getenv("AMC_TEST_ENROLLMENT_FENCE")
	if dir == "" {
		t.Skip("subprocess helper")
	}
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = store.WithEnrollmentFence(context.Background(), func() error {
		if _, err := io.WriteString(os.Stdout, "locked\n"); err != nil {
			return err
		}
		_, err := io.ReadFull(os.Stdin, make([]byte, 1))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
