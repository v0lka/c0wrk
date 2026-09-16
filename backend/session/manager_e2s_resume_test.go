package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/e2s"
)

// E2S resume tests: a task checkpointed mid-run (in_progress row + non-terminal
// e2s.E2SState persisted in task_e2s_state) must re-enter the E2S loop on
// ResumeTask with its Σ restored — the backend half of the app-restart
// contract. The loop itself (prompt assembly, patch merge, dispatch) is the
// core package's domain; these tests assert the manager/persistence wiring.

// TestResumeTask_RestartResumesE2STaskWithRestoredSigma is the manager-level
// app-restart acceptance criterion for E2S: a task checkpointed with a
// non-terminal (active) E2S state — exactly what a crash/shutdown leaves in
// task_e2s_state — resumes into the E2S loop with the restored Σ, not a fresh
// state. The resumed run's first LLM turn must see the checkpointed state
// contents (objective + findings marker) in its <state> block.
func TestResumeTask_RestartResumesE2STaskWithRestoredSigma(t *testing.T) {
	store := newInMemoryTaskStore()
	sessions := newMockSessionStore()

	// The "crash" checkpoint: a run interrupted mid-flight. task row
	// in_progress + a persisted, non-terminal Σ carrying a distinctive
	// finding a fresh state would never contain.
	seeded := e2s.NewE2SState("ship the e2s resume feature", time.Now().UTC().Add(-time.Minute))
	seeded.TurnCount = 4
	seeded.Status = e2s.StateStatusActive
	seeded.Sigma[e2s.CoreKeyFindings] = []any{"sigma-marker-restored-finding"}
	seededJSON, err := json.Marshal(seeded)
	if err != nil {
		t.Fatalf("marshal seeded state: %v", err)
	}

	ws := runtimeTempDir(t)
	eventChan := make(chan Event, 100)
	caller := &e2sRecordingLLM{}
	mgr := NewManager(functionalOrchestratorFactory(caller), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)
	mgr.SetSessionStore(sessions)
	mgr.SetProjectResolver(func(string) (string, error) { return ws, nil })

	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	seedSession(t, sessions, info.ID, testProjectID, "e2s crash checkpoint", false)

	store.mu.Lock()
	store.tasks["task-e2s-restart"] = TaskRecord{
		ID: "task-e2s-restart", SessionID: info.ID,
		OriginalRequest: "ship the e2s resume feature",
		RoutingDecision: emptyJSONObject, Plan: emptyJSONObject,
		Reflections: emptyJSONArray,
		Status:      "in_progress", CreatedAt: time.Now().Add(-2 * time.Minute),
	}
	store.e2sStates["task-e2s-restart"] = seededJSON
	store.mu.Unlock()

	// ResumeTask is the restart entry point (ResumeSession delegates here).
	if err := mgr.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}

	complete, ok := waitForEvent(eventChan, "task_complete", 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for task_complete event for the resumed E2S task")
	}
	data, ok := complete.Data.(TaskCompleteData)
	if !ok {
		t.Fatalf("expected TaskCompleteData, got %T", complete.Data)
	}
	if !data.Success {
		t.Errorf("expected successful completion, got completion=%q output=%q", data.Completion, data.Output)
	}
	if data.Output != "e2s resumed with restored sigma" {
		t.Errorf("expected the E2S finish answer as output, got %q", data.Output)
	}

	// Σ restoration: the resumed run's first turn saw the checkpointed state —
	// the objective AND the distinctive findings marker a fresh state cannot
	// carry (fresh findings are empty).
	users := caller.userMessages()
	if len(users) == 0 {
		t.Fatal("the resumed E2S run made no LLM calls")
	}
	first := users[0]
	if !strings.Contains(first, "ship the e2s resume feature") {
		t.Errorf("resumed <state> does not carry the checkpointed objective:\n%s", first)
	}
	if !strings.Contains(first, "sigma-marker-restored-finding") {
		t.Errorf("resumed <state> does not carry the checkpointed findings — Σ was not restored:\n%s", first)
	}
}

