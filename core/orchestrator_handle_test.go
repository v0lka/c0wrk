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

// TestPersistTaskOutcome_PausedStaysResumable verifies that a cooperative
// pause (ExecutionStatusPaused) marks the task paused — neither completed nor
// failed — so the resume safety net (GetUnfinishedTask matches the paused
// status) can offer a Resume action and SessionRuntimeStatus.Paused reports
// true. This is the core acceptance criterion: "on pause the task persists
// as paused".
func TestPersistTaskOutcome_PausedStaysResumable(t *testing.T) {
	pbb := newTestPersistableBlackboard()

	persistTaskOutcome(pbb, &orchestration.ExecutionResult{
		Status: orchestration.ExecutionStatusPaused,
		Output: "partial work before pause",
	})

	if pbb.completed {
		t.Fatal("a paused task must NOT be marked completed")
	}
	if pbb.failed {
		t.Fatal("a paused task must NOT be marked failed")
	}
	if !pbb.paused {
		t.Fatal("a paused task must be marked paused (PauseTask called)")
	}
}
