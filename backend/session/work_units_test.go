package session

import (
	"context"
	"testing"

	"github.com/v0lka/c0wrk/core/units"
)

// TestGetSessionRuntimeStatus_WorkUnitSnapshot is the backend half of the
// restart-reconciliation acceptance criterion: after a simulated restart (the
// session is not in memory, its task row is still unfinished), the runtime
// status carries a work-unit snapshot so the frontend can align paused /
// interrupted delegate & plan-step chat blocks instead of leaving them
// "running".
//
// It also verifies the explicit settle: a unit left RUNNING by the crash (the
// resume funnel will not relaunch it) is durably settled as interrupted and a
// "work_unit_settled" event is emitted — while a PAUSED unit (a resumable
// checkpoint the funnel owns) is left untouched.
func TestGetSessionRuntimeStatus_WorkUnitSnapshot(t *testing.T) {
	manager, events, _ := testManager(t)
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()
	manager.SetTaskStore(store)

	const taskID = "task-work-units"
	// The in_progress task row makes the session resumable, which is what
	// makes the snapshot relevant after a restart.
	newUnitTestTask(t, store, sessionID, taskID)

	ledger := units.NewLedger(NewTaskStoreAdapter(store).UnitStore(), taskID, "")
	mustBegin := func(id string, status units.UnitStatus) {
		t.Helper()
		if err := ledger.Begin(units.UnitRecord{ID: id, Kind: units.UnitKindSubagent, Status: status}); err != nil {
			t.Fatalf("Begin(%s): %v", id, err)
		}
	}
	mustBegin("del_done", units.UnitStatusCompleted)
	mustBegin("del_paused", units.UnitStatusPaused)
	mustBegin("del_interrupted", units.UnitStatusRunning)
	mustBegin("del_pending", units.UnitStatusPending)

	status, err := manager.GetSessionRuntimeStatus(sessionID)
	if err != nil {
		t.Fatalf("GetSessionRuntimeStatus: %v", err)
	}
	if !status.HasUnfinishedTask || status.UnfinishedTaskID != taskID {
		t.Fatalf("unfinished task = %v/%q, want true/%q", status.HasUnfinishedTask, status.UnfinishedTaskID, taskID)
	}
	if status.Active {
		t.Fatal("session is not in memory — Active must be false")
	}

	got := map[string]string{}
	kinds := map[string]string{}
	for _, u := range status.WorkUnits {
		got[u.StepID] = u.Status
		kinds[u.StepID] = u.Kind
	}
	if len(got) != 4 {
		t.Fatalf("work_units = %+v, want 4 entries", status.WorkUnits)
	}
	want := map[string]string{
		"del_done":        "completed",
		"del_paused":      "paused",      // a resumable checkpoint — left untouched
		"del_interrupted": "interrupted", // abandoned in flight — explicitly settled
		"del_pending":     "interrupted", // never started, also abandoned
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("work_units[%s] = %q, want %q", id, got[id], w)
		}
		if kinds[id] != string(units.UnitKindSubagent) {
			t.Errorf("work_units[%s] kind = %q, want subagent", id, kinds[id])
		}
	}

	// The settle is durable: a fresh read of the store shows the abandoned
	// units as interrupted, and the paused one is still paused.
	recs, err := store.LoadTaskUnits(context.Background(), taskID)
	if err != nil {
		t.Fatalf("LoadTaskUnits: %v", err)
	}
	persisted := map[string]string{}
	for _, r := range recs {
		persisted[r.UnitID] = r.Status
	}
	if persisted["del_interrupted"] != "interrupted" {
		t.Errorf("persisted del_interrupted = %q, want interrupted", persisted["del_interrupted"])
	}
	if persisted["del_pending"] != "interrupted" {
		t.Errorf("persisted del_pending = %q, want interrupted", persisted["del_pending"])
	}
	if persisted["del_paused"] != "paused" {
		t.Errorf("persisted del_paused = %q, want paused (must not be settled)", persisted["del_paused"])
	}

	// An explicit settle event was emitted for each abandoned unit (and only
	// those) so a live view aligns too.
	settled := map[string]string{}
	for {
		select {
		case e := <-events:
			if e.Type != "work_unit_settled" {
				continue
			}
			data, ok := e.Data.(WorkUnitSettledData)
			if !ok {
				t.Fatalf("work_unit_settled payload = %T, want WorkUnitSettledData", e.Data)
			}
			settled[data.StepID] = data.Status
		default:
			goto drained
		}
	}
drained:
	if len(settled) != 2 {
		t.Fatalf("settled events = %+v, want exactly the 2 abandoned units", settled)
	}
	for _, id := range []string{"del_interrupted", "del_pending"} {
		if settled[id] != "interrupted" {
			t.Errorf("settled[%s] = %q, want interrupted", id, settled[id])
		}
	}

	// Idempotent: a second poll settles nothing more and emits no new event.
	status2, err := manager.GetSessionRuntimeStatus(sessionID)
	if err != nil {
		t.Fatalf("GetSessionRuntimeStatus (2nd): %v", err)
	}
	if len(status2.WorkUnits) != 4 {
		t.Fatalf("2nd snapshot = %+v, want 4 entries", status2.WorkUnits)
	}
	select {
	case e := <-events:
		t.Fatalf("unexpected event on idempotent re-poll: %s", e.Type)
	default:
	}
}

// TestGetSessionRuntimeStatus_WorkUnitsGracefulDegradation verifies the
// snapshot is empty (and the status still usable) when the manager has no task
// store — a status poll must never fail for a session with no persistence.
func TestGetSessionRuntimeStatus_WorkUnitsGracefulDegradation(t *testing.T) {
	manager, _, _ := testManager(t)

	status, err := manager.GetSessionRuntimeStatus("no-such-session")
	if err != nil {
		t.Fatalf("GetSessionRuntimeStatus: %v", err)
	}
	if status.HasUnfinishedTask || len(status.WorkUnits) != 0 {
		t.Errorf("status = %+v, want idle with no work units", status)
	}
}
