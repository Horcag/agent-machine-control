package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

var (
	ErrBootstrapDrift       = errors.New("bootstrap: owned task state has drifted")
	ErrBootstrapUnsupported = errors.New("bootstrap: WSL host integration is unavailable")
	ErrBootstrapUnhealthy   = errors.New("bootstrap: daemon did not reach the required health state")
	ErrBootstrapAbsent      = errors.New("bootstrap: owned task is absent")
	ErrBootstrapPriorFailed = errors.New("bootstrap: prior exact attempt failed")
)

type BootstrapState string

const (
	BootstrapAbsent  BootstrapState = "absent"
	BootstrapStopped BootstrapState = "stopped"
	BootstrapHealthy BootstrapState = "healthy"
	BootstrapDrift   BootstrapState = "drift"
)

const (
	BootstrapReasonAbsent       = "owned task and artifacts are absent"
	BootstrapReasonStopped      = "owned task is stopped"
	BootstrapReasonHealthy      = "owned task and daemon are healthy"
	BootstrapReasonTaskMismatch = "owned task fingerprint does not match"
	BootstrapReasonEndpoint     = "daemon endpoint ownership does not match"
)

type BootstrapIdentity struct {
	Account string `json:"account"`
	SID     string `json:"sid"`
}

func (i BootstrapIdentity) Validate() error {
	if err := domain.ValidateBoundedString(i.Account, 1, 256, ErrBootstrapUnsupported); err != nil {
		return err
	}
	if err := domain.ValidateBoundedString(i.SID, 1, 184, ErrBootstrapUnsupported); err != nil {
		return err
	}
	sid := strings.ToUpper(i.SID)
	if sid == "S-1-5-18" || sid == "S-1-5-19" || sid == "S-1-5-20" {
		return fmt.Errorf("%w: service identities are forbidden", ErrBootstrapUnsupported)
	}
	return nil
}

type BootstrapSpec struct {
	TaskPath              string `json:"task_path"`
	TaskName              string `json:"task_name"`
	ActionExecutable      string `json:"action_executable"`
	ActionArguments       string `json:"action_arguments"`
	Account               string `json:"account"`
	UserSID               string `json:"user_sid"`
	LogonType             string `json:"logon_type"`
	RunLevel              string `json:"run_level"`
	LogonTrigger          bool   `json:"logon_trigger"`
	StartWhenAvailable    bool   `json:"start_when_available"`
	MultipleInstances     string `json:"multiple_instances"`
	RestartCount          int    `json:"restart_count"`
	RestartInterval       string `json:"restart_interval"`
	ExecutionTimeLimit    string `json:"execution_time_limit"`
	AllowStartOnBatteries bool   `json:"allow_start_on_batteries"`
	DontStopOnBatteries   bool   `json:"dont_stop_on_batteries"`
	Distro                string `json:"distro"`
	LinuxUser             string `json:"linux_user"`
	StateDir              string `json:"state_dir"`
	WrapperPath           string `json:"wrapper_path"`
	WrapperSHA256         string `json:"wrapper_sha256"`
	MetadataPath          string `json:"metadata_path"`
	MetadataSHA256        string `json:"metadata_sha256"`
	BinaryPath            string `json:"binary_path"`
	BinarySHA256          string `json:"binary_sha256"`
	WSLExecutable         string `json:"wsl_executable"`
	ListenAddress         string `json:"listen_address"`
}

func (s BootstrapSpec) Validate(identity BootstrapIdentity) error {
	if err := identity.Validate(); err != nil {
		return err
	}
	if err := s.validatePrincipal(identity); err != nil {
		return err
	}
	if err := s.validateSettings(); err != nil {
		return err
	}
	return s.validateArtifacts()
}

func (s BootstrapSpec) validatePrincipal(identity BootstrapIdentity) error {
	if s.Account != identity.Account || s.UserSID != identity.SID {
		return fmt.Errorf("%w: principal does not match current Windows identity", ErrBootstrapDrift)
	}
	if s.TaskPath != `\AgentMachineControl\` || s.TaskName != "amcd-current-user" {
		return fmt.Errorf("%w: non-canonical task identity", ErrBootstrapDrift)
	}
	if s.LogonType != "S4U" || s.RunLevel != "Limited" || !s.LogonTrigger {
		return fmt.Errorf("%w: task must use current-user S4U Limited with a logon trigger", ErrBootstrapDrift)
	}
	return nil
}

func (s BootstrapSpec) validateSettings() error {
	if !s.StartWhenAvailable || s.MultipleInstances != "IgnoreNew" || s.RestartCount < 1 {
		return fmt.Errorf("%w: task settings are not lifecycle-safe", ErrBootstrapDrift)
	}
	if s.ExecutionTimeLimit != "PT0S" || !s.AllowStartOnBatteries || !s.DontStopOnBatteries {
		return fmt.Errorf("%w: task settings can interrupt the daemon", ErrBootstrapDrift)
	}
	if s.ListenAddress != "127.0.0.1:0" {
		return fmt.Errorf("%w: daemon listen address must be ephemeral loopback", ErrBootstrapDrift)
	}
	return nil
}

func (s BootstrapSpec) validateArtifacts() error {
	for name, value := range map[string]string{
		"action executable": s.ActionExecutable, "action arguments": s.ActionArguments,
		"distro": s.Distro, "Linux user": s.LinuxUser, "state dir": s.StateDir,
		"wrapper path": s.WrapperPath, "metadata path": s.MetadataPath,
		"binary path": s.BinaryPath, "WSL executable": s.WSLExecutable,
	} {
		if err := domain.ValidateBoundedString(value, 1, 4096, ErrBootstrapDrift); err != nil {
			return fmt.Errorf("bootstrap: invalid %s: %w", name, err)
		}
	}
	for _, digest := range []string{s.WrapperSHA256, s.MetadataSHA256, s.BinarySHA256} {
		if err := domain.Fingerprint(digest).Validate(); err != nil {
			return fmt.Errorf("%w: invalid executable fingerprint", ErrBootstrapDrift)
		}
	}
	return nil
}

type BootstrapObservation struct {
	State       BootstrapState `json:"state"`
	Reason      string         `json:"reason,omitempty"`
	Exact       bool           `json:"exact"`
	TaskRunning bool           `json:"task_running"`
}

type BootstrapResult struct {
	SchemaVersion int            `json:"schema_version"`
	Status        BootstrapState `json:"status"`
	Reason        string         `json:"reason,omitempty"`
	TaskPath      string         `json:"task_path,omitempty"`
	TaskName      string         `json:"task_name,omitempty"`
	ReceiptID     string         `json:"receipt_id,omitempty"`
	Replayed      bool           `json:"replayed,omitempty"`
}

type BootstrapMutationRequest struct {
	StateDir       string
	Reason         string
	IdempotencyKey string
	Deadline       time.Time
}

type BootstrapAdapter interface {
	Identity(context.Context) (BootstrapIdentity, error)
	Desired(context.Context, string, BootstrapIdentity) (BootstrapSpec, error)
	Inspect(context.Context, BootstrapSpec) (BootstrapObservation, error)
	Install(context.Context, BootstrapSpec) error
	StartTask(context.Context, BootstrapSpec) error
	StopTask(context.Context, BootstrapSpec) error
	Remove(context.Context, BootstrapSpec) error
}

type BootstrapDaemon interface {
	Healthy(context.Context, string) (bool, error)
	Stop(context.Context, string) error
}
