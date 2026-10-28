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
	// displayContextWindow, when > 0, makes ContextFill present fill relative
	// to the real advertised window instead of the executor's internal
	// "effective max"; lastEffectiveMax caches that internal basis so
	// ContextCompaction rescales its percentages to the display basis.
	// See specs/domains/memory/compaction.md.
	displayContextWindow int
	lastEffectiveMax     int
	tokenPersist         func(inputTokens, outputTokens int, model, family string, fillPercent float64) // callback to persist tokens
}

// toolCallIDGen holds a shared monotonic counter for generating unique tool_call_id values
// across all emitter copies (plan-step, retry scopes) within a session.
type toolCallIDGen struct {
	counter atomic.Int64
	epoch   int64 // millisecond timestamp at creation, ensures uniqueness across session reloads

	// toolCallSink, when set, is invoked after each ToolCall with the tool name
	// and generated tool_call_id (the Manager registers it so tool_confirm
	// payloads carry the matching id). Guarded by mu.
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

func (g *toolCallIDGen) sink() func(tool, toolCallID string) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.toolCallSink
}

// EventEmitter implements core.Emitter and routes events to a callback.
//
// Lock ordering: tokens.mu must be acquired BEFORE e.mu; methods needing
// both always take tokens.mu first.
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

	// Shared activity tracking: the last user-facing activity label and the
	// open-stream flag, replayed by the runtime-status snapshot after a
	// session/project switch.
	activity *activityState

	// Shared agent quality metrics (shared across WithPlanStepID copies)
	metrics *metricsState

	// Shared recent-execution window (trajectory + counters the silent-mode
	// step-limit judge reasons over). See execution_window.go.
	window *executionWindow

	// Tool call ID generation (shared across WithPlanStepID copies)
	toolCallIDs *toolCallIDGen

	// Per-copy mapping of (stepNum:callIdx) -> tool_call_id
	// Each scoped emitter copy gets its own map (not shared),
	// since each executor starts stepNum from 1.
	localToolIDs map[string]string

	// isSessionRoot marks the conductor's own emitter (scoped copies are NOT
	// root); only the root updates the session-level fill cache, so a
	// subagent's fill never clobbers the status bar's.
	isSessionRoot bool

	// attachmentNameResolver maps a read_attachment attachment_id to its
	// original file name for the tool-call event/metadata. Wired per-task by
	// the orchestrator; nil when unsupported. Guarded by mu.
	attachmentNameResolver func(string) string

	logger *slog.Logger
}

// NewEventEmitter creates a new EventEmitter for a session.
func NewEventEmitter(sessionID string, emit func(Event)) *EventEmitter {
	return &EventEmitter{
		sessionID:     sessionID,
		emit:          emit,
		tokens:        &tokenState{lastFillStatus: "ok"},
		activity:      &activityState{},
		metrics:       &metricsState{},
		window:        &executionWindow{},
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

// SetDisplayContextWindow injects the model's advertised context window so
// ContextFill presents fill on the display basis; <= 0 clears the override.
// See specs/domains/memory/compaction.md.
func (e *EventEmitter) SetDisplayContextWindow(window int) {
	e.tokens.mu.Lock()
	defer e.tokens.mu.Unlock()
	e.tokens.displayContextWindow = window
}

// SetDisplayContextWindowForModel applies the lazy self-hosted server probe's
// LATE-ARRIVING display-window correction (the initial context_fill precedes
// the probe). The update is MODEL-SCOPED — dropped when the last reported
// model differs, since a stale window would corrupt a switched model's
// display — and when a fill was already reported on the session root, a
// refreshed context_fill is re-broadcast so an idle status bar corrects
// immediately (no further events arrive after a task ends).
// See specs/domains/llm-providers.md (Context Window Resolution).
func (e *EventEmitter) SetDisplayContextWindowForModel(model string, window int) {
	if window <= 0 {
		return
	}
	e.tokens.mu.Lock()
	if e.tokens.lastModel != "" && !strings.EqualFold(e.tokens.lastModel, model) {
		e.tokens.mu.Unlock()
		return
	}
	e.tokens.displayContextWindow = window
	if !e.isSessionRoot || e.tokens.lastUsedTokens <= 0 {
		e.tokens.mu.Unlock()
		return
	}
	// Snapshot the cached fill state for the re-broadcast, then emit outside
	// the tokens lock (emitEvent takes e.mu).
	used := e.tokens.lastUsedTokens
	status := e.tokens.lastFillStatus
	totalIn := e.tokens.sessionInputTokens
	totalOut := e.tokens.sessionOutputTokens
	lastModel := e.tokens.lastModel
	lastFamily := e.tokens.lastFamily
	percent := float64(used) / float64(window) * 100
	e.tokens.lastFillPercent = percent
	e.tokens.lastMaxTokens = window
	e.tokens.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "context_fill",
		Data: ContextFillEventData{
			FillPercent:         percent,
			UsedTokens:          used,
			MaxTokens:           window,
			Status:              status,
			SessionInputTokens:  totalIn,
			SessionOutputTokens: totalOut,
			Model:               lastModel,
			Family:              lastFamily,
		},
	})
}

