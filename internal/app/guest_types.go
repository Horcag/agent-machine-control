package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

var ErrGuestAuthority = errors.New("app: guest authority changed or unavailable")
var ErrGuestNotPrepared = errors.New("app: guest worker requires explicit preparation")
var ErrGuestOwnership = errors.New("app: guest artifact ownership or identity mismatch")
var ErrGuestUncertain = errors.New("app: guest operation outcome is uncertain; do not replay")

// GuestConfiguration is refreshed at admission and at the effect boundary.
// Current must revalidate enrollment, credentials and key pins, current actor
// authority, and the server-provisioned workspace; Generation binds all of them.
// WorkspaceIdentity must come from authorized provisioning, never path adoption.
type GuestConfiguration struct {
	Generation        string
	Caller            domain.ActorContext
	WorkspaceIdentity stream.RootIdentity
}

type GuestConfigProvider interface {
	Current(context.Context, domain.MachineRef, domain.ActorContext) (GuestConfiguration, error)
}

type GuestTransport interface {
	Prepare(context.Context, domain.MachineRef, string, string) (stream.WorkerIdentity, error)
	Execute(context.Context, domain.MachineRef, string, string, stream.Request, io.Reader, io.Writer) (stream.Result, error)
	Retire(domain.MachineRef, string, string) error
}

// GuestOperationParams carries bounded public metadata, never a guest path.
// Put and exec stream exactly Size bytes matching SHA256. Get and remove use
// immutable opaque references returned by put. Deadlines are exact and required.
type GuestOperationParams struct {
	Target         string              `json:"target"`
	Caller         domain.ActorContext `json:"-"`
	Operation      string              `json:"operation"`
	ArtifactIDs    []string            `json:"artifact_ids,omitempty"`
	Size           int64               `json:"size"`
	SHA256         string              `json:"sha256,omitempty"`
	MaxOutput      int64               `json:"max_output"`
	Reason         string              `json:"reason"`
	IdempotencyKey string              `json:"idempotency_key"`
	Deadline       time.Time           `json:"deadline"`
	ApprovalID     string              `json:"approval_id,omitempty"`
}

type GuestArtifact struct {
	ID     string `json:"id"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type GuestRemoval struct {
	ArtifactID string `json:"artifact_id"`
	Attempted  bool   `json:"attempted"`
	Accepted   bool   `json:"accepted"`
	Absent     bool   `json:"absent"`
	Status     string `json:"status"`
}

type GuestOperationResult struct {
	Receipt       *domain.Receipt       `json:"receipt,omitempty"`
	CachedReceipt bool                  `json:"cached_receipt"`
	Worker        stream.WorkerIdentity `json:"worker"`
	Artifacts     []GuestArtifact       `json:"artifacts,omitempty"`
	Removed       []GuestRemoval        `json:"removed,omitempty"`
	Size          int64                 `json:"size"`
	SHA256        string                `json:"sha256"`
	ExitCode      int                   `json:"exit_code"`
}

type guestOwnedArtifact struct {
	Artifact   GuestArtifact
	Target     domain.MachineRef
	Caller     domain.ActorID
	Actor      domain.ActorID
	Generation string
	Identity   stream.FileIdentity
}

type guestWorkerRecord struct {
	RecordID   string
	Target     domain.MachineRef
	Caller     domain.ActorID
	Actor      domain.ActorID
	Generation string
	Identity   stream.WorkerIdentity
	Root       stream.RootIdentity
}

type GuestService struct {
	session    *SessionService
	resolver   SessionTargetResolver
	config     GuestConfigProvider
	transport  GuestTransport
	store      *GuestStore
	mu         sync.Mutex
	workers    map[string]guestWorkerRecord
	spoolBytes int64
}

func NewGuestService(session *SessionService, resolver SessionTargetResolver, config GuestConfigProvider, transport GuestTransport, store *GuestStore) *GuestService {
	return &GuestService{session: session, resolver: resolver, config: config, transport: transport, store: store, workers: map[string]guestWorkerRecord{}}
}
