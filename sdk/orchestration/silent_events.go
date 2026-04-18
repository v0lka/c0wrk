package orchestration

import "time"

// silentPlanEvents wraps an Events implementation to suppress plan step
// visualization events while preserving all other events (service messages,
// agent events, reflection, retry, etc.).
// This is used for simple plans executed as a unified ReAct cycle where the
// plan is just guidance text, not a tracked DAG.
type silentPlanEvents struct {
	inner Events
}

// compile-time check
var _ Events = (*silentPlanEvents)(nil)

// --- Suppressed plan visualization events (no-ops) ---

func (*silentPlanEvents) OnPlanGenerated(_ int, _ []PlanStepEvent)                   {}
func (*silentPlanEvents) OnStepStarted(_, _ string)                                  {}
func (*silentPlanEvents) OnStepCompleted(_ string, _ bool, _ time.Duration, _ string) {}
func (*silentPlanEvents) OnStepRetry(_ string, _, _ int)                             {}

// --- Passed-through orchestration events ---

func (s *silentPlanEvents) OnReflected(r *Reflection, a, m int) { s.inner.OnReflected(r, a, m) }
func (s *silentPlanEvents) OnRetry(a, m int)                     { s.inner.OnRetry(a, m) }
func (s *silentPlanEvents) OnService(c string)                   { s.inner.OnService(c) }
func (s *silentPlanEvents) OnServiceMeta(c string, m map[string]any) {
	s.inner.OnServiceMeta(c, m)
}
func (s *silentPlanEvents) OnReplanFailed(err error)             { s.inner.OnReplanFailed(err) }
func (s *silentPlanEvents) OnFileRollbackError(id string, err error) {
	s.inner.OnFileRollbackError(id, err)
}

// --- Passed-through agent.AgentEvents methods ---

func (s *silentPlanEvents) StepStart(n int)                                    { s.inner.StepStart(n) }
func (s *silentPlanEvents) Thought(n int, c, r string)                         { s.inner.Thought(n, c, r) }
func (s *silentPlanEvents) ToolCall(n, ci int, name, args, src string)  { s.inner.ToolCall(n, ci, name, args, src) }
func (s *silentPlanEvents) ToolResult(n, ci, l int, p string)              { s.inner.ToolResult(n, ci, l, p) }
func (s *silentPlanEvents) StepComplete(n int, d time.Duration)                { s.inner.StepComplete(n, d) }
func (s *silentPlanEvents) SubAgentLaunch(id, desc string)                     { s.inner.SubAgentLaunch(id, desc) }
func (s *silentPlanEvents) SubAgentComplete(id string, ok bool, d time.Duration) {
	s.inner.SubAgentComplete(id, ok, d)
}
func (s *silentPlanEvents) AssistantChunk(c string)                  { s.inner.AssistantChunk(c) }
func (s *silentPlanEvents) AssistantDone(c string, in, out int)      { s.inner.AssistantDone(c, in, out) }
func (s *silentPlanEvents) TokensUsed(in, out int, model, fam string) { s.inner.TokensUsed(in, out, model, fam) }
func (s *silentPlanEvents) ContextFill(pct float64, used, maxTokens int, status, stepID string) {
	s.inner.ContextFill(pct, used, maxTokens, status, stepID)
}
func (s *silentPlanEvents) ContextCompaction(before, after float64, stepID string) {
	s.inner.ContextCompaction(before, after, stepID)
}
func (s *silentPlanEvents) ExecutorDiagnostic(n int, event string, details map[string]any) {
	s.inner.ExecutorDiagnostic(n, event, details)
}

// WithStepID implements StepScopable but stays silent — the returned Events
// still suppresses plan visualization events.
func (s *silentPlanEvents) WithStepID(_ string) Events {
	return s
}
