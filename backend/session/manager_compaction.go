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
// compaction is already running — the server-side backstop behind the UI's
// locked compact button.
var ErrCompactionInFlight = errors.New("session compaction is already in flight")

// ErrSessionCompacting is returned by SendMessage/ResumeTask while a manual
// compaction holds the idle window. See specs/domains/memory/compaction.md.
var ErrSessionCompacting = errors.New("session is compacting context — wait for it to finish")

// validCompactionStrategies lists the strategy names accepted by
// CompactSessionContext (mirroring sp4rk's names), so an invalid name fails
// fast before any pause/resume churn.
var validCompactionStrategies = []string{"sliding_window", "summarization", "hierarchical"}

// compactionForecastStateKey returns the app_state key for the model-scoped
// EWMA-calibrated compression-ratio forecast (the empty model selects the
// legacy unscoped fallback key). See specs/domains/memory/compaction.md.
func compactionForecastStateKey(model string) string {
	if model == "" {
		return "compaction_forecast"
	}
	return "compaction_forecast:" + model
}

// compactionForecastState is the JSON mirror of sdkmemory.CompactionForecast
// for app_state persistence; zero values mean "use the config seed" on load.
type compactionForecastState struct {
	SummarizationRatio       float64 `json:"summarization_ratio"`
	HierarchicalDistantRatio float64 `json:"hierarchical_distant_ratio"`
	HierarchicalMiddleRatio  float64 `json:"hierarchical_middle_ratio"`
}

// compactMarkerRole is the persisted role marking a manual compaction; the
// row renders as the compaction card on reload and its metadata snapshot
// reseeds the LLM-visible history. See specs/domains/memory/compaction.md.
const compactMarkerRole = "context_compaction"

// CompactSessionContext starts a MANUAL context compaction of the session's
// cross-task conversation history with the named strategy; the UI chat
// history is untouched. Asynchronous: emit compaction_started + flag
// compacting → pause a running task and wait for its checkpoint → compact →
// persist the marker row → auto-resume only a pause this flow still owns →
// emit compaction_finished. A no-op (ErrNothingCompacted) is success with
// nothing_compacted=true and no marker. Full flow, pause ownership and
// cancellation semantics: specs/domains/memory/compaction.md.
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
	// Pause a running task first (PauseSession's mechanism); the recorded
	// owner lets phase 5 tell this flow's pause from a user's — a user pause
	// is never stolen. doneCh closes when the request goroutine finishes.
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

