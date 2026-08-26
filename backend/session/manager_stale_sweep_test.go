package session

import (
	"context"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/orchestration"
)

// sweepableTaskStore is a TaskStore mock for the stale-task sweep tests: it
// serves a queue of unfinished TaskRecords from GetUnfinishedTask and pops the
// head on CancelTask, recording every cancel so the sweep's effect on the
// store can be asserted.
type sweepableTaskStore struct {
	mockTaskStoreForResumable
	mu          sync.Mutex
	unfinishedQ []*TaskRecord
	cancelled   []string
}

func (s *sweepableTaskStore) GetUnfinishedTask(_ context.Context, _ string) (*TaskRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.unfinishedQ) == 0 {
		return nil, nil
	}
	cp := *s.unfinishedQ[0]
	return &cp, nil
}

func (s *sweepableTaskStore) CancelTask(_ context.Context, taskID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelled = append(s.cancelled, taskID)
	if len(s.unfinishedQ) > 0 && s.unfinishedQ[0].ID == taskID {
		s.unfinishedQ = s.unfinishedQ[1:]
	}
	return nil
}

// TestShouldRetryContinuationFresh verifies the fresh-workflow fallback guard:
// a failed continuation is retried as a fresh task when the continuation
// never started executing (the anchor is not the session's unfinished task),
// and when the anchor was ALREADY unfinished before the send began (the
// resume path fell back after a restore error — the fresh retry is the only
// way to unblock the user, and the stale sweep cleans the row up). When a
// terminal anchor IS unfinished after the attempt, its execution started and
// failed mid-flight — retrying fresh would orphan the reactivated row.
func TestShouldRetryContinuationFresh(t *testing.T) {
	cases := []struct {
		name                string
		lastTaskID          string
		anchorWasUnfinished bool
		unfinished          *TaskRecord
		want                bool
	}{
		{name: "no anchor is a fresh send, never a fallback", lastTaskID: "", unfinished: nil, want: false},
		{name: "anchor unfinished means execution started, no fallback", lastTaskID: "task-a", unfinished: &TaskRecord{ID: "task-a", Status: "in_progress"}, want: false},
		{name: "no unfinished means pre-commit failure, fall back", lastTaskID: "task-a", unfinished: nil, want: true},
		{name: "different unfinished row, anchor untouched, fall back", lastTaskID: "task-a", unfinished: &TaskRecord{ID: "task-other", Status: "failed"}, want: true},
		{name: "anchor unfinished before send: retry fresh despite anchor row", lastTaskID: "task-a", anchorWasUnfinished: true, unfinished: &TaskRecord{ID: "task-a", Status: "failed"}, want: true},
		{name: "anchor unfinished before send, row since gone: retry fresh", lastTaskID: "task-a", anchorWasUnfinished: true, unfinished: nil, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager, _, _ := testManager(t)
			manager.SetTaskStore(&mockTaskStoreForResumable{unfinished: tc.unfinished})
			if got := manager.shouldRetryContinuationFresh("sess-1", tc.lastTaskID, tc.anchorWasUnfinished); got != tc.want {
				t.Errorf("shouldRetryContinuationFresh(%q, %v) = %v, want %v", tc.lastTaskID, tc.anchorWasUnfinished, got, tc.want)
			}
		})
	}
}

