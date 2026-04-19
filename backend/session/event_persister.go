package session

import (
	"encoding/json"
	"log/slog"
	"time"
)

// EventPersister persists chat-visible events to the session store (SQLite).
// It is decoupled from the UI — the desktop layer is responsible for emitting
// Wails events separately.
type EventPersister struct {
	store  SessionStore
	logger *slog.Logger
}

// NewEventPersister creates a new EventPersister backed by the given store.
// If store is nil, Persist is a no-op.
func NewEventPersister(store SessionStore) *EventPersister {
	return &EventPersister{store: store}
}

// log returns the persister's logger, falling back to slog.Default().
func (p *EventPersister) log() *slog.Logger {
	if p.logger != nil {
		return p.logger
	}
	return slog.Default()
}

// SetLogger sets the logger for the event persister.
func (p *EventPersister) SetLogger(l *slog.Logger) {
	p.logger = l
}

// Persist saves a chat-visible event to the session store.
// Transient events (session_tokens, etc.) are silently skipped.
// Errors are logged but never returned — persistence is best-effort.
func (p *EventPersister) Persist(evt Event) {
	if p.store == nil {
		return
	}

	var role, content string

	switch evt.Type {
	case "routing":
		role = "routing"
	case "tool_call":
		role = "tool_call"
	case "tool_result":
		role = "tool_result"
	case "evaluation":
		role = "eval"
	case "reflection":
		role = "reflection"
	case "plan_generated":
		role = "plan"
	case "error":
		role = "error"
	case "assistant_done":
		role = "assistant"
		switch d := evt.Data.(type) {
		case AssistantDoneEventData:
			content = d.Content
		case map[string]any:
			if c, ok := d["content"].(string); ok {
				content = c
			}
		}
	case "task_complete":
		switch d := evt.Data.(type) {
		case TaskCompleteData:
			if d.Output != "" {
				role = "assistant"
				content = d.Output
			}
		case map[string]any:
			if output, ok := d["output"].(string); ok && output != "" {
				role = "assistant"
				content = output
			}
		}
	case "thought":
		role = "thought"
		switch d := evt.Data.(type) {
		case ThoughtEventData:
			content = d.Content
		case map[string]any:
			if c, ok := d["content"].(string); ok {
				content = c
			}
		}
	case "step_start":
		role = "thinking"
	case "step_complete":
		role = "step_done"
	case "plan_step_start":
		role = "plan_step_start"
	case "plan_step_complete":
		role = "plan_step_complete"
	case "retry":
		role = "retry"
	case "subagent_launch":
		role = "subagent_launch"
	case "subagent_complete":
		role = "subagent_complete"
	case "task_failed_resumable":
		role = "task_failed_resumable"
	case "task_resumed":
		role = "task_resumed"
	case "tool_confirm":
		role = "tool_confirm"
	case "ask_user":
		role = "ask_user"
	case "step_limit":
		role = "step_limit"
	case "task_cancelled":
		role = "task_cancelled"
	case "step_retry":
		role = "step_retry"
	case "service":
		// Only persist orchestration phase service events.
		if data, ok := evt.Data.(map[string]any); ok {
			if phase, _ := data["phase"].(string); phase == "orchestration" {
				role = "status"
			}
		}
	case "session_tokens":
		return // transient — no persistence needed
	default:
		return // unknown transient events — skip
	}

	if role == "" {
		return
	}

	// Serialize event data as metadata JSON.
	var metadata json.RawMessage
	if evt.Data != nil {
		if b, err := json.Marshal(evt.Data); err == nil {
			metadata = b
		} else {
			metadata = json.RawMessage("{}")
		}
	} else {
		metadata = json.RawMessage("{}")
	}

	// For non-assistant roles, use metadata as content if content is empty.
	if content == "" {
		content = string(metadata)
	}

	if err := p.store.SaveMessage(ChatMessage{
		SessionID: evt.SessionID,
		Role:      role,
		Content:   content,
		Metadata:  metadata,
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		p.log().Error("failed to persist event message", "type", evt.Type, "session", evt.SessionID, "error", err)
	}
}
