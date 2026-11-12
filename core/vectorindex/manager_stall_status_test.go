package vectorindex

import (
	"strings"
	"testing"
	"time"
)

// syntheticStallTimers is the injected timer factory the stall-watchdog
// transition tests drive: every arm captures its callback (and the arm
// order), and the test fires them explicitly — no sleeps, no real timers.
type syntheticStallTimers struct {
	fired []func()
}

func (s *syntheticStallTimers) after(_ time.Duration, fn func()) *time.Timer {
	s.fired = append(s.fired, fn)
	return nil // heartbeat tolerates a nil timer; nothing real to stop
}

// TestPassWatchdog_StallTransitions pins the pass-liveness event pair the
// Manager's status and the frontend's stalled overlay feed on: fire opens a
// window (Stalled=true, stage/file evidence, live silence), the FIRST
// heartbeat after the fire closes it (Stalled=false), and a released
// watchdog reads as live again — a finished or cancelled pass is never
// stalled.
func TestPassWatchdog_StallTransitions(t *testing.T) {
	timers := &syntheticStallTimers{}
	wd := newPassWatchdog(nil, timers.after)
	t.Cleanup(wd.stop)

	var transitions []StallSnapshot
	wd.setOnStallChange(func(snap StallSnapshot) {
		transitions = append(transitions, snap)
	})
	if wd.watch == 0 {
		t.Fatal("watchdog has a zero identity — consumers could not tell watchdogs apart")
	}

	wd.start()
	if len(timers.fired) != 1 {
		t.Fatalf("start armed %d timers, want 1", len(timers.fired))
	}
	// Fire the armed callback: the watchdog's gen is still 0 — accepted.
	timers.fired[0]()
	if len(transitions) != 1 {
		t.Fatalf("fire produced %d transitions, want 1 (open)", len(transitions))
	}
	open := transitions[0]
	if !open.Stalled {
		t.Error("open transition reported Stalled=false")
	}
	if open.Watch != wd.watch {
		t.Errorf("open transition Watch = %d, want %d", open.Watch, wd.watch)
	}
	if open.Stage != "" || open.LastFile != "" {
		t.Errorf("open transition before any heartbeat carried evidence: stage %q file %q", open.Stage, open.LastFile)
	}

	// The first progress point resolves the window: Stalled=false, same
	// watchdog identity, and the snapshot the manager would read is clean.
	wd.heartbeat(StageEmbedding, "/ws/notes.md")
	if len(transitions) != 2 {
		t.Fatalf("heartbeat produced %d transitions total, want 2 (open+close)", len(transitions))
	}
	closeSnap := transitions[1]
	if closeSnap.Stalled {
		t.Error("resolve transition reported Stalled=true")
	}
	if closeSnap.Watch != wd.watch {
		t.Errorf("resolve transition Watch = %d, want %d", closeSnap.Watch, wd.watch)
	}
	if closeSnap.SilentFor != 0 {
		t.Errorf("resolve transition carried SilentFor %v, want 0", closeSnap.SilentFor)
	}

	// A second window opens with the progress evidence attached: the stall
	// names where progress stopped.
	timers.fired[1]()
	if len(transitions) != 3 {
		t.Fatalf("second fire produced %d transitions total, want 3", len(transitions))
	}
	second := transitions[2]
	if !second.Stalled || second.Stage != string(StageEmbedding) || second.LastFile != "/ws/notes.md" {
		t.Errorf("second open = %+v, want stalled with stage embedding and last file /ws/notes.md", second)
	}

	// A progress heartbeat WHILE the second window is open resolves it —
	// the close event fires, not a fabricated one.
	wd.heartbeat(StageBleveUpsert, "")
	if len(transitions) != 4 {
		t.Fatalf("progress heartbeat produced %d transitions total, want 4 (second close)", len(transitions))
	}
	if transitions[3].Stalled {
		t.Error("second resolve transition reported Stalled=true")
	}

	// After stop, the verdict reads live again — and firing the LAST armed
	// callback stays silent (the released pass must never flip the status
	// back to stalled; stop silences every pending fire).
	wd.stop()
	if snap := wd.snapshot(); snap.Stalled {
		t.Error("released watchdog still reads stalled")
	}
	timers.fired[len(timers.fired)-1]()
	if len(transitions) != 4 {
		t.Fatalf("fire after stop produced a transition (%d total), want 4", len(transitions))
	}
}

// TestPassWatchdog_Snapshot pins the poll path: snapshot reads the current
// verdict without transitioning anything, a nil watchdog reads live (no
// watchdog must not read as stalled), and the silence grows between reads.
func TestPassWatchdog_Snapshot(t *testing.T) {
	if snap := (*passWatchdog)(nil).snapshot(); snap.Stalled {
		t.Fatal("nil watchdog snapshot reported stalled")
	}

	timers := &syntheticStallTimers{}
	wd := newPassWatchdog(nil, timers.after)
	t.Cleanup(wd.stop)
	wd.start()
	if snap := wd.snapshot(); snap.Stalled {
		t.Error("fresh watchdog snapshot reported stalled before firing")
	}
	timers.fired[0]()
	snap := wd.snapshot()
	if !snap.Stalled {
		t.Fatal("fired watchdog snapshot reported live")
	}
	if snap.SilentFor < 0 {
		t.Errorf("snapshot silence = %v, want non-negative", snap.SilentFor)
	}

	// Resolving via snapshot-mutation-free path: the heartbeat clears the
	// verdict the next snapshot reads.
	wd.heartbeat(StageEmbedding, "")
	if snap := wd.snapshot(); snap.Stalled {
		t.Error("heartbeat did not clear the snapshot verdict")
	}
}