// SetToolCallIDSink registers the post-ToolCall callback so tool_confirm
// payloads carry the matching tool_call_id (name matching is ambiguous when
// two calls share a name). The sink lives on the shared gen, so scoped copies
// report to the same store.
func (e *EventEmitter) SetToolCallIDSink(fn func(tool, toolCallID string)) {
	e.toolCallIDs.setSink(fn)
}

// SetAttachmentNameResolver enriches read_attachment tool-call events (and
// their persisted metadata) with the original file name so cards render it
// even after restart. Scoped copies inherit the same resolver.
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
		sessionID:              e.sessionID,
		emit:                   e.emit,
		planStepID:             id,
		retryAttempt:           e.retryAttempt,
		tokens:                 e.tokens,
		activity:               e.activity,
		metrics:                e.metrics,
		window:                 e.window,
		toolCallIDs:            e.toolCallIDs,
		logger:                 e.logger,
		attachmentNameResolver: e.attachmentNameResolver,
	}
}

// WithRetryAttempt returns a shallow copy of the emitter with retryAttempt set.
// Events emitted by the copy will include "retry_attempt" in their Data map when > 0.
func (e *EventEmitter) WithRetryAttempt(attempt int) core.Emitter {
	e.log().Debug("emitter: creating retry-scoped emitter", "sessionID", e.sessionID, "retryAttempt", attempt)
	return &EventEmitter{
		sessionID:              e.sessionID,
		emit:                   e.emit,
		planStepID:             e.planStepID,
		retryAttempt:           attempt,
		tokens:                 e.tokens,
		activity:               e.activity,
		metrics:                e.metrics,
		window:                 e.window,
		toolCallIDs:            e.toolCallIDs,
		logger:                 e.logger,
		attachmentNameResolver: e.attachmentNameResolver,
	}
}

// SetCurrentStepID mutates the receiver's plan_step_id in place (unlike the
// WithPlanStepID copies, which carry a fixed scope) — the inline Conductor's
// single emitter tracks the "current step" through
// update_checklist/declare_step_complete. "" clears the scope; scoped copies
// are unaffected.
func (e *EventEmitter) SetCurrentStepID(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.planStepID = id
}

// CurrentStepID returns the dynamic scope set via SetCurrentStepID ("" when
// unscoped) so out-of-band emitters (e.g. Manager.EmitAutonomyDecision)
// attribute to the same inline step the executor's events carry.
func (e *EventEmitter) CurrentStepID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.planStepID
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
	// Track the last activity label / open-stream flag BEFORE the event
	// leaves: a listener may not exist, and the runtime-status snapshot
	// replays this state on switch-back. See activityState.
	e.activity.updateActivity(evt)
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