// runSessionCompaction runs the manual-compaction flow on a background
// goroutine; it clears the compacting flag/compactCancel before the
// auto-resume so ResumeTask passes the compacting guards.
func (m *Manager) runSessionCompaction(compCtx context.Context, cancel context.CancelFunc, compactDone chan struct{}, sessionID string, session *Session, orch *core.Orchestrator, strategy string, doneCh chan struct{}, pausedForCompaction bool) {
	defer cancel()
	// Shutdown joins this goroutine via compactDone: everything after this
	// line must tolerate the backend tearing down around it.
	defer close(compactDone)

	// Phase 2: wait for the running request to reach its pause checkpoint and
	// exit; a cancellation still waits (unflipping mid-flight would race the
	// executor's step-boundary check and strand the pause).
	cancelled := false
	if doneCh != nil {
		select {
		case <-doneCh:
			// Checkpoint and cancellation raced (select picked randomly);
			// honour the cancellation — it was requested no later than the
			// checkpoint landed.
			if compCtx.Err() != nil {
				cancelled = true
			}
		case <-compCtx.Done():
			cancelled = true
			<-doneCh
		}
	}

	// Phase 3: compact the history; ErrNothingCompacted is a successful no-op.
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
	// Shutdown cancels the flow's context: the tail phases must not run
	// against a torn-down backend; the paused checkpoint (if any) is persisted
	// by Shutdown's own persistPauseIfUnfinished.
	shuttingDown := m.shuttingDown.Load()
	if shuttingDown {
		cancelled = true
	}

	// Phase 3.5: a no-op outcome with a paused unfinished task waiting defers
	// the compaction to the resume — arm RequestResumeCompaction BEFORE the
	// auto-resume so the resumed run force-compacts the merged trajectory
	// up front; clearResumeRequests drops the flag if the task is cancelled
	// or abandoned instead. Deferred only for the no-op: a successful
	// compaction already shrank the history and wrote the marker.
	deferredToResume := false
	if nothingCompacted && !shuttingDown && m.hasPausedUnfinishedTask(sessionID) {
		orch.RequestResumeCompaction(strategy)
		deferredToResume = true
	}

	// Liveness gate: once session.deleting is visible the session is being
	// torn down — the flow must not touch the store (phase 4) or resurrect
	// it via auto-resume. The flag covers the window where the flow got here
	// before DeleteSession's cancel landed.
	session.mu.Lock()
	deleting := session.deleting
	session.mu.Unlock()

	// Phase 4: persist the marker so a restart restores the compacted history;
	// skipped for the no-op outcome (nothing to snapshot).
	if err == nil && !cancelled && !nothingCompacted && !shuttingDown && !deleting {
		if perr := m.persistCompactionMarker(sessionID, orch, strategy, before, after); perr != nil {
			m.log().Error("manual compaction: failed to persist marker", "session", sessionID, "error", perr)
			// Non-fatal: the in-memory history is already compacted; only the
			// restart-restore falls back to the full history.
		}
		// Persist the EWMA-calibrated forecast; best-effort, like the marker.
		m.persistCompactionForecast(orch)
	}

	// Release the compacting window BEFORE the auto-resume so ResumeTask
	// passes the compacting guard.
	session.mu.Lock()
	session.compacting = false
	session.compactCancel = nil
	session.compactDone = nil
	session.mu.Unlock()

	// Phase 5: auto-resume only a pause this flow still owns (the owner check
	// keeps a user-initiated pause alive in both orderings); the owner is
	// consumed by this decision.
	resumed := false
	pausedWithoutResume := false
	session.mu.Lock()
	ownsPause := session.pauseOwner == pauseOwnerCompaction
	deleting = session.deleting // re-read: DeleteSession may have flipped it while phases 3.5/4 ran
	session.pauseOwner = pauseOwnerNone
	session.mu.Unlock()
	// Re-read both flags: Shutdown may have begun after the mid-flow snapshot
	// (skip keeps the terminal event honest; the checkpoint persists via
	// persistPauseIfUnfinished), and a flipped deleting flag means an
	// auto-resume here would ghost-resurrect the just-deleted session.
	if pausedForCompaction && !shuttingDown && !deleting && !m.shuttingDown.Load() && m.hasPausedUnfinishedTask(sessionID) {
		if ownsPause {
			if rerr := m.ResumeTask(context.Background(), sessionID, "", "", ""); rerr != nil {
				// Nobody else will resume the checkpoint and the UI never saw
				// session_paused (suppressed while compacting) — clients must
				// re-apply the paused state.
				pausedWithoutResume = true
				m.log().Warn("manual compaction: auto-resume failed", "session", sessionID, "error", rerr)
			} else {
				resumed = true
			}
		} else {
			// Honour the user's pause — same client contract as a failed
			// auto-resume (the UI never saw session_paused while compacting).
			pausedWithoutResume = true
			m.log().Info("manual compaction: keeping user-requested pause", "session", sessionID)
		}
	}

	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	// Post-flow per-strategy availability verdict for the client (the compact
	// menu), recomputed on the orchestrator's CURRENT history: a successful
	// compaction left the dialogue under every strategy's window → all
	// unavailable; a no-op outcome changed nothing → all unavailable; a
	// cancelled/failed flow left the history untouched → its own verdict.
	availability := orch.ManualCompactionAvailability()
	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "compaction_finished",
		Data: CompactionFinishedEventData{
			Strategy:               strategy,
			Success:                err == nil && !cancelled,
			Cancelled:              cancelled,
			Error:                  errMsg,
			BeforePercent:          before,
			AfterPercent:           after,
			Resumed:                resumed,
			PausedWithoutResume:    pausedWithoutResume,
			NothingCompacted:       nothingCompacted,
			DeferredToResume:       deferredToResume,
			CompactionAvailability: availability,
		},
	})
}

// CancelSessionCompaction aborts an in-flight manual compaction: during the
// pause-wait it stops there (skip + auto-resume once the checkpoint lands),
// during the compaction it aborts the summarize calls with the history
// untouched. No-op when nothing is in flight. See
// specs/domains/memory/compaction.md.
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

// clearResumeRequests discards the session's armed one-shot resume requests
// (compaction and/or re-route) when its unfinished task is cancelled or
// abandoned: the flags belonged to THAT task's future resume. In-memory only
// (no store restore); race-free with a concurrent Resume consume.
func (m *Manager) clearResumeRequests(sessionID string) {
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess == nil {
		return
	}
	if orch := sess.GetOrchestrator(); orch != nil {
		orch.ClearResumeCompaction()
		orch.ClearResumeReroute()
	}
}

