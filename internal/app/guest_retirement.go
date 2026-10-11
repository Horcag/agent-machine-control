package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

// GuestRetirementVerifier supplies fresh exact native process absence evidence.
// Closing SSH alone cannot confirm the Windows process or descendant job exited.
type GuestRetirementVerifier interface {
	ConfirmGuestRetirement(context.Context, domain.MachineRef, domain.ActorID, string, stream.WorkerIdentity) (bool, error)
}

func (s *GuestService) checkRetirement(ctx context.Context, key string, allowCurrent bool, worker guestWorkerRecord) error {
	entries, err := os.ReadDir(s.store.directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrGuestUncertain
	}
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".launch") {
			continue
		}
		priorKey := strings.TrimSuffix(entry.Name(), ".launch")
		var launch guestWorkerRecord
		if err := s.store.read(ctx, priorKey, ".launch", &launch); err != nil || launch.RecordID != priorKey {
			return ErrGuestUncertain
		}
		if launch.Target != worker.Target || launch.Actor != worker.Actor || launch.Caller != worker.Caller {
			continue
		}
		count++
		if count > 128 {
			return ErrGuestUncertain
		}
		if err := s.checkPriorGuestLaunch(ctx, key, priorKey, allowCurrent, launch); err != nil {
			return err
		}

	}
	return nil
}

func (s *GuestService) confirmRetirement(ctx context.Context, key string, worker guestWorkerRecord) error {
	var confirmed guestWorkerRecord
	if err := s.store.read(ctx, key, ".retired", &confirmed); err == nil {
		if !reflect.DeepEqual(confirmed, worker) {
			return ErrGuestUncertain
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrGuestUncertain
	}
	verifier, ok := s.config.(GuestRetirementVerifier)
	if !ok || !validGuestWorker(worker.Identity) {
		return ErrGuestUncertain
	}
	absent, err := verifier.ConfirmGuestRetirement(ctx, worker.Target, worker.Actor, worker.Generation, worker.Identity)
	if err != nil || !absent || ctx.Err() != nil {
		return errors.Join(ErrGuestUncertain, err, ctx.Err())
	}
	if err = s.store.write(ctx, key, ".retired", worker); err != nil {
		if errors.Is(err, os.ErrExist) {
			var existing guestWorkerRecord
			if readErr := s.store.read(ctx, key, ".retired", &existing); readErr == nil && reflect.DeepEqual(existing, worker) {
				return nil
			}
		}
		return errors.Join(ErrGuestUncertain, err)
	}
	return nil
}

func (s *GuestService) retireWorker(parent context.Context, key string, worker guestWorkerRecord) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	return s.retireWorkerContext(ctx, key, worker)
}

func (s *GuestService) retireWorkerContext(ctx context.Context, key string, worker guestWorkerRecord) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrGuestUncertain, err)
	}
	s.mu.Lock()
	s.workers[key] = worker
	s.mu.Unlock()
	persistErr := s.store.write(ctx, worker.RecordID, ".retirement", worker)
	if persistErr != nil {
		var prior guestWorkerRecord
		if readErr := s.store.read(ctx, worker.RecordID, ".retirement", &prior); readErr == nil && reflect.DeepEqual(prior, worker) {
			persistErr = nil
		}
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrGuestUncertain, persistErr, err)
	}
	closeErr := s.transport.Retire(worker.Target, key, worker.Generation)
	if closeErr != nil || persistErr != nil {
		return errors.Join(ErrGuestUncertain, closeErr, persistErr)
	}
	return s.confirmRetirement(ctx, worker.RecordID, worker)
}

func (s *GuestService) recordLaunch(ctx context.Context, key string, record guestWorkerRecord) error {
	if err := s.store.write(ctx, key, ".launch", record); err != nil {
		var prior guestWorkerRecord
		if readErr := s.store.read(ctx, key, ".launch", &prior); readErr != nil || !reflect.DeepEqual(prior, record) {
			return ErrGuestUncertain
		}
	}
	return nil
}

func (s *GuestService) recordReady(ctx context.Context, key string, worker guestWorkerRecord) error {
	if err := s.store.write(ctx, key, ".ready", worker); err != nil {
		var prior guestWorkerRecord
		if readErr := s.store.read(ctx, key, ".ready", &prior); readErr != nil || !reflect.DeepEqual(prior, worker) {
			return ErrGuestUncertain
		}
	}
	return nil
}

func (s *GuestService) checkPriorGuestLaunch(ctx context.Context, key, priorKey string, allowCurrent bool, launch guestWorkerRecord) error {
	var err error
	var pending guestWorkerRecord
	pendingErr := s.store.read(ctx, priorKey, ".retirement", &pending)
	if pendingErr == nil {
		if !sameGuestLaunch(pending, launch) {
			return ErrGuestUncertain
		}
		if err = s.confirmRetirement(ctx, priorKey, pending); err != nil {
			return err
		}
		return nil
	}
	if !errors.Is(pendingErr, os.ErrNotExist) {
		return ErrGuestUncertain
	}
	s.mu.Lock()
	active := s.workers[key]
	s.mu.Unlock()
	if allowCurrent && active.RecordID == priorKey {
		return nil
	}
	var ready guestWorkerRecord
	if err = s.store.read(ctx, priorKey, ".ready", &ready); err != nil {
		return ErrGuestUncertain
	}
	if !sameGuestLaunch(ready, launch) {
		return ErrGuestUncertain
	}
	if err = s.confirmRetirement(ctx, priorKey, ready); err != nil {
		return err
	}
	return nil
}

func sameGuestLaunch(record, launch guestWorkerRecord) bool {
	return record.RecordID == launch.RecordID && record.Target == launch.Target && record.Caller == launch.Caller && record.Actor == launch.Actor && record.Generation == launch.Generation && record.Root == launch.Root
}
