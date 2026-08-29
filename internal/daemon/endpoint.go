package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

// EndpointRecord represents the active daemon network location and process metadata.
type EndpointRecord struct {
	SchemaVersion    string    `json:"schema_version"`
	PID              int       `json:"pid"`
	RuntimeID        string    `json:"runtime_id"`
	ProcessStartTime string    `json:"process_start_time,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	Endpoint         string    `json:"endpoint"`
}

// ValidateEndpointURL validates that an endpoint URL is a valid HTTP loopback URL.
func ValidateEndpointURL(endpointURL string) error {
	if endpointURL == "" {
		return errors.New("daemon: endpoint URL cannot be empty")
	}
	u, err := url.Parse(endpointURL)
	if err != nil {
		return fmt.Errorf("daemon: invalid endpoint URL %q: %w", endpointURL, err)
	}
	if u.Scheme != "http" {
		return fmt.Errorf("daemon: endpoint URL scheme must be http, got %q", u.Scheme)
	}
	host, _, err := net.SplitHostPort(u.Host)
	if err != nil {
		host = u.Host
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("daemon: endpoint host %q must be a loopback IP literal", host)
	}
	return nil
}

// WriteEndpointFile persists the endpoint record atomically to disk.
func WriteEndpointFile(daemonDir string, rec EndpointRecord) error {
	if daemonDir == "" {
		return errors.New("daemon: daemonDir cannot be empty")
	}
	if err := ValidateEndpointURL(rec.Endpoint); err != nil {
		return err
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}

	finalPath := filepath.Join(daemonDir, "endpoint.json")
	tmpPath := fmt.Sprintf("%s.tmp.%d", finalPath, time.Now().UnixNano())

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	return statedir.SyncDir(daemonDir)
}

// ReadEndpointFile reads and validates the endpoint record from disk.
func ReadEndpointFile(daemonDir string) (*EndpointRecord, error) {
	finalPath := filepath.Join(daemonDir, "endpoint.json")
	fi, err := os.Lstat(finalPath)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("daemon: endpoint file is a symlink")
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("daemon: endpoint file is not a regular file")
	}
	if fi.Size() > 4096 {
		return nil, fmt.Errorf("daemon: endpoint file too large")
	}

	data, err := os.ReadFile(finalPath)
	if err != nil {
		return nil, err
	}

	var rec EndpointRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return nil, fmt.Errorf("daemon: corrupt endpoint file: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("daemon: trailing data in endpoint file")
	}

	if rec.SchemaVersion != SchemaVersion || rec.PID <= 0 {
		return nil, fmt.Errorf("daemon: invalid endpoint record content")
	}
	if err := ValidateEndpointURL(rec.Endpoint); err != nil {
		return nil, fmt.Errorf("daemon: invalid endpoint record URL: %w", err)
	}

	return &rec, nil
}

// RemoveEndpointFileIfOwned deletes the endpoint file if owned by the specified process identity.
func RemoveEndpointFileIfOwned(daemonDir string, pid int, runtimeID, startTime string) error {
	rec, err := ReadEndpointFile(daemonDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("daemon: failed to read endpoint file during cleanup: %w", err)
	}

	if rec.PID == pid && rec.RuntimeID == runtimeID && rec.ProcessStartTime == startTime {
		finalPath := filepath.Join(daemonDir, "endpoint.json")
		if err := os.Remove(finalPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("daemon: failed to remove endpoint file: %w", err)
		}
		return statedir.SyncDir(daemonDir)
	}
	return nil
}
