package app

import (
	"context"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

// ConsoleProvider operates directly on VM display and input devices.
type ConsoleProvider interface {
	CaptureConsole(context.Context, string, int, int) (domain.ConsoleFrame, error)
	SendConsoleInput(context.Context, string, domain.ConsoleInput) error
}

// ConsoleScreenshotRequest authorizes a sensitive observation of the enrolled VM.
type ConsoleScreenshotRequest struct {
	Target string `json:"target"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// ConsoleInputRequest combines a bounded input action with normal mutation metadata.
type ConsoleInputRequest struct {
	Target         string              `json:"target"`
	Input          domain.ConsoleInput `json:"input"`
	Reason         string              `json:"reason"`
	IdempotencyKey string              `json:"idempotency_key"`
	Deadline       string              `json:"deadline"`
	ApprovalID     string              `json:"approval_id,omitempty"`
}
