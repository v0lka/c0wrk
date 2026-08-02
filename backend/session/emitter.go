// Package session provides session-scoped event emission for the desktop UI.
package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/orchestration"
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
	lastModel           string // last model used
	lastFamily          string // last model family used
	// displayContextWindow is the model's advertised context-window size, injected
	// by the orchestrator via SetDisplayContextWindow. When > 0, ContextFill
	// recomputes fill relative to this real window instead of the executor's
	// internal "effective max" (window − output limit − safety margin).
	displayContextWindow int
	// lastEffectiveMax caches the executor's effective max from the most recent
	// ContextFill so ContextCompaction can scale its before/after percentages
	// from the internal effective-max basis to the display (real window) basis.
	lastEffectiveMax int
	tokenPersist     func(inputTokens, outputTokens int, model, family string, fillPercent float64) // callback to persist tokens
}

// toolCallIDGen holds a shared monotonic counter for generating unique tool_call_id values
// across all emitter copies (plan-step, retry scopes) within a session.
type toolCallIDGen struct {
	counter atomic.Int64
	epoch   int64 // millisecond timestamp at creation, ensures uniqueness across session reloads

	// toolCallSink, when set, is invoked after each ToolCall with the tool name
	// and the generated tool_call_id. The session Manager registers it so the
	// desktop confirmation callback can attach the matching tool_call_id to the
	// tool_confirm payload — enabling precise tool_call ↔ tool_confirm
	// correlation instead of fragile tool-name matching. Guarded by mu.
	mu           sync.RWMutex
	toolCallSink func(tool, toolCallID string)
}

func (g *toolCallIDGen) next() string {
	n := g.counter.Add(1)
	return fmt.Sprintf("tc_%d_%d", g.epoch, n)
}

// setSink registers the post-ToolCall callback (write-once at session creation).
func (g *toolCallIDGen) setSink(fn func(tool, toolCallID string)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.toolCallSink = fn
}

// sink returns the registered callback, or nil if none is set.
func (g *toolCallIDGen) sink() func(tool, toolCallID string) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.toolCallSink
}

// EventEmitter implements core.Emitter and routes events to a callback function.
//
// Lock ordering: tokens.mu must be acquired BEFORE e.mu to avoid deadlocks.
// Methods that need both (e.g. AssistantDone, EmitSessionTokens, ContextFill)
// always lock tokens.mu first, then e.mu.
type EventEmitter struct {
	sessionID    string
	emit         func(Event)
	mu           sync.Mutex
	planStepID   string // if set, injected into event Data for plan-step scoping
	retryAttempt int    // if > 0, injected into event Data for retry disambiguation

	// Plan progress tracking (guarded by mu)
	planTotalSteps    int
	planCompletedSet  map[string]bool // set of completed step IDs
	planStartedSet    map[string]bool // set of step IDs whose PlanStepStart was already emitted
	planCurrentStepID string          // currently running step ID

	// Streaming content accumulation (guarded by mu)
	streamAccumulated strings.Builder

	// Shared token accumulation (shared across WithPlanStepID copies)
	tokens *tokenState

	// Tool call ID generation (shared across WithPlanStepID copies)
	toolCallIDs *toolCallIDGen

	// Per-copy mapping of (stepNum:callIdx) -> tool_call_id
	// Each scoped emitter copy gets its own map (not shared),
	// since each executor starts stepNum from 1.
	localToolIDs map[string]string

	// isSessionRoot marks the conductor's own emitter. Scoped copies created by
	// WithPlanStepID/WithRetryAttempt (subagents) are NOT root. Only the root
	// emitter updates the session-level fill cache so a subagent's own context
	// fill never overwrites the conductor's fill shown in the status bar.
	isSessionRoot bool

	// attachmentNameResolver resolves a read_attachment attachment_id to its
	// original file name so the tool-call event (and thus the persisted
	// message metadata) carries a human-readable name. Wired per-task by the
	// orchestrator from the blackboard; nil when no task is active or the
	// emitter predates resolver support. Guarded by mu.
	attachmentNameResolver func(string) string

	logger *slog.Logger
}

