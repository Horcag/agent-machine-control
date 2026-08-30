package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

const (
	// OperatorTokenFileName is the filename for the operator bearer token.
	OperatorTokenFileName = "operator.token"

	// AgentMCPTokenFileName is the filename for the agent-mcp bearer token.
	AgentMCPTokenFileName = "agent-mcp.token" //nolint:gosec // filename constant, not hardcoded credentials

	// AgentMCPPrincipal is the fixed logical actor identity for agent-mcp.
	AgentMCPPrincipal = "agent:mcp-local"
)

var (
	// ErrUnauthorized indicates missing or invalid bearer credentials.
	ErrUnauthorized = errors.New("auth: unauthorized or invalid bearer token")

	// ErrOriginForbidden indicates an HTTP request contained a non-empty browser Origin header.
	ErrOriginForbidden = errors.New("auth: browser Origin headers are forbidden")
)

// TokenType specifies the intended consumer of a bearer token.
type TokenType string

const (
	TokenTypeOperator TokenType = "operator"
	TokenTypeAgentMCP TokenType = "agent-mcp"
)

// PrincipalResolver resolves the operator identity.
type PrincipalResolver func() (string, error)

type options struct {
	resolver PrincipalResolver
}

// Option configures Store initialization.
type Option func(*options)

// WithPrincipalResolver configures a custom identity resolver.
func WithPrincipalResolver(resolver PrincipalResolver) Option {
	return func(o *options) {
		o.resolver = resolver
	}
}

// Store manages bearer token files and server-side actor/scope mapping.
type Store struct {
	authDir           string
	operatorToken     string
	agentToken        string
	operatorPrincipal string
	operatorActor     domain.ActorContext
	agentActor        domain.ActorContext
}

// LoadOrCreate initializes the auth directory, generating 256-bit cryptographic tokens if needed.
func LoadOrCreate(authDir string, opts ...Option) (*Store, error) {
	if authDir == "" {
		return nil, errors.New("auth: authDir cannot be empty")
	}

	var opt options
	for _, fn := range opts {
		fn(&opt)
	}

	resolver := opt.resolver
	if resolver == nil {
		resolver = defaultPrincipalResolver
	}

	operatorPath := filepath.Join(authDir, OperatorTokenFileName)
	agentPath := filepath.Join(authDir, AgentMCPTokenFileName)

	opToken, err := ensureSingleToken(authDir, operatorPath)
	if err != nil {
		return nil, fmt.Errorf("auth: failed to initialize operator token: %w", err)
	}

	agToken, err := ensureSingleToken(authDir, agentPath)
	if err != nil {
		return nil, fmt.Errorf("auth: failed to initialize agent-mcp token: %w", err)
	}

	principal, err := resolver()
	if err != nil {
		return nil, fmt.Errorf("auth: failed to derive operator principal: %w", err)
	}
	if err := domain.ActorID(principal).Validate(); err != nil {
		return nil, fmt.Errorf("auth: invalid derived operator principal %q: %w", principal, err)
	}

	opScopes := []string{
		domain.ScopeMachineRead,
		domain.ScopeMachineWrite,
		domain.ScopeAuditRead,
		domain.ScopeOperationCancel,
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
		domain.ScopeSessionOpen,
		domain.ScopeSessionClose,
		domain.ScopeSessionAdmin,
		domain.ScopeEvidenceCapture,
	}
	opScopeSet := domain.NewScopeSet(opScopes...)
	opActor, err := domain.NewActorContext(domain.ActorID(principal), domain.ActorID(principal), opScopeSet, opScopeSet)
	if err != nil {
		return nil, fmt.Errorf("auth: failed to validate operator actor context: %w", err)
	}

	agScopes := []string{
		domain.ScopeMachineRead,
		domain.ScopeMachineWrite,
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
		domain.ScopeSessionOpen,
		domain.ScopeSessionClose,
		domain.ScopeEvidenceCapture,
	}
	agScopeSet := domain.NewScopeSet(agScopes...)
	agActor, err := domain.NewActorContext(domain.ActorID(AgentMCPPrincipal), domain.ActorID(AgentMCPPrincipal), agScopeSet, agScopeSet)
	if err != nil {
		return nil, fmt.Errorf("auth: failed to validate agent actor context: %w", err)
	}

	return &Store{
		authDir:           authDir,
		operatorToken:     opToken,
		agentToken:        agToken,
		operatorPrincipal: principal,
		operatorActor:     opActor,
		agentActor:        agActor,
	}, nil
}

