// Package tools provides the tool abstraction, registry, and security policies for agent tool execution.
package tools

import (
	"context"
	"encoding/json"

	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

// Type aliases for SDK types — core/tools re-exports these for convenience.
type Tool = sdktools.Tool
type ToolPolicy = sdktools.ToolPolicy
type ToolResult = sdktools.ToolResult
type ToolDescriptor = sdktools.ToolDescriptor
type BaseTool = sdktools.BaseTool
type EnvInfo = sdktools.EnvInfo
type AskUserQuestion = sdktools.AskUserQuestion
type FileCoherenceChecker = sdktools.FileCoherenceChecker
type FileSig = sdktools.FileSig
type CoherenceConflict = sdktools.CoherenceConflict

// Re-export SDK constants.
const (
	PolicyAlwaysAllow = sdktools.PolicyAlwaysAllow
	PolicyAlwaysDeny  = sdktools.PolicyAlwaysDeny
	PolicyUserConfirm = sdktools.PolicyUserConfirm
)

// Re-export SDK functions.
var (
	ErrorResult           = sdktools.ErrorResult
	ParseInputError       = sdktools.ParseInputError
	ParseToolPolicy       = sdktools.ParseToolPolicy
	WithWorkspacePath     = sdktools.WithWorkspacePath
	WorkspacePathFrom     = sdktools.WorkspacePathFrom
	WithTaskContext       = sdktools.WithTaskContext
	TaskContextFrom       = sdktools.TaskContextFrom
	WithEnvInfo           = sdktools.WithEnvInfo
	EnvInfoFrom           = sdktools.EnvInfoFrom
	CollectEnvInfo        = sdktools.CollectEnvInfo
	FormatFullEnvBlock    = sdktools.FormatFullEnvBlock
	FormatCompactEnvBlock = sdktools.FormatCompactEnvBlock
	WithTempDir           = sdktools.WithTempDir
	WithCoherence         = sdktools.WithCoherence
	CoherenceFrom         = sdktools.CoherenceFrom
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
	ConfirmAllowOnce   ConfirmationResponse = iota // Allow this single execution
	ConfirmDeny                                    // Deny this execution
	ConfirmDenyAndStop                             // Deny and cancel the entire task
)

// ConfirmFunc is called before executing a mutating tool.
// If nil, all tools execute without confirmation (CLI mode).
type ConfirmFunc func(ctx context.Context, req ConfirmationRequest) (ConfirmationResponse, error)

// AskUserOption represents a single answer option for the ask_user tool.
type AskUserOption = sdktools.AskUserOption

// AskUserRequest describes a question to ask the user via the UI.
type AskUserRequest = sdktools.AskUserRequest

// AskUserResponse represents the user's answer.
type AskUserResponse = sdktools.AskUserResponse

// AskUserAnswer is a single answer from the user.
type AskUserAnswer = sdktools.AskUserAnswer

// AskUserFunc is called when the ask_user tool needs to display a question to the user.
// If nil, ask_user is not available (CLI mode).
type AskUserFunc = sdktools.AskUserFunc

// AutoInjectedParamProject is the parameter name auto-injected by param injectors
// (e.g. project path). Schema sanitizers strip this parameter from tool schemas so
// the LLM never sees it, while the injector adds it at execution time.
const AutoInjectedParamProject = "project"
