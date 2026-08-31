package mcpadapter

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/buildinfo"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const SchemaVersion = "1"

// Adapter contains dependencies to handle MCP requests.
type Adapter struct {
	stateDir         string
	client           *client.Client
	discoveryService *app.DiscoveryService
	recoveryService  *app.RecoveryService
	targetService    *app.TargetService
}

func NewAdapter(stateDir string) *Adapter {
	return &Adapter{stateDir: stateDir}
}

func (a *Adapter) getDiscoveryService() *app.DiscoveryService {
	if a.discoveryService != nil {
		return a.discoveryService
	}
	return app.NewDiscoveryService(hyperv.New())
}

func (a *Adapter) getRecoveryService() *app.RecoveryService {
	if a.recoveryService != nil {
		return a.recoveryService
	}
	return app.NewRecoveryService(hyperv.New(), nil, nil, nil, nil)
}

func (a *Adapter) getClient() (*client.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	return client.Discover(a.stateDir, client.TokenTypeAgentMCP)
}

func convertToMachineDTO(m domain.MachineObservation) MachineDTO {
	caps := m.Capabilities.Slice()
	if caps == nil {
		caps = []string{}
	}

	adapters := []NetworkAdapterDTO{}
	if len(m.NetworkAdapters) > 0 {
		adapters = make([]NetworkAdapterDTO, len(m.NetworkAdapters))
		for i, na := range m.NetworkAdapters {
			ips := []string{}
			if len(na.IPAddresses) > 0 {
				ips = make([]string, len(na.IPAddresses))
				copy(ips, na.IPAddresses)
				sort.Strings(ips)
			}
			adapters[i] = NetworkAdapterDTO{
				Name:        na.Name,
				SwitchName:  na.SwitchName,
				MACAddress:  na.MACAddress,
				IPAddresses: ips,
				Status:      na.Status,
			}
		}
		sort.Slice(adapters, func(i, j int) bool {
			if adapters[i].Name == adapters[j].Name {
				return adapters[i].MACAddress < adapters[j].MACAddress
			}
			return adapters[i].Name < adapters[j].Name
		})
	}

	return MachineDTO{
		ID:                  m.ID,
		Name:                m.Name,
		State:               string(m.State),
		RawState:            m.RawState,
		RawStatus:           m.RawStatus,
		Generation:          m.Generation,
		Version:             m.Version,
		UptimeMs:            m.UptimeMs,
		CPUUsagePercent:     m.CPUUsagePercent,
		MemoryAssignedBytes: m.MemoryAssignedBytes,
		NetworkAdapters:     adapters,
		Capabilities:        caps,
		ObservedAt:          m.ObservedAt.UTC().Format(time.RFC3339),
		ObservationType:     string(m.ObservationType),
	}
}

func convertToCheckpointDTO(c domain.CheckpointObservation) CheckpointDTO {
	return CheckpointDTO{
		ID:              c.ID,
		Name:            c.Name,
		VMID:            c.VMID,
		ParentID:        c.ParentID,
		CheckpointType:  c.CheckpointType,
		CreatedAt:       c.CreatedAt.UTC().Format(time.RFC3339),
		ObservedAt:      c.ObservedAt.UTC().Format(time.RFC3339),
		ObservationType: string(c.ObservationType),
	}
}

type InputError struct {
	Reason string
}

func (e *InputError) Error() string {
	return "invalid input: " + e.Reason
}

func NewInputError(reason string) error {
	return &InputError{Reason: reason}
}