// NewEventEmitter creates a new EventEmitter for a session.
func NewEventEmitter(sessionID string, emit func(Event)) *EventEmitter {
	return &EventEmitter{
		sessionID:     sessionID,
		emit:          emit,
		tokens:        &tokenState{lastFillStatus: "ok"},
		toolCallIDs:   &toolCallIDGen{epoch: time.Now().UnixMilli()},
		isSessionRoot: true,
	}
}

// log returns the emitter's logger, falling back to slog.Default().
func (e *EventEmitter) log() *slog.Logger {
	if e.logger != nil {
		return e.logger
	}
	return slog.Default()
}

// SetTokenPersist sets a callback that is invoked with cumulative session token
// totals each time the UsageTracker observer fires. Use this to persist tokens to the store
// without introducing a direct store dependency in the emitter.
func (e *EventEmitter) SetTokenPersist(fn func(inputTokens, outputTokens int, model, family string, fillPercent float64)) {
	e.tokens.mu.Lock()
	defer e.tokens.mu.Unlock()
	e.tokens.tokenPersist = fn
}

// SetDisplayContextWindow injects the model's advertised context-window size
// so ContextFill presents fill relative to the real window rather than the
// executor's internal "effective max" (window − output limit − safety margin).
// The orchestrator calls this after resolving the model meta, before the first
// context_fill. A value <= 0 clears the override and falls back to the
// executor-reported max.
func (e *EventEmitter) SetDisplayContextWindow(window int) {
	e.tokens.mu.Lock()
	defer e.tokens.mu.Unlock()
	e.tokens.displayContextWindow = window
}

// SetToolCallIDSink registers a callback invoked after each ToolCall with the
// tool name and the generated tool_call_id. The session Manager sets it so the
// desktop-layer confirmation callback can attach the matching tool_call_id to
// the tool_confirm payload, enabling precise tool_call ↔ tool_confirm
// correlation on the frontend (instead of matching by tool name, which is
// ambiguous when two calls share a name). The sink lives on the shared gen, so
// scoped copies (WithPlanStepID/WithRetryAttempt) report to the same store.
func (e *EventEmitter) SetToolCallIDSink(fn func(tool, toolCallID string)) {
	e.toolCallIDs.setSink(fn)
}

// SetAttachmentNameResolver wires a resolver that maps a read_attachment
// attachment_id to its original file name, used to enrich read_attachment
// tool-call events (and their persisted metadata) so cards render the file
// name even after restart. Wired per-task by the orchestrator from the
// blackboard. Scoped copies (WithPlanStepID/WithRetryAttempt) inherit the same
// resolver so subagents/retries resolve names against the shared blackboard.
func (e *EventEmitter) SetAttachmentNameResolver(resolve func(attachmentID string) string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.attachmentNameResolver = resolve
}

// WithPlanStepID returns a shallow copy of the emitter with planStepID set.
// Events emitted by the copy will include "plan_step_id" in their Data map.
func (e *EventEmitter) WithPlanStepID(id string) core.Emitter {
	e.log().Debug("emitter: creating plan-step-scoped emitter", "sessionID", e.sessionID, "planStepID", id)
	return &EventEmitter{
		sessionID:     e.sessionID,
		emit:          e.emit,
		planStepID:    id,
		retryAttempt:  e.retryAttempt, // preserve retry attempt across copies
		tokens:        e.tokens,       // share token accumulation state across copies
		toolCallIDs:   e.toolCallIDs,  // share tool call ID counter across copies
		logger:        e.logger,       // propagate logger to copies
		isSessionRoot: false,          // scoped copies are never the session root
		// Inherit the task's attachment-name resolver so subagent/retry
		// read_attachment calls resolve names against the shared blackboard.
		attachmentNameResolver: e.attachmentNameResolver,
	}
}

