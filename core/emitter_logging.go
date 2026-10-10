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

// loggingEmitter wraps an Emitter to log all events via a session-specific logger.
// It implements Emitter and PlanStepScopable.
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
// plan-step scoping. This allows the inlineStepLifecycle to dynamically
// tag the Conductor's inline executor events with plan_step_id through
// the logging wrapper.
func (l *loggingEmitter) SetCurrentStepID(id string) {
	if sc, ok := l.inner.(CurrentStepScopable); ok {
		sc.SetCurrentStepID(id)
	}
}

// SetDisplayContextWindow delegates to the inner emitter if it supports
// display-context-window injection, so the orchestrator can route the
// model's advertised window through the logging wrapper.
func (l *loggingEmitter) SetDisplayContextWindow(window int) {
	if s, ok := l.inner.(DisplayContextWindowSetter); ok {
		s.SetDisplayContextWindow(window)
	}
}

// SetDisplayContextWindowForModel delegates to the inner emitter if it
// supports model-scoped display-window injection, so the lazy probe's
// late-arriving runtime window can refresh the status bar through the
// logging wrapper.
func (l *loggingEmitter) SetDisplayContextWindowForModel(model string, window int) {
	if s, ok := l.inner.(DisplayContextWindowForModelSetter); ok {
		s.SetDisplayContextWindowForModel(model, window)
	}
}

// SetLastModel delegates to the inner emitter if it supports last-model
// injection, so the orchestrator can route the selected model/family through
// the logging wrapper for immediate context_fill synchronization.
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
	// description is model-authored prose (the plan-step description forwarded
	// by planStepEventTranslator): bounded preview plus its length only, never
	// the payload unbounded ("no secrets in logs", cf. SubAgentComplete).
	l.logger.Debug("subagent: launch", "stepID", stepID, "descriptionLen", len(description),
		"descriptionPreview", strutil.TruncateUTF8(description, 200))
	l.inner.SubAgentLaunch(stepID, description)
}

func (l *loggingEmitter) SubAgentComplete(stepID string, success bool, duration time.Duration, errMsg string) {
	// errMsg is model-authored: the SDK computes it from a hard error, the
	// abort reason, or raw result.Output — prose that can embed quoted file
	// contents and paths. Log a bounded preview plus its length only, never
	// the payload unbounded ("no secrets in logs", cf. E2SState above).
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
	// summary is model-authored prose (the run's delivered answer; in the E2S
	// path it is literally the model's final Answer): bounded preview plus its
	// length only ("no secrets in logs", cf. SubAgentComplete above).
	l.logger.Debug("executor: finishing", "stepNum", stepNum, "summaryLen", len(summary),
		"summaryPreview", strutil.TruncateUTF8(summary, 200))
	l.inner.Finishing(stepNum, summary)
}

func (l *loggingEmitter) ExecutorDiagnostic(stepNum int, event string, details map[string]any) {
	// details is an arbitrary payload map that can embed quoted file contents
	// or secrets: log the shape only (sorted key + string byte-length / Go
	// type), never the raw values ("no secrets in logs", cf. E2SState below).
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
	// description/summary are model-authored prose (declare_plan fields): log
	// bounded previews plus their lengths only, never the payloads unbounded
	// ("no secrets in logs", cf. SubAgentComplete below).
	l.logger.Info("plan step start", "stepID", stepID,
		"descriptionLen", len(description), "descriptionPreview", strutil.TruncateUTF8(description, 200),
		"summaryLen", len(summary), "summaryPreview", strutil.TruncateUTF8(summary, 200))
	l.inner.PlanStepStart(stepID, description, summary)
}

func (l *loggingEmitter) PlanStepComplete(stepID string, success bool, duration time.Duration, errMsg string) {
	// errMsg is model-authored (mirrors SubAgentComplete below): bounded
	// preview plus its length only, never the payload unbounded
	// ("no secrets in logs").
	l.logger.Info("plan step complete", "stepID", stepID, "success", success,
		"durationMs", duration.Milliseconds(), "errMsgLen", len(errMsg),
		"errMsgPreview", strutil.TruncateUTF8(errMsg, 200))
	l.inner.PlanStepComplete(stepID, success, duration, errMsg)
}

func (l *loggingEmitter) PlanStepPaused(stepID string, duration time.Duration, errMsg string) {
	// errMsg is model-authored (mirrors SubAgentComplete below): bounded
	// preview plus its length only ("no secrets in logs").
	l.logger.Info("plan step paused", "stepID", stepID, "durationMs", duration.Milliseconds(),
		"errMsgLen", len(errMsg), "errMsgPreview", strutil.TruncateUTF8(errMsg, 200))
	l.inner.PlanStepPaused(stepID, duration, errMsg)
}