func mcpToolError(err error) *mcp.CallToolResult {
	if err == nil {
		return &mcp.CallToolResult{
			IsError: true,
			Content: []mcp.Content{
				&mcp.TextContent{Text: "unknown error"},
			},
		}
	}
	var inputErr *InputError
	var cleanMsg string
	if errors.As(err, &inputErr) {
		cleanMsg = inputErr.Error()
	} else {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "connection refused") || strings.Contains(msg, "dial tcp"):
			cleanMsg = "service connection failed: daemon is unreachable"
		case strings.Contains(msg, "unauthorized") || strings.Contains(msg, "token"):
			cleanMsg = "authentication failed"
		case strings.Contains(msg, "not found") || strings.Contains(msg, "404"):
			cleanMsg = "requested resource not found"
		case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
			cleanMsg = "operation timeout exceeded"
		case strings.Contains(msg, "domain:"):
			cleanMsg = msg
		default:
			cleanMsg = "an internal daemon error occurred"
		}
	}
	if len(cleanMsg) > 200 {
		cleanMsg = cleanMsg[:197] + "..."
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{Text: cleanMsg},
		},
	}
}

func parseTimeout(timeoutStr string, required bool) (time.Duration, error) {
	if timeoutStr == "" {
		if required {
			return 0, NewInputError("timeout is required")
		}
		return 5 * time.Minute, nil
	}
	d, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return 0, NewInputError("invalid timeout format")
	}
	if d <= 0 {
		return 0, NewInputError("timeout must be a positive duration")
	}
	if d > 1*time.Hour {
		return 0, NewInputError("timeout exceeds the maximum allowed duration (1h)")
	}
	return d, nil
}

func validateMutationParams(targetID, reason, idempotencyKey string) error {
	if err := domain.ValidateMachineGUID(targetID); err != nil {
		return NewInputError("invalid target GUID")
	}
	if err := domain.ValidateReason(reason); err != nil {
		return NewInputError("invalid reason")
	}
	if err := domain.ValidateIdempotencyKey(idempotencyKey); err != nil {
		return NewInputError("invalid idempotency key")
	}
	return nil
}

func (a *Adapter) validateMutationTarget(targetID, reason, idempotencyKey string) error {
	if a.stateDir == "" && a.targetService == nil {
		return validateMutationParams(targetID, reason, idempotencyKey)
	}
	if targetID != "" && strings.TrimSpace(targetID) != targetID {
		return NewInputError("invalid target reference")
	}
	if err := domain.ValidateReason(reason); err != nil {
		return NewInputError("invalid reason")
	}
	if err := domain.ValidateIdempotencyKey(idempotencyKey); err != nil {
		return NewInputError("invalid idempotency key")
	}
	return nil
}

// BuildServer constructs and configures the MCP server with tools.
func (a *Adapter) BuildServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "amc-mcp",
		Version: buildinfo.Version(),
	}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{
			Tools: &mcp.ToolCapabilities{ListChanged: false},
		},
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "doctor",
		Description: "Return host diagnostics and provider capabilities",
	}, a.Doctor)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "machine_list",
		Description: "List all discovered virtual machines",
	}, a.MachineList)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "machine_inspect",
		Description: "Inspect detailed configuration/state of a VM",
	}, a.MachineInspect)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "checkpoint_list",
		Description: "List virtual machine checkpoints",
	}, a.CheckpointList)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "machine_start",
		Description: "Start a virtual machine",
	}, a.MachineStart)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "machine_stop",
		Description: "Stop a virtual machine (shutdown/save/turn-off)",
	}, a.MachineStop)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "checkpoint_create",
		Description: "Create a new checkpoint for a VM",
	}, a.CheckpointCreate)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "checkpoint_restore",
		Description: "Restore a VM checkpoint",
	}, a.CheckpointRestore)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "operation_list",
		Description: "List recent operations",
	}, a.OperationList)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "operation_show",
		Description: "Show details of a specific operation",
	}, a.OperationShow)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "operation_wait",
		Description: "Wait for an operation to complete",
	}, a.OperationWait)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "receipt_show",
		Description: "Show execution receipt details",
	}, a.ReceiptShow)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_open",
		Description: "Open a persistent SSH pseudo-terminal session with a guest VM",
	}, a.SessionOpen)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_read",
		Description: "Read output chunks from a persistent terminal session",
	}, a.SessionRead)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_write",
		Description: "Write input data to a persistent terminal session",
	}, a.SessionWrite)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_control",
		Description: "Send a control key (ctrl-c, ctrl-d, enter, etc.) to a session",
	}, a.SessionControl)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_wait",
		Description: "Wait for output settle or regex match on a session",
	}, a.SessionWait)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_list",
		Description: "List active persistent terminal sessions",
	}, a.SessionList)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_show",
		Description: "Show details of a persistent terminal session",
	}, a.SessionShow)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "session_close",
		Description: "Close a persistent terminal session",
	}, a.SessionClose)

	return server
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid address format: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("address host %q is not a valid IP literal", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("address host %q is not a loopback address", host)
	}
	return nil
}

