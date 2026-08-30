package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/operations"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type contextKey string

const callerContextKey contextKey = "callerContext"

var (
	// ErrShutdownIncomplete means admitted operations, sessions, or transport cleanup remain live.
	// Endpoint and singleton ownership are intentionally retained so the same process can retry.
	ErrShutdownIncomplete = errors.New("daemon: shutdown drain incomplete")
)

// Server is the HTTP/1.1 daemon server for Agent Machine Control.
type Server struct {
	cfg              Config
	stateDir         *statedir.StateDir
	authStore        *auth.Store
	leaseMgr         *lease.Manager
	auditStore       *audit.Store
	receiptStore     *receipt.Store
	approvalStore    *approval.Store
	recoveryService  *app.RecoveryService
	eventHub         *events.Hub
	opMgr            *operations.Manager
	sessionMgr       *sessions.Manager
	sessionService   *app.SessionService
	singletonLock    *SingletonLock
	httpServer       *http.Server
	listener         net.Listener
	endpoint         string
	startedAt        time.Time
	pid              int
	runtimeID        string
	startTime        string
	shutdownChan     chan struct{}
	shutdownOnce     sync.Once
	admissionClosed  atomic.Bool
	semaphore        chan struct{}
	identityProvider lease.IdentityProvider
	shutdownHTTP     func(context.Context) error
	closeHTTP        func() error

	afterEarlyMutationAdmissionCheck func()

	serveErrMu sync.Mutex
	serveErr   error
}

func (s *Server) now() time.Time {
	if s.cfg.Clock != nil {
		return s.cfg.Clock().UTC()
	}
	return time.Now().UTC()
}

// NewServer initializes state, reconciles previous crashes, and creates the daemon server.
func NewServer(cfg Config) (*Server, error) {
	sd, err := statedir.Resolve(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("daemon: failed to resolve state dir: %w", err)
	}
	if err := sd.EnsureDirs(); err != nil {
		return nil, fmt.Errorf("daemon: failed to initialize state dirs: %w", err)
	}

	ident := cfg.IdentityProvider
	if ident == nil {
		ident = &lease.DefaultIdentityProvider{}
	}
	checker := cfg.LivenessChecker
	if checker == nil {
		checker = &lease.DefaultLivenessChecker{}
	}

	now := time.Now().UTC()
	if cfg.Clock != nil {
		now = cfg.Clock().UTC()
	}

	// 1. Acquire singleton lock FIRST
	lock, err := AcquireSingletonLock(sd.DaemonDir(), ident, checker, now)
	if err != nil {
		return nil, err
	}

	// 2. Initialize auth
	var authOpts []auth.Option
	if cfg.PrincipalResolver != nil {
		authOpts = append(authOpts, auth.WithPrincipalResolver(cfg.PrincipalResolver))
	}
	authStore, err := auth.LoadOrCreate(sd.AuthDir(), authOpts...)
	if err != nil {
		_ = lock.Release()
		return nil, fmt.Errorf("daemon: failed to initialize auth: %w", err)
	}

	leaseMgr := lease.NewManager(sd.LeasesDir(), lease.WithIdentityProvider(ident), lease.WithLivenessChecker(checker))
	auditStore := audit.NewStore(sd.AuditDir(), audit.WithIdentityProvider(ident), audit.WithLivenessChecker(checker))
	receiptStore := receipt.NewStore(sd.ReceiptsDir())
	approvalStore := approval.NewStore(sd.ApprovalsDir())

	eventHub := events.NewHub(sd.OperationsDir())

	backend := cfg.Backend
	if backend == nil {
		backend = hyperv.New()
	}

	recoverySvc := app.NewRecoveryService(backend, leaseMgr, auditStore, receiptStore, approvalStore)
	opMgr := operations.NewManager(sd.OperationsDir(), recoverySvc, receiptStore, auditStore, eventHub)

	keyProvider := cfg.KeyProvider
	if keyProvider == nil {
		keyProvider = guestssh.NewLocalKeyProvider(sd)
	}
	transport := cfg.Transport
	if transport == nil {
		transport = guestssh.NewTransport(keyProvider)
	}
	safetyResolver := app.NewDefaultSafetyResolver(sshSafetyConfigLoader{provider: keyProvider}, backend)
	sanitizerConfig := daemonSessionSanitizerConfig(authStore, cfg.SessionSanitizerConfig)
	sessionMgr := sessions.NewManager(sd.SessionsDir(), transport, cfg.Clock, sessions.WithSanitizerConfig(sanitizerConfig))
	sessionSvc := app.NewSessionService(sessionMgr, safetyResolver, leaseMgr, auditStore, receiptStore, approvalStore, app.WithSessionClock(cfg.Clock))

	// 3. Fail-closed startup crash recovery & stale lease reclamation
	if err := reconcileStartupState(sd, sessionSvc, receiptStore, auditStore, eventHub, leaseMgr, now); err != nil {
		_ = lock.Release()
		return nil, err
	}

	runtimeID, pid, startTime := ident.CurrentIdentity()

	srv := &Server{
		cfg:              cfg,
		stateDir:         sd,
		authStore:        authStore,
		leaseMgr:         leaseMgr,
		auditStore:       auditStore,
		receiptStore:     receiptStore,
		approvalStore:    approvalStore,
		recoveryService:  recoverySvc,
		eventHub:         eventHub,
		opMgr:            opMgr,
		sessionMgr:       sessionMgr,
		sessionService:   sessionSvc,
		singletonLock:    lock,
		startedAt:        now,
		pid:              pid,
		runtimeID:        runtimeID,
		startTime:        startTime,
		shutdownChan:     make(chan struct{}),
		semaphore:        make(chan struct{}, 100),
		identityProvider: ident,
	}

	srv.setupHTTPServer()
	return srv, nil
}

