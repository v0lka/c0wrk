package core

import (
	"log/slog"
	"time"
)

// loggingEmitter wraps an Emitter to log all events via a session-specific logger.
// It implements Emitter and PlanStepScopable.
type loggingEmitter struct {
	inner  Emitter
	logger *slog.Logger
}

// ensure loggingEmitter implements Emitter and PlanStepScopable.
var (
	_ Emitter          = (*loggingEmitter)(nil)
	_ PlanStepScopable = (*loggingEmitter)(nil)
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

// ---------------------------------------------------------------------------
// agent.AgentEvents methods (executor-level)
// ---------------------------------------------------------------------------

func (l *loggingEmitter) StepStart(stepNum int) {
	l.logger.Debug("executor: step start", "stepNum", stepNum)
	l.inner.StepStart(stepNum)
}

func (l *loggingEmitter) Thought(stepNum int, content, reasoning string) {
	l.logger.Debug("executor: thought", "stepNum", stepNum)
	l.inner.Thought(stepNum, content, reasoning)
}

func (l *loggingEmitter) ToolCall(stepNum int, toolName, argsPreview, source string) {
	l.logger.Debug("executor: tool call", "stepNum", stepNum, "tool", toolName, "source", source)
	l.inner.ToolCall(stepNum, toolName, argsPreview, source)
}

func (l *loggingEmitter) ToolResult(stepNum, resultLen int, preview string) {
	l.logger.Debug("executor: tool result", "stepNum", stepNum, "resultLen", resultLen)
	l.inner.ToolResult(stepNum, resultLen, preview)
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

func (l *loggingEmitter) TokensUsed(inputTokens, outputTokens int) {
	l.logger.Debug("executor: tokens used", "inputTokens", inputTokens, "outputTokens", outputTokens)
	l.inner.TokensUsed(inputTokens, outputTokens)
}

func (l *loggingEmitter) ContextFill(fillPercent float64, usedTokens, maxTokens int, status, stepID string) {
	l.logger.Debug("executor: context fill", "fillPercent", fillPercent, "usedTokens", usedTokens, "maxTokens", maxTokens, "status", status, "stepID", stepID)
	l.inner.ContextFill(fillPercent, usedTokens, maxTokens, status, stepID)
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

func (l *loggingEmitter) PlanGenerated(stepCount int, steps []PlanStepEvent) {
	l.logger.Info("plan generated", "stepCount", stepCount)
	l.inner.PlanGenerated(stepCount, steps)
}

func (l *loggingEmitter) PlanStepStart(stepID, description string) {
	l.logger.Info("plan step start", "stepID", stepID, "description", description)
	l.inner.PlanStepStart(stepID, description)
}

func (l *loggingEmitter) PlanStepComplete(stepID string, success bool, duration time.Duration) {
	l.logger.Info("plan step complete", "stepID", stepID, "success", success, "durationMs", duration.Milliseconds())
	l.inner.PlanStepComplete(stepID, success, duration)
}

func (l *loggingEmitter) Reflection(summary string, insights []string, attempt, maxAttempts int) {
	l.logger.Info("reflection", "attempt", attempt, "maxAttempts", maxAttempts, "insightCount", len(insights))
	l.inner.Reflection(summary, insights, attempt, maxAttempts)
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

func (l *loggingEmitter) ReplanFailed(err error) {
	l.logger.Warn("replan failed", "error", err)
	l.inner.ReplanFailed(err)
}

func (l *loggingEmitter) FileRollbackError(stepID string, err error) {
	l.logger.Warn("file rollback error", "stepID", stepID, "error", err)
	l.inner.FileRollbackError(stepID, err)
}
