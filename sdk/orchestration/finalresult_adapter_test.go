package orchestration

import (
	"testing"

	"github.com/v0lka/sp4rk/agent"
)

// mockBlackboardForFinalResult is a minimal Blackboard mock for testing
// NewFinalResultStore. Only GetFinalResult is exercised.
type mockBlackboardForFinalResult struct {
	finalResult string
}

func (m *mockBlackboardForFinalResult) GetFinalResult() string { return m.finalResult }

// Stub the rest of the Blackboard interface so the mock compiles.
func (m *mockBlackboardForFinalResult) GetOriginalRequest() string { return "" }
func (m *mockBlackboardForFinalResult) SetOriginalRequest(string)  {}
func (m *mockBlackboardForFinalResult) GetPlan() *Plan             { return nil }
func (m *mockBlackboardForFinalResult) SetPlan(*Plan)              {}
func (m *mockBlackboardForFinalResult) GetStepResult(string) (StepResult, bool) {
	return StepResult{}, false
}
func (m *mockBlackboardForFinalResult) GetStepSummary(string) string             { return "" }
func (m *mockBlackboardForFinalResult) GetAllStepResults() map[string]StepResult { return nil }
func (m *mockBlackboardForFinalResult) SetStepResult(string, string, error, []agent.Step) {
}
func (m *mockBlackboardForFinalResult) AddReflection(Reflection)        {}
func (m *mockBlackboardForFinalResult) GetReflections() []Reflection    { return nil }
func (m *mockBlackboardForFinalResult) SetFinalResult(string)           {}
func (m *mockBlackboardForFinalResult) Search(string) []BlackboardEntry { return nil }
func (m *mockBlackboardForFinalResult) StoreFact(Fact)                  {}
func (m *mockBlackboardForFinalResult) SearchFacts([]string) []Fact     { return nil }
func (m *mockBlackboardForFinalResult) GetFacts() []Fact                { return nil }
func (m *mockBlackboardForFinalResult) SetFacts([]Fact)                 {}

func TestNewFinalResultStore_WithResult(t *testing.T) {
	bb := &mockBlackboardForFinalResult{finalResult: "task completed: variants a, b, c"}
	store := NewFinalResultStore(bb)

	got, ok := store.GetFinalResult()
	if !ok {
		t.Error("expected ok=true when blackboard has a final result")
	}
	if got != "task completed: variants a, b, c" {
		t.Errorf("expected final result content, got %q", got)
	}
}

func TestNewFinalResultStore_EmptyResult(t *testing.T) {
	bb := &mockBlackboardForFinalResult{finalResult: ""}
	store := NewFinalResultStore(bb)

	got, ok := store.GetFinalResult()
	if ok {
		t.Error("expected ok=false when blackboard has empty final result")
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// Verify the adapter satisfies the agent.FinalResultStore interface.
var _ agent.FinalResultStore = (*blackboardFinalResultStore)(nil)
