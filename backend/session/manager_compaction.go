package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/llm"
)

// ErrCompactionInFlight is returned by CompactSessionContext when a manual
// compaction is already running for the session. The UI locks the compact
// button while compacting, so this is the server-side backstop for racing
// callers.
var ErrCompactionInFlight = errors.New("session compaction is already in flight")

// ErrSessionCompacting is returned by SendMessage/ResumeTask while a manual
// context compaction is running. The compaction flow needs an idle window (it
// waits for any running task to reach a cooperative pause checkpoint first);
// a send or resume in that window would race the history swap. The UI mirrors
// this by locking the input and the pause/resume controls.
var ErrSessionCompacting = errors.New("session is compacting context — wait for it to finish")

// validCompactionStrategies lists the strategy names accepted by
// CompactSessionContext. They mirror sp4rk's NewCompactionStrategy names; the
// check exists so an invalid name fails fast (before any pause/resume
// churn) with a clear error instead of failing mid-flow.
var validCompactionStrategies = []string{"sliding_window", "summarization", "hierarchical"}

// compactMarkerRole is the persisted ChatMessage role marking a manual context
// compaction. The row renders as the existing context-compaction card on
// reload (chatUtils maps the role) and carries the compacted history snapshot
// in its metadata so history restore (convertChatMessagesToLLM) can rebuild
// the LLM-visible conversation exactly as it was after compaction.
const compactMarkerRole = "context_compaction"

// CompactSessionContext starts a MANUAL context compaction for the session:
// the session's cross-task conversation history (what the orchestrator injects
// as prior conversation into every request) is compacted with the named
// strategy. The UI chat history is untouched — only the LLM-visible context
// shrinks.
//
// Flow (asynchronous — returns immediately after validation):
//  1. compaction_started is emitted and the session is flagged compacting
//     (sends/resumes now fail with ErrSessionCompacting).
//  2. If a task is running, the flow pauses it exactly like PauseSession
//     (cooperative pause at the next step boundary) and waits for the request
//     goroutine to exit — the same wait semantics as a user-initiated pause.
//  3. The orchestrator's history is compacted (orchestrator.
//     CompactConversationHistory). LLM-backed strategies run their summarize
//     calls through the session's tracking caller, so tokens are counted.
//     A no-op compaction (ErrNothingCompacted — the dialogue already fits
//     within the strategy's limits) is NOT a failure: the flow finishes
//     successfully with nothing_compacted=true and no marker row.
//     3.5. When nothing was compacted but a paused unfinished task is about to
//     be auto-resumed, the compaction is deferred to the resume instead:
//     the orchestrator's one-shot resume-compaction request is armed
//     (RequestResumeCompaction) BEFORE the auto-resume below, so the resumed
//     Conductor run force-compacts the merged trajectory up front — the real
//     numbers then arrive as the executor's context_compaction card.
//  4. On success a marker row (role context_compaction) is persisted with the
//     compacted history snapshot, so a restart restores the compacted history.
//     Skipped for the no-op outcome (phase 3): there is no compacted history
//     to snapshot.
//  5. If THIS flow paused the session, a paused checkpoint remains, AND the
//     pause is still owned by this flow (a user pause is never stolen: one
//     that races or follows the flow's own pause overwrites the owner, and
//     one already in flight when the flow armed keeps the user as owner —
//     either way the user's pause survives), the task is auto-resumed.
//     compaction_finished is emitted with the outcome (success / cancelled /
//     error + whether the session was resumed, nothing was compacted, or the
//     compaction was deferred to the resume).
//
// Cancellation (CancelSessionCompaction) during the pause-wait still waits for
// the checkpoint to land (unflipping the pause signal mid-flight would race
// the executor's step-boundary check) and then skips the compaction and
// auto-resumes; during the compaction itself it aborts the summarize calls,
// leaving the history untouched.
func (m *Manager) CompactSessionContext(ctx context.Context, sessionID, strategy string) error {
	if !slices.Contains(validCompactionStrategies, strategy) {
		return fmt.Errorf("unknown compaction strategy %q (valid: %s)", strategy, strings.Join(validCompactionStrategies, ", "))
	}
	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	session.mu.Lock()
	if session.Archived {
		session.mu.Unlock()
		return ErrSessionArchived
	}
	if session.compacting {
		session.mu.Unlock()
		return ErrCompactionInFlight
	}
	orch := session.orchestrator
	if orch == nil {
		session.mu.Unlock()
		return errors.New("orchestrator not initialized")
	}
	session.compacting = true
	compCtx, cancel := context.WithCancel(context.Background())
	session.compactCancel = cancel
	compactDone := make(chan struct{})
	session.compactDone = compactDone
	// If a task is running, take it to a cooperative pause checkpoint first —
	// the same mechanism PauseSession uses (pausing window + signal flip).
	// Recording the pause owner lets phase 5 distinguish this flow's pause
	// from a user-initiated one: a user pause is NEVER stolen — neither one
	// that lands after this arming (PauseSession overwrites the owner) nor
	// one that is already in flight when the arming happens (a user-owned
	// pause is left owned by the user). Either way the flow leaves the task
	// paused in phase 5 instead of silently resuming an operator pause.
	// doneCh closes when the request goroutine finishes its epilogue.
	var doneCh chan struct{}
	pausedForCompaction := false
	if session.active {
		pausedForCompaction = true
		session.pausing = true
		if session.pauseOwner != pauseOwnerUser {
			session.pauseOwner = pauseOwnerCompaction
		}
		doneCh = session.done
	}
	session.mu.Unlock()

	if pausedForCompaction {
		orch.PauseSession()
	}

	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "compaction_started",
		Data:      CompactionStartedEventData{Strategy: strategy},
	})

	go m.runSessionCompaction(compCtx, cancel, compactDone, sessionID, session, orch, strategy, doneCh, pausedForCompaction)
	return nil
}

