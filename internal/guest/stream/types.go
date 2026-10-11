// Package stream transports whole guest operations over an enrolled binary SSH
// worker. Admission and ownership-reference authorization remain with the app.
package stream

import (
	"context"
	"errors"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

const MaxControl = 256 * 1024
const BufferSize = 64 * 1024
const MaxManifest = 128
const MaxScriptSize = 16 * 1024 * 1024
const MaxCommandOutput = 16 * 1024 * 1024
const MaxWorkers = 16
const MaxTransferSize int64 = 1 << 40

var ErrProtocol = errors.New("guest stream: invalid or incomplete protocol")
var ErrIntegrity = errors.New("guest stream: size or digest mismatch")
var ErrRequest = errors.New("guest stream: invalid request")
var ErrClosed = errors.New("guest stream: client closed")

// DuplexOpener accepts server-owned targets only, never client endpoints.
type DuplexOpener interface {
	OpenDuplex(context.Context, domain.MachineRef, string) (guestssh.Duplex, error)
}

type Request struct {
	ID           string         `json:"id"`
	Operation    string         `json:"operation"`
	DeadlineUTC  time.Time      `json:"deadlineUtc"`
	Path         string         `json:"path,omitempty"`
	Size         int64          `json:"size"`
	SHA256       string         `json:"sha256,omitempty"`
	MaxOutput    int64          `json:"maxOutput"`
	Owned        []FileIdentity `json:"owned,omitempty"`
	ExpectedRoot *RootIdentity  `json:"expectedRoot,omitempty"`
}

type RootIdentity struct {
	Path         string `json:"path"`
	Volume       uint64 `json:"volume"`
	FileID       string `json:"fileId"`
	CreationTime int64  `json:"creationTime"`
	Owner        string `json:"owner"`
	SDDLHash     string `json:"sddlHash"`
}
type FileIdentity struct {
	Path             string `json:"path"`
	Volume           uint64 `json:"volume"`
	FileID           string `json:"fileId"`
	CreationTime     int64  `json:"creationTime"`
	Owner            string `json:"owner"`
	SDDLHash         string `json:"sddlHash"`
	Size             int64  `json:"size"`
	SHA256           string `json:"sha256"`
	RootPath         string `json:"rootPath"`
	RootVolume       uint64 `json:"rootVolume"`
	RootFileID       string `json:"rootFileId"`
	RootCreationTime int64  `json:"rootCreationTime"`
	RootOwner        string `json:"rootOwner"`
	RootSDDLHash     string `json:"rootSDDLHash"`
}
type WorkerIdentity struct {
	PID        int    `json:"pid"`
	BirthTicks int64  `json:"birthTicks"`
	Image      string `json:"image"`
	SID        string `json:"sid"`
}
type Removal struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Absent bool   `json:"absent"`
}
type Result struct {
	ID       string         `json:"id"`
	Status   string         `json:"status"`
	ExitCode int            `json:"exitCode"`
	Size     int64          `json:"size"`
	SHA256   string         `json:"sha256"`
	Worker   WorkerIdentity `json:"worker"`
	Root     *RootIdentity  `json:"root,omitempty"`
	Files    []FileIdentity `json:"files,omitempty"`
	Removed  []Removal      `json:"removed,omitempty"`
	Failure  *WorkerFailure `json:"failure,omitempty"`
}
