package sessions

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func sessionStatePath(sessionsDir string, id domain.SessionID) (string, error) {
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return "", err
	}

	root, err := filepath.Abs(filepath.Clean(sessionsDir))
	if err != nil {
		return "", errors.New("sessions: session state directory is invalid")
	}
	filename := string(id) + ".json"
	candidate, err := filepath.Abs(filepath.Join(root, filename))
	if err != nil {
		return "", errors.New("sessions: session state path is invalid")
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel != filename || filepath.IsAbs(rel) || filepath.Dir(rel) != "." {
		return "", errors.New("sessions: session state path escapes the session directory")
	}
	return candidate, nil
}

func sessionIDFromStateFilename(name string) (domain.SessionID, bool) {
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return "", false
	}
	id := domain.SessionID(strings.TrimSuffix(name, ".json"))
	if domain.ValidateSessionID(string(id)) != nil {
		return "", false
	}
	return id, true
}