// runSessionCompaction executes the manual-compaction flow on a background
// goroutine (see CompactSessionContext for the phase description). The
// session's compacting flag and compactCancel are owned by this flow: they are
// cleared here before the auto-resume so the resumed task is not rejected by
// the compacting guards.
func (m *Manager) runSessionCompaction(compCtx context.Context, cancel context.CancelFunc, compactDone chan struct{}, sessionID string, session *Session, orch *core.Orchestrator, strategy string, doneCh chan struct{}, pausedForCompaction bool) {
	defer cancel()
	// Shutdown joins this goroutine via compactDone: everything after this
	// line must tolerate the backend tearing down around it.
	defer close(compactDone)

	// Phase 2: wait for the running request to reach its pause checkpoint and
	// exit. A cancellation during the wait still waits for the checkpoint
	// (skipping the compaction afterwards) — unflipping the pause signal
	// mid-flight would race the executor's step-boundary check and could leave
	// the session paused with nobody to resume it.
	cancelled := false
	if doneCh != nil {
		select {
		case <-doneCh:
			// The checkpoint and the cancellation raced: both channels were
			// ready, and select picked one at random. Honour the cancellation —
			// it was requested no later than the checkpoint landing, so the
			// compaction is skipped like any other pause-wait cancellation.
			if compCtx.Err() != nil {
				cancelled = true
			}
		case <-compCtx.Done():
			cancelled = true
			<-doneCh
		}
	}

	// Phase 3: compact the orchestrator's conversation history. A no-op
	// compaction (ErrNothingCompacted — the dialogue already fits within the
	// strategy's limits) is not a failure: the sentinel returns zero
	// percentages and leaves the history untouched, so the flow finishes
	// successfully with nothing_compacted=true.
	var before, after float64
	var err error
	nothingCompacted := false
	if !cancelled {
		before, after, err = orch.CompactConversationHistory(compCtx, strategy)
		if errors.Is(err, core.ErrNothingCompacted) {
			nothingCompacted = true
			err = nil
		} else if err != nil && compCtx.Err() != nil {
			// The summarize calls were aborted by the cancellation — report it
			// as a cancellation, not a failure.
			cancelled = true
			err = nil
		}
	}
	// Shutdown cancels the flow's context (Manager.Shutdown): the tail phases
	// below must not run against a torn-down backend — arming a
	// resume-compaction for a dead process, persisting a marker into a DB
	// that is closing, or auto-resuming a task after Shutdown declared all
	// tasks stopped. The paused checkpoint (if any) is preserved and
	// persisted by Shutdown's own persistPauseIfUnfinished.
	shuttingDown := m.shuttingDown.Load()
	if shuttingDown {
		cancelled = true
	}

	// Phase 3.5: when nothing was compacted but a paused unfinished task
	// waits (for this flow's auto-resume, or a later user resume), defer the
	// compaction to the resume: arm the orchestrator's one-shot
	// resume-compaction request BEFORE the auto-resume in phase 5, so the
	// flag is consumed by the resumed run's Resume call. If the task is
	// instead cancelled or abandoned before resuming (user discard, goal
	// takeover, archival), the session layer's clearResumeCompaction drops
	// the flag so it never fires for an unrelated task. The resumed
	// Conductor run then force-compacts the merged trajectory (seeded
	// checkpoint + the resumed run) up front, and the real numbers arrive as
	// the executor's context_compaction card. Deferred only for the no-op
	// outcome: a successful compaction already shrank the history and
	// persisted the marker, so re-compacting the merged trajectory at
	// resume would duplicate the work.
	deferredToResume := false
	if nothingCompacted && !shuttingDown && m.hasPausedUnfinishedTask(sessionID) {
		orch.RequestResumeCompaction(strategy)
		deferredToResume = true
	}

	// Phase 4: persist the marker so a restart restores the compacted history.
	// Skipped for the no-op outcome — there is no compacted history to
	// snapshot, and the executor's context_compaction card (phase 3.5) is the
	// user-facing record of the deferred compaction instead.
	if err == nil && !cancelled && !nothingCompacted && !shuttingDown {
		if perr := m.persistCompactionMarker(sessionID, orch, strategy, before, after); perr != nil {
			m.log().Error("manual compaction: failed to persist marker", "session", sessionID, "error", perr)
			// Non-fatal: the in-memory history is already compacted; only the
			// restart-restore falls back to the full history.
		}
	}

	// Release the compacting window BEFORE the auto-resume so ResumeTask is
	// not rejected by the compacting guard. pausing was already cleared by the
	// request epilogue (deactivateSessionTask) when the goroutine exited.
	session.mu.Lock()
	session.compacting = false
	session.compactCancel = nil
	session.compactDone = nil
	session.mu.Unlock()

	// Phase 5: auto-resume the task this flow paused, when a paused checkpoint
	// remains (the task may have completed normally in the pause window — then
	// there is nothing to resume) AND the pause is still owned by this flow.
	// The owner check is what keeps a user-initiated pause alive in BOTH
	// orderings: a pause the user requested after this flow armed (PauseSession
	// writes pauseOwnerUser on every call, racing the flow or landing inside
	// the compaction window) overwrites the owner, and a pause the user
	// requested BEFORE the arming keeps it — the arming never steals a
	// user-owned pause. Either way the task stays paused and clients are told
	// to re-apply the paused state. The owner is consumed (reset) by this
	// decision.
	resumed := false
	pausedWithoutResume := false
	session.mu.Lock()
	ownsPause := session.pauseOwner == pauseOwnerCompaction
	session.pauseOwner = pauseOwnerNone
	session.mu.Unlock()
	// Re-read the flag here: Shutdown may have begun after the mid-flow
	// snapshot above — the choke points (getOrRestoreSession / ResumeTask)
	// would reject the resume anyway; skipping it outright keeps the
	// terminal event honest (no spurious "auto-resume failed" warning
	// during teardown) and the checkpoint persists as resumable via
	// Shutdown's persistPauseIfUnfinished.
	if pausedForCompaction && !shuttingDown && !m.shuttingDown.Load() && m.hasPausedUnfinishedTask(sessionID) {
		if ownsPause {
			if rerr := m.ResumeTask(context.Background(), sessionID, "", "", ""); rerr != nil {
				// The checkpoint is still there with nobody else to resume it, and
				// the UI never saw session_paused (it is suppressed while
				// compacting) — flag it so clients re-apply the paused state.
				pausedWithoutResume = true
				m.log().Warn("manual compaction: auto-resume failed", "session", sessionID, "error", rerr)
			} else {
				resumed = true
			}
		} else {
			// The user asked for the paused state while this flow held the
			// session: honour it — same client contract as a failed
			// auto-resume (the UI never saw session_paused while compacting).
			pausedWithoutResume = true
			m.log().Info("manual compaction: keeping user-requested pause", "session", sessionID)
		}
	}

	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	// Post-flow no-op verdict for the client (the compact button's disabled
	// state), recomputed on the orchestrator's CURRENT history: a successful
	// compaction left the dialogue within the target → true; a no-op outcome
	// changed nothing → true; a cancelled/failed flow left the history
	// untouched → its own verdict.
	compactionNoOp := orch.ManualCompactionWouldNoOp()
	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "compaction_finished",
		Data: CompactionFinishedEventData{
			Strategy:            strategy,
			Success:             err == nil && !cancelled,
			Cancelled:           cancelled,
			Error:               errMsg,
			BeforePercent:       before,
			AfterPercent:        after,
			Resumed:             resumed,
			PausedWithoutResume: pausedWithoutResume,
			NothingCompacted:    nothingCompacted,
			DeferredToResume:    deferredToResume,
			CompactionNoOp:      compactionNoOp,
		},
	})
}

