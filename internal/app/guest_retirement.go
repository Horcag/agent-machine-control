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

func (s *GuestService) checkRetirement(ctx context.Context, key string, worker guestWorkerRecord) error {
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
		count++
		if count > 128 {
			return ErrGuestUncertain
		}
		if err := s.checkPriorGuestLaunch(ctx, key, strings.TrimSuffix(entry.Name(), ".launch"), worker); err != nil {
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
	if err != nil || !absent {
		return errors.Join(ErrGuestUncertain, err)
	}
	if err = s.store.write(ctx, key, ".retired", worker); err != nil {
		return errors.Join(ErrGuestUncertain, err)
	}
	return nil
}

func (s *GuestService) retireWorker(parent context.Context, key string, worker guestWorkerRecord) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	s.mu.Lock()
	s.workers[key] = worker
	s.mu.Unlock()
	persistErr := s.store.write(ctx, key, ".retirement", worker)
	if persistErr != nil {
		var prior guestWorkerRecord
		if readErr := s.store.read(ctx, key, ".retirement", &prior); readErr == nil && reflect.DeepEqual(prior, worker) {
			persistErr = nil
		}
	}
	closeErr := s.transport.Retire(worker.Target, key, worker.Generation)
	if closeErr != nil || persistErr != nil {
		return errors.Join(ErrGuestUncertain, closeErr, persistErr)
	}
	return s.confirmRetirement(ctx, key, worker)
}

func (s *GuestService) recordLaunch(ctx context.Context, key string, record guestWorkerRecord) error {
	if err := s.store.write(ctx, key, ".launch", record); err != nil {
		var prior guestWorkerRecord
		if readErr := s.store.read(ctx, key, ".launch", &prior); readErr != nil || prior.Target != record.Target || prior.Caller != record.Caller || prior.Actor != record.Actor || prior.Generation != record.Generation {
			return ErrGuestUncertain
		}
	}
	return nil
}

func (s *GuestService) recordReady(ctx context.Context, key string, worker guestWorkerRecord) error {
	if err := s.store.write(ctx, key, ".ready", worker); err != nil {
		var prior guestWorkerRecord
		if readErr := s.store.read(ctx, key, ".ready", &prior); readErr != nil || prior.Identity != worker.Identity || prior.Target != worker.Target || prior.Caller != worker.Caller || prior.Actor != worker.Actor || prior.Generation != worker.Generation {
			return ErrGuestUncertain
		}
	}
	return nil
}

func (s *GuestService) checkPriorGuestLaunch(ctx context.Context, key, priorKey string, worker guestWorkerRecord) error {
	var err error
	var launch guestWorkerRecord
	if err = s.store.read(ctx, priorKey, ".launch", &launch); err != nil {
		return ErrGuestUncertain
	}
	if launch.Target != worker.Target || launch.Actor != worker.Actor || launch.Caller != worker.Caller {
		return nil
	}
	var pending guestWorkerRecord
	pendingErr := s.store.read(ctx, priorKey, ".retirement", &pending)
	if pendingErr == nil {
		if err = s.confirmRetirement(ctx, priorKey, pending); err != nil {
			return err
		}
		if priorKey == key {
			return ErrGuestNotPrepared
		}
		return nil
	}
	if !errors.Is(pendingErr, os.ErrNotExist) {
		return ErrGuestUncertain
	}
	s.mu.Lock()
	active := s.workers[priorKey]
	s.mu.Unlock()
	if priorKey == key && active.RecordID != "" {
		return nil
	}
	var ready guestWorkerRecord
	if err = s.store.read(ctx, priorKey, ".ready", &ready); err != nil {
		return ErrGuestUncertain
	}
	if err = s.confirmRetirement(ctx, priorKey, ready); err != nil {
		return err
	}
	if priorKey == key {
		return ErrGuestNotPrepared
	}
	return nil
}
