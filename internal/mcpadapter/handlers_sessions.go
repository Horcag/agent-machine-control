package mcpadapter

import (
	"context"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ReceiptDTO = receipt.DTO

func (a *Adapter) SessionOpen(ctx context.Context, _ *mcp.CallToolRequest, in SessionOpenInput) (*mcp.CallToolResult, SessionOpenResult, error) {
	if err := validateMutationParams(in.Target, in.Reason, in.IdempotencyKey); err != nil {
		return mcpToolError(err), SessionOpenResult{}, nil
	}
	if err := validateSessionOpenInput(in); err != nil {
		return mcpToolError(NewInputError(err.Error())), SessionOpenResult{}, nil
	}
	if _, err := a.resolveTarget(ctx, in.Target); err != nil {
		return mcpToolError(err), SessionOpenResult{}, nil
	}
	if err := validateApprovalID(in.ApprovalID); err != nil {
		return mcpToolError(err), SessionOpenResult{}, nil
	}
	deadline, err := validateApprovalDeadline(in.ApprovalID, in.Deadline)
	if err != nil {
		return mcpToolError(err), SessionOpenResult{}, nil
	}

	timeout, err := parseTimeout(in.Timeout, true)
	if err != nil {
		return mcpToolError(err), SessionOpenResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), SessionOpenResult{}, nil
	}

	req := daemon.SessionOpenRequest{
		Target:         in.Target,
		Reason:         in.Reason,
		IdempotencyKey: in.IdempotencyKey,
		Cols:           in.Cols,
		Rows:           in.Rows,
		Term:           in.Term,
		ApprovalID:     in.ApprovalID,
		Deadline:       formatApprovalDeadline(deadline),
	}
	req.TimeoutSeconds, req.TimeoutMillis, err = daemon.EncodeSessionTimeout(timeout)
	if err != nil {
		return mcpToolError(NewInputError(err.Error())), SessionOpenResult{}, nil
	}

	resp, err := cl.OpenSession(ctx, req)
	if err != nil {
		return mcpToolError(err), SessionOpenResult{}, nil
	}

	return nil, SessionOpenResult{
		SchemaVersion:   SchemaVersion,
		ObservationType: string(domain.ObservationObserved),
		Session:         resp.Session,
		Receipt:         resp.Receipt,
	}, nil
}

func validateSessionOpenInput(in SessionOpenInput) error {
	cols := in.Cols
	if cols == 0 {
		cols = domain.DefaultCols
	}
	rows := in.Rows
	if rows == 0 {
		rows = domain.DefaultRows
	}
	term := in.Term
	if term == "" {
		term = domain.DefaultTermType
	}
	return domain.ValidateOperationParameters("session.open", map[string]any{
		"cols": cols,
		"rows": rows,
		"term": term,
	})
}

func (a *Adapter) SessionRead(ctx context.Context, _ *mcp.CallToolRequest, in SessionReadInput) (*mcp.CallToolResult, SessionReadResult, error) {
	if err := domain.ValidateSessionID(in.SessionID); err != nil {
		return mcpToolError(NewInputError("invalid session ID")), SessionReadResult{}, nil
	}

	timeout, err := parseTimeout(in.Timeout, false)
	if err != nil {
		return mcpToolError(err), SessionReadResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), SessionReadResult{}, nil
	}

	resp, err := cl.ReadSession(ctx, in.SessionID, in.AfterSeq, in.Limit, timeout)
	if err != nil {
		return mcpToolError(err), SessionReadResult{}, nil
	}

	return nil, SessionReadResult{
		SchemaVersion: SchemaVersion,
		SessionID:     resp.SessionID,
		Chunks:        resp.Chunks,
		NextSeq:       resp.NextSeq,
		LossBytes:     resp.LossBytes,
		HasMore:       resp.HasMore,
		Closed:        resp.Closed,
		ExitCode:      resp.ExitCode,
	}, nil
}

