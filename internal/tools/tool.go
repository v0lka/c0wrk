package tools

import (
	"context"
	"encoding/json"
)

// ToolPolicy defines the security policy for a tool.
type ToolPolicy int

const (
	// PolicyAlwaysAllow executes the tool without any confirmation or judge check.
	PolicyAlwaysAllow ToolPolicy = iota
	// PolicyAlwaysDeny blocks the tool from executing.
	PolicyAlwaysDeny
	// PolicyUserConfirm always requires user confirmation before executing.
	PolicyUserConfirm
	// PolicyAuto uses tool-specific heuristics with LLM Judge fallback.
	PolicyAuto
)

// Tool — unified interface for all tools (Core, MCP, Skills).
type Tool interface {
	Name() string
	Description() string
	InputSchema() json.RawMessage
	Execute(ctx context.Context, input json.RawMessage) (ToolResult, error)
	DefaultPolicy() ToolPolicy
}

// ToolJudger is an optional interface that tools can implement to provide
// tool-specific safety heuristics for Auto policy mode.
// If allow is true, the tool call is safe to execute.
// If allow is false and reasoning is non-empty, the tool explicitly flags the call (ask user).
// If allow is false and reasoning is empty, the tool defers to the LLM Judge.
type ToolJudger interface {
	Judge(ctx context.Context, input json.RawMessage) (allow bool, reasoning string)
}

// ToolResult — result of tool execution.
type ToolResult struct {
	Content string
	IsError bool
}

// ToolDescriptor — describes a tool for Planner/Executor (metadata only, no execution).
type ToolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Source      string          `json:"source"` // "core" | "mcp" | "skill"
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
	ConfirmAllowOnce   ConfirmationResponse = iota // Allow this single execution
	ConfirmDeny                                    // Deny this execution
	ConfirmDenyAndStop                             // Deny and cancel the entire task
)

// ConfirmFunc is called before executing a mutating tool.
// If nil, all tools execute without confirmation (CLI mode).
type ConfirmFunc func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error)

// AskUserOption represents a single answer option for the ask_user tool.
type AskUserOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// AskUserRequest describes a question to ask the user via the UI.
type AskUserRequest struct {
	Question    string          `json:"question"`
	Options     []AskUserOption `json:"options"`
	MultiSelect bool            `json:"multi_select"`
	Recommended []string        `json:"recommended,omitempty"`
}

// AskUserResponse represents the user's answer.
type AskUserResponse struct {
	Selected   []string `json:"selected"`
	CustomText string   `json:"custom_text,omitempty"`
}

// AskUserFunc is called when the ask_user tool needs to display a question to the user.
// If nil, ask_user is not available (CLI mode).
type AskUserFunc func(ctx context.Context, req AskUserRequest) (AskUserResponse, error)

// workspacePathKey is the context key for the session workspace path.
type workspacePathKey struct{}

// WithWorkspacePath returns a new context with the session workspace path attached.
func WithWorkspacePath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, workspacePathKey{}, path)
}

// WorkspacePathFrom extracts the session workspace path from the context.
// Returns an empty string if not found.
func WorkspacePathFrom(ctx context.Context) string {
	if v, ok := ctx.Value(workspacePathKey{}).(string); ok {
		return v
	}
	return ""
}
