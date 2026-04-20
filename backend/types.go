package backend

import (
	"encoding/json"

	"github.com/user/agent/core"
	"github.com/user/agent/core/tools"
	"github.com/user/agent/core/tools/mcp"
)

// ---------------------------------------------------------------------------
// Tool confirmation types (from core/tools)
// ---------------------------------------------------------------------------

// ConfirmFunc is called before executing a mutating tool.
type ConfirmFunc = tools.ConfirmFunc

// ConfirmationRequest describes a tool execution that needs user confirmation.
type ConfirmationRequest = tools.ConfirmationRequest

// ConfirmationResponse represents the user's confirmation decision.
type ConfirmationResponse = tools.ConfirmationResponse

// Confirmation response constants.
const (
	ConfirmAllowOnce   = tools.ConfirmAllowOnce
	ConfirmDeny        = tools.ConfirmDeny
	ConfirmDenyAndStop = tools.ConfirmDenyAndStop
)

// ---------------------------------------------------------------------------
// Ask-user types (from core/tools)
// ---------------------------------------------------------------------------

// AskUserFunc is the callback for the ask_user tool.
type AskUserFunc = tools.AskUserFunc

// AskUserRequest describes a question to ask the user via the UI.
type AskUserRequest = tools.AskUserRequest

// AskUserResponse represents the user's answer.
type AskUserResponse = tools.AskUserResponse

// AskUserAnswer is a single answer from the user.
type AskUserAnswer = tools.AskUserAnswer

// AskUserQuestion is a single question in an ask_user request.
type AskUserQuestion = tools.AskUserQuestion

// ---------------------------------------------------------------------------
// Step limit types (from core)
// ---------------------------------------------------------------------------

// StepLimitFunc is called when an executor reaches its step limit.
type StepLimitFunc = core.StepLimitFunc

// StepLimitResponse represents the user's decision when the step limit is reached.
type StepLimitResponse = core.StepLimitResponse

// Step limit response constants.
var (
	StepLimitAllowOnce   = core.StepLimitAllowOnce
	StepLimitAllowAlways = core.StepLimitAllowAlways
	StepLimitDeny        = core.StepLimitDeny
)

// ---------------------------------------------------------------------------
// Security policy types (from core/tools)
// ---------------------------------------------------------------------------

// ToolPolicy represents a security policy for a tool.
type ToolPolicy = tools.ToolPolicy

// Policy constants.
const (
	PolicyAlwaysAllow = tools.PolicyAlwaysAllow
	PolicyAlwaysDeny  = tools.PolicyAlwaysDeny
	PolicyUserConfirm = tools.PolicyUserConfirm
)

// ParseToolPolicy parses a string into a ToolPolicy.
var ParseToolPolicy = tools.ParseToolPolicy

// IsInternalTool returns true if the tool is always allowed.
var IsInternalTool = tools.IsInternalTool

// ---------------------------------------------------------------------------
// Tool types (from core/tools)
// ---------------------------------------------------------------------------

// ToolDescriptor describes a tool's schema and metadata.
type ToolDescriptor = tools.ToolDescriptor

// EnvInfo holds environment information for context injection.
type EnvInfo = tools.EnvInfo

// CollectEnvInfo gathers environment information.
var CollectEnvInfo = tools.CollectEnvInfo

// TaskContextFrom extracts the task context string from a context.
var TaskContextFrom = tools.TaskContextFrom

// JudgeVerdict represents the safety assessment of a tool call.
type JudgeVerdict = tools.JudgeVerdict

// Judge verdict constants.
const (
	VerdictAllow   = tools.VerdictAllow
	VerdictConfirm = tools.VerdictConfirm
)

// ---------------------------------------------------------------------------
// Vector search types (from core/tools)
// ---------------------------------------------------------------------------

// VectorSearchFunc is the callback for the semantic_search tool.
type VectorSearchFunc = tools.VectorSearchFunc

// VectorSearchWaitFunc blocks until the vector index is ready.
type VectorSearchWaitFunc = tools.VectorSearchWaitFunc

// VectorSearchResult holds a single vector search hit.
type VectorSearchResult = tools.VectorSearchResult

// ---------------------------------------------------------------------------
// MCP types (from core/tools/mcp)
// ---------------------------------------------------------------------------

// MCPServerStatus represents the status of an MCP server.
type MCPServerStatus = mcp.ServerStatus

// MCPServerEntry defines how to connect to an MCP server.
type MCPServerEntry = mcp.ServerEntry

// MCPGatewayConfig holds the MCP gateway configuration.
type MCPGatewayConfig = mcp.GatewayConfig

// MCPSchemaSanitizer transforms an MCP tool input schema before registration.
type MCPSchemaSanitizer = mcp.SchemaSanitizer

// MCPStripParamsFromSchema strips named parameters from a JSON Schema.
var MCPStripParamsFromSchema = mcp.StripParamsFromSchema

// ---------------------------------------------------------------------------
// Emitter type (from core)
// ---------------------------------------------------------------------------

// Emitter defines the interface for emitting agent execution events.
type Emitter = core.Emitter

// BlackboardFactory creates a Blackboard for a new task.
type BlackboardFactory = core.BlackboardFactory

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

// RawMessage is an alias for encoding/json.RawMessage to avoid import in desktop.
type RawMessage = json.RawMessage