// WithRetryAttempt returns a shallow copy of the emitter with retryAttempt set.
// Events emitted by the copy will include "retry_attempt" in their Data map when > 0.
func (e *EventEmitter) WithRetryAttempt(attempt int) core.Emitter {
	e.log().Debug("emitter: creating retry-scoped emitter", "sessionID", e.sessionID, "retryAttempt", attempt)
	return &EventEmitter{
		sessionID:     e.sessionID,
		emit:          e.emit,
		planStepID:    e.planStepID,
		retryAttempt:  attempt,
		tokens:        e.tokens,      // share token accumulation state across copies
		toolCallIDs:   e.toolCallIDs, // share tool call ID counter across copies
		logger:        e.logger,      // propagate logger to copies
		isSessionRoot: false,         // scoped copies are never the session root
		// Inherit the task's attachment-name resolver (see WithPlanStepID).
		attachmentNameResolver: e.attachmentNameResolver,
	}
}

// SetCurrentStepID dynamically updates the plan_step_id injected into
// subsequent events emitted by this receiver. Unlike WithPlanStepID (which
// returns a scoped copy with a fixed planStepID), SetCurrentStepID mutates
// the receiver in place — use it to track the "current step" during inline
// Conductor execution, where a single emitter instance serves the whole
// ReAct loop and the active step changes via update_checklist /
// declare_step_complete tool calls. Pass an empty string to clear the scope.
//
// Scoped copies created by WithPlanStepID have their own planStepID field
// and are unaffected by calls to this method on the original emitter.
func (e *EventEmitter) SetCurrentStepID(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.planStepID = id
}

// ensure EventEmitter implements core.Emitter, core.PlanStepScopable, and core.RetryAttemptScopable at compile time.
var (
	_ core.Emitter              = (*EventEmitter)(nil)
	_ core.PlanStepScopable     = (*EventEmitter)(nil)
	_ core.RetryAttemptScopable = (*EventEmitter)(nil)
)