func reconcileStartupState(
	sd *statedir.StateDir,
	sessionSvc *app.SessionService,
	receiptStore *receipt.Store,
	auditStore *audit.Store,
	eventHub *events.Hub,
	leaseMgr *lease.Manager,
	now time.Time,
) error {
	ctx := context.Background()
	if _, err := sessionSvc.ReconcileMutationFinalizations(ctx, now); err != nil {
		return fmt.Errorf("daemon: failed to reconcile session mutations: %w", err)
	}
	if _, err := operations.ReconcileCrashedOperations(ctx, sd.OperationsDir(), receiptStore, auditStore, eventHub, now); err != nil {
		return fmt.Errorf("daemon: failed to reconcile crashed operations: %w", err)
	}
	if _, err := sessions.ReconcileCrashedSessions(ctx, sd.SessionsDir(), now); err != nil {
		return fmt.Errorf("daemon: failed to reconcile crashed sessions: %w", err)
	}
	if _, err := leaseMgr.ReclaimStaleLeases(ctx); err != nil {
		return fmt.Errorf("daemon: failed to reclaim stale leases: %w", err)
	}
	return nil
}

func (s *Server) setupHTTPServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/", s.dispatchV1)

	s.httpServer = &http.Server{
		Handler:           s.authMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
		TLSNextProto:      make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}
	s.shutdownHTTP = s.httpServer.Shutdown
	s.closeHTTP = s.httpServer.Close
}

