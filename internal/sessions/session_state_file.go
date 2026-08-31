package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

const maxSessionStateBytes = 1 << 20

func readSessionState(sessionsDir string, requestedID domain.SessionID) (*domain.SessionObservation, error) {
	if err := requestedID.Validate(); err != nil {
		return nil, err
	}
	file, err := openSessionStateFile(sessionsDir, string(requestedID)+".json")
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxSessionStateBytes+1))
	if err != nil {
		return nil, fmt.Errorf("sessions: failed to read canonical session %s: %w", requestedID, err)
	}
	if len(data) > maxSessionStateBytes {
		return nil, fmt.Errorf("sessions: canonical session %s exceeds %d bytes", requestedID, maxSessionStateBytes)
	}
	var obs domain.SessionObservation
	if err := json.Unmarshal(data, &obs); err != nil {
		return nil, fmt.Errorf("sessions: malformed canonical session %s: %w", requestedID, err)
	}
	if obs.ID != requestedID {
		return nil, fmt.Errorf("sessions: canonical session identity mismatch: filename %s payload %s", requestedID, obs.ID)
	}
	if err := obs.Validate(); err != nil {
		return nil, fmt.Errorf("sessions: invalid canonical session %s: %w", requestedID, err)
	}
	return &obs, nil
}

func sessionStateExistsSafely(sessionsDir string, id domain.SessionID) error {
	_, err := readSessionState(sessionsDir, id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sessions: existing canonical session %s is invalid: %w", id, err)
	}
	return nil
}