func (a *Adapter) SessionWrite(ctx context.Context, _ *mcp.CallToolRequest, in SessionWriteInput) (*mcp.CallToolResult, SessionWriteResult, error) {
	if err := domain.ValidateSessionID(in.SessionID); err != nil {
		return mcpToolError(NewInputError("invalid session ID")), SessionWriteResult{}, nil
	}
	if in.Data == "" {
		return mcpToolError(NewInputError("data cannot be empty")), SessionWriteResult{}, nil
	}
	if in.Reason == "" {
		return mcpToolError(NewInputError("reason cannot be empty")), SessionWriteResult{}, nil
	}
	if in.IdempotencyKey == "" {
		return mcpToolError(NewInputError("idempotency_key cannot be empty")), SessionWriteResult{}, nil
	}
	if err := validateApprovalID(in.ApprovalID); err != nil {
		return mcpToolError(err), SessionWriteResult{}, nil
	}
	deadline, err := validateApprovalDeadline(in.ApprovalID, in.Deadline)
	if err != nil {
		return mcpToolError(err), SessionWriteResult{}, nil
	}
	timeout, err := parseTimeout(in.Timeout, true)
	if err != nil {
		return mcpToolError(err), SessionWriteResult{}, nil
	}
	if _, err := a.resolveTarget(ctx, ""); err != nil {
		return mcpToolError(err), SessionWriteResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), SessionWriteResult{}, nil
	}

	resp, err := cl.WriteSessionWithApprovalReference(ctx, in.SessionID, in.Data, in.Reason, in.IdempotencyKey, timeout, in.ApprovalID, deadline)
	if err != nil {
		return mcpToolError(err), SessionWriteResult{}, nil
	}

	return nil, SessionWriteResult{
		SchemaVersion: SchemaVersion,
		BytesWritten:  resp.BytesWritten,
		Receipt:       resp.Receipt,
	}, nil
}

func (a *Adapter) SessionControl(ctx context.Context, _ *mcp.CallToolRequest, in SessionControlInput) (*mcp.CallToolResult, SessionControlResult, error) {
	if err := domain.ValidateSessionID(in.SessionID); err != nil {
		return mcpToolError(NewInputError("invalid session ID")), SessionControlResult{}, nil
	}
	if in.Reason == "" {
		return mcpToolError(NewInputError("reason cannot be empty")), SessionControlResult{}, nil
	}
	if in.IdempotencyKey == "" {
		return mcpToolError(NewInputError("idempotency_key cannot be empty")), SessionControlResult{}, nil
	}
	if err := validateApprovalID(in.ApprovalID); err != nil {
		return mcpToolError(err), SessionControlResult{}, nil
	}
	deadline, err := validateApprovalDeadline(in.ApprovalID, in.Deadline)
	if err != nil {
		return mcpToolError(err), SessionControlResult{}, nil
	}
	timeout, err := parseTimeout(in.Timeout, true)
	if err != nil {
		return mcpToolError(err), SessionControlResult{}, nil
	}
	normKey, err := domain.NormalizeControlKey(in.Key)
	if err != nil {
		return mcpToolError(NewInputError("invalid control key")), SessionControlResult{}, nil
	}
	if _, err := a.resolveTarget(ctx, ""); err != nil {
		return mcpToolError(err), SessionControlResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), SessionControlResult{}, nil
	}

	resp, err := cl.SendControlKeyWithApprovalReference(ctx, in.SessionID, normKey, in.Reason, in.IdempotencyKey, timeout, in.ApprovalID, deadline)
	if err != nil {
		return mcpToolError(err), SessionControlResult{}, nil
	}

	return nil, SessionControlResult{
		SchemaVersion: SchemaVersion,
		Status:        resp.Status,
		Receipt:       resp.Receipt,
	}, nil
}

func (a *Adapter) SessionWait(ctx context.Context, _ *mcp.CallToolRequest, in SessionWaitInput) (*mcp.CallToolResult, SessionWaitResult, error) {
	if err := domain.ValidateSessionID(in.SessionID); err != nil {
		return mcpToolError(NewInputError("invalid session ID")), SessionWaitResult{}, nil
	}

	timeout, err := parseTimeout(in.Timeout, false)
	if err != nil {
		return mcpToolError(err), SessionWaitResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), SessionWaitResult{}, nil
	}

	req := daemon.SessionWaitRequest{
		SettleMs: in.SettleMs,
		Regex:    in.Regex,
		AfterSeq: in.AfterSeq,
	}
	req.TimeoutSeconds, req.TimeoutMillis, err = daemon.EncodeSessionTimeout(timeout)
	if err != nil {
		return mcpToolError(NewInputError(err.Error())), SessionWaitResult{}, nil
	}

	resp, err := cl.WaitSession(ctx, in.SessionID, req)
	if err != nil {
		return mcpToolError(err), SessionWaitResult{}, nil
	}

	return nil, SessionWaitResult{
		SchemaVersion: SchemaVersion,
		SessionID:     resp.SessionID,
		Chunks:        resp.Chunks,
		NextSeq:       resp.NextSeq,
		LossBytes:     resp.LossBytes,
		Matched:       resp.Matched,
		Closed:        resp.Closed,
	}, nil
}

