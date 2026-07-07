package orchestration

import (
	"errors"
	"testing"

	"github.com/v0lka/sp4rk/agent"
)

func TestNewStepOutputStore_GetStepOutput(t *testing.T) {
	bb := NewMapBlackboard()
	bb.SetStepResult("step_1", "full output here", nil, nil)

	store := NewStepOutputStore(bb)

	output, ok := store.GetStepOutput("step_1")
	if !ok {
		t.Fatal("expected step_1 to exist")
	}
	if output != "full output here" {
		t.Errorf("got %q, want %q", output, "full output here")
	}
}

func TestNewStepOutputStore_GetStepOutput_NotFound(t *testing.T) {
	bb := NewMapBlackboard()
	store := NewStepOutputStore(bb)

	_, ok := store.GetStepOutput("nonexistent")
	if ok {
		t.Error("expected ok=false for nonexistent step")
	}
}

func TestNewStepOutputStore_GetStepOutput_ErrorStep(t *testing.T) {
	bb := NewMapBlackboard()
	bb.SetStepResult("step_1", "partial output", errors.New("something failed"), nil)

	store := NewStepOutputStore(bb)

	_, ok := store.GetStepOutput("step_1")
	if ok {
		t.Error("expected ok=false for step with error")
	}
}

func TestNewStepOutputStore_GetStepOutput_EmptyOutput(t *testing.T) {
	bb := NewMapBlackboard()
	bb.SetStepResult("step_1", "", nil, nil)

	store := NewStepOutputStore(bb)

	_, ok := store.GetStepOutput("step_1")
	if ok {
		t.Error("expected ok=false for step with empty output")
	}
}

func TestNewStepOutputStore_ListStepOutputs(t *testing.T) {
	bb := NewMapBlackboard()
	bb.SetStepResult("step_2", "second output", nil, nil)
	bb.SetStepResult("step_1", "first output", nil, nil)
	bb.SetStepResult("step_3", "third output", errors.New("fail"), nil) // should be excluded

	store := NewStepOutputStore(bb)

	entries := store.ListStepOutputs()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Deterministic order: sorted by step ID
	if entries[0].StepID != "step_1" {
		t.Errorf("entries[0].StepID = %q, want %q", entries[0].StepID, "step_1")
	}
	if entries[1].StepID != "step_2" {
		t.Errorf("entries[1].StepID = %q, want %q", entries[1].StepID, "step_2")
	}
}

func TestNewStepOutputStore_ListStepOutputs_Empty(t *testing.T) {
	bb := NewMapBlackboard()
	store := NewStepOutputStore(bb)

	entries := store.ListStepOutputs()
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// Compile-time check that the adapter implements the interface.
var _ agent.StepOutputStore = (*blackboardStepOutputStore)(nil)