// persistCompactionMarker writes the context_compaction marker row with the
// compacted history snapshot. See specs/domains/memory/compaction.md.
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
	// Bounded (restoreDBReadTimeout): this runs on the compaction flow
	// goroutine sharing the app's single SQLite pool; an unbounded wait would
	// stall the flow's finish path indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
	defer cancel()
	return store.SaveMessage(ctx, ChatMessage{
		SessionID: sessionID,
		Role:      compactMarkerRole,
		Content:   fmt.Sprintf("Context compacted from %.0f%% to %.0f%%", before, after),
		Metadata:  metaJSON,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// persistCompactionForecast writes the EWMA-calibrated compression-ratio
// forecast to app_state; best-effort — a failed write only loses the
// incremental calibration. See specs/domains/memory/compaction.md.
func (m *Manager) persistCompactionForecast(orch *core.Orchestrator) {
	m.mu.RLock()
	store := m.projectStore
	m.mu.RUnlock()
	if store == nil {
		return // no persistence — in-memory calibration is still effective
	}
	f := orch.CompactionForecast()
	state := compactionForecastState{
		SummarizationRatio:       f.SummarizationRatio,
		HierarchicalDistantRatio: f.HierarchicalDistantRatio,
		HierarchicalMiddleRatio:  f.HierarchicalMiddleRatio,
	}
	raw, err := json.Marshal(state)
	if err != nil {
		m.log().Warn("manual compaction: failed to marshal forecast", "error", err)
		return
	}
	// Bounded like the restore head-reads (restoreDBReadTimeout): the shared
	// SQLite connection serves every active session's writes.
	ctx, cancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
	defer cancel()
	key := compactionForecastStateKey(orch.CurrentModel())
	if err := store.SaveAppState(ctx, key, string(raw)); err != nil {
		m.log().Warn("manual compaction: failed to persist forecast", "error", err)
		return
	}
	// Also refresh the model-independent entry: a restart may restore the
	// session under a different model, and loadCompactionForecast falls back
	// to this key — that is what makes "survives restarts" hold.
	if fallback := compactionForecastStateKey(""); key != fallback {
		if err := store.SaveAppState(ctx, fallback, string(raw)); err != nil {
			m.log().Warn("manual compaction: failed to persist unscoped forecast", "error", err)
		}
	}
}

// loadCompactionForecast applies a persisted forecast over the config seed;
// missing/unparsable state or zero fields leave the seed untouched. See
// specs/domains/memory/compaction.md.
func (m *Manager) loadCompactionForecast(orch *core.Orchestrator) {
	m.mu.RLock()
	store := m.projectStore
	m.mu.RUnlock()
	if store == nil {
		return
	}
	// Bounded (restoreDBReadTimeout): this runs inside the lazy-restore path,
	// where an unbounded read would park the restore and every concurrent
	// waiter behind it.
	ctx, cancel := context.WithTimeout(context.Background(), restoreDBReadTimeout)
	defer cancel()
	raw, err := store.LoadAppState(ctx, compactionForecastStateKey(orch.CurrentModel()))
	if err != nil {
		m.log().Warn("manual compaction: failed to load forecast", "error", err)
		return
	}
	if raw == "" {
		// Fall back to the model-independent entry (see persistCompactionForecast).
		raw, err = store.LoadAppState(ctx, compactionForecastStateKey(""))
		if err != nil {
			m.log().Warn("manual compaction: failed to load fallback forecast", "error", err)
			return
		}
		if raw == "" {
			return
		}
	}
	var state compactionForecastState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		m.log().Warn("manual compaction: unparsable forecast state", "error", err)
		return
	}
	// Only non-zero persisted ratios override; a partial state keeps the seed.
	f := orch.CompactionForecast()
	if state.SummarizationRatio > 0 {
		f.SummarizationRatio = state.SummarizationRatio
	}
	if state.HierarchicalDistantRatio > 0 {
		f.HierarchicalDistantRatio = state.HierarchicalDistantRatio
	}
	if state.HierarchicalMiddleRatio > 0 {
		f.HierarchicalMiddleRatio = state.HierarchicalMiddleRatio
	}
	orch.SetCompactionForecast(f)
}

// compactedHistoryFromMarker parses the history snapshot from a
// context_compaction marker row; nil when absent/unparsable (old markers are
// restore no-ops).
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
