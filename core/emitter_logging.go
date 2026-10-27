package core

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/strutil"
)

// loggingEmitter wraps an Emitter to log all events via a session-specific
// logger. Log-redaction policy for EVERY handler in this file: model- or
// judge-authored payload text is never logged verbatim — scalar prose gets a
// bounded 200-rune preview plus its byte length, and payload maps get
// payloadShape (sizes/types only) — because such prose can embed quoted file
// contents, absolute paths, or secrets, and payload maps are re-logged after
// every applied patch. Each handler below carries only its delta.
// See specs/domains/orchestration/README.md (Invariants).
type loggingEmitter struct {
	inner  Emitter
	logger *slog.Logger
}

// ensure loggingEmitter implements Emitter, PlanStepScopable, RetryAttemptScopable,
// and CurrentStepScopable.
var (
	_ Emitter                    = (*loggingEmitter)(nil)
	_ PlanStepScopable           = (*loggingEmitter)(nil)
	_ RetryAttemptScopable       = (*loggingEmitter)(nil)
	_ CurrentStepScopable        = (*loggingEmitter)(nil)
	_ DisplayContextWindowSetter = (*loggingEmitter)(nil)
	_ LastModelSetter            = (*loggingEmitter)(nil)
)

// NewLoggingEmitter wraps an Emitter to log all events via the given logger.
// If logger is nil, returns inner unchanged.
func NewLoggingEmitter(inner Emitter, logger *slog.Logger) Emitter {
	if logger == nil {
		return inner
	}
	return &loggingEmitter{inner: inner, logger: logger}
}

// WithPlanStepID returns a new loggingEmitter wrapping the scoped inner emitter,
// with the planStepID added to the logger context.
func (l *loggingEmitter) WithPlanStepID(id string) Emitter {
	scoped := l.inner
	if s, ok := l.inner.(PlanStepScopable); ok {
		scoped = s.WithPlanStepID(id)
	}
	return &loggingEmitter{
		inner:  scoped,
		logger: l.logger.With("planStepID", id),
	}
}

// WithRetryAttempt returns a new loggingEmitter wrapping the retry-scoped inner emitter,
// with the retryAttempt added to the logger context.
func (l *loggingEmitter) WithRetryAttempt(attempt int) Emitter {
	scoped := l.inner
	if r, ok := l.inner.(RetryAttemptScopable); ok {
		scoped = r.WithRetryAttempt(attempt)
	}
	return &loggingEmitter{
		inner:  scoped,
		logger: l.logger.With("retryAttempt", attempt),
	}
}

// SetCurrentStepID delegates to the inner emitter if it supports dynamic
// plan-step scoping (the inlineStepLifecycle's tag channel).
func (l *loggingEmitter) SetCurrentStepID(id string) {
	if sc, ok := l.inner.(CurrentStepScopable); ok {
		sc.SetCurrentStepID(id)
	}
}

func (l *loggingEmitter) SetDisplayContextWindow(window int) {
	if s, ok := l.inner.(DisplayContextWindowSetter); ok {
		s.SetDisplayContextWindow(window)
	}
}

func (l *loggingEmitter) SetDisplayContextWindowForModel(model string, window int) {
	if s, ok := l.inner.(DisplayContextWindowForModelSetter); ok {
		s.SetDisplayContextWindowForModel(model, window)
	}
}

func (l *loggingEmitter) SetLastModel(model, family string) {
	if s, ok := l.inner.(LastModelSetter); ok {
		s.SetLastModel(model, family)
	}
}

// ---------------------------------------------------------------------------
// agent.Events methods (executor-level)
// ---------------------------------------------------------------------------

func (l *loggingEmitter) StepStart(stepNum int) {
	l.logger.Debug("executor: step start", "stepNum", stepNum)
	l.inner.StepStart(stepNum)
}

func (l *loggingEmitter) Thought(stepNum int, content, reasoning string) {
	l.logger.Debug("executor: thought", "stepNum", stepNum)
	l.inner.Thought(stepNum, content, reasoning)
}

