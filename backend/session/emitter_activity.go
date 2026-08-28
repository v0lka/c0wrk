package session

import (
	"fmt"
	"strconv"
	"sync"
)

// activityState tracks the session's last user-facing activity label and the
// open-stream flag so GetSessionRuntimeStatus can report them for a session
// the frontend stopped listening to (project/mode switch). It is shared across
// emitter copies (WithPlanStepID/WithRetryAttempt) like tokenState: conductor
// and subagent emitters all update the same session-level view.
//
// Locking: activity.mu is a leaf lock — it is acquired from emitEvent while
// e.mu is held, and readers (LastActivity/StreamingActive) acquire it alone.
// Nothing acquires e.mu while holding activity.mu, so the e.mu → activity.mu
// order cannot deadlock.
type activityState struct {
	mu        sync.Mutex
	last      string // last activity label; empty until the first tracked event
	streaming bool   // assistant_chunk seen without the closing assistant_done
}

// trackedStreaming states for updateActivity. zero means "leave unchanged".
const (
	streamingUnchanged = 0
	streamingOn        = 1
	streamingOff       = 2
)

// activityForEvent maps an event to (label, streamingState, tracked). The
// labels mirror the frontend's live handlers (useLifecycleEvents /
// useChatEvents / useSubagentEvents / usePlanEvents) so a session snapshot
// renders the same text a live listener would have produced. Events that
// don't map to an activity label (tool_call, context_fill, blackboard, ...)
// return tracked=false and leave the state untouched.
func activityForEvent(evt Event) (label string, streaming int8, tracked bool) {
	switch evt.Type {
	case "routing":
		return "Analyzing request...", streamingUnchanged, true
	case "step_start":
		return "Thinking...", streamingUnchanged, true
	case "thought":
		return "Reasoning...", streamingUnchanged, true
	case "reflection":
		return "Reflecting on results...", streamingUnchanged, true
	case "subagent_launch":
		return "Launching sub-agent...", streamingUnchanged, true
	case "plan_generated":
		return "Executing plan...", streamingUnchanged, true
	case "finishing":
		return "Finishing...", streamingUnchanged, true
	case "assistant_chunk":
		return "Generating response...", streamingOn, true
	case "assistant_done":
		return "", streamingOff, true
	case "service":
		content, _ := mapString(evt.Data, "content")
		if content == "" {
			return "", streamingUnchanged, false
		}
		return content, streamingUnchanged, true
	case "retry":
		attempt, _ := mapInt(evt.Data, "attempt")
		maxAttempts, _ := mapInt(evt.Data, "max_attempts")
		return fmt.Sprintf("Retrying (attempt %d/%d)...", attempt, maxAttempts), streamingUnchanged, true
	case "step_retry":
		attempt, _ := mapInt(evt.Data, "attempt")
		maxAttempts, _ := mapInt(evt.Data, "max_attempts")
		return fmt.Sprintf("Retrying step %d/%d...", attempt, maxAttempts), streamingUnchanged, true
	case "plan_step_start":
		stepID, _ := mapString(evt.Data, "step_id")
		if stepID == "" {
			return "", streamingUnchanged, false
		}
		return "Executing step " + stepID + "...", streamingUnchanged, true
	case "tool_judge_started":
		// Strict judge (Smart Approve) evaluation is in flight — at this point
		// NO confirmation card exists yet, so the label must describe the
		// judge, not a (nonexistent) pending user response.
		return "Safety judge evaluating...", streamingUnchanged, true
	case "tool_judge_finished":
		tool, _ := mapString(evt.Data, "tool")
		if tool == "" {
			return "", streamingUnchanged, false
		}
		// Mirrors the frontend's live tool_call label: on a strict-ALLOW verdict
		// the tool is executing, so the label holds; on a CONFIRM fallback the
		// tracked tool_confirm event below overwrites it within milliseconds.
		return fmt.Sprintf("Running tool: %s...", tool), streamingUnchanged, true
	case "tool_confirm":
		// Emitted by the desktop confirm callback through the session emitter:
		// the agent goroutine is blocked on the user's decision, so this is the
		// honest label for however long the wait lasts. Mirrors the frontend's
		// live handleToolConfirmEvent.
		return "Awaiting confirmation...", streamingUnchanged, true
	default:
		return "", streamingUnchanged, false
	}
}

