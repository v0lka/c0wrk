// Package session provides typed event payloads for session lifecycle events.
package session

import (
	"encoding/json"
	"time"

	"github.com/user/agent/core"
	sdktools "github.com/user/agent/sdk/tools"
)

// Event type constants for backend-to-frontend communication.
const (
	EventStepLimit         = "step_limit"
	EventStepLimitResponse = "step_limit_response"
)

// --- Session lifecycle event data ---

// SessionCreatedData is the payload for "session_created" events.
type SessionCreatedData struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// SessionDeletedData is the payload for "session_deleted" events.
type SessionDeletedData struct {
	ID string `json:"id"`
}

// SessionRenamedData is the payload for "session_renamed" events.
type SessionRenamedData struct {
	ID      string `json:"id"`
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
}

// SessionArchivedData is the payload for "session_archived" / "session_unarchived" events.
type SessionArchivedData struct {
	ID       string `json:"id"`
	Archived bool   `json:"archived"`
}

// MessageReceivedData is the payload for "message_received" events.
type MessageReceivedData struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// TaskCompleteData is the payload for "task_complete" events.
type TaskCompleteData struct {
	SessionID       string                `json:"session_id"`
	Output          string                `json:"output"`
	RoutingDecision *core.RoutingDecision `json:"routing_decision"`
	Plan            *core.Plan            `json:"plan,omitempty"`
	AttemptCount    int                   `json:"attempt_count,omitempty"`
	Reflections     []core.Reflection     `json:"reflections,omitempty"`
}

// TaskCancelledData is the payload for "task_cancelled" events.
type TaskCancelledData struct {
	SessionID string `json:"session_id"`
}

// TaskFailedResumableData is the payload for "task_failed_resumable" events.
// Emitted when plan execution fails but the task can be resumed.
type TaskFailedResumableData struct {
	Message string `json:"message"`
}

// ErrorData is the payload for "error" events.
type ErrorData struct {
	SessionID string `json:"session_id"`
	Error     string `json:"error"`
}

// --- Tool confirmation payloads ---

// ToolConfirmPayload is sent to the frontend when a tool needs user confirmation.
type ToolConfirmPayload struct {
	ConfirmID string `json:"confirm_id"`
	Tool      string `json:"tool"`
	Args      string `json:"args"`
	Reasoning string `json:"reasoning"`
}

// JudgeRequestPayload is received from the frontend when the user requests an on-demand judge verdict.
type JudgeRequestPayload struct {
	ConfirmID string `json:"confirm_id"`
}

// JudgeResponsePayload is sent to the frontend with the judge's verdict.
type JudgeResponsePayload struct {
	ConfirmID string `json:"confirm_id"`
	Reasoning string `json:"reasoning"`
	Error     string `json:"error,omitempty"`
}

// --- Ask-user payloads ---

// AskUserPayload is sent to the frontend when the agent asks the user questions.
type AskUserPayload struct {
	RequestID string                     `json:"request_id"`
	Questions []sdktools.AskUserQuestion `json:"questions"`
}

// --- Step limit payloads ---

// StepLimitPayload is emitted when an agent reaches its tool call step limit,
// prompting the user for a decision on whether to continue.
type StepLimitPayload struct {
	RequestID   string `json:"request_id"`
	CurrentStep int    `json:"current_step"`
	MaxSteps    int    `json:"max_steps"`
}

// StepLimitResponsePayload carries the user's decision about continuing
// past the step limit.
type StepLimitResponsePayload struct {
	RequestID string `json:"request_id"`
	Response  string `json:"response"` // "allow_once", "allow_always", or "deny"
}

// --- Emitter event data types (typed Data field payloads) ---
// These mirror the event data produced by the EventEmitter methods,
// enabling type-safe assertions in the emitFunc / persistence layer.

// ThoughtEventData is the typed Data payload for "thought" events.
type ThoughtEventData struct {
	StepNum    int    `json:"step_num"`
	Content    string `json:"content"`
	Reasoning  string `json:"reasoning"`
	PlanStepID string `json:"plan_step_id,omitempty"`
}

// AssistantDoneEventData is the typed Data payload for "assistant_done" events.
type AssistantDoneEventData struct {
	Content      string `json:"content"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	PlanStepID   string `json:"plan_step_id,omitempty"`
}

// ContextFillEventData is the typed Data payload for "context_fill" events.
type ContextFillEventData struct {
	FillPercent         float64 `json:"fill_percent"`
	UsedTokens          int     `json:"used_tokens"`
	MaxTokens           int     `json:"max_tokens"`
	Status              string  `json:"status"`
	PlanStepID          string  `json:"plan_step_id,omitempty"`
	SessionInputTokens  int     `json:"session_input_tokens"`
	SessionOutputTokens int     `json:"session_output_tokens"`
	Model               string  `json:"model"`
	Tier                string  `json:"tier"`
}

// SessionTokensEventData is the typed Data payload for "session_tokens" events.
type SessionTokensEventData struct {
	SessionInputTokens  int    `json:"session_input_tokens"`
	SessionOutputTokens int    `json:"session_output_tokens"`
	Model               string `json:"model"`
	Tier                string `json:"tier"`
}

// --- ChatMessage metadata helpers ---

// MetadataFrom marshals any value into a json.RawMessage suitable for ChatMessage.Metadata.
func MetadataFrom(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage("{}")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
