package core

import (
	"testing"

	"github.com/v0lka/sp4rk/orchestration"
)

// newTestPersistableBlackboard builds a store-less testPersistableBlackboard for
// unit tests that only need the in-memory blackboard surface.
func newTestPersistableBlackboard() *testPersistableBlackboard {
	return &testPersistableBlackboard{
		MapBlackboard: orchestration.NewMapBlackboard(),
		taskID:        "test-task",
	}
}

// TestPersistTaskOutcome_SuccessRecordsFinalResult verifies that a successful
// execution records the Conductor's output as the blackboard final result
// before completion (specs/domains/memory/blackboard.md). Regression test for
// the bug where tasks.final_output was always empty because nobody called
// SetFinalResult with the real output.
func TestPersistTaskOutcome_SuccessRecordsFinalResult(t *testing.T) {
	pbb := newTestPersistableBlackboard()

	const want = "the final answer of the task"
	persistTaskOutcome(pbb, &orchestration.ExecutionResult{
		Status: orchestration.ExecutionStatusSuccess,
		Output: want,
	})

	if got := pbb.GetFinalResult(); got != want {
		t.Fatalf("final result not recorded on success: got %q, want %q", got, want)
	}
	if !pbb.completed {
		t.Fatal("expected task to be marked completed on success")
	}
}

// TestPersistTaskOutcome_FailureDoesNotRecordFinalResult ensures only the
// success path writes the final result; failures leave it untouched.
func TestPersistTaskOutcome_FailureDoesNotRecordFinalResult(t *testing.T) {
	pbb := newTestPersistableBlackboard()

	persistTaskOutcome(pbb, &orchestration.ExecutionResult{
		Status: orchestration.ExecutionStatusFailed,
		Output: "should be ignored",
	})

	if got := pbb.GetFinalResult(); got != "" {
		t.Fatalf("final result should be empty on failure, got %q", got)
	}
	if !pbb.failed {
		t.Fatal("expected task to be marked failed")
	}
}