// updateActivity applies an event's activity mapping to the shared state.
// Called from emitEvent (under e.mu); safe for concurrent use.
func (a *activityState) updateActivity(evt Event) {
	label, streaming, tracked := activityForEvent(evt)
	if !tracked {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if label != "" {
		a.last = label
	}
	switch streaming {
	case streamingOn:
		a.streaming = true
	case streamingOff:
		a.streaming = false
	}
}

// mapString reads a string field from the map[string]any / map[string]string
// shapes used by emitter payloads. Missing keys or other shapes return "".
func mapString(data any, key string) (string, bool) {
	switch m := data.(type) {
	case map[string]any:
		if v, ok := m[key].(string); ok {
			return v, true
		}
	case map[string]string:
		if v, ok := m[key]; ok {
			return v, true
		}
	}
	return "", false
}

// mapInt reads an int field from the map[string]any / map[string]int shapes
// used by emitter payloads. Missing keys or other shapes return 0.
func mapInt(data any, key string) (int, bool) {
	switch m := data.(type) {
	case map[string]any:
		switch v := m[key].(type) {
		case int:
			return v, true
		case float64:
			return int(v), true
		case string:
			if n, err := strconv.Atoi(v); err == nil {
				return n, true
			}
		}
	case map[string]int:
		if v, ok := m[key]; ok {
			return v, true
		}
	}
	return 0, false
}

// LastActivity returns the session's last activity label ("Thinking...",
// "Routing request...", "Generating response...", ...). The value is
// meaningful while the session's task is active; terminal transitions
// (task_complete / error / cancellation) are emitted by the Manager outside
// the EventEmitter, so consumers should treat it as authoritative only when
// the session reports Active=true.
func (e *EventEmitter) LastActivity() string {
	e.activity.mu.Lock()
	defer e.activity.mu.Unlock()
	return e.activity.last
}

// StreamingActive reports whether an assistant stream is currently open in
// this session (assistant_chunk emitted without the closing assistant_done).
// Used by the runtime-status snapshot to let the frontend clear stale frozen
// streaming text after a switch back to a session it stopped listening to.
func (e *EventEmitter) StreamingActive() bool {
	e.activity.mu.Lock()
	defer e.activity.mu.Unlock()
	return e.activity.streaming
}

// TokenSnapshot is the live, in-memory view of a session's token/fill state —
// the same values the emitter broadcasts via session_tokens / context_fill.
// Unlike the persisted session row it carries used/max tokens, which the
// status bar renders as "N of M" after a switch back to a session.
type TokenSnapshot struct {
	InputTokens  int
	OutputTokens int
	UsedTokens   int
	MaxTokens    int
	Model        string
	Family       string
	FillPercent  float64
}

// TokenSnapshot returns the cached session-level token and context-fill
// state. Fresh values exist only after the first LLM call of a task in this
// process; zero values otherwise (the caller falls back to the store).
func (e *EventEmitter) TokenSnapshot() TokenSnapshot {
	e.tokens.mu.Lock()
	defer e.tokens.mu.Unlock()
	return TokenSnapshot{
		InputTokens:  e.tokens.sessionInputTokens,
		OutputTokens: e.tokens.sessionOutputTokens,
		UsedTokens:   e.tokens.lastUsedTokens,
		MaxTokens:    e.tokens.lastMaxTokens,
		Model:        e.tokens.lastModel,
		Family:       e.tokens.lastFamily,
		FillPercent:  e.tokens.lastFillPercent,
	}
}