// TestManagerRecordStall_StatusAndCallback pins the manager-side sub-status:
// an open window surfaces in GetIndexStatus with live-growing silence, the
// transition reaches the OnStallChange status callback (the push stream), a
// watchdog resolves only its own window (a different watchdog's resolve must
// not erase a live stall), and any progress callback clears the evidence.
func TestManagerRecordStall_StatusAndCallback(t *testing.T) {
	var pushed []StallSnapshot
	m := &Manager{}
	m.projectCallbacks = ProjectCallbacks{
		OnStallChange: func(snap StallSnapshot) { pushed = append(pushed, snap) },
	}

	m.recordStall(StallSnapshot{Stalled: true, Stage: string(StageEmbedding), LastFile: "/ws/a.md", SilentFor: 300 * time.Second, Watch: 7})

	st := m.GetIndexStatus()
	if !st.Stall.Stalled || st.Stall.Stage != string(StageEmbedding) || st.Stall.LastFile != "/ws/a.md" {
		t.Fatalf("status stall = %+v, want open with stage/file evidence", st.Stall)
	}
	// The silence is recomputed live: at least the recorded 5 minutes,
	// growing past it between reads — never frozen at fire time.
	if st.Stall.SilentFor < 300*time.Second {
		t.Errorf("status silence = %v, want >= the recorded 300s", st.Stall.SilentFor)
	}
	if len(pushed) != 1 || !pushed[0].Stalled {
		t.Fatalf("pushed transitions = %+v, want the single open event", pushed)
	}

	// A DIFFERENT watchdog's resolve must not clear this live stall (two
	// overlapping passes on one indexer).
	m.recordStall(StallSnapshot{Stalled: false, Watch: 9})
	if st := m.GetIndexStatus(); !st.Stall.Stalled {
		t.Fatal("another watchdog's resolve erased a live stall it did not open")
	}
	if len(pushed) != 1 {
		t.Fatalf("foreign resolve was pushed (%d events), want 1", len(pushed))
	}

	// Its own resolve clears — and the resolve IS pushed so the event
	// stream mirrors the poll path.
	m.recordStall(StallSnapshot{Stalled: false, Watch: 7})
	if st := m.GetIndexStatus(); st.Stall.Stalled {
		t.Fatal("own resolve did not clear the stall")
	}
	if len(pushed) != 2 || pushed[1].Stalled {
		t.Fatalf("pushed transitions = %+v, want the close event second", pushed)
	}

	// Any progress callback is a liveness proof: a stall recorded before it
	// is gone.
	m.recordStall(StallSnapshot{Stalled: true, Stage: string(StageEmbedding), Watch: 7})
	m.wrapProgress(nil)(PhaseEmbedding, IndexStateIndexing, 1, 10, "/ws/b.md")
	if st := m.GetIndexStatus(); st.Stall.Stalled {
		t.Fatal("progress callback did not clear the stall")
	}

	// A status reset (initial/reset values) drops the evidence too — a new
	// attempt never inherits the old pass's stall.
	m.recordStall(StallSnapshot{Stalled: true, Watch: 7})
	m.setStatus(map[string]any{"branch": "main"})
	if st := m.GetIndexStatus(); st.Stall.Stalled {
		t.Fatal("status reset did not clear the stall")
	}
}

// TestManagerRecordStall_NilCallback pins the optional shape: without an
// OnStallChange consumer the status still records transitions (the RPC poll
// path works event-free).
func TestManagerRecordStall_NilCallback(t *testing.T) {
	m := &Manager{}
	m.recordStall(StallSnapshot{Stalled: true, Stage: string(StageChromemCommit), Watch: 3})
	if st := m.GetIndexStatus(); !st.Stall.Stalled || st.Stall.Stage != string(StageChromemCommit) {
		t.Fatalf("status stall = %+v, want open with the commit stage", st.Stall)
	}
}

// TestGoroutineDumpExported pins the on-demand diagnostics seam: the exported
// wrapper returns the same evidence shape the watchdog logs — a non-empty
// all-goroutine dump whose truncated flag is honest for the tiny real one.
func TestGoroutineDumpExported(t *testing.T) {
	dump, truncated := GoroutineDump()
	if !strings.Contains(dump, "goroutine ") {
		t.Fatal("goroutine dump without any goroutine header")
	}
	if truncated {
		t.Error("a live test process must not exhaust the 512 KiB dump cap")
	}
}
