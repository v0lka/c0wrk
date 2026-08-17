package tools

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/v0lka/sp4rk/tools"
)

const toolCancelDelegationDescription = `Purpose: cancel a currently running subagent task.
Use when: only when the delegated work has clearly gone the wrong way or duplicates other work — the subagent may already be minutes into execution and cancellation discards its progress.
Inputs: id (the running delegation's id, as returned by delegate).
Outputs: cancellation confirmation; the subagent is terminated.
Example: cancel a subagent that started editing the wrong module.
Anti-example: do not cancel on the first odd intermediate event — read its progress first; completed tasks cannot be canceled; to redo work launch a corrected delegation instead of cancel-and-hope.`

// CancelDelegationTool cancels an async delegation via the registry in context.
type CancelDelegationTool struct {
	*sdktools.BaseTool
}

// NewCancelDelegationTool creates the cancel_delegation tool.
func NewCancelDelegationTool() *CancelDelegationTool {
	return &CancelDelegationTool{
		BaseTool: &sdktools.BaseTool{
			ToolGroup:       sdktools.GroupSystem,
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