// CancelSessionCompaction aborts an in-flight manual compaction. When the flow
// is still waiting for the running task's pause checkpoint it stops there
// (skipping the compaction and auto-resuming once the checkpoint lands); when
// the compaction itself is running it aborts the summarize calls, leaving the
// history untouched. A no-op when no compaction is in flight.
func (m *Manager) CancelSessionCompaction(sessionID string) error {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	cancel := sess.compactCancel
	sess.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// clearResumeCompaction discards any armed one-shot resume-compaction
// request on the session's in-memory orchestrator. Called when the
// session's unfinished task is cancelled or abandoned: the armed flag
// belonged to THAT task's future resume and must not leak into an unrelated
// later one. The flag lives only on the in-memory orchestrator, so a
// session that is not currently in memory cannot have one — a plain map
// lookup suffices (no store restore). Best-effort and race-free with a
// concurrent Resume consume (mutex-guarded on the orchestrator).
func (m *Manager) clearResumeCompaction(sessionID string) {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return
	}
	if orch := sess.GetOrchestrator(); orch != nil {
		orch.ClearResumeCompaction()
	}
}

// persistCompactionMarker writes the context_compaction marker row carrying
// the compacted history snapshot. The row's role renders as the existing
// compaction card on reload; its metadata.messages snapshot lets
// convertChatMessagesToLLM rebuild the LLM-visible history exactly.
func (m *Manager) persistCompactionMarker(sessionID string, orch *core.Orchestrator, strategy string, before, after float64) error {
	m.mu.RLock()
	store := m.sessionStore
	m.mu.RUnlock()
	if store == nil {
		return nil // no persistence — in-memory compaction is still effective
	}

	meta := map[string]any{
		"strategy":       strategy,
		"before_percent": before,
		"after_percent":  after,
		"messages":       orch.ConversationHistory(),
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal compaction marker: %w", err)
	}
	return store.SaveMessage(context.Background(), ChatMessage{
		SessionID: sessionID,
		Role:      compactMarkerRole,
		Content:   fmt.Sprintf("Context compacted from %.0f%% to %.0f%%", before, after),
		Metadata:  metaJSON,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// compactedHistoryFromMarker parses the compacted history snapshot embedded in
// a persisted context_compaction marker row. Returns nil when the metadata
// carries no (or an unparsable) snapshot — old markers without the messages
// field are treated as no-ops by the restore path.
func compactedHistoryFromMarker(raw json.RawMessage) []llm.Message {
	if len(raw) == 0 {
		return nil
	}
	var meta struct {
		Messages []llm.Message `json:"messages"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil || len(meta.Messages) == 0 {
		return nil
	}
	return meta.Messages
}
