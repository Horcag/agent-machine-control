package mcpadapter

import (
	"context"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ReceiptListInput struct {
	Limit          int    `json:"limit,omitempty" jsonschema:"Recent receipts to search before filtering (default 50, maximum 1000)"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"Optional exact idempotency key within the bounded recent results; missing entries do not prove no effect or lack of admission"`
}

type ReceiptListResult struct {
	SchemaVersion string        `json:"schema_version"`
	Receipts      []receipt.DTO `json:"receipts"`
}

func (a *Adapter) ReceiptList(ctx context.Context, _ *mcp.CallToolRequest, in ReceiptListInput) (*mcp.CallToolResult, ReceiptListResult, error) {
	if in.Limit < 0 || in.Limit > 1000 {
		return mcpToolError(NewInputError("receipt limit must be between 0 and 1000")), ReceiptListResult{}, nil
	}
	if in.IdempotencyKey != "" && domain.ValidateIdempotencyKey(in.IdempotencyKey) != nil {
		return mcpToolError(NewInputError("invalid idempotency key")), ReceiptListResult{}, nil
	}
	cl, err := a.getClient()
	if err != nil {
		return mcpToolError(err), ReceiptListResult{}, nil
	}
	recent, err := cl.ListReceipts(ctx, in.Limit)
	if err != nil {
		return mcpToolError(err), ReceiptListResult{}, nil
	}
	matches := make([]receipt.DTO, 0, len(recent))
	for _, rcpt := range recent {
		if in.IdempotencyKey == "" || rcpt.IdempotencyKey == in.IdempotencyKey {
			matches = append(matches, rcpt)
		}
	}
	return nil, ReceiptListResult{SchemaVersion: SchemaVersion, Receipts: matches}, nil
}