func (l *loggingEmitter) ToolCall(stepNum, callIdx int, toolName, argsPreview, source string) {
	l.logger.Debug("executor: tool call", "stepNum", stepNum, "callIdx", callIdx, "tool", toolName, "source", source)
	l.inner.ToolCall(stepNum, callIdx, toolName, argsPreview, source)
}

func (l *loggingEmitter) ToolResult(stepNum, callIdx, resultLen int, preview string, isError bool) {
	l.logger.Debug("executor: tool result", "stepNum", stepNum, "callIdx", callIdx, "resultLen", resultLen, "isError", isError)
	l.inner.ToolResult(stepNum, callIdx, resultLen, preview, isError)
}

func (l *loggingEmitter) StepComplete(stepNum int, duration time.Duration) {
	l.logger.Debug("executor: step complete", "stepNum", stepNum, "durationMs", duration.Milliseconds())
	l.inner.StepComplete(stepNum, duration)
}

func (l *loggingEmitter) SubAgentLaunch(stepID, description string) {
	// Model-authored prose — bounded preview + length (file policy).
	l.logger.Debug("subagent: launch", "stepID", stepID, "descriptionLen", len(description),
		"descriptionPreview", strutil.TruncateUTF8(description, 200))
	l.inner.SubAgentLaunch(stepID, description)
}

func (l *loggingEmitter) SubAgentComplete(stepID string, success bool, duration time.Duration, errMsg string) {
	// Model-authored errMsg — bounded preview + length (file policy).
	l.logger.Debug("subagent: complete", "stepID", stepID, "success", success,
		"durationMs", duration.Milliseconds(), "errMsgLen", len(errMsg),
		"errMsgPreview", strutil.TruncateUTF8(errMsg, 200))
	l.inner.SubAgentComplete(stepID, success, duration, errMsg)
}

func (l *loggingEmitter) SubAgentPaused(stepID string, duration time.Duration) {
	l.logger.Debug("subagent: paused", "stepID", stepID, "durationMs", duration.Milliseconds())
	l.inner.SubAgentPaused(stepID, duration)
}

func (l *loggingEmitter) AssistantChunk(content string) {
	// No logging — too noisy for streaming.
	l.inner.AssistantChunk(content)
}

func (l *loggingEmitter) AssistantDone(content string, inputTokens, outputTokens int) {
	l.logger.Debug("executor: assistant done", "inputTokens", inputTokens, "outputTokens", outputTokens)
	l.inner.AssistantDone(content, inputTokens, outputTokens)
}

func (l *loggingEmitter) ContextFill(fillPercent float64, usedTokens, maxTokens int, status, stepID string) {
	l.logger.Debug("executor: context fill", "fillPercent", fillPercent, "usedTokens", usedTokens, "maxTokens", maxTokens, "status", status, "stepID", stepID)
	l.inner.ContextFill(fillPercent, usedTokens, maxTokens, status, stepID)
}

func (l *loggingEmitter) ContextCompaction(beforePercent, afterPercent float64, stepID string) {
	l.logger.Debug("executor: context compaction", "beforePercent", beforePercent, "afterPercent", afterPercent, "stepID", stepID)
	l.inner.ContextCompaction(beforePercent, afterPercent, stepID)
}

func (l *loggingEmitter) Finishing(stepNum int, summary string) {
	// Model-authored summary — bounded preview + length (file policy).
	l.logger.Debug("executor: finishing", "stepNum", stepNum, "summaryLen", len(summary),
		"summaryPreview", strutil.TruncateUTF8(summary, 200))
	l.inner.Finishing(stepNum, summary)
}

func (l *loggingEmitter) ExecutorDiagnostic(stepNum int, event string, details map[string]any) {
	// Arbitrary payload map — shape only (file policy).
	l.logger.Debug("executor: diagnostic", "stepNum", stepNum, "event", event,
		"detailsShape", payloadShape(details))
	l.inner.ExecutorDiagnostic(stepNum, event, details)
}

// ---------------------------------------------------------------------------
// core.Emitter methods (orchestration-level)
// ---------------------------------------------------------------------------

func (l *loggingEmitter) Routing(mode, domain, complexity string) {
	l.logger.Info("routing", "mode", mode, "domain", domain, "complexity", complexity)
	l.inner.Routing(mode, domain, complexity)
}