// emitEvent is a helper that emits an event, injecting plan_step_id and retry_attempt if set.
func (e *EventEmitter) emitEvent(evt Event) {
	e.log().Debug("emitter: dispatching event", "type", evt.Type, "sessionID", e.sessionID, "planStepID", e.planStepID)
	if data, ok := evt.Data.(map[string]any); ok {
		if e.planStepID != "" {
			data["plan_step_id"] = e.planStepID
		}
		if e.retryAttempt > 0 {
			data["retry_attempt"] = e.retryAttempt
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
func (e *EventEmitter) PlanGenerated(stepCount int, steps []orchestration.PlanStepEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Initialize plan progress tracking
	e.planTotalSteps = stepCount
	e.planCompletedSet = make(map[string]bool, stepCount)
	e.planStartedSet = make(map[string]bool, stepCount)
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
// Duplicate calls for the same step ID are suppressed — the event is emitted
// only the first time a step starts. This lets both the Conductor's inline
// todo callback and the subagent launcher call PlanStepStart without worrying
// about double-emission.
func (e *EventEmitter) PlanStepStart(stepID, description, summary string) {
	e.log().Debug("emitter: plan step start", "sessionID", e.sessionID, "stepID", stepID, "description", description, "summary", summary)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.planStartedSet != nil && e.planStartedSet[stepID] {
		// Already started — update current step but don't re-emit.
		e.planCurrentStepID = stepID
		return
	}
	if e.planStartedSet == nil {
		e.planStartedSet = make(map[string]bool)
	}
	e.planStartedSet[stepID] = true
	e.planCurrentStepID = stepID
	completedCount := len(e.planCompletedSet)
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "plan_step_start",
		Data: map[string]any{
			"step_id":            stepID,
			"description":        description,
			"summary":            summary,
			"progress":           e.computeProgress(completedCount),
			"current_step_index": completedCount, // 0-based index of current step
			"completed_count":    completedCount,
			"total_count":        e.planTotalSteps,
		},
	})
}

// PlanStepComplete emits a plan step completion event with updated progress.
func (e *EventEmitter) PlanStepComplete(stepID string, success bool, duration time.Duration, errMsg string) {
	e.log().Debug("emitter: plan step complete", "sessionID", e.sessionID, "stepID", stepID, "success", success, "duration", duration, "errMsg", errMsg)
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
	data := map[string]any{
		"step_id":            stepID,
		"success":            success,
		"duration":           duration.Milliseconds(),
		"progress":           e.computeProgress(completedCount),
		"current_step_index": -1,
		"completed_count":    completedCount,
		"total_count":        e.planTotalSteps,
	}
	if errMsg != "" {
		data["error"] = errMsg
	}
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "plan_step_complete",
		Data:      data,
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

// Finishing emits a finishing event when the agent calls the finish tool.
// The frontend uses this to show "Finishing..." status instead of "Running tool: finish".
func (e *EventEmitter) Finishing(stepNum int, summary string) {
	e.log().Debug("emitter: finishing", "sessionID", e.sessionID, "step", stepNum)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "finishing",
		Data: map[string]any{
			"step_num": stepNum,
			"summary":  summary,
		},
	})
}

// ToolCall emits a tool call event.
// If argsPreview is valid JSON, a pre-parsed map is included as "parsed_args"
// so the frontend doesn't need to JSON.parse() at render time.
func (e *EventEmitter) ToolCall(stepNum, callIdx int, toolName, argsPreview, source string) {
	e.log().Debug("emitter: tool call", "sessionID", e.sessionID, "tool", toolName, "step", stepNum, "callIdx", callIdx)
	e.mu.Lock()
	defer e.mu.Unlock()

	// Generate unique tool_call_id
	toolCallID := e.toolCallIDs.next()
	// Record the last tool_call_id for this session so the desktop
	// confirmation callback can correlate the (immediately-following)
	// tool_confirm with this exact tool_call event.
	if sink := e.toolCallIDs.sink(); sink != nil {
		sink(toolName, toolCallID)
	}
	key := fmt.Sprintf("%d:%d", stepNum, callIdx)
	if e.localToolIDs == nil {
		e.localToolIDs = make(map[string]string)
	}
	e.localToolIDs[key] = toolCallID

	data := map[string]any{
		"tool_call_id": toolCallID,
		"step":         stepNum,
		"call_idx":     callIdx,
		"tool":         toolName,
		"args":         argsPreview,
		"source":       source,
	}
	// Pre-parse JSON arguments for the frontend
	if trimmed := strings.TrimSpace(argsPreview); trimmed != "" && trimmed[0] == '{' {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(argsPreview), &parsed); err == nil {
			data["parsed_args"] = parsed
		}
	}
	// Enrich read_attachment calls with the attachment's original file name so
	// the persisted tool-call metadata (and thus the card) shows the name even
	// after an app restart, when the frontend's in-memory name cache is empty.
	if toolName == "read_attachment" && e.attachmentNameResolver != nil {
		if id := readAttachmentID(data); id != "" {
			if name := e.attachmentNameResolver(id); name != "" {
				data["attachment_name"] = name
			}
		}
	}
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "tool_call",
		Data:      data,
	})
}

// readAttachmentID extracts the attachment_id argument from a read_attachment
// tool-call event's data map — preferring the pre-parsed args and falling back
// to the raw args JSON. Returns "" when absent.
func readAttachmentID(data map[string]any) string {
	if parsed, ok := data["parsed_args"].(map[string]any); ok {
		if id, _ := parsed["attachment_id"].(string); id != "" {
			return id
		}
	}
	raw, ok := data["args"].(string)
	if !ok {
		return ""
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return ""
	}
	id, _ := m["attachment_id"].(string)
	return id
}

