// Package session provides session-scoped event emission for the desktop UI.
package session

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/user/agent/core"
)

// Event represents a structured event emitted during agent execution.
type Event struct {
	SessionID string `json:"session_id"`
	Type      string `json:"type"`
	Data      any    `json:"data"`
}

// tokenState holds shared token accumulation state across emitter copies
// (e.g. WithPlanStepID) so all agents within a session contribute to the same totals.
type tokenState struct {
	mu                  sync.Mutex
	sessionInputTokens  int
	sessionOutputTokens int
	lastFillPercent     float64
	lastUsedTokens      int
	lastMaxTokens       int
	lastFillStatus      string
	tokenPersist        func(inputTokens, outputTokens int) // callback to persist tokens
}

// EventEmitter implements core.Emitter and routes events to a callback function.
type EventEmitter struct {
	sessionID  string
	emit       func(Event)
	mu         sync.Mutex
	planStepID string // if set, injected into event Data for plan-step scoping

	// Plan progress tracking (guarded by mu)
	planTotalSteps    int
	planCompletedSet  map[string]bool // set of completed step IDs
	planCurrentStepID string          // currently running step ID

	// Streaming content accumulation (guarded by mu)
	streamAccumulated strings.Builder

	// Shared token accumulation (shared across WithPlanStepID copies)
	tokens *tokenState
}

// NewEventEmitter creates a new EventEmitter for a session.
func NewEventEmitter(sessionID string, emit func(Event)) *EventEmitter {
	return &EventEmitter{
		sessionID: sessionID,
		emit:      emit,
		tokens:    &tokenState{lastFillStatus: "ok"},
	}
}

// SetTokenPersist sets a callback that is invoked with cumulative session token
// totals each time TokensUsed fires. Use this to persist tokens to the store
// without introducing a direct store dependency in the emitter.
func (e *EventEmitter) SetTokenPersist(fn func(inputTokens, outputTokens int)) {
	e.tokens.mu.Lock()
	defer e.tokens.mu.Unlock()
	e.tokens.tokenPersist = fn
}

// WithPlanStepID returns a shallow copy of the emitter with planStepID set.
// Events emitted by the copy will include "plan_step_id" in their Data map.
func (e *EventEmitter) WithPlanStepID(id string) core.Emitter {
	slog.Debug("emitter: creating plan-step-scoped emitter", "sessionID", e.sessionID, "planStepID", id)
	return &EventEmitter{
		sessionID:  e.sessionID,
		emit:       e.emit,
		planStepID: id,
		tokens:     e.tokens, // share token accumulation state across copies
	}
}

// ensure EventEmitter implements core.Emitter and core.PlanStepScopable at compile time.
var (
	_ core.Emitter          = (*EventEmitter)(nil)
	_ core.PlanStepScopable = (*EventEmitter)(nil)
)

// emitEvent is a helper that emits an event, injecting plan_step_id if set.
func (e *EventEmitter) emitEvent(evt Event) {
	slog.Debug("emitter: dispatching event", "type", evt.Type, "sessionID", e.sessionID, "planStepID", e.planStepID)
	if e.planStepID != "" {
		// Inject plan_step_id into Data if it's a map[string]any
		if data, ok := evt.Data.(map[string]any); ok {
			data["plan_step_id"] = e.planStepID
		}
	}
	e.emit(evt)
}

// Routing emits a routing decision event.
func (e *EventEmitter) Routing(mode, domain, complexity string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "routing",
		Data: map[string]any{
			"mode":       mode,
			"domain":     domain,
			"complexity": complexity,
		},
	})
}

// PlanGenerated emits a plan generation event with initial progress info.
func (e *EventEmitter) PlanGenerated(stepCount int, steps []core.PlanStepEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Initialize plan progress tracking
	e.planTotalSteps = stepCount
	e.planCompletedSet = make(map[string]bool, stepCount)
	e.planCurrentStepID = ""
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "plan_generated",
		Data: map[string]any{
			"step_count":         stepCount,
			"steps":              steps,
			"progress":           0.0,
			"current_step_index": -1,
			"completed_count":    0,
			"total_count":        stepCount,
		},
	})
}

// PlanStepStart emits a plan step start event with progress info.
func (e *EventEmitter) PlanStepStart(stepID, description string) {
	slog.Debug("emitter: plan step start", "sessionID", e.sessionID, "stepID", stepID, "description", description)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.planCurrentStepID = stepID
	completedCount := len(e.planCompletedSet)
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "plan_step_start",
		Data: map[string]any{
			"step_id":            stepID,
			"description":        description,
			"progress":           e.computeProgress(completedCount),
			"current_step_index": completedCount, // 0-based index of current step
			"completed_count":    completedCount,
			"total_count":        e.planTotalSteps,
		},
	})
}

