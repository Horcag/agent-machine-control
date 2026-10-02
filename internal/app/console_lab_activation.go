package app

import (
	"context"
	"errors"
	"os"
)

type consoleLabActivation struct {
	GrantID string `json:"grant_id"`
}

// Authority becomes usable only after issuance receipt and terminal audit are durable.
func (s *ConsoleService) activateLabGrant(ctx context.Context, id string) error {
	if err := s.labActivation(ctx, id); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.writeLabDocument(ctx, id, ".active", consoleLabActivation{GrantID: id})
}

func (s *ConsoleService) labActivation(ctx context.Context, id string) error {
	var value consoleLabActivation
	if err := s.readLabDocument(ctx, id, ".active", &value); err != nil {
		return err
	}
	if value.GrantID != id {
		return ErrInvalidConsoleLabGrant
	}
	return nil
}
