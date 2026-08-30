package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func reconcileSessionFile(ctx context.Context, sessionsDir string, id domain.SessionID, now time.Time) (*domain.SessionID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	obs, err := readSessionState(sessionsDir, id)
	if err != nil {
		return nil, err
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
	filePath, err := sessionStatePath(sessionsDir, id)
	if err != nil {
		return nil, err
	}
	if err := replaceSessionFileContext(ctx, filePath, updatedData); err != nil {
		return nil, fmt.Errorf("sessions: failed to write session file %s: %w", filePath, err)
	}

	return &obs.ID, nil
}

// ReconcileCrashedSessions inspects the sessions directory on startup and marks dangling sessions as crashed.
func ReconcileCrashedSessions(ctx context.Context, sessionsDir string, now time.Time) ([]domain.SessionID, error) {
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
		if err := ctx.Err(); err != nil {
			return reconciled, err
		}
		candidateID, valid := sessionIDFromStateFilename(entry.Name())
		if !valid {
			continue
		}
		id, err := reconcileSessionFile(ctx, sessionsDir, candidateID, now)
		if err != nil {
			return reconciled, err
		}
		if id != nil {
			reconciled = append(reconciled, *id)
		}
	}

	return reconciled, nil
}
