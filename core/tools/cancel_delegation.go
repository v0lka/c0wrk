package tools

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

const toolCancelDelegationDescription = `Cancel a pending or running async delegation by its ID. The subagent's context is cancelled and the delegation is marked cancelled. No-op for already completed, failed, or cancelled delegations. Use this before calling finish if you have pending async delegations you no longer need.`

// CancelDelegationTool cancels an async delegation via the registry in context.
type CancelDelegationTool struct {
	*sdktools.BaseTool
}

// NewCancelDelegationTool creates the cancel_delegation tool.
func NewCancelDelegationTool() *CancelDelegationTool {
	return &CancelDelegationTool{
		BaseTool: &sdktools.BaseTool{
			ToolName:        "cancel_delegation",
			ToolDescription: toolCancelDelegationDescription,
			Schema: json.RawMessage(`{
	"type": "object",
	"properties": {
		"id": {"type": "string", "description": "The delegation ID to cancel"}
	},
	"required": ["id"]
}`),
			Policy: sdktools.PolicyAlwaysAllow,
		},
	}
}

type cancelDelegationInput struct {
	ID string `json:"id"`
}

func (t *CancelDelegationTool) Execute(ctx context.Context, input json.RawMessage) (sdktools.ToolResult, error) {
	var params cancelDelegationInput
	if err := json.Unmarshal(input, &params); err != nil {
		return sdktools.ParseInputError(err)
	}
	if params.ID == "" {
		return sdktools.ErrorResult("validation error: id is required"), nil
	}

	registry := DelegationRegistryFrom(ctx)
	if registry == nil {
		return sdktools.ErrorResult("cancel_delegation: no delegation registry in context"), nil
	}

	d := registry.Get(params.ID)
	if d == nil {
		return sdktools.ErrorResult("cancel_delegation: unknown delegation id %q", params.ID), nil
	}
	if d.Status == DelegationStatusCompleted || d.Status == DelegationStatusFailed || d.Status == DelegationStatusCancelled {
		return sdktools.ToolResult{Content: fmt.Sprintf("Delegation %q is already %s; no action taken.", params.ID, d.Status)}, nil
	}

	registry.Cancel(params.ID)
	return sdktools.ToolResult{Content: fmt.Sprintf("Delegation %q cancelled.", params.ID)}, nil
}