func (a *Adapter) SessionList(ctx context.Context, _ *mcp.CallToolRequest, in SessionListInput) (*mcp.CallToolResult, SessionListResult, error) {
	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), SessionListResult{}, nil
	}

	sessions, err := cl.ListSessions(ctx, in.Machine)
	if err != nil {
		return mcpToolError(err), SessionListResult{}, nil
	}

	return nil, SessionListResult{
		SchemaVersion: SchemaVersion,
		Sessions:      sessions,
	}, nil
}

func (a *Adapter) SessionShow(ctx context.Context, _ *mcp.CallToolRequest, in SessionShowInput) (*mcp.CallToolResult, SessionShowResult, error) {
	if err := domain.ValidateSessionID(in.SessionID); err != nil {
		return mcpToolError(NewInputError("invalid session ID")), SessionShowResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), SessionShowResult{}, nil
	}

	sess, err := cl.GetSession(ctx, in.SessionID)
	if err != nil {
		return mcpToolError(err), SessionShowResult{}, nil
	}

	return nil, SessionShowResult{
		SchemaVersion:   SchemaVersion,
		ObservationType: string(domain.ObservationObserved),
		Session:         *sess,
	}, nil
}

func (a *Adapter) SessionClose(ctx context.Context, _ *mcp.CallToolRequest, in SessionCloseInput) (*mcp.CallToolResult, SessionCloseResult, error) {
	if err := domain.ValidateSessionID(in.SessionID); err != nil {
		return mcpToolError(NewInputError("invalid session ID")), SessionCloseResult{}, nil
	}
	if in.Reason == "" {
		return mcpToolError(NewInputError("reason cannot be empty")), SessionCloseResult{}, nil
	}
	if in.IdempotencyKey == "" {
		return mcpToolError(NewInputError("idempotency_key cannot be empty")), SessionCloseResult{}, nil
	}
	if err := validateApprovalID(in.ApprovalID); err != nil {
		return mcpToolError(err), SessionCloseResult{}, nil
	}
	deadline, err := validateApprovalDeadline(in.ApprovalID, in.Deadline)
	if err != nil {
		return mcpToolError(err), SessionCloseResult{}, nil
	}
	timeout, err := parseTimeout(in.Timeout, true)
	if err != nil {
		return mcpToolError(err), SessionCloseResult{}, nil
	}
	if _, err := a.resolveTarget(ctx, ""); err != nil {
		return mcpToolError(err), SessionCloseResult{}, nil
	}

	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), SessionCloseResult{}, nil
	}

	resp, err := cl.CloseSessionWithApprovalReference(ctx, in.SessionID, in.Reason, in.IdempotencyKey, timeout, in.ApprovalID, deadline)
	if err != nil {
		return mcpToolError(err), SessionCloseResult{}, nil
	}

	return nil, SessionCloseResult{
		SchemaVersion: SchemaVersion,
		Session:       resp.Session,
		Receipt:       resp.Receipt,
	}, nil
}

func validateApprovalID(id string) error {
	if id == "" {
		return nil
	}
	if err := domain.ValidateApprovalID(id); err != nil {
		return NewInputError("invalid approval_id")
	}
	return nil
}

func validateApprovalDeadline(approvalID, raw string) (time.Time, error) {
	deadline, err := daemon.ResolveSessionDeadline(approvalID, raw)
	if err != nil {
		return time.Time{}, NewInputError("approval_id requires the exact canonical deadline returned by issuance")
	}
	return deadline, nil
}

func formatApprovalDeadline(deadline time.Time) string {
	if deadline.IsZero() {
		return ""
	}
	return deadline.UTC().Format(time.RFC3339Nano)
}