// ToolResult emits a tool result event.
func (e *EventEmitter) ToolResult(stepNum, callIdx, resultLen int, preview string, isError bool) {
	e.log().Debug("emitter: tool result", "sessionID", e.sessionID, "step", stepNum, "callIdx", callIdx, "resultLen", resultLen, "isError", isError)
	e.mu.Lock()
	defer e.mu.Unlock()

	// Look up tool_call_id from this executor's local map
	key := fmt.Sprintf("%d:%d", stepNum, callIdx)
	toolCallID := e.localToolIDs[key]

	data := map[string]any{
		"step":       stepNum,
		"call_idx":   callIdx,
		"result_len": resultLen,
		"result":     preview,
		"error":      isError,
	}
	if toolCallID != "" {
		data["tool_call_id"] = toolCallID
	}
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "tool_result",
		Data:      data,
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
func (e *EventEmitter) Reflection(reflection *orchestration.Reflection, attempt, maxAttempts int) {
	e.log().Info("emitter: reflection completed",
		"sessionID", e.sessionID,
		"summary", reflection.Summary,
		"suggested_action", reflection.SuggestedAction,
		"root_cause", reflection.RootCause,
		"action_plan", reflection.ActionPlan,
		"attempt", attempt,
		"max_attempts", maxAttempts,
	)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "reflection",
		Data: map[string]any{
			"summary":          reflection.Summary,
			"insights":         reflection.Hypotheses,
			"suggested_action": reflection.SuggestedAction,
			"root_cause":       reflection.RootCause,
			"failure_analysis": reflection.FailureAnalysis,
			"action_plan":      reflection.ActionPlan,
			"reasoning":        reflection.Reasoning,
			"attempt":          attempt,
			"max_attempts":     maxAttempts,
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
			"content":             content,
			"accumulated_content": e.streamAccumulated.String(),
		},
	})
}

// AssistantDone emits an assistant response completion event and resets the accumulator.
// Token accumulation is handled by the UsageTracker; session totals are cached in tokenState.
func (e *EventEmitter) AssistantDone(fullContent string, inputTokens, outputTokens int) {
	e.log().Debug("emitter: completion (assistant done)", "sessionID", e.sessionID, "inputTokens", inputTokens, "outputTokens", outputTokens)
	// Read current session totals (cached by EmitSessionTokens).
	e.tokens.mu.Lock()
	totalIn := e.tokens.sessionInputTokens
	totalOut := e.tokens.sessionOutputTokens
	lastModel := e.tokens.lastModel
	lastFamily := e.tokens.lastFamily
	lastFill := e.tokens.lastFillPercent
	lastUsed := e.tokens.lastUsedTokens
	lastMax := e.tokens.lastMaxTokens
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
			Model:               lastModel,
			Family:              lastFamily,
			FillPercent:         lastFill,
			UsedTokens:          lastUsed,
			MaxTokens:           lastMax,
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

// SetLastModel updates the cached model/family so that subsequent context_fill
// events report the newly-selected model immediately, before the first LLM call
// reports actual usage via the UsageTracker observer. It is invoked by the
// orchestrator's ApplyRequestOverrides when a per-request model override is
// applied, so the status bar does not show the previous task's stale model
// during a continuation or resume. A no-op when model is empty.
func (e *EventEmitter) SetLastModel(model, family string) {
	if model == "" {
		return
	}
	e.tokens.mu.Lock()
	e.tokens.lastModel = model
	e.tokens.lastFamily = family
	e.tokens.mu.Unlock()
}

// EmitSessionTokens emits a "session_tokens" event with the given totals.
// This is called by the UsageTracker observer — accumulation is handled externally.
// The context-window fill percent and used/max token counts are read from the shared
// cache (updated only by the session-root emitter in ContextFill) and forwarded for
// persistence and display.
func (e *EventEmitter) EmitSessionTokens(totalIn, totalOut int, model, family string) {
	e.log().Debug("emitter: session tokens update", "sessionID", e.sessionID, "totalIn", totalIn, "totalOut", totalOut, "model", model, "family", family)
	e.tokens.mu.Lock()
	// Update cached state for ContextFill enrichment
	e.tokens.sessionInputTokens = totalIn
	e.tokens.sessionOutputTokens = totalOut
	if model != "" {
		e.tokens.lastModel = model
		e.tokens.lastFamily = family
	}
	fillPercent := e.tokens.lastFillPercent
	usedTokens := e.tokens.lastUsedTokens
	maxTokens := e.tokens.lastMaxTokens
	persist := e.tokens.tokenPersist
	e.tokens.mu.Unlock()

	if persist != nil {
		persist(totalIn, totalOut, model, family, fillPercent)
	}

	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "session_tokens",
		Data: SessionTokensEventData{
			SessionInputTokens:  totalIn,
			SessionOutputTokens: totalOut,
			Model:               model,
			Family:              family,
			FillPercent:         fillPercent,
			UsedTokens:          usedTokens,
			MaxTokens:           maxTokens,
		},
	})
}

