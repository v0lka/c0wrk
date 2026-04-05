// Package session provides typed event payloads for session lifecycle events.
package session

import (
	"encoding/json"
	"time"

	"github.com/user/agent/internal/core"
	"github.com/user/agent/internal/tools"
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
	SessionID       string               `json:"session_id"`
	Output          string               `json:"output"`
	RoutingDecision *core.RoutingDecision `json:"routing_decision"`
	Plan            *core.Plan            `json:"plan,omitempty"`
	EvalResult      *core.EvalResult      `json:"eval_result,omitempty"`
	AttemptCount    int                   `json:"attempt_count,omitempty"`
	Reflections     []core.Reflection     `json:"reflections,omitempty"`
	Escalated       bool                  `json:"escalated,omitempty"`
	OriginalMode    string                `json:"original_mode,omitempty"`
}

// TaskCancelledData is the payload for "task_cancelled" events.
type TaskCancelledData struct {
	SessionID string `json:"session_id"`
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

// --- Ask-user payloads ---

// AskUserPayload is sent to the frontend when the agent asks the user a question.
type AskUserPayload struct {
	RequestID   string              `json:"request_id"`
	Question    string              `json:"question"`
	Options     []tools.AskUserOption `json:"options"`
	MultiSelect bool                `json:"multi_select"`
	Recommended []string            `json:"recommended,omitempty"`
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

// --- ChatMessage metadata helpers ---

// MetadataFrom marshals any value into a json.RawMessage suitable for ChatMessage.Metadata.
func MetadataFrom(v interface{}) json.RawMessage {
	if v == nil {
		return json.RawMessage("{}")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
