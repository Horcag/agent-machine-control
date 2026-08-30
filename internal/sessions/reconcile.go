package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func reconcileSessionFile(filePath string, now time.Time) (*domain.SessionID, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("sessions: failed to read session file %s: %w", filePath, err)
	}

	var obs domain.SessionObservation
	if err := json.Unmarshal(data, &obs); err != nil {
		return nil, fmt.Errorf("sessions: malformed session file %s: %w", filePath, err)
	}

	if obs.State.IsTerminal() {
		return nil, nil
	}

	obs.State = domain.SessionStateCrashed
	obs.ClosedAt = &now
	obs.ErrorMessage = "daemon_crash_recovered"

	updatedData, err := json.MarshalIndent(obs, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("sessions: failed to marshal session %s: %w", obs.ID, err)
	}
	if err := replaceSessionFile(filePath, updatedData); err != nil {
		return nil, fmt.Errorf("sessions: failed to write session file %s: %w", filePath, err)
	}

	return &obs.ID, nil
}

// ReconcileCrashedSessions inspects the sessions directory on startup and marks dangling sessions as crashed.
func ReconcileCrashedSessions(_ context.Context, sessionsDir string, now time.Time) ([]domain.SessionID, error) {
	if sessionsDir == "" {
		return nil, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("sessions: failed to read sessions dir: %w", err)
	}

	var reconciled []domain.SessionID

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		candidateID, valid := sessionIDFromStateFilename(entry.Name())
		if !valid {
			continue
		}
		filePath, err := sessionStatePath(sessionsDir, candidateID)
		if err != nil {
			return reconciled, err
		}
		id, err := reconcileSessionFile(filePath, now)
		if err != nil {
			return reconciled, err
		}
		if id != nil {
			reconciled = append(reconciled, *id)
		}
	}

	return reconciled, nil
}
