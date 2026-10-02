package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func (a *App) runDesktop(ctx context.Context, direct bool, stateDir string, args []string, stdout, stderr io.Writer) int {
	if direct || len(args) == 0 {
		fmt.Fprintln(stderr, "amc desktop: requires daemon; use observe, action, enable, status, or disable (console --direct remains available)")
		return ExitUsage
	}
	options, ok := parseDesktopFlags(args, stderr)
	if !ok {
		return ExitUsage
	}
	pos := options.pos
	target, reason, key, grant := &options.target, &options.reason, &options.key, &options.grant
	forMCP, ack, validFor, deadline := &options.forMCP, &options.ack, &options.validFor, &options.deadline
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		return mapClientError(err, stderr, "desktop")
	}
	var out any
	switch args[0] {
	case "enable":
		if len(pos) != 0 {
			return ExitUsage
		}
		beneficiary := "self"
		if *forMCP {
			beneficiary = "agent:mcp-local"
		}
		out, err = cl.IssueConsoleLabGrant(ctx, app.ConsoleLabGrantIssueRequest{Target: *target, Reason: *reason, IdempotencyKey: *key, Beneficiary: beneficiary, ValidForMillis: validFor.Milliseconds(), AcknowledgeExternalEffects: *ack})
	case "status":
		if len(pos) == 1 {
			*grant = pos[0]
		}
		out, err = cl.ConsoleLabGrantStatus(ctx, *grant)
	case "disable":
		if len(pos) == 1 {
			*grant = pos[0]
		}
		if *deadline == "" {
			*deadline = a.now().Add(30 * time.Second).Format(time.RFC3339Nano)
		}
		out, err = cl.RevokeConsoleLabGrant(ctx, app.ConsoleLabGrantRevokeRequest{GrantID: *grant, Reason: *reason, IdempotencyKey: *key, Deadline: *deadline})
	case "observe":
		out, err = a.observeDesktop(ctx, cl, *target, pos)

	case "action":
		out, err = executeDesktopRequestFile(ctx, cl, options)

	default:
		return ExitUsage
	}
	if err != nil {
		return mapClientError(err, stderr, "desktop")
	}
	if json.NewEncoder(stdout).Encode(out) != nil {
		return ExitMalformedProvider
	}
	return ExitSuccess
}

func readDesktopRequestFile(path string, out *domain.DesktopRequest) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 128*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return domain.ErrInvalidDesktopRequest
	}
	return out.Validate()
}

type desktopFlags struct {
	pos                                               []string
	target, reason, key, grant, requestFile, deadline string
	forMCP, ack                                       bool
	validFor                                          time.Duration
}

func parseDesktopFlags(args []string, stderr io.Writer) (desktopFlags, bool) {
	pos, flags := consoleArgs(args[1:])
	fs := flag.NewFlagSet("desktop "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("target", "", "enrolled target reference")
	reason := fs.String("reason", "", "mutation reason")
	key := fs.String("idempotency-key", "", "exact retry key")
	grant := fs.String("lab-grant-id", "", "active operator-issued grant")
	requestFile := fs.String("request-file", "", "JSON DesktopRequest file")
	forMCP := fs.Bool("for-mcp", false, "grant agent:mcp-local instead of self")
	ack := fs.Bool("acknowledge-external-effects", false, "acknowledge guest external effects are not rolled back")
	validFor := fs.Duration("valid-for", time.Hour, "grant lifetime (up to 8h)")
	deadline := fs.String("deadline", "", "exact operation deadline")
	_ = fs.Bool("json", true, "emit JSON")
	if fs.Parse(flags) != nil || len(pos) > 1 {
		return desktopFlags{}, false
	}
	return desktopFlags{pos: pos, target: *target, reason: *reason, key: *key, grant: *grant, requestFile: *requestFile, forMCP: *forMCP, ack: *ack, validFor: *validFor, deadline: *deadline}, true
}

func (a *App) observeDesktop(ctx context.Context, cl *client.Client, target string, pos []string) (app.DesktopActionResult, error) {
	action := "windows"
	if len(pos) == 1 {
		action = pos[0]
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return app.DesktopActionResult{}, err
	}
	return cl.DesktopAction(ctx, app.DesktopActionRequest{Target: target, Request: domain.DesktopRequest{RequestID: hex.EncodeToString(id[:]), Deadline: a.now().Add(30 * time.Second).Format(time.RFC3339Nano), Action: action}})
}

func executeDesktopRequestFile(ctx context.Context, cl *client.Client, options desktopFlags) (app.DesktopActionResult, error) {
	if len(options.pos) != 0 || options.requestFile == "" {
		return app.DesktopActionResult{}, client.ErrInvalidArgument
	}
	var request domain.DesktopRequest
	if err := readDesktopRequestFile(options.requestFile, &request); err != nil {
		return app.DesktopActionResult{}, client.ErrInvalidArgument
	}
	return cl.DesktopAction(ctx, app.DesktopActionRequest{Target: options.target, Request: request, Reason: options.reason, IdempotencyKey: options.key, LabGrantID: options.grant})
}