// PlanStepStart emits a plan step start event; duplicate calls for the same
// step ID are suppressed, so the inline todo callback and the subagent
// launcher can both call it.
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
	} else {
		// A failed step is re-runnable: drop it from the started-set so the
		// re-run's PlanStepStart re-emits and the UI flips back to "running".
		// Completed steps keep their dedupe (re-declared/skipped steps
		// synthesize their own pair).
		delete(e.planStartedSet, stepID)
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

// PlanStepPaused emits a plan step pause event: the step stopped via a
// cooperative pause checkpoint — a recoverable state, not a failure or a
// completion. The step is NOT marked completed (planCompletedSet is
// untouched); a later Resume re-enters it.
func (e *EventEmitter) PlanStepPaused(stepID string, duration time.Duration, errMsg string) {
	e.log().Debug("emitter: plan step paused", "sessionID", e.sessionID, "stepID", stepID, "duration", duration, "errMsg", errMsg)
	e.mu.Lock()
	defer e.mu.Unlock()
	// The emitter outlives runs: drop the paused step from the started-set or
	// the resumed run's PlanStepStart is suppressed as a duplicate and the UI
	// never flips back to "running".
	delete(e.planStartedSet, stepID)
	completedCount := len(e.planCompletedSet)
	data := map[string]any{
		"step_id":            stepID,
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
		Type:      "plan_step_paused",
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
	e.metrics.observeStep()
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

// JudgeStarted emits the start of a strict-judge (Smart Approve) evaluation —
// the honest activity label while the judge LLM call is in flight, before any
// confirmation card exists. Transient (the persister skips it).
func (e *EventEmitter) JudgeStarted(toolName string) {
	e.log().Debug("emitter: judge started", "sessionID", e.sessionID, "tool", toolName)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "tool_judge_started",
		Data:      map[string]any{"tool": toolName},
	})
}

// JudgeFinished emits the end of a strict-judge evaluation; the tool-execute
// or card events land right after, so the label mirrors the live tool-call
// convention rather than guessing the verdict. Transient (the persister skips
// it).
func (e *EventEmitter) JudgeFinished(toolName string) {
	e.log().Debug("emitter: judge finished", "sessionID", e.sessionID, "tool", toolName)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "tool_judge_finished",
		Data:      map[string]any{"tool": toolName},
	})
}

// AutonomyDecision emits an automatic (no-human) security decision — the
// persisted, auditable record of WHAT was decided (unlike the judge phase
// telemetry, which only says a judge ran). See
// specs/contracts/event-catalog.md.
func (e *EventEmitter) AutonomyDecision(payload AutonomyDecisionData) {
	e.log().Debug("emitter: autonomy decision",
		"sessionID", e.sessionID, "kind", payload.Kind,
		"tool", payload.Tool, "verdict", payload.Verdict)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      EventAutonomyDecision,
		Data:      payload,
	})
}

// ToolConfirm emits a tool-confirmation request via the live session emitter
// (Manager.EmitToolConfirm) so the runtime-status snapshot reports "Awaiting
// confirmation..." for however long the agent blocks on the user. Transient:
// pending confirmations are process-local. See
// specs/contracts/event-catalog.md.
func (e *EventEmitter) ToolConfirm(payload ToolConfirmPayload) {
	e.log().Debug("emitter: tool confirm", "sessionID", e.sessionID, "tool", payload.Tool, "confirmID", payload.ConfirmID)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "tool_confirm",
		Data:      payload,
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
	// Record the tool_call_id so the confirmation callback correlates the
	// immediately-following tool_confirm with this exact tool_call.
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
	// Enrich read_attachment calls with the original file name: the card shows
	// it even after restart, when the frontend's name cache is empty.
	if toolName == "read_attachment" && e.attachmentNameResolver != nil {
		if id := readAttachmentID(data); id != "" {
			if name := e.attachmentNameResolver(id); name != "" {
				data["attachment_name"] = name
			}
		}
	}
	e.window.recordToolCall(stepNum, toolName, argsPreview)
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "tool_call",
		Data:      data,
	})
}

// readAttachmentID extracts the attachment_id argument, preferring the
// pre-parsed args; "" when absent.
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
	e.metrics.observeToolResult(isError, preview)
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
	e.window.recordToolResult(stepNum, preview, isError)
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
	e.window.recordStep(stepNum)
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