// TestEmitTaskComplete_SuccessSweepsStaleUnfinishedTasks verifies the healing
// half of the orphaned-task fix: a successful completion cancels leftover
// unfinished rows in the same session (orphans of an abandoned continuation)
// and resolves their persisted task_failed_resumable banners, so the session's
// has_unfinished_task flag finally clears and no useless resume banner is
// re-injected after an app restart. The just-completed task itself must not
// be touched.
func TestEmitTaskComplete_SuccessSweepsStaleUnfinishedTasks(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	store := &sweepableTaskStore{
		unfinishedQ: []*TaskRecord{
			{ID: "task-orphan", SessionID: "sess-1", Status: "in_progress"},
			{ID: "task-orphan-2", SessionID: "sess-1", Status: "failed"},
		},
	}
	manager.SetTaskStore(store)
	sessStore := &recordingSessionStore{}
	manager.SetSessionStore(sessStore)

	drainEvents(eventChan)

	// A successful result whose blackboard belongs to the completed task.
	result := &core.HandleResult{
		Output:     "done",
		Status:     orchestration.ExecutionStatusSuccess,
		Blackboard: NewPersistentBlackboard("task-done", "sess-1", NewTaskStoreAdapter(store), testLogger()),
	}
	manager.emitTaskComplete("sess-1", result, nil)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.cancelled) != 2 {
		t.Fatalf("expected both orphaned tasks cancelled, got %v", store.cancelled)
	}
	if store.cancelled[0] != "task-orphan" || store.cancelled[1] != "task-orphan-2" {
		t.Errorf("unexpected cancel order: %v", store.cancelled)
	}

	sessStore.mu.Lock()
	defer sessStore.mu.Unlock()
	if len(sessStore.resolved) != 2 {
		t.Fatalf("expected both orphan banners resolved, got %d calls", len(sessStore.resolved))
	}
	for i, call := range sessStore.resolved {
		if call.role != "task_failed_resumable" || call.matchField != "task_id" {
			t.Errorf("call %d: expected task_failed_resumable matched by task_id, got %s/%s", i, call.role, call.matchField)
		}
		if call.extra["decision"] != "cancelled" {
			t.Errorf("call %d: expected decision=cancelled, got %v", i, call.extra["decision"])
		}
	}

	// The success contract stays intact: task_complete only — the sweep must
	// not surface a resume banner or a service warning (the orphans are not
	// the completed task, so warnIfCompletionDidNotPersist stays silent).
	if n := countEvents(eventChan, "task_complete"); n != 1 {
		t.Errorf("expected exactly one task_complete event, got %d", n)
	}
	if n := countEvents(eventChan, "task_failed_resumable"); n != 0 {
		t.Errorf("expected no task_failed_resumable event on success sweep, got %d", n)
	}
	if n := countEvents(eventChan, "service"); n != 0 {
		t.Errorf("expected no service warnings, got %d", n)
	}
}

// TestEmitTaskComplete_SuccessNoSweepWithoutOrphans verifies the sweep is a
// no-op when the session has no leftover unfinished rows: a clean success
// must not write cancellations.
func TestEmitTaskComplete_SuccessNoSweepWithoutOrphans(t *testing.T) {
	manager, _, _ := testManager(t)

	store := &sweepableTaskStore{}
	manager.SetTaskStore(store)

	result := &core.HandleResult{
		Output:     "done",
		Status:     orchestration.ExecutionStatusSuccess,
		Blackboard: NewPersistentBlackboard("task-done", "sess-1", NewTaskStoreAdapter(store), testLogger()),
	}
	manager.emitTaskComplete("sess-1", result, nil)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.cancelled) != 0 {
		t.Errorf("expected no CancelTask calls, got %v", store.cancelled)
	}
}

// TestEmitTaskComplete_SuccessNoPersistableBlackboardSkipsSweep pins the
// sweep's symmetry with warnIfCompletionDidNotPersist: when a successful
// result carries no persistable blackboard, the sweep has no ID identifying
// the just-completed task and must cancel nothing — an unfinished row may
// belong to that very task (its completion write racing), and cancelling it
// would fabricate an orphan where none exists.
func TestEmitTaskComplete_SuccessNoPersistableBlackboardSkipsSweep(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	store := &sweepableTaskStore{
		unfinishedQ: []*TaskRecord{
			{ID: "task-maybe-live", SessionID: "sess-1", Status: "in_progress"},
		},
	}
	manager.SetTaskStore(store)

	drainEvents(eventChan)

	result := &core.HandleResult{
		Output: "done",
		Status: orchestration.ExecutionStatusSuccess,
	}
	manager.emitTaskComplete("sess-1", result, nil)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.cancelled) != 0 {
		t.Errorf("expected no CancelTask calls without a persistable blackboard, got %v", store.cancelled)
	}
}

// TestEmitTaskComplete_DegradedKeepsResumableNoSweep verifies the sweep runs
// ONLY on success: a degraded completion (partial/failed) leaves the
// resumable safety net untouched — no orphan cancellation may fire while the
// banner still offers a legitimate resume.
func TestEmitTaskComplete_DegradedKeepsResumableNoSweep(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	store := &sweepableTaskStore{
		unfinishedQ: []*TaskRecord{
			{ID: "task-resumable", SessionID: "sess-1", Status: "in_progress"},
		},
	}
	manager.SetTaskStore(store)

	drainEvents(eventChan)

	result := &core.HandleResult{
		Output: "partial output",
		Status: orchestration.ExecutionStatusPartial,
	}
	manager.emitTaskComplete("sess-1", result, nil)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.cancelled) != 0 {
		t.Errorf("expected no CancelTask calls on degraded completion, got %v", store.cancelled)
	}
	if n := countEvents(eventChan, "task_failed_resumable"); n != 1 {
		t.Errorf("expected the resumable banner on degraded completion, got %d", n)
	}
}
