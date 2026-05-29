package orchestration

import (
	"time"

	"github.com/v0lka/c0wrk/sdk/agent"
)

// NoopEvents is a no-op implementation of Events.
type NoopEvents struct {
	agent.NoopEvents
}

// compile-time check
var _ Events = (*NoopEvents)(nil)

func (*NoopEvents) OnPlanGenerated(_ int, _ []PlanStepEvent)                    {}
func (*NoopEvents) OnStepStarted(_, _, _ string)                                {}
func (*NoopEvents) OnStepCompleted(_ string, _ bool, _ time.Duration, _ string) {}
func (*NoopEvents) OnReflected(_ *Reflection, _, _ int)                         {}
func (*NoopEvents) OnRetry(_, _ int)                                            {}
func (*NoopEvents) OnStepRetry(_ string, _, _ int)                              {}
func (*NoopEvents) OnService(_ string)                                          {}
func (*NoopEvents) OnServiceMeta(_ string, _ map[string]any)                    {}
func (*NoopEvents) OnReplanFailed(_ error)                                      {}
func (*NoopEvents) OnStepTodoUpdate(_ string, _ []agent.TodoItem)               {}
