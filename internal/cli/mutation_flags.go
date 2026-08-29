package cli

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// CommonFlags holds the parsed and validated flags for a mutation command.
type CommonFlags struct {
	Reason         string
	IdempotencyKey string
	Timeout        time.Duration
	Approval       *domain.Approval
	JSON           bool
}

// Prompter prompts the operator for interactive confirmation.
type Prompter interface {
	PromptConfirmation(prompt string) bool
}

// DefaultPrompter implements interactive confirmation on terminal stdin.
type DefaultPrompter struct {
	Stdin  io.Reader
	Stdout io.Writer
}

// PromptConfirmation asks the user on Stdin for confirmation.
func (p *DefaultPrompter) PromptConfirmation(prompt string) bool {
	if p.Stdin == nil {
		p.Stdin = os.Stdin
	}
	if p.Stdout == nil {
		p.Stdout = os.Stderr
	}

	if file, ok := p.Stdin.(*os.File); ok {
		fi, err := file.Stat()
		if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
			// Non-interactive terminal
			return false
		}
	}

	fmt.Fprintf(p.Stdout, "%s [y/N]: ", prompt)
	reader := bufio.NewReader(p.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

var knownValueFlags = map[string]bool{
	"--reason":          true,
	"-reason":           true,
	"--idempotency-key": true,
	"-idempotency-key":  true,
	"--timeout":         true,
	"-timeout":          true,
	"--mode":            true,
	"-mode":             true,
	"--name":            true,
	"-name":             true,
	"--state-dir":       true,
	"-state-dir":        true,
}

func splitPositionalAndFlags(args []string) ([]string, []string) {
	var positionals []string
	var flags []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if knownValueFlags[arg] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
		} else {
			positionals = append(positionals, arg)
		}
	}
	return positionals, flags
}

func parseCommonFlags(fs *flag.FlagSet, args []string, stderr io.Writer, opName string) (*CommonFlags, error) {
	reason := fs.String("reason", "", "reason for mutation (required)")
	idempotencyKey := fs.String("idempotency-key", "", "unique client idempotency key (required)")
	timeoutStr := fs.String("timeout", "30s", "maximum operation duration (e.g. 30s, 1m)")
	jsonOutput := fs.Bool("json", false, "emit machine-readable JSON output")
	_ = fs.String("state-dir", "", "state directory path")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if *reason == "" {
		fmt.Fprintf(stderr, "amc %s: missing required flag --reason\n", opName)
		return nil, errors.New("missing --reason")
	}
	if err := domain.ValidateReason(*reason); err != nil {
		fmt.Fprintf(stderr, "amc %s: invalid --reason: %v\n", opName, err)
		return nil, err
	}

	if *idempotencyKey == "" {
		fmt.Fprintf(stderr, "amc %s: missing required flag --idempotency-key\n", opName)
		return nil, errors.New("missing --idempotency-key")
	}
	if err := domain.ValidateIdempotencyKey(*idempotencyKey); err != nil {
		fmt.Fprintf(stderr, "amc %s: invalid --idempotency-key: %v\n", opName, err)
		return nil, err
	}

	timeout, err := time.ParseDuration(*timeoutStr)
	if err != nil || timeout <= 0 {
		fmt.Fprintf(stderr, "amc %s: invalid --timeout %q\n", opName, *timeoutStr)
		return nil, errors.New("invalid --timeout")
	}

	return &CommonFlags{
		Reason:         *reason,
		IdempotencyKey: *idempotencyKey,
		Timeout:        timeout,
		Approval:       nil,
		JSON:           *jsonOutput,
	}, nil
}

func promptForApproval(
	prompter Prompter,
	nowFn func() time.Time,
	actor domain.ActorContext,
	targetID string,
	opKind domain.OperationKind,
	capability domain.Capability,
	initialClass domain.OperationClass,
	reason string,
	key string,
	timeout time.Duration,
	params map[string]any,
	promptMsg string,
) (*domain.Approval, time.Time, bool) {
	if prompter == nil || !prompter.PromptConfirmation(promptMsg) {
		return nil, time.Time{}, false
	}
	now := time.Now().UTC()
	if nowFn != nil {
		now = nowFn().UTC()
	}
	deadline := now.Add(timeout)
	op := domain.Operation{
		Kind:                opKind,
		Target:              domain.MachineRef(targetID),
		Actor:               actor,
		Reason:              reason,
		Deadline:            deadline,
		IdempotencyKey:      key,
		RequiredCapability:  string(capability),
		RequiredScopes:      []string{"machine:write"},
		Classification:      initialClass,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          params,
	}
	fp, err := op.Fingerprint()
	if err != nil {
		return nil, time.Time{}, false
	}
	issued, err := issueInteractiveApproval(now, actor, targetID, fp, key, timeout+5*time.Minute)
	if err != nil {
		return nil, time.Time{}, false
	}
	return &issued, deadline, true
}

func issueInteractiveApproval(
	now time.Time,
	actor domain.ActorContext,
	targetID string,
	fp domain.Fingerprint,
	idempotencyKey string,
	ttl time.Duration,
) (domain.Approval, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return domain.Approval{}, fmt.Errorf("failed to generate random approval id: %w", err)
	}
	return domain.Approval{
		ID:              domain.ApprovalID(fmt.Sprintf("app-interactive-%s", hex.EncodeToString(b))),
		Actor:           actor.EffectiveActor,
		Target:          domain.MachineRef(targetID),
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  idempotencyKey,
		IssuedAt:        now,
		ExpiresAt:       now.Add(ttl),
	}, nil
}