// ContextFill emits a context fill status event, enriched with session-level token totals.
//
// The executor reports fill relative to its internal "effective max"
// (context window − output limit − safety margin), which is the ceiling the
// agent's compaction logic manages against. The user-facing display, however,
// must reflect the model's advertised context window so the status bar never
// exposes internal compaction thresholds. When a display context window has
// been injected via SetDisplayContextWindow, the percent and max are
// recomputed relative to that real window before emission and caching.
func (e *EventEmitter) ContextFill(fillPercent float64, usedTokens, maxTokens int, status, stepID string) {
	// Cache fill state and read session totals atomically.
	e.tokens.mu.Lock()
	// The executor's maxTokens is the internal effective max; remember it so
	// ContextCompaction can scale its before/after percentages to the display
	// basis.
	e.tokens.lastEffectiveMax = maxTokens
	// Recompute display values relative to the real advertised context window
	// (falling back to the executor-reported max when no window is known).
	displayMax := e.tokens.displayContextWindow
	if displayMax <= 0 {
		displayMax = maxTokens
	}
	displayPercent := fillPercent
	if displayMax > 0 {
		displayPercent = float64(usedTokens) / float64(displayMax) * 100
	}
	// Only the session-root (conductor) emitter updates the session-level fill
	// cache. Scoped copies (subagents) report their own fill via the emitted
	// event and stepContextFill, but must not clobber the conductor's fill used
	// by the status bar / persisted session tokens.
	if e.isSessionRoot {
		e.tokens.lastFillPercent = displayPercent
		e.tokens.lastUsedTokens = usedTokens
		e.tokens.lastMaxTokens = displayMax
		e.tokens.lastFillStatus = status
	}
	totalIn := e.tokens.sessionInputTokens
	totalOut := e.tokens.sessionOutputTokens
	lastModel := e.tokens.lastModel
	lastFamily := e.tokens.lastFamily
	e.tokens.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "context_fill",
		Data: ContextFillEventData{
			FillPercent:         displayPercent,
			UsedTokens:          usedTokens,
			MaxTokens:           displayMax,
			Status:              status,
			PlanStepID:          stepID,
			SessionInputTokens:  totalIn,
			SessionOutputTokens: totalOut,
			Model:               lastModel,
			Family:              lastFamily,
		},
	})
}

// Service emits a general service message without metadata.
func (e *EventEmitter) Service(content string) {
	e.log().Debug("emitter: service message", "sessionID", e.sessionID)
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

// GoalStatus emits a dedicated goal_status session event carrying the full goal
// state snapshot. Unlike the phase-discriminated `service` channel, it is its
// own event type so the frontend's live subscription reliably reaches the goal
// store (the goal status indicator + turn transitions).
func (e *EventEmitter) GoalStatus(data map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "goal_status",
		Data:      data,
	})
}

