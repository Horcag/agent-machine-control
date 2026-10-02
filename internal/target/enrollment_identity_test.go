package target

import (
	"context"
	"testing"
)

func TestEnrollmentIdentitySurvivesRestartButNotReenrollment(t *testing.T) {
	ctx := context.Background()
	dir := testDirectory(t)
	s := testStore(t, dir)
	v := testDefault(t, vmA)
	p, err := s.Save(ctx, v)
	requireDurablePublication(t, "save", p, err)
	_, first, err := s.EnrollmentIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, restarted, err := testStore(t, dir).EnrollmentIdentity(ctx)
	if err != nil || first != restarted {
		t.Fatalf("restart identity changed: %v", err)
	}
	p, err = s.Save(ctx, v)
	requireDurablePublication(t, "idempotent save", p, err)
	_, same, err := s.EnrollmentIdentity(ctx)
	if err != nil || same != first {
		t.Fatalf("idempotent identity changed: %v", err)
	}
	p, err = s.Clear(ctx)
	requireDurablePublication(t, "clear", p, err)
	if _, _, err := s.EnrollmentIdentity(ctx); err == nil {
		t.Fatal("absent enrollment accepted")
	}
	p, err = s.Save(ctx, v)
	requireDurablePublication(t, "reenroll", p, err)
	_, second, err := s.EnrollmentIdentity(ctx)
	if err != nil || first == second {
		t.Fatalf("reenrollment reused identity: %v", err)
	}
}
