package app

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

// ActiveLabGrant discovers only the caller's currently usable authority for the enrolled VM.
func (s *ConsoleService) ActiveLabGrant(ctx context.Context, actor domain.ActorContext, reference string) (ConsoleLabGrantStatus, error) {
	disabled := ConsoleLabGrantStatus{State: "disabled"}
	if actor.Validate() != nil || actor.IsDelegated() {
		return disabled, ErrInvalidConsoleLabGrant
	}
	if err := s.validateDependencies(); err != nil {
		return disabled, err
	}
	resolution, err := s.target.ResolveTarget(ctx, reference)
	if err != nil {
		return disabled, err
	}
	dir := s.labDirectory()
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return disabled, nil
	} else if err != nil {
		return disabled, err
	}
	if err := target.NewPrivatePathSecurity().ValidateDir(ctx, dir); err != nil {
		return disabled, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return disabled, err
	}
	defer root.Close()
	file, err := root.Open(".")
	if err != nil {
		return disabled, err
	}
	defer file.Close()
	entries, err := file.ReadDir(129)
	if err != nil && !errors.Is(err, io.EOF) {
		return disabled, err
	}
	if len(entries) > 128 {
		return disabled, ErrInvalidConsoleLabGrant
	}
	selected, err := s.selectActiveLabGrant(ctx, actor, domain.MachineRef(resolution.Locator.String()), entries)
	if err != nil {
		return disabled, err
	}
	if selected.GrantID == "" {
		return disabled, nil
	}
	return ConsoleLabGrantStatus{Grant: selected, State: "active"}, nil
}

func (s *ConsoleService) selectActiveLabGrant(ctx context.Context, actor domain.ActorContext, canonical domain.MachineRef, entries []os.DirEntry) (ConsoleLabGrant, error) {
	var selected ConsoleLabGrant
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return ConsoleLabGrant{}, err
		}
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !validFrameID(id) {
			continue
		}
		grant, err := s.loadLabGrant(ctx, id)
		if err != nil || grant.Beneficiary != actor.EffectiveActor || grant.Target != canonical || s.requireActiveLabGrant(ctx, grant) != nil {
			continue
		}
		if selected.GrantID == "" || grant.IssuedAt.After(selected.IssuedAt) || (grant.IssuedAt.Equal(selected.IssuedAt) && grant.GrantID > selected.GrantID) {
			selected = grant
		}
	}
	return selected, nil
}