// PlanStepComplete emits a plan step completion event with updated progress.
func (e *EventEmitter) PlanStepComplete(stepID string, success bool, duration time.Duration) {
	slog.Debug("emitter: plan step complete", "sessionID", e.sessionID, "stepID", stepID, "success", success, "duration", duration)
	e.mu.Lock()
	defer e.mu.Unlock()
	if success {
		if e.planCompletedSet == nil {
			e.planCompletedSet = make(map[string]bool)
		}
		e.planCompletedSet[stepID] = true
	}
	if e.planCurrentStepID == stepID {
		e.planCurrentStepID = ""
	}
	completedCount := len(e.planCompletedSet)
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "plan_step_complete",
		Data: map[string]any{
			"step_id":            stepID,
			"success":            success,
			"duration":           duration.Milliseconds(),
			"progress":           e.computeProgress(completedCount),
			"current_step_index": -1,
			"completed_count":    completedCount,
			"total_count":        e.planTotalSteps,
		},
	})
}

// computeProgress returns plan progress as a float64 in [0.0, 1.0].
// Must be called with e.mu held.
func (e *EventEmitter) computeProgress(completedCount int) float64 {
	if e.planTotalSteps <= 0 {
		return 0.0
	}
	return float64(completedCount) / float64(e.planTotalSteps)
}

// StepStart emits a step start event.
func (e *EventEmitter) StepStart(stepNum int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "step_start",
		Data: map[string]any{
			"step_num": stepNum,
		},
	})
}

// Thought emits a thought event for LLM reasoning.
func (e *EventEmitter) Thought(stepNum int, content, reasoning string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "thought",
		Data: map[string]any{
			"step_num":  stepNum,
			"content":   content,
			"reasoning": reasoning,
		},
	})
}

// ToolCall emits a tool call event.
// If argsPreview is valid JSON, a pre-parsed map is included as "parsed_args"
// so the frontend doesn't need to JSON.parse() at render time.
func (e *EventEmitter) ToolCall(stepNum int, toolName, argsPreview string) {
	slog.Debug("emitter: tool call", "sessionID", e.sessionID, "tool", toolName, "step", stepNum)
	e.mu.Lock()
	defer e.mu.Unlock()
	data := map[string]any{
		"step": stepNum,
		"tool": toolName,
		"args": argsPreview,
	}
	// Pre-parse JSON arguments for the frontend
	if trimmed := strings.TrimSpace(argsPreview); trimmed != "" && trimmed[0] == '{' {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(argsPreview), &parsed); err == nil {
			data["parsed_args"] = parsed
		}
	}
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "tool_call",
		Data:      data,
	})
}

// ToolResult emits a tool result event.
func (e *EventEmitter) ToolResult(stepNum, resultLen int, preview string) {
	slog.Debug("emitter: tool result", "sessionID", e.sessionID, "step", stepNum, "resultLen", resultLen)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "tool_result",
		Data: map[string]any{
			"step":       stepNum,
			"result_len": resultLen,
			"result":     preview,
		},
	})
}

// StepComplete emits a step completion event.
func (e *EventEmitter) StepComplete(stepNum int, duration time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "step_complete",
		Data: map[string]any{
			"step_num": stepNum,
			"duration": duration.Milliseconds(),
		},
	})
}

// SubAgentLaunch emits a subagent launch event.
func (e *EventEmitter) SubAgentLaunch(stepID, description string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "subagent_launch",
		Data: map[string]string{
			"step_id":     stepID,
			"description": description,
		},
	})
}

// SubAgentComplete emits a subagent completion event.
func (e *EventEmitter) SubAgentComplete(stepID string, success bool, duration time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "subagent_complete",
		Data: map[string]any{
			"step_id":  stepID,
			"success":  success,
			"duration": duration.Milliseconds(),
		},
	})
}

// Reflection emits a reflection event.
func (e *EventEmitter) Reflection(summary string, insights []string, attempt, maxAttempts int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "reflection",
		Data: map[string]any{
			"summary":      summary,
			"insights":     insights,
			"attempt":      attempt,
			"max_attempts": maxAttempts,
		},
	})
}

// Retry emits a retry event.
func (e *EventEmitter) Retry(attempt, maxAttempts int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "retry",
		Data: map[string]int{
			"attempt":      attempt,
			"max_attempts": maxAttempts,
		},
	})
}

// StepRetry emits a step retry event.
func (e *EventEmitter) StepRetry(stepID string, attempt, maxAttempts int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "step_retry",
		Data: map[string]any{
			"step_id":      stepID,
			"attempt":      attempt,
			"max_attempts": maxAttempts,
		},
	})
}

// AssistantChunk emits an assistant response chunk for streaming.
// It accumulates all chunks and emits both the delta and the full accumulated
// content so the frontend can simply SET the content instead of appending.
func (e *EventEmitter) AssistantChunk(content string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.streamAccumulated.WriteString(content)
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "assistant_chunk",
		Data: map[string]any{
			"content":     content,
			"accumulated": e.streamAccumulated.String(),
		},
	})
}