func runStdio(ctx context.Context, server *mcp.Server, sigChan <-chan os.Signal, cancel context.CancelFunc, stderr io.Writer) int {
	session, err := server.Connect(ctx, &mcp.StdioTransport{}, nil)
	if err != nil {
		fmt.Fprintf(stderr, "amc-mcp: failed to connect stdio transport: %v\n", err)
		return 2
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := session.Wait(); err != nil && !errors.Is(err, mcp.ErrConnectionClosed) {
			fmt.Fprintf(stderr, "amc-mcp session error: %v\n", err)
		}
	}()

	select {
	case sig := <-sigChan:
		fmt.Fprintf(stderr, "Received signal %v, closing session...\n", sig)
		_ = session.Close()
		cancel()
	case <-done:
	}

	return 0
}

func runHTTP(ctx context.Context, server *mcp.Server, stateDir, listenAddr string, sigChan <-chan os.Signal, stderr io.Writer) int {
	if err := validateLoopbackAddress(listenAddr); err != nil {
		fmt.Fprintf(stderr, "amc-mcp: %v\n", err)
		return 2
	}

	sd, err := statedir.Resolve(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "amc-mcp: failed to resolve state directory: %v\n", err)
		return 2
	}
	expectedAgentToken, err := auth.ReadTokenFile(sd.AuthDir(), auth.TokenTypeAgentMCP)
	if err != nil {
		fmt.Fprintf(stderr, "amc-mcp: failed to read agent-mcp token: %v\n", err)
		return 2
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fmt.Fprintf(stderr, "amc-mcp: HTTP server bind failed: %v\n", err)
		return 2
	}
	defer listener.Close()
	return runHTTPListener(ctx, server, listener, expectedAgentToken, sigChan, nil, stderr)
}

func runHTTPListener(
	ctx context.Context,
	server *mcp.Server,
	listener net.Listener,
	expectedAgentToken string,
	sigChan <-chan os.Signal,
	ready chan<- struct{},
	stderr io.Writer,
) int {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return server
	}, nil)

	authMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if origin := r.Header.Get("Origin"); origin != "" {
				http.Error(w, auth.ErrOriginForbidden.Error(), http.StatusForbidden)
				return
			}

			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			token := strings.TrimPrefix(authHeader, "Bearer ")
			if subtle.ConstantTimeCompare([]byte(token), []byte(expectedAgentToken)) != 1 {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}

	httpServer := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           authMiddleware(mcpHandler),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErrChan := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrChan <- err
		}
		close(serveErrChan)
	}()
	if ready != nil {
		close(ready)
	}

	fmt.Fprintf(stderr, "amc-mcp streamable HTTP server listening on %s\n", listener.Addr().String())

	select {
	case sig := <-sigChan:
		fmt.Fprintf(stderr, "Received signal %v, shutting down...\n", sig)
	case err := <-serveErrChan:
		if err != nil {
			fmt.Fprintf(stderr, "amc-mcp HTTP server failed: %v\n", err)
			return 2
		}
	case <-ctx.Done():
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	return 0
}

// Run executes the MCP adapter server according to the configuration.
func Run(stateDir string, listenAddr string, _, stderr io.Writer) int {
	a := NewAdapter(stateDir)
	server := a.BuildServer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	if listenAddr == "" {
		return runStdio(ctx, server, sigChan, cancel, stderr)
	}
	return runHTTP(ctx, server, stateDir, listenAddr, sigChan, stderr)
}