func (s *Server) dispatchV1(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	path = strings.TrimPrefix(path, "/")

	switch {
	case path == "health" && r.Method == http.MethodGet:
		s.handleHealth(w, r)
	case path == "events" && r.Method == http.MethodGet:
		s.handleGlobalEvents(w, r)
	case path == "operations" || strings.HasPrefix(path, "operations/"):
		s.dispatchOperations(w, r, path)
	case path == "sessions" || strings.HasPrefix(path, "sessions/"):
		s.dispatchSessions(w, r, path)
	case path == "session-approvals":
		if r.Method == http.MethodPost {
			s.handleIssueSessionApproval(w, r)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	default:
		s.dispatchOtherV1(w, r, path)
	}
}

func (s *Server) dispatchOtherV1(w http.ResponseWriter, r *http.Request, path string) {
	switch {
	case path == "audit":
		if r.Method == http.MethodGet {
			s.handleGetAudit(w, r)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	case path == "receipts":
		if r.Method == http.MethodGet {
			s.handleListReceipts(w, r)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	case strings.HasPrefix(path, "receipts/"):
		s.dispatchReceiptSubroute(w, r, strings.TrimPrefix(path, "receipts/"))
	case path == "daemon/stop":
		if r.Method == http.MethodPost {
			s.handleStopDaemon(w, r)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	default:
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func (s *Server) dispatchOperations(w http.ResponseWriter, r *http.Request, path string) {
	if path == "operations" {
		switch r.Method {
		case http.MethodPost:
			s.handleCreateOperation(w, r)
		case http.MethodGet:
			s.handleListOperations(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return
	}
	s.dispatchOperationSubroute(w, r, strings.TrimPrefix(path, "operations/"))
}

func (s *Server) dispatchOperationSubroute(w http.ResponseWriter, r *http.Request, rest string) {
	parts := strings.Split(rest, "/")
	opID := parts[0]

	switch {
	case len(parts) == 1:
		if r.Method == http.MethodGet {
			s.handleGetOperation(w, r, opID)
		} else {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	case len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost:
		s.handleCancelOperation(w, r, opID)
	case len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet:
		s.handleOperationEvents(w, r, opID)
	default:
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func (s *Server) dispatchReceiptSubroute(w http.ResponseWriter, r *http.Request, receiptID string) {
	if r.Method == http.MethodGet {
		s.handleGetReceipt(w, r, receiptID)
	} else {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

// Start opens the network listener and records the daemon endpoint.
func (s *Server) Start() error {
	addr := s.cfg.ListenAddr
	if addr == "" {
		addr = "127.0.0.1:0"
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		if s.singletonLock != nil {
			_ = s.singletonLock.Release()
		}
		return fmt.Errorf("daemon: invalid listen address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		if s.singletonLock != nil {
			_ = s.singletonLock.Release()
		}
		return fmt.Errorf("daemon: listen address host %q must be a loopback IP literal", host)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if s.singletonLock != nil {
			_ = s.singletonLock.Release()
		}
		return fmt.Errorf("daemon: failed to listen on %s: %w", addr, err)
	}

	s.listener = ln
	s.endpoint = fmt.Sprintf("http://%s", ln.Addr().String())

	rec := EndpointRecord{
		SchemaVersion:    SchemaVersion,
		PID:              s.pid,
		RuntimeID:        s.runtimeID,
		ProcessStartTime: s.startTime,
		StartedAt:        s.startedAt,
		Endpoint:         s.endpoint,
	}

	if err := WriteEndpointFile(s.stateDir.DaemonDir(), rec); err != nil {
		_ = ln.Close()
		if s.singletonLock != nil {
			_ = s.singletonLock.Release()
		}
		return fmt.Errorf("daemon: failed to write endpoint file: %w", err)
	}

	go func() {
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			s.serveErrMu.Lock()
			s.serveErr = err
			s.serveErrMu.Unlock()
			s.TriggerShutdown()
		}
	}()

	return nil
}

// Endpoint returns the active server URL.
func (s *Server) Endpoint() string {
	return s.endpoint
}

// PID returns the server process ID.
func (s *Server) PID() int {
	return s.pid
}

// TriggerShutdown initiates an asynchronous server shutdown.
func (s *Server) TriggerShutdown() {
	s.shutdownOnce.Do(func() {
		s.admissionClosed.Store(true)
		close(s.shutdownChan)
	})
}

// Wait blocks until the daemon receives a stop signal or shutdown request.
func (s *Server) Wait() {
	<-s.shutdownChan
}

func getCallerContext(ctx context.Context) (domain.ActorContext, bool) {
	val := ctx.Value(callerContextKey)
	if val == nil {
		return domain.ActorContext{}, false
	}
	act, ok := val.(domain.ActorContext)
	return act, ok
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, statusCode int, category, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(ErrorEnvelope{
		SchemaVersion: SchemaVersion,
		Error: ErrorField{
			Category: category,
			Message:  message,
		},
	})
}