// TestResumeTask_E2STerminalStateNotResumed verifies the terminal-status
// guard: a checkpoint whose Σ status is terminal (cancelled) does NOT re-enter
// the E2S loop on resume — the task falls through to the plain resume path
// instead of resurrecting a finished run.
func TestResumeTask_E2STerminalStateNotResumed(t *testing.T) {
	store := newInMemoryTaskStore()
	sessions := newMockSessionStore()

	seeded := e2s.NewE2SState("already cancelled run", time.Now().UTC().Add(-time.Minute))
	seeded.Status = e2s.StateStatusCancelled
	seeded.Sigma[e2s.CoreKeyStatus] = string(e2s.StateStatusCancelled)
	seededJSON, err := json.Marshal(seeded)
	if err != nil {
		t.Fatalf("marshal seeded state: %v", err)
	}

	ws := runtimeTempDir(t)
	eventChan := make(chan Event, 100)
	caller := &e2sRecordingLLM{}
	mgr := NewManager(functionalOrchestratorFactory(caller), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)
	mgr.SetSessionStore(sessions)
	mgr.SetProjectResolver(func(string) (string, error) { return ws, nil })

	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	seedSession(t, sessions, info.ID, testProjectID, "terminal e2s checkpoint", false)

	store.mu.Lock()
	store.tasks["task-e2s-terminal"] = TaskRecord{
		ID: "task-e2s-terminal", SessionID: info.ID,
		OriginalRequest: "already cancelled run",
		RoutingDecision: emptyJSONObject, Plan: emptyJSONObject,
		Reflections: emptyJSONArray,
		Status:      "in_progress", CreatedAt: time.Now().Add(-2 * time.Minute),
	}
	store.e2sStates["task-e2s-terminal"] = seededJSON
	store.mu.Unlock()

	if err := mgr.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
		t.Fatalf("ResumeTask failed: %v", err)
	}

	// The plain Conductor resume runs to completion (finishLLM-style finish),
	// but the E2S loop must NOT have been entered: none of the recorded user
	// messages carry the E2S <state> block.
	for _, u := range caller.userMessages() {
		if strings.Contains(u, "<state>") {
			t.Fatalf("terminal E2S state re-entered the E2S loop; first state-bearing message:\n%s", u)
		}
	}
	_, ok := waitForEvent(eventChan, "task_complete", 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for task_complete — the plain resume must still complete")
	}
}

// TestAbandonE2SIfUnfinished_CancelsNonTerminalState verifies the cancel arm:
// a user cancel terminalizes the unfinished task's E2S state (status
// cancelled, Σ core status key in sync) so a later resume does not re-enter
// the loop with a stale Σ.
func TestAbandonE2SIfUnfinished_CancelsNonTerminalState(t *testing.T) {
	store := newInMemoryTaskStore()
	sessions := newMockSessionStore()

	ws := runtimeTempDir(t)
	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(&e2sRecordingLLM{}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)
	mgr.SetSessionStore(sessions)
	mgr.SetProjectResolver(func(string) (string, error) { return ws, nil })

	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	seedSession(t, sessions, info.ID, testProjectID, "e2s cancel", false)

	seeded := e2s.NewE2SState("cancelled e2s run", time.Now().UTC())
	seeded.Status = e2s.StateStatusActive
	seededJSON, err := json.Marshal(seeded)
	if err != nil {
		t.Fatalf("marshal seeded state: %v", err)
	}
	store.mu.Lock()
	store.tasks["task-e2s-cancel"] = TaskRecord{
		ID: "task-e2s-cancel", SessionID: info.ID,
		OriginalRequest: "cancelled e2s run",
		RoutingDecision: emptyJSONObject, Plan: emptyJSONObject,
		Reflections: emptyJSONArray,
		Status:      "in_progress", CreatedAt: time.Now().Add(-time.Minute),
	}
	store.e2sStates["task-e2s-cancel"] = seededJSON
	store.mu.Unlock()

	// The cancel-time abandonment (called by the manager's user-cancel
	// branches BEFORE the task row itself is terminalized).
	mgr.abandonE2SIfUnfinished(info.ID)

	adapter := NewTaskStoreAdapter(store)
	st, err := adapter.LoadE2SState("task-e2s-cancel")
	if err != nil {
		t.Fatalf("LoadE2SState: %v", err)
	}
	if st == nil {
		t.Fatal("expected the E2S state to remain persisted after abandonment")
	}
	if st.Status != e2s.StateStatusCancelled {
		t.Errorf("status = %q, want cancelled", st.Status)
	}
	if got := st.Sigma[e2s.CoreKeyStatus]; got != string(e2s.StateStatusCancelled) {
		t.Errorf("Σ core status key = %v, want %q", got, e2s.StateStatusCancelled)
	}
}

