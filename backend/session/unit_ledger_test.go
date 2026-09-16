package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
)

// newUnitTestTask seeds a task row so unit writes have a parent task to hang
// off (task_units has an FK to tasks with ON DELETE CASCADE).
func newUnitTestTask(t *testing.T, store *SQLiteSessionStore, sessionID, taskID string) {
	t.Helper()
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: taskID, SessionID: sessionID, OriginalRequest: "req", Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
}

// TestUnitLedger_RoundTripThroughStore is the headline acceptance criterion: a
// unit round-trips (spec + status + steps) through the store via the ISOLATED
// constructor, and a second ledger over the same store reads it back from
// SQLite.
func TestUnitLedger_RoundTripThroughStore(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()
	const taskID = "task-units"
	newUnitTestTask(t, store, sessionID, taskID)

	adapter := NewTaskStoreAdapter(store)
	if adapter.UnitStore() == nil {
		t.Fatal("adapter.UnitStore() = nil, want a durable unit store")
	}

	// ISOLATED constructor: (taskStore, taskID, namespace).
	iso := units.NewLedger(adapter.UnitStore(), taskID, "verifier")
	spec := json.RawMessage(`{"condition":"tests pass"}`)
	if err := iso.Begin(units.UnitRecord{ID: "v1", Kind: units.UnitKindGoalVerification, Spec: spec}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := iso.Checkpoint("v1", []agent.Step{{Thought: "check"}, {Thought: "confirm"}}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if err := iso.Settle("v1", units.UnitStatusCompleted); err != nil {
		t.Fatalf("Settle: %v", err)
	}

	// A fresh ledger over a fresh adapter proves the write was durable, not
	// just resident in the writer's memory.
	fresh := units.NewLedger(NewTaskStoreAdapter(store).UnitStore(), taskID, "verifier")
	recs, err := fresh.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("List returned %d units, want 1", len(recs))
	}
	got := recs[0]
	if got.ID != "v1" || got.TaskID != taskID || got.Namespace != "verifier" {
		t.Errorf("identity = %+v, want v1/%s/verifier", got, taskID)
	}
	if got.Kind != units.UnitKindGoalVerification || got.Status != units.UnitStatusCompleted {
		t.Errorf("kind/status = %q/%q, want goal_verification/completed", got.Kind, got.Status)
	}
	if string(got.Spec) != string(spec) {
		t.Errorf("spec = %s, want %s", got.Spec, spec)
	}
	var steps []agent.Step
	if err := json.Unmarshal(got.Steps, &steps); err != nil {
		t.Fatalf("unmarshal steps: %v", err)
	}
	if len(steps) != 2 || steps[0].Thought != "check" || steps[1].Thought != "confirm" {
		t.Errorf("steps = %+v, want two thoughts", steps)
	}

	// The row really lives in the task_units table under the task.
	rows, err := store.LoadTaskUnits(context.Background(), taskID)
	if err != nil {
		t.Fatalf("LoadTaskUnits: %v", err)
	}
	if len(rows) != 1 || rows[0].UnitID != "v1" || rows[0].Status != "completed" || rows[0].Namespace != "verifier" {
		t.Fatalf("persisted row = %+v, want v1/completed/verifier", rows)
	}
}

// TestUnitLedger_MainlineAndIsolatedShareTask is the second acceptance
// criterion: the mainline constructor (a ledger-backed persistent blackboard)
// and the isolated constructor both persist units under the SAME task, so each
// ledger sees both units.
func TestUnitLedger_MainlineAndIsolatedShareTask(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()
	const taskID = "task-shared-units"
	newUnitTestTask(t, store, sessionID, taskID)

	adapter := NewTaskStoreAdapter(store)

	// MAINLINE constructor: ledger-backed persistent blackboard.
	pb := NewPersistentBlackboard(taskID, sessionID, adapter, nil)
	mainline := core.NewBlackboardLedger(pb)
	if pb.UnitLedger() != mainline {
		t.Error("NewBlackboardLedger should return the blackboard's cached ledger")
	}

	// ISOLATED constructor.
	isolated := units.NewLedger(adapter.UnitStore(), taskID, "verifier")

	if err := mainline.Begin(units.UnitRecord{ID: "step_1", Kind: units.UnitKindPlanStep}); err != nil {
		t.Fatalf("mainline Begin: %v", err)
	}
	if err := isolated.Begin(units.UnitRecord{ID: "step_1", Kind: units.UnitKindGoalVerification}); err != nil {
		t.Fatalf("isolated Begin: %v", err)
	}

	// Both constructors wrote under the same task (same unit id, different
	// namespace), so each ledger lists both records.
	mainRecs, err := mainline.List()
	if err != nil {
		t.Fatalf("mainline List: %v", err)
	}
	if len(mainRecs) != 2 {
		t.Fatalf("mainline List returned %d units, want 2", len(mainRecs))
	}
	for _, rec := range mainRecs {
		if rec.TaskID != taskID {
			t.Errorf("unit %s task = %q, want %q", rec.ID, rec.TaskID, taskID)
		}
	}

	isoRecs, err := isolated.List()
	if err != nil {
		t.Fatalf("isolated List: %v", err)
	}
	if len(isoRecs) != 2 {
		t.Fatalf("isolated List returned %d units, want 2", len(isoRecs))
	}
	kinds := map[string]units.UnitKind{}
	for _, rec := range isoRecs {
		kinds[rec.Namespace] = rec.Kind
	}
	if kinds[""] != units.UnitKindPlanStep || kinds["verifier"] != units.UnitKindGoalVerification {
		t.Errorf("namespaced kinds = %+v, want plan_step + goal_verification", kinds)
	}
}

// TestSettleTaskUnitStatusIfInFlight_TargetedAndConditional verifies the
// column-scoped conditional settle the session-runtime snapshot uses: it
// transitions only an in-flight unit, reports the transition (so concurrent
// callers have exactly one winner), and leaves the spec and checkpoint — the
// columns a whole-record upsert could drop — untouched.
func TestSettleTaskUnitStatusIfInFlight_TargetedAndConditional(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()
	const taskID = "task-targeted-settle"
	newUnitTestTask(t, store, sessionID, taskID)

	adapter := NewTaskStoreAdapter(store)
	us := adapter.UnitStore()
	l := units.NewLedger(us, taskID, "")
	if err := l.Begin(units.UnitRecord{ID: "u1", Kind: units.UnitKindSubagent, Status: units.UnitStatusRunning, Spec: json.RawMessage(`{"a":1}`)}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := l.Checkpoint("u1", []agent.Step{{Thought: "kept"}}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	changed, err := us.SettleUnitStatusIfInFlight(taskID, "", "u1", units.UnitStatusInterrupted)
	if err != nil || !changed {
		t.Fatalf("SettleUnitStatusIfInFlight = %v/%v, want true/nil", changed, err)
	}
	if changed, err := us.SettleUnitStatusIfInFlight(taskID, "", "u1", units.UnitStatusInterrupted); err != nil || changed {
		t.Errorf("second settle = %v/%v, want false/nil (already out of flight)", changed, err)
	}
	if changed, _ := us.SettleUnitStatusIfInFlight(taskID, "other", "u1", units.UnitStatusInterrupted); changed {
		t.Error("a wrong namespace must not settle")
	}
	if changed, _ := us.SettleUnitStatusIfInFlight(taskID, "", "missing", units.UnitStatusInterrupted); changed {
		t.Error("an unknown id must not settle")
	}

	rows, err := store.LoadTaskUnits(context.Background(), taskID)
	if err != nil {
		t.Fatalf("LoadTaskUnits: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want 1", rows)
	}
	row := rows[0]
	if row.Status != "interrupted" {
		t.Errorf("status = %q, want interrupted", row.Status)
	}
	if string(row.Spec) != `{"a":1}` {
		t.Errorf("spec = %s, want it preserved (a targeted settle must not drop it)", row.Spec)
	}
	var steps []agent.Step
	if err := json.Unmarshal(row.Steps, &steps); err != nil || len(steps) != 1 || steps[0].Thought != "kept" {
		t.Errorf("steps = %s (err %v), want the stored checkpoint preserved", row.Steps, err)
	}
}

// TestTaskUnits_MigrationAdditiveAndIdempotent verifies the migration is
// additive: re-running table creation (as a later app open would) leaves
// existing unit rows intact.
func TestTaskUnits_MigrationAdditiveAndIdempotent(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()
	const taskID = "task-migrate-units"
	newUnitTestTask(t, store, sessionID, taskID)

	if err := store.SaveTaskUnit(context.Background(), TaskUnitRecord{
		TaskID: taskID, UnitID: "u1", Status: "pending", Spec: json.RawMessage(`{}`),
		Steps: json.RawMessage(`[]`), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTaskUnit: %v", err)
	}

	if err := store.createTables(); err != nil {
		t.Fatalf("re-running createTables: %v", err)
	}
	rows, err := store.LoadTaskUnits(context.Background(), taskID)
	if err != nil {
		t.Fatalf("LoadTaskUnits after re-migration: %v", err)
	}
	if len(rows) != 1 || rows[0].UnitID != "u1" {
		t.Fatalf("row lost across additive migration: %+v", rows)
	}
}

// taskStoreWithoutUnits satisfies TaskStore but exposes no unit persistence:
// embedding the TaskStore interface caps the method set at the interface's, so
// the optional unitTaskStore assertion fails.
type taskStoreWithoutUnits struct{ TaskStore }

// TestUnitLedger_GracefulDegradationWithoutUnitStore verifies the ledger
// degrades to best-effort in-memory operation when the store has no unit
// persistence.
func TestUnitLedger_GracefulDegradationWithoutUnitStore(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()
	const taskID = "task-nounit"
	newUnitTestTask(t, store, sessionID, taskID)

	adapter := NewTaskStoreAdapter(taskStoreWithoutUnits{store})
	if got := adapter.UnitStore(); got != nil {
		t.Fatalf("UnitStore() = %v, want nil for a store without unit persistence", got)
	}

	l := units.NewLedger(adapter.UnitStore(), taskID, "verifier")
	if err := l.Begin(units.UnitRecord{ID: "u1"}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	recs, err := l.List()
	if err != nil || len(recs) != 1 {
		t.Fatalf("List = %+v, %v; want one in-memory unit", recs, err)
	}
	// Best-effort in-memory means nothing reached the database.
	rows, err := store.LoadTaskUnits(context.Background(), taskID)
	if err != nil {
		t.Fatalf("LoadTaskUnits: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected no persisted rows, got %+v", rows)
	}
}

// TestTaskUnits_CascadeOnSessionDelete verifies unit rows are removed with
// their task/session (ON DELETE CASCADE).
func TestTaskUnits_CascadeOnSessionDelete(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()
	const taskID = "task-cascade-units"
	newUnitTestTask(t, store, sessionID, taskID)

	if err := store.SaveTaskUnit(context.Background(), TaskUnitRecord{
		TaskID: taskID, UnitID: "u1", Status: "running", Steps: json.RawMessage(`[]`),
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTaskUnit: %v", err)
	}
	if err := store.DeleteSession(context.Background(), sessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	rows, err := store.LoadTaskUnits(context.Background(), taskID)
	if err != nil {
		t.Fatalf("LoadTaskUnits after cascade: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows after cascade, got %+v", rows)
	}
}
