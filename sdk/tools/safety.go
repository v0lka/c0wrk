package tools

import (
	"context"
	"encoding/json"
)

// ToolJudger is an optional interface that tools can implement to provide
// tool-specific safety heuristics. When a tool with PolicyAlwaysAllow implements
// this interface, the registry calls Judge before execution. If the judge returns
// allow=false with non-empty reasoning, the call is escalated to user confirmation.
type ToolJudger interface {
	Judge(ctx context.Context, input json.RawMessage) (allow bool, reasoning string)
}

// ConfirmationRequest describes a tool execution that needs user confirmation.
type ConfirmationRequest struct {
	ToolName       string          `json:"tool_name"`
	Input          json.RawMessage `json:"input"`
	JudgeReasoning string          `json:"judge_reasoning,omitempty"`
}

// ConfirmationResponse represents the user's confirmation decision.
type ConfirmationResponse int

const (
	// ConfirmAllowOnce allows this single execution.
	ConfirmAllowOnce ConfirmationResponse = iota
	// ConfirmDeny denies this execution.
	ConfirmDeny
	// ConfirmDenyAndStop denies the execution and cancels the entire task.
	ConfirmDenyAndStop
)

// ConfirmFunc is called before executing a mutating tool.
// If nil, all tools execute without confirmation (CLI mode).
type ConfirmFunc func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error)

// AutoInjectedParamProject is the parameter name auto-injected by param injectors
// (e.g. project path). Schema sanitizers strip this parameter from tool schemas so
// the LLM never sees it, while the injector adds it at execution time.
const AutoInjectedParamProject = "project"
