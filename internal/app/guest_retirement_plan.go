package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// This invocation-local proof replaces the effect-capable retirement scan. It
// never caches authority: the final authority gate still precedes transport I/O.
type guestRetirementPlan struct {
	operation domain.Operation
	config    GuestConfiguration
	key       string
	consumed  bool
}

func (p *guestRetirementPlan) consume(op domain.Operation, config GuestConfiguration, key string) bool {
	if p.consumed || p.key != key || !reflect.DeepEqual(p.operation, op) || !reflect.DeepEqual(p.config, config) {
		return false
	}
	p.consumed = true
	return true
}

func (s *GuestService) readonlyRetirementPlan(ctx context.Context, p GuestOperationParams, op domain.Operation, config GuestConfiguration, key string) *guestRetirementPlan {
	if p.Operation == "prepare" || ctx.Err() != nil {
		return nil
	}
	s.mu.Lock()
	current := s.workers[key]
	s.mu.Unlock()
	if current.RecordID == "" || !validGuestWorkerBinding(current, op, config) {
		return nil
	}
	if !s.readonlyLaunchInventory(ctx, key, current) {
		return nil
	}
	config.Caller = config.Caller.Clone()
	return &guestRetirementPlan{operation: op.Clone(), config: config, key: key}
}

func (s *GuestService) readonlyLaunchInventory(ctx context.Context, key string, current guestWorkerRecord) bool {
	entries, err := os.ReadDir(s.store.directory)
	if err != nil {
		return false
	}
	found, count := false, 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".launch") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".launch")
		var launch guestWorkerRecord
		if err := s.store.read(ctx, id, ".launch", &launch); err != nil || launch.RecordID != id {
			return false
		}
		if launch.Target != current.Target || launch.Caller != current.Caller || launch.Actor != current.Actor {
			continue
		}
		count++
		if count > 128 || !s.readonlyGuestLaunch(ctx, key, launch, current) {
			return false
		}
		found = found || id == current.RecordID
	}
	return found && ctx.Err() == nil
}

func (s *GuestService) readonlyGuestLaunch(ctx context.Context, key string, launch, current guestWorkerRecord) bool {
	var pending guestWorkerRecord
	err := s.store.read(ctx, launch.RecordID, ".retirement", &pending)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if launch.RecordID == current.RecordID {
		return errors.Is(err, os.ErrNotExist) && s.validateCurrentGuestLaunch(ctx, key, launch.RecordID, current, launch) == nil
	}
	var original guestWorkerRecord
	if s.store.read(ctx, launch.RecordID, ".worker", &original) != nil || !sameGuestLaunch(original, launch) || !validGuestWorker(original.Identity) || !validGuestRoot(original.Root) || original.Identity.SID != original.Root.Owner {
		return false
	}
	for _, suffix := range []string{".ready", ".retired"} {
		var record guestWorkerRecord
		if s.store.read(ctx, launch.RecordID, suffix, &record) != nil || !reflect.DeepEqual(record, original) {
			return false
		}
	}
	return (err != nil || reflect.DeepEqual(pending, original)) && ctx.Err() == nil
}