// SubAgentComplete emits a subagent completion event. errMsg carries the
// failure reason for a failed subagent ("" on success) and is surfaced as the
// "error" field so the UI can render why a delegated subagent failed. Mirrors
// PlanStepComplete.
func (e *EventEmitter) SubAgentComplete(stepID string, success bool, duration time.Duration, errMsg string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	data := map[string]any{
		"step_id":  stepID,
		"success":  success,
		"duration": duration.Milliseconds(),
	}
	if errMsg != "" {
		data["error"] = errMsg
	}
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "subagent_complete",
		Data:      data,
	})
}

// SubAgentPaused emits a subagent pause event: the sub-agent stopped via a
// cooperative pause checkpoint (a recoverable state, not a failure).
func (e *EventEmitter) SubAgentPaused(stepID string, duration time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "subagent_paused",
		Data: map[string]any{
			"step_id":  stepID,
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

// SetLastModel refreshes the cached model/family on a per-request override
// (orchestrator's ApplyRequestOverrides) so context_fill events report the
// new model immediately instead of the previous task's stale one during a
// continuation/resume. No-op when empty.
func (e *EventEmitter) SetLastModel(model, family string) {
	if model == "" {
		return
	}
	e.tokens.mu.Lock()
	e.tokens.lastModel = model
	e.tokens.lastFamily = family
	e.tokens.mu.Unlock()
}

// EmitSessionTokens emits "session_tokens" with the given totals (UsageTracker
// observer); fill/used/max come from the shared cache, updated only by the
// session-root emitter in ContextFill.
func (e *EventEmitter) EmitSessionTokens(totalIn, totalOut int, model, family string) {
	e.emitSessionTokens(totalIn, totalOut, model, family, 0, 0)
}

// EmitSessionTokensWithThroughput additionally carries the median output-token
// throughput and its sample count — the preferred seam of the UsageTracker's
// timed observer in core/builder.go. Zeros mean the throughput window has not
// reached its minimum samples; the fields are omitted (omitempty).
func (e *EventEmitter) EmitSessionTokensWithThroughput(totalIn, totalOut int, model, family string, medianOutputTokPerSec float64, throughputSamples int) {
	e.emitSessionTokens(totalIn, totalOut, model, family, medianOutputTokPerSec, throughputSamples)
}

// emitSessionTokens is the shared body of both session-token emission seams:
// it refreshes the cached totals (used by ContextFill enrichment and
// TokenSnapshot), invokes the persistence callback, and emits the typed event.
func (e *EventEmitter) emitSessionTokens(totalIn, totalOut int, model, family string, medianOutputTokPerSec float64, throughputSamples int) {
	e.log().Debug("emitter: session tokens update", "sessionID", e.sessionID, "totalIn", totalIn, "totalOut", totalOut, "model", model, "family", family, "medianOutputTokPerSec", medianOutputTokPerSec, "throughputSamples", throughputSamples)
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

	// emitEvent reads planStepID/retryAttempt, written under e.mu on the
	// conductor goroutine while this can run on a token-observer goroutine —
	// hold e.mu across the dispatch so the read cannot race the writer.
	e.mu.Lock()
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
			MedianOutputTokS:    medianOutputTokPerSec,
			TokSSamples:         throughputSamples,
		},
	})
	e.mu.Unlock()
}