func (l *loggingEmitter) PlanGenerated(stepCount int, steps []orchestration.PlanStepEvent) {
	l.logger.Info("plan generated", "stepCount", stepCount)
	l.inner.PlanGenerated(stepCount, steps)
}

func (l *loggingEmitter) PlanStepStart(stepID, description, summary string) {
	// Model-authored fields — bounded previews + lengths (file policy).
	l.logger.Info("plan step start", "stepID", stepID,
		"descriptionLen", len(description), "descriptionPreview", strutil.TruncateUTF8(description, 200),
		"summaryLen", len(summary), "summaryPreview", strutil.TruncateUTF8(summary, 200))
	l.inner.PlanStepStart(stepID, description, summary)
}

func (l *loggingEmitter) PlanStepComplete(stepID string, success bool, duration time.Duration, errMsg string) {
	// Model-authored errMsg — bounded preview + length (file policy).
	l.logger.Info("plan step complete", "stepID", stepID, "success", success,
		"durationMs", duration.Milliseconds(), "errMsgLen", len(errMsg),
		"errMsgPreview", strutil.TruncateUTF8(errMsg, 200))
	l.inner.PlanStepComplete(stepID, success, duration, errMsg)
}

func (l *loggingEmitter) PlanStepPaused(stepID string, duration time.Duration, errMsg string) {
	// Model-authored errMsg — bounded preview + length (file policy).
	l.logger.Info("plan step paused", "stepID", stepID, "durationMs", duration.Milliseconds(),
		"errMsgLen", len(errMsg), "errMsgPreview", strutil.TruncateUTF8(errMsg, 200))
	l.inner.PlanStepPaused(stepID, duration, errMsg)
}

func (l *loggingEmitter) Reflection(reflection *orchestration.Reflection, attempt, maxAttempts int) {
	summary, action, cause := "", "", ""
	if reflection != nil {
		summary, action, cause = reflection.Summary, reflection.SuggestedAction, reflection.RootCause
	}
	// LLM-authored reflection fields — bounded previews + lengths (file policy).
	l.logger.Info("reflection", "attempt", attempt, "maxAttempts", maxAttempts,
		"summaryLen", len(summary), "summaryPreview", strutil.TruncateUTF8(summary, 200),
		"suggestedActionLen", len(action), "suggestedActionPreview", strutil.TruncateUTF8(action, 200),
		"rootCauseLen", len(cause), "rootCausePreview", strutil.TruncateUTF8(cause, 200))
	l.inner.Reflection(reflection, attempt, maxAttempts)
}

func (l *loggingEmitter) Retry(attempt, maxAttempts int) {
	l.logger.Info("retry", "attempt", attempt, "maxAttempts", maxAttempts)
	l.inner.Retry(attempt, maxAttempts)
}

func (l *loggingEmitter) StepRetry(stepID string, attempt, maxAttempts int) {
	l.logger.Info("step retry", "stepID", stepID, "attempt", attempt, "maxAttempts", maxAttempts)
	l.inner.StepRetry(stepID, attempt, maxAttempts)
}

func (l *loggingEmitter) Service(content string) {
	// Model/service-authored content — bounded preview + length (file policy).
	l.logger.Debug("service", "contentLen", len(content),
		"contentPreview", strutil.TruncateUTF8(content, 200))
	l.inner.Service(content)
}

func (l *loggingEmitter) ServiceWithMeta(content string, meta map[string]any) {
	// Meta is an arbitrary map — shape only (file policy).
	l.logger.Debug("service", "contentLen", len(content),
		"contentPreview", strutil.TruncateUTF8(content, 200), "metaShape", payloadShape(meta))
	l.inner.ServiceWithMeta(content, meta)
}

func (l *loggingEmitter) GoalStatus(data map[string]any) {
	// Model/judge-authored payload — non-prose metadata + bounded previews +
	// shape only (file policy).
	l.logger.Debug("goal_status", "turn", data["turn"], "max_turns", data["max_turns"],
		"status", data["status"], "verdict", boundedPreview(data["verdict"]),
		"verification", data["verification"], "verification_mode", data["verification_mode"],
		"payloadShape", payloadShape(data))
	l.inner.GoalStatus(data)
}

