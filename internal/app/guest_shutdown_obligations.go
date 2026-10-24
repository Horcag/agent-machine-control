package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	"github.com/Horcag/agent-machine-control/internal/target"
)

const maxGuestShutdownEntries = 4096
const maxGuestShutdownObligations = 128

// Durable records are evidence of obligations, never channel ownership. Even a
// restarted service with no owned workers must resolve these before succeeding.
func (s *GuestService) reconcileShutdownObligations(ctx context.Context) error {
	if s.store == nil {
		return ErrGuestUncertain
	}
	records, err := s.shutdownObligationRecords(ctx)
	if err != nil {
		return errors.Join(ErrGuestUncertain, err)
	}
	var result error
	for id, suffixes := range records {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, ErrGuestUncertain, err)
		}
		result = errors.Join(result, s.reconcileShutdownObligation(ctx, id, suffixes))
	}
	return result
}

func (s *GuestService) shutdownObligationRecords(ctx context.Context) (map[string][]string, error) {
	records := make(map[string][]string)
	if _, err := os.Lstat(s.store.directory); errors.Is(err, os.ErrNotExist) {
		return records, ctx.Err()
	} else if err != nil {
		return nil, err
	}
	if err := target.NewPrivatePathSecurity().ValidateDir(ctx, s.store.directory); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.store.directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	count := 0
	for {
		entries, readErr := directory.ReadDir(128)
		count += len(entries)
		if count > maxGuestShutdownEntries {
			return nil, ErrGuestUncertain
		}
		for _, entry := range entries {
			if err := collectGuestObligationEntry(records, entry.Name()); err != nil {
				return nil, err
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if readErr == io.EOF {
			return records, nil
		}
		if readErr != nil {
			return nil, readErr
		}
	}
}

func collectGuestObligationEntry(records map[string][]string, name string) error {
	suffix := filepath.Ext(name)
	switch suffix {
	case ".launch", ".worker", ".ready", ".retirement", ".retired":
	default:
		return nil
	}
	id := strings.TrimSuffix(name, suffix)
	if !validLabDigest(id) {
		return ErrGuestUncertain
	}
	records[id] = append(records[id], suffix)
	if len(records) > maxGuestShutdownObligations {
		return ErrGuestUncertain
	}
	return nil
}

func (s *GuestService) reconcileShutdownObligation(ctx context.Context, id string, suffixes []string) error {
	var launch guestWorkerRecord
	if err := s.store.read(ctx, id, ".launch", &launch); err != nil || !validGuestShutdownLaunch(id, launch) {
		return ErrGuestUncertain
	}
	identity := launch
	found := validGuestWorker(launch.Identity)
	if launch.Identity != (stream.WorkerIdentity{}) && !found {
		return ErrGuestUncertain
	}
	for _, suffix := range suffixes {
		if suffix == ".launch" {
			continue
		}
		var record guestWorkerRecord
		if err := s.store.read(ctx, id, suffix, &record); err != nil || !sameGuestLaunch(record, launch) || !validGuestWorker(record.Identity) {
			return ErrGuestUncertain
		}
		if found && !reflect.DeepEqual(record, identity) {
			return ErrGuestUncertain
		}
		identity = record
		found = true
	}
	if !found {
		return ErrGuestUncertain
	}
	if err := s.recordShutdownRetirementIntent(ctx, id, identity); err != nil {
		return err
	}
	return s.confirmRetirement(ctx, id, identity)
}

func validGuestShutdownLaunch(id string, launch guestWorkerRecord) bool {
	_, err := domain.ParseMachineLocator(string(launch.Target))
	return launch.RecordID == id && err == nil && launch.Actor.Validate() == nil && launch.Caller.Validate() == nil && domain.ValidateBoundedString(launch.Generation, 1, 256, ErrGuestUncertain) == nil
}

func (s *GuestService) recordShutdownRetirementIntent(ctx context.Context, id string, worker guestWorkerRecord) error {
	err := s.store.write(ctx, id, ".retirement", worker)
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrExist) {
		var existing guestWorkerRecord
		if readErr := s.store.read(ctx, id, ".retirement", &existing); readErr == nil && reflect.DeepEqual(existing, worker) {
			return nil
		}
	}
	return errors.Join(ErrGuestUncertain, err)
}