// ContextFill emits a context fill event enriched with session token totals,
// rescaled to the display (advertised-window) basis so the status bar never
// exposes internal compaction thresholds. See
// specs/domains/memory/compaction.md.
func (e *EventEmitter) ContextFill(fillPercent float64, usedTokens, maxTokens int, status, stepID string) {
	// The executor passes its own plan-step id (delegated subagents only).
	// Inline Conductor steps rely on the emitter's own scope: emitEvent's
	// plan_step_id injection covers only map payloads, NOT this typed struct,
	// so without this fallback their fills arrived session-root and the step
	// header's fill badge never populated. "" on both sides keeps the event
	// session-root. The dynamic scope is valid only within a task — the
	// orchestrator clears it at task entry (resetDynamicStepScope), and
	// session-root emissions that must never inherit a scope reset it
	// explicitly before emitting.
	if stepID == "" {
		e.mu.Lock()
		stepID = e.planStepID
		e.mu.Unlock()
	}
	// Cache fill state and read session totals atomically.
	e.tokens.mu.Lock()
	// maxTokens is the internal effective max; cached so ContextCompaction
	// can rescale to the display basis.
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
	// Only the session-root emitter updates the session-level fill cache;
	// subagent copies must not clobber the conductor's status-bar fill.
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
			FillPercent: displayPercent,
			UsedTokens:  usedTokens,
			MaxTokens:   displayMax,
			Status:      status,
			PlanStepID:  stepID,
			// See ContextFillEventData.SessionRootMirror; subagent copies
			// never set it (no isSessionRoot).
			SessionRootMirror:   e.isSessionRoot && stepID != "",
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

// GoalStatus emits the dedicated goal_status event (its own type so the live
// subscription reliably reaches the goal store, unlike the service channel).
func (e *EventEmitter) GoalStatus(data map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "goal_status",
		Data:      data,
	})
}

// GoalProgress emits goal_progress turn/budget telemetry mid-loop so the
// frontend can show live progress toward the budget.
func (e *EventEmitter) GoalProgress(data map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "goal_progress",
		Data:      data,
	})
}

// E2SState emits the full E2S execution-state snapshot after every applied
// state patch so the Execution State panel renders the live Σ. See
// specs/domains/e2s.md.
func (e *EventEmitter) E2SState(data map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "e2s_state",
		Data:      data,
	})
}

// ExecutorDiagnostic logs an internal (not user-facing) diagnostic at DEBUG.
func (e *EventEmitter) ExecutorDiagnostic(stepNum int, event string, details map[string]any) {
	e.metrics.observeDiagnostic(event)
	e.window.recordDiagnostic(stepNum, event)
	e.log().Debug("emitter: executor diagnostic",
		"stepNum", stepNum,
		"event", event,
		"details", details,
	)
}

// ContextCompaction emits a context compaction event with before/after fill percentages.
func (e *EventEmitter) ContextCompaction(beforePercent, afterPercent float64, stepID string) {
	// Rescale to the display basis (real window) so the message stays
	// consistent with the status bar.
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

// SetModelProfile snapshots the Model Profiles state annotating the
// "agent_metrics" payload; collected regardless (unset reports enabled=false).
func (e *EventEmitter) SetModelProfile(info ModelProfilesMetaInfo) {
	e.metrics.setModelProfiles(info)
}

// EmitAgentMetrics emits "agent_metrics" (see specs/contracts/event-catalog.md)
// covering exactly one task run, and resets the counters + execution window.
// finish describes the terminal state ("full", "partial", "failed",
// "aborted" or "cancelled"). Returns the emitted payload.
func (e *EventEmitter) EmitAgentMetrics(finish string) AgentMetricsData {
	_, outputTokens := e.SessionTokenTotals()
	data := e.metrics.snapshot(finish, outputTokens)
	e.window.reset()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitEvent(Event{
		SessionID: e.sessionID,
		Type:      "agent_metrics",
		Data:      data,
	})
	return data
}

// ExecutionWindowSnapshot returns a copy of the recent-execution window plus
// the plan-progress view — the silent-mode step-limit judge's evidence source
// (immutable, safe for a decision goroutine).
func (e *EventEmitter) ExecutionWindowSnapshot() ExecutionWindowSnapshot {
	e.mu.Lock()
	planTotal := e.planTotalSteps
	planDone := len(e.planCompletedSet)
	planStep := e.planCurrentStepID
	e.mu.Unlock()

	return ExecutionWindowSnapshot{
		Entries:   e.window.copyEntries(),
		Counters:  e.window.copyCounters(),
		PlanTotal: planTotal,
		PlanDone:  planDone,
		PlanStep:  planStep,
	}
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
