package core

import (
	"testing"

	"github.com/v0lka/sp4rk/llm"
)

// scopableFillEmitter records ContextFill step scopes and mirrors the
// CurrentStepScopable contract of the real root emitter so tests can plant a
// stale dynamic scope and observe the orchestrator's task-boundary reset.
type scopableFillEmitter struct {
	mockEmitter
	fills         []fillRecord
	currentStepID string
}

func (s *scopableFillEmitter) ContextFill(percent float64, used, maxTokens int, status, stepID string) {
	s.fills = append(s.fills, fillRecord{percent, used, maxTokens, status, stepID})
	// The real root emitter treats the resolved stepID as its scope for later
	// emissions; the orchestrator must have cleared it BEFORE this call.
	s.currentStepID = stepID
}

func (s *scopableFillEmitter) SetCurrentStepID(id string) {
	s.currentStepID = id
}

// TestEmitInitialContextFill_ResetsStaleStepScope covers the pause→abandon
// leak (review finding [3]): completeAll is skipped on cooperative pause, so
// a task abandoned without resume leaves the root emitter's dynamic step
// scope pointing at the abandoned task's step. The next task's baseline
// context_fill (emitInitialContextFill — the first empty-stepID emission of
// every task entry: HandleMessage → Conductor/E2S/Goal and Resume) must start
// session-root, or the early fills of the new task land under a stale step id
// that can collide with the new plan's own ids.
func TestEmitInitialContextFill_ResetsStaleStepScope(t *testing.T) {
	spy := &scopableFillEmitter{}
	o := &Orchestrator{
		emitter: spy,
		modelRegistry: llm.NewModelRegistry(map[string]llm.ModelMetadata{
			"test-model": {ContextWindow: 1000, OutputLimit: 100, Family: "test"},
		}),
		config: OrchestratorConfig{Model: "test-model"},
	}
	spy.currentStepID = "step_2" // stale scope from the abandoned task

	o.emitInitialContextFill()

	if len(spy.fills) != 1 {
		t.Fatalf("expected exactly 1 ContextFill emission, got %d", len(spy.fills))
	}
	if got := spy.fills[0].stepID; got != "" {
		t.Fatalf("initial fill must be session-root after a stale-scope reset, got plan_step_id %q", got)
	}
	if spy.currentStepID != "" {
		t.Fatalf("dynamic scope must stay cleared after task entry, got %q", spy.currentStepID)
	}
}
