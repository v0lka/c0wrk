package orchestration

import (
	"time"

	"github.com/v0lka/sp4rk/agent"
)

// NoopEvents is a no-op implementation of Events.
type NoopEvents struct {
	agent.NoopEvents
}

// compile-time check
var _ Events = (*NoopEvents)(nil)

// OnPlanGenerated is a no-op.
func (*NoopEvents) OnPlanGenerated(_ int, _ []PlanStepEvent) {}

// OnStepStarted is a no-op.
func (*NoopEvents) OnStepStarted(_, _, _ string) {}

// OnStepCompleted is a no-op.
func (*NoopEvents) OnStepCompleted(_ string, _ bool, _ time.Duration, _ string) {}

// OnReflected is a no-op.
func (*NoopEvents) OnReflected(_ *Reflection, _, _ int) {}

// OnRetry is a no-op.
func (*NoopEvents) OnRetry(_, _ int) {}

// OnStepRetry is a no-op.
func (*NoopEvents) OnStepRetry(_ string, _, _ int) {}

// OnService is a no-op.
func (*NoopEvents) OnService(_ string) {}

// OnServiceMeta is a no-op.
func (*NoopEvents) OnServiceMeta(_ string, _ map[string]any) {}

// OnReplanFailed is a no-op.
func (*NoopEvents) OnReplanFailed(_ error) {}

// OnStepTodoUpdate is a no-op.
func (*NoopEvents) OnStepTodoUpdate(_ string, _ []agent.TodoItem) {}