func (l *loggingEmitter) Reflection(reflection *orchestration.Reflection, attempt, maxAttempts int) {
	summary, action, cause := "", "", ""
	if reflection != nil {
		summary, action, cause = reflection.Summary, reflection.SuggestedAction, reflection.RootCause
	}
	// The reflector's Summary/SuggestedAction/RootCause are LLM-authored prose
	// derived from the failing trajectory and its tool output — they restate
	// observed failure output and can embed quoted file contents: bounded
	// previews plus lengths only ("no secrets in logs", cf. SubAgentComplete).
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
	// content can embed model/service-authored prose: bounded preview plus its
	// length only ("no secrets in logs", cf. SubAgentComplete above).
	l.logger.Debug("service", "contentLen", len(content),
		"contentPreview", strutil.TruncateUTF8(content, 200))
	l.inner.Service(content)
}

func (l *loggingEmitter) ServiceWithMeta(content string, meta map[string]any) {
	// content bounded as in Service; meta is an arbitrary map that can embed
	// file contents or secrets: log the shape only ("no secrets in logs").
	l.logger.Debug("service", "contentLen", len(content),
		"contentPreview", strutil.TruncateUTF8(content, 200), "metaShape", payloadShape(meta))
	l.inner.ServiceWithMeta(content, meta)
}

func (l *loggingEmitter) GoalStatus(data map[string]any) {
	// The goal payload carries model/judge-authored prose (condition, reason,
	// evidence, verification_* — and verdict.Status, which is free-form model
	// text, core/goal/types.go): log the short non-prose metadata plus bounded
	// previews and the payload shape only, never the prose unbounded ("no
	// secrets in logs", cf. E2SState below).
	l.logger.Debug("goal_status", "turn", data["turn"], "max_turns", data["max_turns"],
		"status", data["status"], "verdict", boundedPreview(data["verdict"]),
		"verification", data["verification"], "verification_mode", data["verification_mode"],
		"payloadShape", payloadShape(data))
	l.inner.GoalStatus(data)
}

func (l *loggingEmitter) GoalProgress(data map[string]any) {
	// Same policy as GoalStatus: metadata + shape only, prose never unbounded.
	l.logger.Debug("goal_progress", "turn", data["turn"], "max_turns", data["max_turns"],
		"payloadShape", payloadShape(data))
	l.inner.GoalProgress(data)
}

// payloadShape summarizes an event payload map as a deterministic,
// human-readable string of "key(byteLength)" entries for string values and
// "key(goType)" entries otherwise (sorted by key), so the log keeps the
// payload's structure and sizes without any model/judge-authored prose. It is
// the shared form of the bound-and-truncate policy this file applies to
// model-authored text (SubAgentComplete's length+preview, E2SState's
// metadata-only logging): the maps it renders can embed quoted file contents
// or secrets and are never logged raw ("no secrets in logs").
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
// log attribute: strings are truncated like every prose preview in this file,
// and any other non-nil value is rendered through %v and truncated the same
// way — a struct (e.g. a goal Verdict) can embed unbounded model prose, so it
// must never reach the log verbatim ("no secrets in logs").
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
	// Log METADATA only, never the Σ payload: Σ is model-authored working
	// memory that regularly carries distilled file contents, absolute paths,
	// and findings (possible secrets/PII), and it is re-logged after every
	// applied patch — the neighboring handlers deliberately log sizes and
	// previews for the same reason ("no secrets in logs").
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

// EmitSessionTokens forwards session token totals to the inner emitter if it supports it.
// This enables the UsageTracker observer (registered via builder.go type assertion) to
// propagate accumulated tokens through the logging wrapper.
func (l *loggingEmitter) EmitSessionTokens(totalIn, totalOut int, model, family string) {
	l.logger.Debug("session tokens update", "totalIn", totalIn, "totalOut", totalOut, "model", model, "family", family)
	if te, ok := l.inner.(SessionTokenEmitter); ok {
		te.EmitSessionTokens(totalIn, totalOut, model, family)
	}
}

// EmitSessionTokensWithThroughput forwards session token totals plus the median
// output-token throughput to the inner emitter. The inner emitter's capability
// decides the path: the throughput seam when it supports it, degraded to the
// plain totals seam otherwise — mirroring the builder's observer-selection
// fallback so the logging wrapper never masks an inner capability.
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
