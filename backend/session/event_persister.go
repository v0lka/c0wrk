package session

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// EventPersister persists chat-visible events to the session store (SQLite).
// It is decoupled from the UI — the desktop layer is responsible for emitting
// Wails events separately.
type EventPersister struct {
	store  SessionStore
	mu     sync.RWMutex
	logger *slog.Logger

	// assistantMu guards lastAssistantContent: per-session tracking of the most
	// recent assistant_done content. Used to dedup task_complete against the
	// streamed answer in the implicit text-only finish path (where the executor
	// emits assistant_done AND sets Output), so the final answer is not
	// persisted — and therefore not rendered — twice on session reload.
	assistantMu          sync.Mutex
	lastAssistantContent map[string]string
}

// NewEventPersister creates a new EventPersister backed by the given store.
// If store is nil, Persist is a no-op.
func NewEventPersister(store SessionStore) *EventPersister {
	return &EventPersister{store: store, lastAssistantContent: make(map[string]string)}
}

// log returns the persister's logger, falling back to slog.Default().
func (p *EventPersister) log() *slog.Logger {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.logger != nil {
		return p.logger
	}
	return slog.Default()
}

// SetLogger sets the logger for the event persister.
func (p *EventPersister) SetLogger(l *slog.Logger) {
	p.mu.Lock()
	defer p.mu.Unlock()
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

	// Reset per-session assistant tracking on a new user message or session
	// deletion so the task_complete dedup (below) is scoped to the current
	// task only and cannot false-positive against a prior task's streamed
	// answer. session_deleted also prevents unbounded growth of the
	// lastAssistantContent map in the long-lived persister singleton.
	if evt.Type == "message_received" || evt.Type == "session_deleted" {
		p.assistantMu.Lock()
		delete(p.lastAssistantContent, evt.SessionID)
		p.assistantMu.Unlock()
	}

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
		// Track the streamed content so task_complete can dedup against it.
		p.assistantMu.Lock()
		p.lastAssistantContent[evt.SessionID] = content
		p.assistantMu.Unlock()
	case "task_complete":
		var output string
		switch d := evt.Data.(type) {
		case TaskCompleteData:
			output = d.Output
		case map[string]any:
			if o, ok := d["output"].(string); ok {
				output = o
			} else {
				return // no output field — nothing to persist
			}
		}
		// Dedup: in the implicit text-only finish path the executor streams
		// the answer via assistant_done (persisted above) AND sets it as
		// Output, so task_complete would otherwise persist a duplicate
		// assistant row — rendering the final answer twice on reload. Skip
		// when the output matches the last streamed assistant content.
		if output != "" {
			p.assistantMu.Lock()
			last := p.lastAssistantContent[evt.SessionID]
			p.assistantMu.Unlock()
			if output == last {
				return
			}
		}
		role = "assistant"
		content = output
		// Guard against empty output: a task that completes with
		// no content must still persist a message so that session
		// continuations can see the full conversation history.
		// Without this, an empty-output completion is silently
		// dropped from the message store, breaking continuation
		// context.
		if content == "" {
			content = "[Task completed]"
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
	case "skills_activated":
		role = "status"
	case "step_todo_update":
		role = "step_todo_update"
	case "session_tokens",
		"assistant_chunk", "context_fill", "context_compaction", "finishing",
		"memory_read", "message_received", "blackboard_updated",
		"tool_judge_response", "session_created", "session_deleted",
		"session_renamed",
		"goal_status", "goal_progress":
		return // transient — no persistence needed
	case "plan_review_ready":
		role = "plan_review"
	case "goal_proposal":
		// Persist the goal-proposal pending action so it reappears via
		// GetPendingActions after a reload (the agent remains blocked until
		// the user confirms/cancels via the goal_proposal_response flow).
		role = "goal_proposal"
	default:
		// Unknown event types are dropped from history. Log so schema drift
		// between emitters and the persister is visible instead of silent.
		p.log().Warn("skipping unknown event type in persister", "type", evt.Type, "session", evt.SessionID)
		return
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
	// Exception: for "thought" events, empty content is valid (reasoning lives in metadata).
	if content == "" && role != "thought" {
		content = string(metadata)
	}

	if err := p.store.SaveMessage(context.Background(), ChatMessage{
		SessionID: evt.SessionID,
		Role:      role,
		Content:   content,
		Metadata:  metadata,
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		p.log().Error("failed to persist event message", "type", evt.Type, "session", evt.SessionID, "error", err)
	}
}
