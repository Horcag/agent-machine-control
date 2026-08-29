package actor_test

import (
	"os/user"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/actor"
)

func TestDefaultResolver_Identity(t *testing.T) {
	resolver := &actor.DefaultResolver{}
	actCtx, err := resolver.Resolve()
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if err := actCtx.Validate(); err != nil {
		t.Fatalf("resolved ActorContext is invalid: %v", err)
	}

	if actCtx.AuthenticatedCaller != actCtx.EffectiveActor {
		t.Errorf("expected authenticated caller == effective actor for local operator")
	}

	u, _ := user.Current()
	if u != nil && u.Uid != "" {
		expectedID := "user:" + strings.TrimSpace(u.Uid)
		if string(actCtx.AuthenticatedCaller) != expectedID {
			t.Errorf("expected actor ID %q, got %q", expectedID, actCtx.AuthenticatedCaller)
		}
	}
}

func TestDefaultResolver_Permissions(t *testing.T) {
	resolver := &actor.DefaultResolver{}
	actCtx, err := resolver.Resolve()
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if !actCtx.EffectivePermissions.Has("machine:write") {
		t.Errorf("expected machine:write scope")
	}
	if !actCtx.EffectivePermissions.Has("machine:read") {
		t.Errorf("expected machine:read scope")
	}
	if actCtx.EffectivePermissions.Has("machine:admin") {
		t.Errorf("did not expect machine:admin scope")
	}
	if actCtx.EffectivePermissions.Has("evidence:sensitive") {
		t.Errorf("did not expect evidence:sensitive scope")
	}
}

func TestDefaultResolver_Errors(t *testing.T) {
	cases := []struct {
		name    string
		userFn  func() (*user.User, error)
		wantErr bool
	}{
		{
			name:    "user lookup error",
			userFn:  func() (*user.User, error) { return nil, user.UnknownUserError("none") },
			wantErr: true,
		},
		{
			name:    "nil user",
			userFn:  func() (*user.User, error) { return nil, nil },
			wantErr: true,
		},
		{
			name:    "empty uid",
			userFn:  func() (*user.User, error) { return &user.User{Uid: "  "}, nil },
			wantErr: true,
		},
		{
			name:    "invalid derived actor ID exceeding max length",
			userFn:  func() (*user.User, error) { return &user.User{Uid: strings.Repeat("a", 300)}, nil },
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &actor.DefaultResolver{CurrentFn: tc.userFn}
			_, err := res.Resolve()
			if (err != nil) != tc.wantErr {
				t.Errorf("expected error %v, got %v", tc.wantErr, err)
			}
		})
	}
}
