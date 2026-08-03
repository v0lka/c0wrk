package core

import (
	"log/slog"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/orchestration"
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
	l.logger.Debug("subagent: launch", "stepID", stepID, "description", description)
	l.inner.SubAgentLaunch(stepID, description)
}

func (l *loggingEmitter) SubAgentComplete(stepID string, success bool, duration time.Duration) {
	l.logger.Debug("subagent: complete", "stepID", stepID, "success", success, "durationMs", duration.Milliseconds())
	l.inner.SubAgentComplete(stepID, success, duration)
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
	l.logger.Debug("executor: finishing", "stepNum", stepNum, "summary", summary)
	l.inner.Finishing(stepNum, summary)
}

func (l *loggingEmitter) ExecutorDiagnostic(stepNum int, event string, details map[string]any) {
	l.logger.Debug("executor: diagnostic", "stepNum", stepNum, "event", event, "details", details)
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
	l.logger.Info("plan step start", "stepID", stepID, "description", description, "summary", summary)
	l.inner.PlanStepStart(stepID, description, summary)
}

func (l *loggingEmitter) PlanStepComplete(stepID string, success bool, duration time.Duration, errMsg string) {
	l.logger.Info("plan step complete", "stepID", stepID, "success", success, "durationMs", duration.Milliseconds(), "errMsg", errMsg)
	l.inner.PlanStepComplete(stepID, success, duration, errMsg)
}

func (l *loggingEmitter) Reflection(reflection *orchestration.Reflection, attempt, maxAttempts int) {
	summary, action, cause := "", "", ""
	if reflection != nil {
		summary, action, cause = reflection.Summary, reflection.SuggestedAction, reflection.RootCause
	}
	l.logger.Info("reflection", "attempt", attempt, "maxAttempts", maxAttempts, "summary", summary, "suggestedAction", action, "rootCause", cause)
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
	l.logger.Debug("service", "content", content)
	l.inner.Service(content)
}

func (l *loggingEmitter) ServiceWithMeta(content string, meta map[string]any) {
	l.logger.Debug("service", "content", content, "meta", meta)
	l.inner.ServiceWithMeta(content, meta)
}

func (l *loggingEmitter) GoalStatus(data map[string]any) {
	l.logger.Debug("goal_status", "data", data)
	l.inner.GoalStatus(data)
}

func (l *loggingEmitter) GoalProgress(data map[string]any) {
	l.logger.Debug("goal_progress", "data", data)
	l.inner.GoalProgress(data)
}

func (l *loggingEmitter) ReplanFailed(err error) {
	l.logger.Warn("replan failed", "error", err)
	l.inner.ReplanFailed(err)
}

func (l *loggingEmitter) SkillsActivated(skillNames []string) {
	l.logger.Info("skills activated", "skills", skillNames)
	l.inner.SkillsActivated(skillNames)
}

func (l *loggingEmitter) ToolsAssigned(toolNames []string) {
	l.logger.Info("tools assigned", "tools", toolNames)
	l.inner.ToolsAssigned(toolNames)
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
	type sessionTokenEmitter interface {
		EmitSessionTokens(totalIn, totalOut int, model, family string)
	}
	if te, ok := l.inner.(sessionTokenEmitter); ok {
		te.EmitSessionTokens(totalIn, totalOut, model, family)
	}
}