// TestAbandonUnfinishedTaskForE2S_TerminalizesStateAndClearsMatchingAnchor
// covers the E2S mode-takeover abandon (an E2S send finding an interrupted
// task). Unlike the goal takeover it must ALSO terminalize the abandoned run's
// Σ and drop the session's continuation anchor when it points at that task:
// runE2SLoop resumes any non-terminal Σ found under the anchor and seeds the
// loop from that Σ's objective (ResumeState takes precedence over the new
// message), so leaving either behind would make the new E2S message a silent
// no-op — the exact restart-then-send scenario (the anchor is restored from
// GetLatestTaskID, which is status-agnostic).
func TestAbandonUnfinishedTaskForE2S_TerminalizesStateAndClearsMatchingAnchor(t *testing.T) {
	store := newInMemoryTaskStore()
	sessions := newMockSessionStore()

	ws := runtimeTempDir(t)
	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(&e2sRecordingLLM{}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)
	mgr.SetSessionStore(sessions)
	mgr.SetProjectResolver(func(string) (string, error) { return ws, nil })

	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	seedSession(t, sessions, info.ID, testProjectID, "e2s takeover", false)

	seeded := e2s.NewE2SState("stale objective", time.Now().UTC())
	seeded.Status = e2s.StateStatusActive
	seededJSON, err := json.Marshal(seeded)
	if err != nil {
		t.Fatalf("marshal seeded state: %v", err)
	}
	store.mu.Lock()
	store.tasks["task-e2s-takeover"] = TaskRecord{
		ID: "task-e2s-takeover", SessionID: info.ID,
		OriginalRequest: "stale objective",
		RoutingDecision: emptyJSONObject, Plan: emptyJSONObject,
		Reflections: emptyJSONArray,
		Status:      "in_progress", CreatedAt: time.Now().Add(-time.Minute),
	}
	store.e2sStates["task-e2s-takeover"] = seededJSON
	store.mu.Unlock()

	// Simulate the app-restart restore: the continuation anchor points at the
	// interrupted task (GetLatestTaskID is status-agnostic).
	mgr.mu.RLock()
	sess := mgr.sessions[info.ID]
	mgr.mu.RUnlock()
	if sess == nil {
		t.Fatal("session not in memory after CreateSession")
	}
	sess.mu.Lock()
	sess.lastCompletedTaskID = "task-e2s-takeover"
	sess.mu.Unlock()

	mgr.abandonUnfinishedTaskForE2S(info.ID)

	store.mu.Lock()
	cancelled := store.tasks["task-e2s-takeover"].Status
	store.mu.Unlock()
	if cancelled != "cancelled" {
		t.Errorf("abandoned task status = %q, want cancelled", cancelled)
	}

	adapter := NewTaskStoreAdapter(store)
	st, err := adapter.LoadE2SState("task-e2s-takeover")
	if err != nil {
		t.Fatalf("LoadE2SState: %v", err)
	}
	if st == nil {
		t.Fatal("expected the E2S state to remain persisted after abandonment")
	}
	if st.Status != e2s.StateStatusCancelled {
		t.Errorf("Σ status = %q, want cancelled", st.Status)
	}
	if got := st.Sigma[e2s.CoreKeyStatus]; got != string(e2s.StateStatusCancelled) {
		t.Errorf("Σ core status key = %v, want %q", got, e2s.StateStatusCancelled)
	}

	sess.mu.Lock()
	anchor := sess.lastCompletedTaskID
	sess.mu.Unlock()
	if anchor != "" {
		t.Errorf("continuation anchor = %q, want cleared so a fresh E2S task starts", anchor)
	}
}

// TestAbandonUnfinishedTaskForE2S_KeepsUnrelatedAnchor verifies the anchor is
// dropped ONLY when it points at the abandoned task: when a different task was
// the last completed one, the anchor must survive so a normal E2S continuation
// still inherits the prior task's blackboard.
func TestAbandonUnfinishedTaskForE2S_KeepsUnrelatedAnchor(t *testing.T) {
	store := newInMemoryTaskStore()
	sessions := newMockSessionStore()

	ws := runtimeTempDir(t)
	eventChan := make(chan Event, 100)
	mgr := NewManager(functionalOrchestratorFactory(&e2sRecordingLLM{}), func(e Event) { eventChan <- e }, runtimeTempDir(t))
	t.Cleanup(mgr.Shutdown)
	mgr.SetTaskStore(store)
	mgr.SetSessionStore(sessions)
	mgr.SetProjectResolver(func(string) (string, error) { return ws, nil })

	info, err := mgr.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	seedSession(t, sessions, info.ID, testProjectID, "e2s takeover unrelated", false)

	seeded := e2s.NewE2SState("interrupted objective", time.Now().UTC())
	seeded.Status = e2s.StateStatusActive
	seededJSON, err := json.Marshal(seeded)
	if err != nil {
		t.Fatalf("marshal seeded state: %v", err)
	}
	store.mu.Lock()
	store.tasks["task-e2s-paused"] = TaskRecord{
		ID: "task-e2s-paused", SessionID: info.ID,
		OriginalRequest: "interrupted objective",
		RoutingDecision: emptyJSONObject, Plan: emptyJSONObject,
		Reflections: emptyJSONArray,
		Status:      "in_progress", CreatedAt: time.Now().Add(-time.Minute),
	}
	store.e2sStates["task-e2s-paused"] = seededJSON
	store.mu.Unlock()

	mgr.mu.RLock()
	sess := mgr.sessions[info.ID]
	mgr.mu.RUnlock()
	if sess == nil {
		t.Fatal("session not in memory after CreateSession")
	}
	sess.mu.Lock()
	sess.lastCompletedTaskID = "task-earlier-completed"
	sess.mu.Unlock()

	mgr.abandonUnfinishedTaskForE2S(info.ID)

	sess.mu.Lock()
	anchor := sess.lastCompletedTaskID
	sess.mu.Unlock()
	if anchor != "task-earlier-completed" {
		t.Errorf("continuation anchor = %q, want the unrelated anchor preserved", anchor)
	}
}
