package session

import (
	"fmt"
	"strconv"
	"sync"
)

// activityState tracks the session's last activity label and open-stream flag
// for the runtime-status snapshot (specs/domains/session-lifecycle.md); shared
// across emitter copies like tokenState. Locking: activity.mu is a leaf lock
// acquired under e.mu — never the reverse — so the order cannot deadlock.
type activityState struct {
	mu        sync.Mutex
	last      string // last activity label; empty until the first tracked event
	streaming bool   // assistant_chunk seen without the closing assistant_done
}

// trackedStreaming states for updateActivity. zero means "leave unchanged".
const (
	streamingUnchanged = 0 // leave the flag untouched
	streamingOn        = 1 // assistant_chunk opened a stream
	streamingOff       = 2 // assistant_done closed it
)

// activityForEvent maps an event to (label, streamingState, tracked); the
// labels mirror the frontend's live handlers so a session snapshot renders
// the same text a live listener would have produced. tracked=false events
// leave the state untouched.
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
		// No confirmation card exists yet — the label must name the judge, not a
		// pending user response (specs/contracts/event-catalog.md).
		return "Safety judge evaluating...", streamingUnchanged, true
	case "tool_judge_finished":
		tool, _ := mapString(evt.Data, "tool")
		if tool == "" {
			return "", streamingUnchanged, false
		}
		// Holds on a strict-ALLOW (tool executing); a CONFIRM fallback overwrites
		// it via tool_confirm (specs/contracts/event-catalog.md).
		return fmt.Sprintf("Running tool: %s...", tool), streamingUnchanged, true
	case "tool_confirm":
		// The agent goroutine blocks on the user's decision — the honest label for
		// the whole wait (specs/contracts/event-catalog.md, tool_confirm).
		return "Awaiting confirmation...", streamingUnchanged, true
	default:
		return "", streamingUnchanged, false
	}
}

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

// LastActivity returns the session's last activity label; terminal
// transitions are emitted by the Manager outside the EventEmitter, so the
// value is authoritative only while the session reports Active=true.
func (e *EventEmitter) LastActivity() string {
	e.activity.mu.Lock()
	defer e.activity.mu.Unlock()
	return e.activity.last
}

// StreamingActive reports an open assistant stream (assistant_chunk without
// the closing assistant_done); feeds the runtime-status stale-stream heal
// (specs/domains/session-lifecycle.md).
func (e *EventEmitter) StreamingActive() bool {
	e.activity.mu.Lock()
	defer e.activity.mu.Unlock()
	return e.activity.streaming
}

// TokenSnapshot is the live, in-memory view of a session's token/fill state;
// GetSessionTokens overlays it over the persisted session row
// (specs/domains/session-lifecycle.md).
type TokenSnapshot struct {
	InputTokens  int
	OutputTokens int
	UsedTokens   int
	MaxTokens    int
	Model        string
	Family       string
	FillPercent  float64
}

// TokenSnapshot returns the cached session-level token/fill state; fresh
// values exist only after the first LLM call in this process (zero values
// otherwise — the caller falls back to the store).
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
