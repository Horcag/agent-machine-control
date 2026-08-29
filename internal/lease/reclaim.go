package lease

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// ReclaimStaleLeases scans for leases owned by dead processes in the current runtime
// and safely reclaims them by persisting a generation tombstone and deleting the lease file.
func (m *Manager) ReclaimStaleLeases(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("lease: failed to read leases directory: %w", err)
	}

	runtimeID, _, _ := m.identityProvider.CurrentIdentity()
	now := m.now()
	var reclaimed []string

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lease.json") {
			continue
		}

		machineID := strings.TrimSuffix(entry.Name(), ".lease.json")
		if machineID == "" {
			continue
		}

		didReclaim, err := m.tryReclaimSingle(ctx, machineID, runtimeID, now)
		if err != nil {
			return reclaimed, err
		}
		if didReclaim {
			reclaimed = append(reclaimed, machineID)
		}
	}

	return reclaimed, nil
}

func (m *Manager) tryReclaimSingle(ctx context.Context, machineID, runtimeID string, now time.Time) (bool, error) {
	var reclaimed bool
	err := m.withLock(ctx, machineID, func() error {
		path := m.leasePath(machineID)
		existing, err := m.readLeaseFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}

		// Cross-runtime leases cannot be safely verified; fail closed.
		if existing.RuntimeID == "" || existing.RuntimeID != runtimeID {
			return nil
		}

		alive, checkErr := m.livenessChecker.IsAlive(existing.PID, existing.ProcessStartTime)
		if checkErr != nil || alive {
			// Process is alive or liveness cannot be determined; do not reclaim.
			return nil
		}

		// Process is confirmed dead in the current runtime: reclaim safely.
		if err := m.writeGeneration(machineID, existing.FencingGeneration, now); err != nil {
			return fmt.Errorf("lease: failed to persist generation tombstone during reclaim: %w", err)
		}

		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}

		if dirF, err := os.Open(m.dir); err == nil {
			_ = dirF.Sync()
			_ = dirF.Close()
		}

		reclaimed = true
		return nil
	})

	return reclaimed, err
}