func validateTokenFile(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("auth: token file %s is a symlink", filepath.Base(path))
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("auth: token file %s is not a regular file", filepath.Base(path))
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0600 {
		return "", fmt.Errorf("auth: token file %s has insecure permissions %04o; must be 0600", filepath.Base(path), fi.Mode().Perm())
	}
	if fi.Size() < 64 || fi.Size() > 66 {
		return "", fmt.Errorf("auth: token file %s has invalid size (%d bytes)", filepath.Base(path), fi.Size())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	str := string(data)
	str = strings.TrimSuffix(str, "\r\n")
	str = strings.TrimSuffix(str, "\n")

	if len(str) != 64 {
		return "", fmt.Errorf("auth: token file %s must contain exactly 64 hexadecimal characters", filepath.Base(path))
	}
	for i := 0; i < len(str); i++ {
		c := str[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("auth: token file %s contains non-hexadecimal characters", filepath.Base(path))
		}
	}
	return str, nil
}

func ensureSingleToken(authDir, path string) (string, error) {
	token, err := validateTokenFile(path)
	if err == nil {
		return token, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	raw := make([]byte, 32) // 256-bit
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("crypto/rand failed: %w", err)
	}
	newToken := hex.EncodeToString(raw)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return validateTokenFile(path)
		}
		return "", err
	}
	defer f.Close()

	if _, err := f.Write([]byte(newToken + "\n")); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := statedir.SyncDir(authDir); err != nil {
		return "", err
	}

	return newToken, nil
}

func defaultPrincipalResolver() (string, error) {
	if runtime.GOOS == "windows" {
		u, err := user.Current()
		if err != nil {
			return "", fmt.Errorf("auth: failed to resolve Windows user: %w", err)
		}
		if u == nil || strings.TrimSpace(u.Username) == "" {
			return "", errors.New("auth: Windows username is empty")
		}
		return fmt.Sprintf("operator:%s", sanitizePrincipal(u.Username)), nil
	}

	uid := os.Getuid()
	if uid < 0 {
		return "", errors.New("auth: failed to resolve valid Unix UID")
	}
	return fmt.Sprintf("operator:uid-%d", uid), nil
}

func sanitizePrincipal(s string) string {
	s = strings.ReplaceAll(s, "\\", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return strings.ToLower(s)
}

// Authenticate maps a bearer token constant-time to a server-side ActorContext and scopes.
func (s *Store) Authenticate(bearerToken string) (*domain.ActorContext, []string, bool) {
	bearerToken = strings.TrimSpace(bearerToken)
	if bearerToken == "" {
		return nil, nil, false
	}

	if subtle.ConstantTimeCompare([]byte(bearerToken), []byte(s.operatorToken)) == 1 {
		act := s.operatorActor.Clone()
		return &act, s.operatorActor.CallerPermissions.Slice(), true
	}

	if subtle.ConstantTimeCompare([]byte(bearerToken), []byte(s.agentToken)) == 1 {
		act := s.agentActor.Clone()
		return &act, s.agentActor.CallerPermissions.Slice(), true
	}

	return nil, nil, false
}

// ActiveBearerSecrets returns independent byte copies of both active server-owned bearer tokens.
func (s *Store) ActiveBearerSecrets() [][]byte {
	if s == nil {
		return nil
	}
	return [][]byte{
		append([]byte(nil), s.operatorToken...),
		append([]byte(nil), s.agentToken...),
	}
}

// ReadTokenFile reads the token corresponding to the requested TokenType from the auth directory.
func ReadTokenFile(authDir string, tokenType TokenType) (string, error) {
	var fileName string
	switch tokenType {
	case TokenTypeOperator:
		fileName = OperatorTokenFileName
	case TokenTypeAgentMCP:
		fileName = AgentMCPTokenFileName
	default:
		return "", fmt.Errorf("auth: unknown token type %q", tokenType)
	}

	path := filepath.Join(authDir, fileName)
	token, err := validateTokenFile(path)
	if err != nil {
		return "", fmt.Errorf("auth: failed to read %s token: %w", tokenType, err)
	}
	return token, nil
}
