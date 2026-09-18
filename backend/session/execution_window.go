// Host-side execution window.
//
// The step-limit HITL boundary is decided by an LLM judge (silent mode,
// security.silent_mode.step_limit.mode = "auto"). To decide well the judge
// needs more than the terse boundary reason it is handed: it needs the recent
// trajectory and the run's quality counters. This file keeps that evidence,
// fed by the session emitter as executor events flow through it (tool calls,
// tool results, step completions and executor diagnostics), so the decision
// context is host-owned and independent of any executor-internal state.
package session

import (
	"sync"

	"github.com/v0lka/sp4rk/strutil"
)

// executionWindowMaxEntries bounds the recent-execution ring. The window is a
// digest for the judge's prompt, not a transcript replay, so it stays small.
const executionWindowMaxEntries = 24

// executionWindowMaxDetail bounds each recorded detail string (a tool's args
// preview or a result preview) so a single large payload cannot dominate the
// window.
const executionWindowMaxDetail = 160

// Execution-window entry kinds. Exported so decision-layer consumers (the
// silent-mode step-limit judge) can classify entries without duplicating the
// string literals.
const (
	ExecWindowToolCallKind     = "tool_call"
	ExecWindowToolResultKind   = "tool_result"
	ExecWindowStepCompleteKind = "step_complete"
	ExecWindowDiagnosticKind   = "diagnostic"
)

// ExecutionWindowEntry is one recorded executor event. It is deliberately
// small: a step index, a kind, an optional tool/detail, and an error flag.
type ExecutionWindowEntry struct {
	Step    int    `json:"step"`
	Kind    string `json:"kind"`
	Tool    string `json:"tool,omitempty"`
	Detail  string `json:"detail,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// ExecutionWindowCounters are the cumulative quality counters for the current
// task run. They mirror the buckets metricsState reports in agent_metrics so
// the judge sees the same evidence at the boundary that the finish report
// presents at the end of the run.
type ExecutionWindowCounters struct {
	Steps            int `json:"steps"`
	ToolCalls        int `json:"tool_calls"`
	ToolErrors       int `json:"tool_errors"`
	Nudges           int `json:"nudges"`
	Aborts           int `json:"aborts"`
	ParseErrors      int `json:"parse_errors"`
	InvalidToolCalls int `json:"invalid_tool_calls"`
}

// ExecutionWindowSnapshot is an immutable copy of the window at a point in
// time, together with the emitter's plan-progress view. Entries are ordered
// oldest-first. PlanStep is the currently running plan step ID (may be empty).
type ExecutionWindowSnapshot struct {
	Entries   []ExecutionWindowEntry
	Counters  ExecutionWindowCounters
	PlanTotal int
	PlanDone  int
	PlanStep  string
}

// AnyWork reports whether the window holds at least one recorded entry. A
// boundary reached with no recorded activity carries no trajectory to judge on.
func (s ExecutionWindowSnapshot) AnyWork() bool { return len(s.Entries) > 0 }

// executionWindow is an emitter-shared, bounded ring of the most recent
// executor events for a session plus cumulative run counters. A single
// instance is shared by an EventEmitter and all of its scoped copies
// (WithPlanStepID / WithRetryAttempt) — mirroring metricsState and tokenState —
// so delegated subagent activity is visible at the boundary too. Safe for
// concurrent use.
type executionWindow struct {
	mu       sync.Mutex
	entries  []ExecutionWindowEntry
	counters ExecutionWindowCounters
}

// pushLocked appends an entry and trims the ring to executionWindowMaxEntries.
// Callers must hold w.mu.
func (w *executionWindow) pushLocked(e ExecutionWindowEntry) {
	w.entries = append(w.entries, e)
	if len(w.entries) > executionWindowMaxEntries {
		w.entries = append(w.entries[:0], w.entries[len(w.entries)-executionWindowMaxEntries:]...)
	}
}

func (w *executionWindow) recordToolCall(step int, tool, args string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.counters.ToolCalls++
	w.pushLocked(ExecutionWindowEntry{
		Step:   step,
		Kind:   ExecWindowToolCallKind,
		Tool:   tool,
		Detail: strutil.TruncateUTF8(args, executionWindowMaxDetail),
	})
}

func (w *executionWindow) recordToolResult(step int, result string, isError bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if isError {
		w.counters.ToolErrors++
	}
	if isInvalidToolCall(result) {
		w.counters.InvalidToolCalls++
	}
	w.pushLocked(ExecutionWindowEntry{
		Step:    step,
		Kind:    ExecWindowToolResultKind,
		Detail:  strutil.TruncateUTF8(result, executionWindowMaxDetail),
		IsError: isError,
	})
}

func (w *executionWindow) recordStep(step int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.counters.Steps++
	w.pushLocked(ExecutionWindowEntry{Step: step, Kind: ExecWindowStepCompleteKind})
}

func (w *executionWindow) recordDiagnostic(step int, event string) {
	nudge, abort, parse := diagnosticBuckets(event)
	w.mu.Lock()
	defer w.mu.Unlock()
	if nudge {
		w.counters.Nudges++
	}
	if abort {
		w.counters.Aborts++
	}
	if parse {
		w.counters.ParseErrors++
	}
	w.pushLocked(ExecutionWindowEntry{Step: step, Kind: ExecWindowDiagnosticKind, Detail: event})
}

// reset clears the window for a new task run (called where metricsState is
// snapshotted, so both cover exactly one run).
func (w *executionWindow) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = nil
	w.counters = ExecutionWindowCounters{}
}

// copyEntries returns a copy of the recorded entries (oldest-first).
func (w *executionWindow) copyEntries() []ExecutionWindowEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.entries) == 0 {
		return nil
	}
	out := make([]ExecutionWindowEntry, len(w.entries))
	copy(out, w.entries)
	return out
}

// copyCounters returns a copy of the cumulative counters.
func (w *executionWindow) copyCounters() ExecutionWindowCounters {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.counters
}

// diagnosticBuckets classifies an executor diagnostic into the quality buckets
// the window counts. It mirrors the event mapping in metricsState.observeDiagnostic
// (agent_metrics.go) — keep the two in sync when a loop-detector event is added.
// Informational diagnostics (executor_finish_nudge, pre_compaction_nudge,
// checklist_* and friends) map to no bucket.
func diagnosticBuckets(event string) (nudge, abort, parse bool) {
	switch event {
	case "repeated_tool_call_nudge", "same_tool_repeat_nudge", "fruitless_nudge":
		return true, false, false
	case "parse_error_nudge", "tool_call_syntax_nudge":
		return true, false, true
	case "repeated_tool_call_abort", "same_tool_repeat_abort", "fruitless_abort", "truncation_abort":
		return false, true, false
	case "parse_error_abort", "tool_call_syntax_abort":
		return false, true, true
	default:
		return false, false, false
	}
}