func (l *loggingEmitter) GoalProgress(data map[string]any) {
	l.logger.Debug("goal_progress", "turn", data["turn"], "max_turns", data["max_turns"],
		"payloadShape", payloadShape(data))
	l.inner.GoalProgress(data)
}

// payloadShape renders an event payload map as "key(byteLength)" entries for
// strings and "key(goType)" otherwise, sorted by key — the map form of this
// file's bounded-logging policy (the maps can embed file contents or secrets).
func payloadShape(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if s, ok := data[k].(string); ok {
			parts = append(parts, fmt.Sprintf("%s(%d)", k, len(s)))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s(%T)", k, data[k]))
	}
	return strings.Join(parts, ", ")
}

// boundedPreview renders a possibly model-authored payload value as a bounded
// log attribute: strings (and %v-rendered non-nil values) truncated like every
// prose preview in this file — a struct can embed unbounded model prose.
func boundedPreview(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		s = fmt.Sprintf("%v", v)
	}
	return strutil.TruncateUTF8(s, 200)
}

func (l *loggingEmitter) E2SState(data map[string]any) {
	// Metadata only: Σ is model-authored working memory, re-logged after
	// every applied patch (file policy).
	turn, _ := data["turn"].(int)
	totalTurns, _ := data["total_turns"].(int)
	maxTurns, _ := data["max_turns"].(int)
	status, _ := data["status"].(string)
	var keys int
	if state, ok := data["state"].(map[string]any); ok {
		keys = len(state)
	}
	l.logger.Debug("e2s_state", "turn", turn, "total_turns", totalTurns,
		"max_turns", maxTurns, "status", status, "state_keys", keys)
	l.inner.E2SState(data)
}

func (l *loggingEmitter) ReplanFailed(err error) {
	l.logger.Warn("replan failed", "error", err)
	l.inner.ReplanFailed(err)
}

func (l *loggingEmitter) SkillsActivated(skillNames []string) {
	l.logger.Info("skills activated", "skills", skillNames)
	l.inner.SkillsActivated(skillNames)
}

func (l *loggingEmitter) StepTodoUpdate(stepID string, items []agent.TodoItem) {
	l.logger.Debug("step todo update", "stepID", stepID, "itemCount", len(items))
	l.inner.StepTodoUpdate(stepID, items)
}

func (l *loggingEmitter) MemoryRead(stepNum int, content string) {
	l.logger.Debug("memory read", "stepNum", stepNum)
	l.inner.MemoryRead(stepNum, content)
}

// EmitSessionTokens forwards session token totals to the inner emitter if it
// supports it (the UsageTracker observer's propagation channel).
func (l *loggingEmitter) EmitSessionTokens(totalIn, totalOut int, model, family string) {
	l.logger.Debug("session tokens update", "totalIn", totalIn, "totalOut", totalOut, "model", model, "family", family)
	if te, ok := l.inner.(SessionTokenEmitter); ok {
		te.EmitSessionTokens(totalIn, totalOut, model, family)
	}
}

// EmitSessionTokensWithThroughput forwards totals plus median throughput,
// degrading to the plain totals seam when the inner emitter predates it —
// mirroring the builder's observer selection (specs/contracts/event-catalog.md,
// "session_tokens").
func (l *loggingEmitter) EmitSessionTokensWithThroughput(totalIn, totalOut int, model, family string, medianOutputTokPerSec float64, throughputSamples int) {
	l.logger.Debug("session tokens update",
		"totalIn", totalIn, "totalOut", totalOut, "model", model, "family", family,
		"medianOutputTokPerSec", medianOutputTokPerSec, "throughputSamples", throughputSamples)
	if te, ok := l.inner.(SessionTokenThroughputEmitter); ok {
		te.EmitSessionTokensWithThroughput(totalIn, totalOut, model, family, medianOutputTokPerSec, throughputSamples)
		return
	}
	if te, ok := l.inner.(SessionTokenEmitter); ok {
		te.EmitSessionTokens(totalIn, totalOut, model, family)
	}
}