// AssistantDone emits an assistant response completion event and resets the accumulator.
// Token accumulation is handled separately by TokensUsed, which is called after every
// LLM response regardless of suppression flags.
func (e *EventEmitter) AssistantDone(fullContent string, inputTokens, outputTokens int) {
	slog.Debug("emitter: completion (assistant done)", "sessionID", e.sessionID, "inputTokens", inputTokens, "outputTokens", outputTokens)
	// Read current session totals (accumulated by TokensUsed).
	e.tokens.mu.Lock()
	totalIn := e.tokens.sessionInputTokens
	totalOut := e.tokens.sessionOutputTokens
	e.tokens.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	// Reset the accumulator for the next streaming session
	e.streamAccumulated.Reset()

	// Emit session-level token totals so frontend can update even for subagents
	// that don't trigger context_fill. Emitted before assistant_done so the
	// frontend has updated totals when it processes the completion event.
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "session_tokens",
		Data: SessionTokensEventData{
			SessionInputTokens:  totalIn,
			SessionOutputTokens: totalOut,
		},
	})

	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "assistant_done",
		Data: map[string]any{
			"content":       fullContent,
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	})
}

// TokensUsed accumulates session-wide token totals, persists them via callback,
// and emits a "session_tokens" event. This is called after every LLM response
// regardless of the suppressAssistantEvents flag, ensuring all tokens (including
// from plan-step subagents) are counted.
func (e *EventEmitter) TokensUsed(inputTokens, outputTokens int) {
	slog.Debug("emitter: token usage", "sessionID", e.sessionID, "inputTokens", inputTokens, "outputTokens", outputTokens)
	e.tokens.mu.Lock()
	e.tokens.sessionInputTokens += inputTokens
	e.tokens.sessionOutputTokens += outputTokens
	totalIn := e.tokens.sessionInputTokens
	totalOut := e.tokens.sessionOutputTokens
	persist := e.tokens.tokenPersist
	e.tokens.mu.Unlock()

	if persist != nil {
		persist(totalIn, totalOut)
	}

	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "session_tokens",
		Data: SessionTokensEventData{
			SessionInputTokens:  totalIn,
			SessionOutputTokens: totalOut,
		},
	})
}

// ContextFill emits a context fill status event, enriched with session-level token totals.
func (e *EventEmitter) ContextFill(fillPercent float64, usedTokens, maxTokens int, status, stepID string) {
	// Cache fill state and read session totals atomically.
	e.tokens.mu.Lock()
	e.tokens.lastFillPercent = fillPercent
	e.tokens.lastUsedTokens = usedTokens
	e.tokens.lastMaxTokens = maxTokens
	e.tokens.lastFillStatus = status
	totalIn := e.tokens.sessionInputTokens
	totalOut := e.tokens.sessionOutputTokens
	e.tokens.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "context_fill",
		Data: ContextFillEventData{
			FillPercent:         fillPercent,
			UsedTokens:          usedTokens,
			MaxTokens:           maxTokens,
			Status:              status,
			PlanStepID:          stepID,
			SessionInputTokens:  totalIn,
			SessionOutputTokens: totalOut,
		},
	})
}

// Service emits a general service message without metadata.
func (e *EventEmitter) Service(content string) {
	slog.Debug("emitter: service message", "sessionID", e.sessionID)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "service",
		Data: map[string]any{
			"content": content,
		},
	})
}

// ServiceWithMeta emits a service message with metadata for frontend filtering.
func (e *EventEmitter) ServiceWithMeta(content string, meta map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	data := map[string]any{
		"content": content,
	}
	for k, v := range meta {
		data[k] = v
	}
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "service",
		Data:      data,
	})
}

// ExecutorDiagnostic logs an internal executor diagnostic at DEBUG level.
// These are internal diagnostics, not user-facing events.
func (e *EventEmitter) ExecutorDiagnostic(stepNum int, event string, details map[string]any) {
	slog.Debug("emitter: executor diagnostic",
		"stepNum", stepNum,
		"event", event,
		"details", details,
	)
}

// ReplanFailed logs a failed replan attempt.
func (e *EventEmitter) ReplanFailed(err error) {
	slog.Debug("emitter: replan failed", "sessionID", e.sessionID, "error", err)
}

// FileRollbackError logs a file rollback failure for a plan step.
func (e *EventEmitter) FileRollbackError(stepID string, err error) {
	slog.Warn("emitter: file rollback error", "sessionID", e.sessionID, "stepID", stepID, "error", err)
}

// SessionTokenTotals returns the accumulated session-wide input and output token counts.
func (e *EventEmitter) SessionTokenTotals() (inputTokens, outputTokens int) {
	e.tokens.mu.Lock()
	defer e.tokens.mu.Unlock()
	return e.tokens.sessionInputTokens, e.tokens.sessionOutputTokens
}
