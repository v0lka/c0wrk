package session

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func newWindowTestEmitter() (e *EventEmitter, ch chan Event) {
	ch = make(chan Event, 256)
	e = NewEventEmitter("sess-window", func(evt Event) {
		select {
		case ch <- evt:
		default:
		}
	})
	return e, ch
}

func TestExecutionWindow_ToolCallRingIsBoundedAndCountersCumulate(t *testing.T) {
	e, _ := newWindowTestEmitter()

	total := executionWindowMaxEntries + 5
	for i := 1; i <= total; i++ {
		e.ToolCall(i, 0, "bash", `{"command":"ls"}`, "core")
	}

	snap := e.ExecutionWindowSnapshot()
	if len(snap.Entries) != executionWindowMaxEntries {
		t.Fatalf("window entries = %d, want %d (bounded)", len(snap.Entries), executionWindowMaxEntries)
	}
	if snap.Counters.ToolCalls != total {
		t.Errorf("ToolCalls = %d, want %d (counters are cumulative, not windowed)", snap.Counters.ToolCalls, total)
	}
	// Oldest entries were evicted: the first retained entry is step
	// (total - executionWindowMaxEntries + 1).
	wantFirst := total - executionWindowMaxEntries + 1
	if snap.Entries[0].Step != wantFirst {
		t.Errorf("first retained step = %d, want %d (oldest evicted)", snap.Entries[0].Step, wantFirst)
	}
	if !snap.AnyWork() {
		t.Error("AnyWork must be true once entries exist")
	}
}

func TestExecutionWindow_SharedAcrossScopedCopies(t *testing.T) {
	e, _ := newWindowTestEmitter()
	scoped, ok := e.WithPlanStepID("step-1").(*EventEmitter)
	if !ok {
		t.Fatal("WithPlanStepID must return an *EventEmitter")
	}
	scoped.ToolCall(1, 0, "read_file", `{"path":"a.go"}`, "core")
	scoped.ToolResult(1, 0, 2, "ok", false)

	if got := len(e.ExecutionWindowSnapshot().Entries); got != 2 {
		t.Fatalf("root window entries = %d, want 2 (window shared across copies)", got)
	}
	if got := e.ExecutionWindowSnapshot().Counters.Steps; got != 0 {
		t.Errorf("unexpected steps counter: %d", got)
	}
}

func TestExecutionWindow_CountersFromResultsStepsAndDiagnostics(t *testing.T) {
	e, _ := newWindowTestEmitter()

	e.ToolCall(1, 0, "bash", "{}", "core")                     // counts one tool call
	e.ToolResult(1, 0, 4, "boom", true)                        // counts one tool error
	e.ToolResult(1, 1, 20, "validation error: bad arg", false) // counts one invalid tool call
	e.StepComplete(1, 0)                                       // counts one step
	e.ExecutorDiagnostic(2, "fruitless_abort", nil)            // counts one abort
	e.ExecutorDiagnostic(2, "repeated_tool_call_nudge", nil)   // counts one nudge
	e.ExecutorDiagnostic(3, "parse_error_abort", nil)          // counts one abort and one parse error
	e.ExecutorDiagnostic(3, "checklist_stale_nudge", nil)      // informational, ignored

	c := e.ExecutionWindowSnapshot().Counters
	if c.ToolCalls != 1 || c.ToolErrors != 1 || c.InvalidToolCalls != 1 {
		t.Errorf("tool counters = %+v", c)
	}
	if c.Steps != 1 {
		t.Errorf("Steps = %d, want 1", c.Steps)
	}
	if c.Aborts != 2 {
		t.Errorf("Aborts = %d, want 2", c.Aborts)
	}
	if c.Nudges != 1 {
		t.Errorf("Nudges = %d, want 1 (informational diagnostics excluded)", c.Nudges)
	}
	if c.ParseErrors != 1 {
		t.Errorf("ParseErrors = %d, want 1", c.ParseErrors)
	}
}

func TestExecutionWindow_ResetsOnAgentMetrics(t *testing.T) {
	e, _ := newWindowTestEmitter()
	e.ToolCall(1, 0, "bash", "{}", "core")
	e.StepComplete(1, 0)
	if !e.ExecutionWindowSnapshot().AnyWork() {
		t.Fatal("expected recorded work before reset")
	}

	e.EmitAgentMetrics("full")

	snap := e.ExecutionWindowSnapshot()
	if snap.AnyWork() || len(snap.Entries) != 0 {
		t.Errorf("window must be empty after agent_metrics, got %d entries", len(snap.Entries))
	}
	if snap.Counters != (ExecutionWindowCounters{}) {
		t.Errorf("counters must reset with the window, got %+v", snap.Counters)
	}
}

func TestExecutionWindow_PlanProgressSnapshot(t *testing.T) {
	e, _ := newWindowTestEmitter()
	e.PlanGenerated(3, nil)
	e.PlanStepStart("s1", "first", "")

	snap := e.ExecutionWindowSnapshot()
	if snap.PlanTotal != 3 || snap.PlanDone != 0 || snap.PlanStep != "s1" {
		t.Fatalf("plan snapshot = %+v, want total=3 done=0 step=s1", snap)
	}

	e.PlanStepComplete("s1", true, 0, "")
	snap = e.ExecutionWindowSnapshot()
	if snap.PlanDone != 1 || snap.PlanStep != "" {
		t.Errorf("after completion: done=%d step=%q, want done=1 step=''", snap.PlanDone, snap.PlanStep)
	}
}

func TestExecutionWindow_ToolResultDetailTruncated(t *testing.T) {
	e, _ := newWindowTestEmitter()
	long := strings.Repeat("x", executionWindowMaxDetail*3)
	e.ToolResult(1, 0, len(long), long, false)
	snap := e.ExecutionWindowSnapshot()
	if len(snap.Entries) != 1 {
		t.Fatalf("entries = %d", len(snap.Entries))
	}
	detail := snap.Entries[0].Detail
	if utf8.RuneCountInString(detail) > executionWindowMaxDetail {
		t.Errorf("detail runes = %d, want <= %d", utf8.RuneCountInString(detail), executionWindowMaxDetail)
	}
	if !strings.HasSuffix(detail, "…") {
		t.Errorf("truncated detail must carry an ellipsis marker, got %q", detail)
	}
}