// GoalProgress emits a dedicated goal_progress session event with turn/budget
// telemetry, emitted mid-loop (after a non-terminal turn) so the frontend can
// show live progress toward the budget.
func (e *EventEmitter) GoalProgress(data map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "goal_progress",
		Data:      data,
	})
}

// ExecutorDiagnostic logs an internal executor diagnostic at DEBUG level.
// These are internal diagnostics, not user-facing events.
func (e *EventEmitter) ExecutorDiagnostic(stepNum int, event string, details map[string]any) {
	e.log().Debug("emitter: executor diagnostic",
		"stepNum", stepNum,
		"event", event,
		"details", details,
	)
}

// ContextCompaction emits a context compaction event with before/after fill percentages.
func (e *EventEmitter) ContextCompaction(beforePercent, afterPercent float64, stepID string) {
	// The executor reports compaction before/after relative to its internal
	// effective max. Scale to the display basis (real context window) so the
	// "Context compacted from X% to Y%" message stays consistent with the
	// status bar, which also presents fill relative to the real window.
	e.tokens.mu.Lock()
	scale := 1.0
	if e.tokens.lastMaxTokens > 0 && e.tokens.lastEffectiveMax > 0 {
		scale = float64(e.tokens.lastEffectiveMax) / float64(e.tokens.lastMaxTokens)
	}
	e.tokens.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "context_compaction",
		Data: ContextCompactionEventData{
			BeforePercent: beforePercent * scale,
			AfterPercent:  afterPercent * scale,
			PlanStepID:    stepID,
		},
	})
}

// ReplanFailed logs a failed replan attempt.
func (e *EventEmitter) ReplanFailed(err error) {
	e.log().Debug("emitter: replan failed", "sessionID", e.sessionID, "error", err)
}

// SkillsActivated emits a skills_activated event listing the skills matched for the current task.
func (e *EventEmitter) SkillsActivated(skillNames []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "skills_activated",
		Data: SkillsActivatedData{
			Skills: skillNames,
		},
	})
}

// ToolsAssigned emits a tools_assigned event listing the tools curated for the
// session by Small-LLM domain narrowing (mirrors SkillsActivated's card).
func (e *EventEmitter) ToolsAssigned(toolNames []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "tools_assigned",
		Data: ToolsAssignedData{
			Tools: toolNames,
		},
	})
}

// StepTodoUpdate emits a step_todo_update event with the current checklist.
// stepID may be empty for a standalone checklist (Conductor without a plan).
func (e *EventEmitter) StepTodoUpdate(stepID string, items []agent.TodoItem) {
	e.log().Debug("emitter: step todo update", "sessionID", e.sessionID, "stepID", stepID, "itemCount", len(items))
	e.mu.Lock()
	defer e.mu.Unlock()

	itemData := make([]map[string]any, len(items))
	completedCount := 0
	for i, item := range items {
		itemData[i] = map[string]any{
			"text":    item.Text,
			"checked": item.Checked,
		}
		if item.Checked {
			completedCount++
		}
	}

	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "step_todo_update",
		Data: map[string]any{
			"step_id":         stepID,
			"items":           itemData,
			"completed_count": completedCount,
			"total_count":     len(items),
		},
	})
}

// MemoryRead emits a memory_read event when the agent reads from its persistent memory.
func (e *EventEmitter) MemoryRead(stepNum int, content string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "memory_read",
		Data: map[string]any{
			"step_num": stepNum,
			"content":  content,
		},
	})
}

// SessionTokenTotals returns the accumulated session-wide input and output token counts.
func (e *EventEmitter) SessionTokenTotals() (inputTokens, outputTokens int) {
	e.tokens.mu.Lock()
	defer e.tokens.mu.Unlock()
	return e.tokens.sessionInputTokens, e.tokens.sessionOutputTokens
}
