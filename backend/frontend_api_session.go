package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
)

// sessionStoreOpTimeout bounds one session/project/review store round-trip
// from the RPC layer: every store shares the app's single SQLite pool, and
// database/sql waits for a free connection with no deadline when the caller
// context carries none — the default mirrors the session package's
// restoreDBReadTimeout so a contended pool yields a prompt, retryable error.
const sessionStoreOpTimeout = 15 * time.Second

// sessionForkTimeout bounds the transactional fork (session + tasks +
// blackboard + review deep copy): one transaction over an unbounded row set,
// derived from f.ctx() so app shutdown cancels it.
const sessionForkTimeout = 60 * time.Second

// storeOpCtx returns the request context (f.ctx(), cancelled at app shutdown)
// bounded by sessionStoreOpTimeout. Every direct store call on an RPC path
// must go through it — a bare context.Background() discards the shutdown
// cancellation and any bound on the pool wait.
func (f *FrontendAPI) storeOpCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(f.ctx(), sessionStoreOpTimeout)
}

// switchStoreOpCtx derives one store round-trip deadline from the
// switch-section deadline (switchSectionTimeout) — the EARLIER of the two, so
// a saturated store cannot stack N full budgets into one switchMu hold. The
// parent is the switch ctx, not f.ctx(): the section deadline must hold even
// when f.ctx() carries no deadline. See specs/domains/workspace.md.
func (f *FrontendAPI) switchStoreOpCtx(switchCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(switchCtx, sessionStoreOpTimeout)
}

// emitSessionsChanged emits the global sessions:changed event (no payload —
// consumers re-read via ListSessions/ListAllSessions) after a list-affecting
// mutation.
func (f *FrontendAPI) emitSessionsChanged() {
	f.seedAcquire()
	if f.emitEvent != nil {
		f.emitEvent(EventSessionsChanged)
	}
}

// CreateSession creates a new agent session within the active project.
func (f *FrontendAPI) CreateSession() (*session.SessionInfo, error) {
	if f.appCell() == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized - check startup logs for LLM router or configuration errors")
	}

	f.activeProjectMu.RLock()
	projectID := f.activeProjectID
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	if projectID == "" {
		return nil, errors.New("no active project — create or select a project first")
	}

	info, err := f.app.Manager().CreateSession(projectID, projectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	// Persist to SQLite
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if f.store != nil {
		ctx, cancel := f.storeOpCtx()
		if err := f.store.SaveSession(ctx, *info); err != nil {
			f.log().Error("failed to save session to store", "error", err)
		}
		cancel()
	}
	// Announce AFTER the persist attempt: the event's debounced re-read must
	// see the persisted row — an emission before SaveSession let a slow save
	// strand the new session out of the sidebar/radar until the safety poll.
	f.emitSessionsChanged()
	return info, nil
}

// ForkSession creates an independent deep copy of a session (messages, tasks,
// blackboard, terminal history, work directories, review) with freshly
// generated identifiers — no shared rows. Only allowed with no unfinished
// task; on success the caller switches the active session to it.
// See specs/domains/session-lifecycle.md.
func (f *FrontendAPI) ForkSession(sessionID string) (*session.SessionInfo, error) {
	if f.appCell() == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized")
	}
	if f.store == nil {
		return nil, errors.New("session store not initialized")
	}

	// Guard: a session with an unfinished task cannot be forked — forking
	// would duplicate a half-completed execution state.
	guardCtx, cancelGuard := f.storeOpCtx()
	unfinished, err := f.store.GetUnfinishedTask(guardCtx, sessionID)
	cancelGuard()
	if err != nil {
		return nil, fmt.Errorf("failed to check session tasks: %w", err)
	}
	if unfinished != nil {
		return nil, errors.New("cannot fork a session that has an unfinished task")
	}

	// Clone the review inside the same transaction as the fork so the whole
	// operation commits atomically; a review-copy failure rolls back the
	// entire fork instead of leaving a review-less fork.
	var reviewCloner session.ForkReviewCloner
	if f.reviewStore != nil {
		reviewCloner = f.reviewStore.CloneReviewTx
	}

	// Managed sources fork into their OWN tree on a derived branch created
	// at the source's committed HEAD (see forkManagedSession); local and
	// CHAT sources keep the plain deep-copy path with the binding they have.
	srcCtx, cancelSrc := f.storeOpCtx()
	src, err := f.store.LoadSession(srcCtx, sessionID)
	cancelSrc()
	if err != nil {
		return nil, fmt.Errorf("failed to load source session: %w", err)
	}
	if src == nil {
		return nil, fmt.Errorf("source session %q not found", sessionID)
	}

	forkCtx, cancelFork := context.WithTimeout(f.ctx(), sessionForkTimeout)
	defer cancelFork()
	if src.WorkspaceBinding != nil && src.WorkspaceBinding.Kind == session.WorkspaceManagedWorktree {
		return f.forkManagedSession(forkCtx, src, reviewCloner)
	}

	info, err := f.store.ForkSession(forkCtx, sessionID, reviewCloner)
	if err != nil {
		return nil, fmt.Errorf("failed to fork session: %w", err)
	}

	f.emitSessionsChanged()
	return info, nil
}

// DeleteSession removes a session including every internal file that belongs
// to it under ~/.c0wrk; archiving does NOT remove files. A session owning a
// managed worktree additionally has its tree released (never its branch); a
// dirty or locked tree blocks with *SessionDeleteBlockedError — re-issue via
// DeleteSessionWithOptions. See specs/domains/session-lifecycle.md,
// specs/domains/session-worktrees.md.
func (f *FrontendAPI) DeleteSession(id string) error {
	return f.DeleteSessionWithOptions(id, SessionDeleteOptions{})
}

// DeleteSessionWithOptions removes a session carrying the user's explicit
// deletion decisions for its managed worktree: the managed pre-flight (join
// the running task → stop the terminal → [count co-owners → own-row removal →
// release]) runs BEFORE any session state is removed, so a blocked deletion
// leaves session, tree, and branch fully intact and retryable. See
// specs/domains/session-worktrees.md (Release).
func (f *FrontendAPI) DeleteSessionWithOptions(id string, opts SessionDeleteOptions) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	cleanupProjectID, err := f.prepareManagedTreeDeletion(context.Background(), id, opts)
	if err != nil {
		return err
	}
	manager := f.app.Manager()

	// The manager lazily restores and deletes in-memory/restorable sessions,
	// closing file handles and removing the entire per-session directory.
	// HasSession (NOT GetSession): restoring a managed session here would
	// re-provision the tree the pre-flight just released. An unrestorable
	// session's project ID is captured for the file cleanup below.
	var unrestorableProjectID string
	switch {
	case manager.HasSession(id):
		if err := manager.DeleteSession(id); err != nil {
			return fmt.Errorf("failed to delete session: %w", err)
		}
	case cleanupProjectID != "":
		// The pre-flight removed the store row itself, so the row-based
		// lookup below can no longer resolve the project.
		unrestorableProjectID = cleanupProjectID
	case f.store != nil:
		ctx, cancel := f.storeOpCtx()
		if info, err := f.store.LoadSession(ctx, id); err == nil && info != nil {
			unrestorableProjectID = info.ProjectID
		}
		cancel()
	}
	// Always delete from store (handles store-only sessions from previous runs).
	if f.store != nil {
		ctx, cancel := f.storeOpCtx()
		if err := f.store.DeleteSession(ctx, id); err != nil {
			f.log().Error("failed to delete session from store", "error", err)
		}
		cancel()
	}
	f.stopSessionTerminal(id)
	// Fallback: remove internal files for sessions the manager could not
	// restore; restored/in-memory sessions were cleaned up above.
	if unrestorableProjectID != "" {
		sessionDir := config.SessionDir(f.agentDir, unrestorableProjectID, id)
		if err := os.RemoveAll(sessionDir); err != nil {
			f.log().Warn("failed to remove session directory", "session_id", id, "dir", sessionDir, "error", err)
		}
	}
	f.emitSessionsChanged()
	return nil
}

// ListSessions returns sessions for the active project.
func (f *FrontendAPI) ListSessions() ([]session.SessionInfo, error) {
	f.activeProjectMu.RLock()
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if projectID == "" {
		return []session.SessionInfo{}, nil
	}

	if f.appCell() == nil || f.app.Manager() == nil {
		return []session.SessionInfo{}, nil
	}

	return f.app.Manager().ListSessionsByProject(projectID)
}

// ListAllSessions returns sessions across ALL projects, ordered pinned first
// then by effective activity — the data source for the cross-project activity
// indicator. An uninitialized manager yields an empty slice, not an error, so
// the UI can call this unconditionally during startup.
func (f *FrontendAPI) ListAllSessions() ([]session.SessionInfo, error) {
	if f.appCell() == nil || f.app.Manager() == nil {
		return []session.SessionInfo{}, nil
	}

	return f.app.Manager().ListSessionsAll()
}

// RenameSession changes session name.
func (f *FrontendAPI) RenameSession(id, name string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	manager := f.app.Manager()
	// HasSession (NOT GetSession): GetSession lazily RESTORES a store-only
	// session (full orchestrator build, managed-worktree re-provision) — a
	// rename of a never-opened session would resurrect it inside a metadata RPC.
	if manager.HasSession(id) {
		if err := manager.RenameSession(id, name); err != nil {
			return fmt.Errorf("failed to rename session: %w", err)
		}
	}
	if f.store != nil {
		ctx, cancel := f.storeOpCtx()
		if err := f.store.RenameSession(ctx, id, name); err != nil {
			f.log().Error("failed to rename session in store", "error", err)
		}
		cancel()
	}
	return nil
}

// ArchiveSession archives/unarchives a session.
func (f *FrontendAPI) ArchiveSession(id string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	manager := f.app.Manager()
	// HasSession, not the restoring GetSession — see RenameSession above.
	if manager.HasSession(id) {
		if err := manager.ArchiveSession(id); err != nil {
			return fmt.Errorf("failed to archive session: %w", err)
		}
	}
	if f.store != nil {
		ctx, cancel := f.storeOpCtx()
		info, err := f.store.LoadSession(ctx, id)
		if err == nil && info != nil {
			if err := f.store.ArchiveSession(ctx, id, !info.Archived); err != nil {
				f.log().Error("failed to archive session in store", "error", err)
			}
		}
		cancel()
	}
	f.emitSessionsChanged()
	return nil
}

// PinSession pins/unpins a session.
func (f *FrontendAPI) PinSession(id string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	manager := f.app.Manager()
	// HasSession, not the restoring GetSession — see RenameSession above.
	if manager.HasSession(id) {
		if err := manager.PinSession(id); err != nil {
			return fmt.Errorf("failed to pin session: %w", err)
		}
	}
	if f.store != nil {
		ctx, cancel := f.storeOpCtx()
		info, err := f.store.LoadSession(ctx, id)
		if err == nil && info != nil {
			if err := f.store.PinSession(ctx, id, !info.Pinned); err != nil {
				f.log().Error("failed to pin session in store", "error", err)
			}
		}
		cancel()
	}
	f.emitSessionsChanged()
	return nil
}

// SendMessage sends a user message to a session (async — results come via
// events). Parameter deltas:
//   - activeSkills/activeAgents/activeMCPServers: /-mention threading (durable
//     task-scoped MCP authorization: specs/domains/session-lifecycle.md).
//   - goal/goalBudget: goal mode for the first message of a task (OR-ed with a
//     /goal command prefix); goalBudget = JSON {"max_turns":N}, empty = defaults.
//   - e2s: the E2S execution mode — experimental (fails closed when the gate is
//     off) and mutually exclusive with goal.
//   - reviewMode: code-review feedback the agent must address.
//   - clientSendID: the sender's optimistic-card id, echoed on message_received
//     so the sender dedups by identity; internal sends pass "".
//   - workspace: the send-time workspace choice (ADR-091, first message of a
//     fresh session only); a provisioning failure rejects the send before any
//     state is touched. Nil = no-op.
func (f *FrontendAPI) SendMessage(id, text string, activeSkills, activeAgents, activeMCPServers []string, modelOverride, reasoningEffort string, goal /* goal */ bool, goalBudget string, e2s /* e2s */, reviewMode /* reviewMode */ bool, clientSendID string, workspace *SessionWorkspaceChoice) error {
	// E2S and goal are alternative task modes with incompatible loop
	// semantics; the frontend enforces exclusivity — this is the server-side
	// defense, checked first (a both-flags request is malformed regardless).
	if e2s && goal {
		return errors.New("E2S mode and goal mode are mutually exclusive — disable one of the toggles before sending")
	}
	// Detect the leading "/goal" prefix on the POST-preprocessing text:
	// preprocessing strips leading /-mention refs, which can expose a "/goal"
	// hidden behind them, and the manager arms goal mode from the processed
	// text — a raw-text check alone misses that form.
	processed := core.PreprocessMessageText(text, activeSkills, activeAgents, activeMCPServers, "")
	_, isGoalPrefix := core.DetectAndStripGoalMode(processed)
	goalEnabled := goal || isGoalPrefix
	// An armed /goal prefix must not slip past the exclusivity check: the
	// manager would otherwise build a HandleOptions with BOTH Goal and E2S
	// set, which core rejects (ErrE2SGoalConflict) AFTER the run's E2S
	// takeover cleanup was skipped. Rejected here, before any side effect.
	if e2s && isGoalPrefix {
		return errors.New("E2S mode and goal mode are mutually exclusive — an E2S message cannot carry a /goal command")
	}
	// E2S is experimental: fail closed BEFORE any side effect. The gate also
	// returns false for a not-yet-loaded config, so an early send can never
	// slip past it — checked even before the manager-initialized guard.
	if e2s && !f.experimentalFeaturesEnabled() {
		return errors.New("E2S mode is experimental and currently disabled — enable experimental features in settings to use it")
	}
	// Goal mode is refused while the Model Profiles essential-tools narrowing
	// is active (the narrowing never applies to a goal run, so the exclusion
	// keeps the toggle from being a silent no-op there): both arming signals
	// are covered, and the rejection lands BEFORE any side effect.
	// See specs/domains/model-profiles.md.
	if goalEnabled && f.modelProfilesGoalBlocked() {
		return errors.New("goal mode is unavailable while the Model Profiles essential-tools profile is active — disable the Model Profiles profile or its essential-tools narrowing to use goal mode")
	}
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized - check startup logs for LLM router or configuration errors")
	}
	// Send-time workspace binding (the fresh window): provisioning and
	// adoption failures reject the send BEFORE any state is touched — no
	// activity bump, no persisted message, no task.
	if workspace != nil {
		if err := f.bindSessionWorkspace(f.ctx(), id, workspace); err != nil {
			return err
		}
	}
	if f.store != nil {
		ctx, cancel := f.storeOpCtx()
		if err := f.store.UpdateSessionActivity(ctx, id); err != nil {
			f.log().Error("failed to update session activity", "error", err)
		}
		cancel()
	}
	// Authoritative live-send gate, moved ahead of the store write so a
	// rejected live send never leaves a phantom persisted message. The
	// manager re-checks the same conditions under the session lock; the race
	// is harmless (a message that passes but finds the task finished simply
	// starts a normal task).
	if err := f.app.Manager().ValidateLiveSend(id, goal, e2s, text, activeSkills, activeAgents, activeMCPServers); err != nil {
		return err
	}

	// Attachments cannot join a live interjection: they are flushed into the
	// blackboard/context only at task start. The manager's live branch
	// ignores attachments (queues text only), so this is purely a UX guard.
	if !goal && !e2s {
		if status, statusErr := f.app.Manager().GetSessionRuntimeStatus(id); statusErr == nil {
			if status.Active && !status.Paused && f.app.Manager().HasPendingAttachments(id) {
				return errors.New("attachments cannot be sent while a task is running — send text only, or wait for the pause/completion")
			}
		}
	}

	// Snapshot the pending-attachment metadata BEFORE the manager call (its
	// fresh/resume path snapshots and clears the pending lists); the message
	// itself is persisted AFTER the authoritative dispatch so is_nudge matches
	// the actual decision and a rejected send never reaches the store.
	var messageMetadata json.RawMessage
	if mgr := f.app.Manager(); mgr != nil {
		if md, mdErr := mgr.PendingMessageMetadata(id, goal); mdErr == nil {
			messageMetadata = md
		} else {
			f.log().Warn("failed to read pending message metadata", "session_id", id, "error", mdErr)
		}
	}

	// Preprocess for the orchestrator (mention refs stripped, @file refs
	// resolved); relative paths resolve against the SESSION's workspace —
	// the authoritative root for both project and No Project sessions.
	// See specs/decisions/095-chat-file-reference-path-semantics.md.
	workspacePath := ""
	if wp, ok := f.app.Manager().GetSessionWorkspacePath(id); ok {
		workspacePath = wp
	}
	processedText := core.PreprocessMessageText(text, activeSkills, activeAgents, activeMCPServers, workspacePath)

	// No send-time vector re-scoping: the per-root registry (ADR-080/081)
	// routes each session's searches through its own task context.
	f.autoAddPromptWorkDirs(id, text)

	// The dispatch consumes the PREPROCESSED text, but the message_received
	// echo must carry the RAW input: the persisted row and the sender's
	// optimistic card both hold the raw form, so the useChatEvents dedup
	// compares against it — a processed echo would duplicate the card. See
	// specs/contracts/event-catalog.md (message_received).
	classification, err := f.app.Manager().SendMessageClassified(f.ctx(), id, processedText, activeSkills, activeAgents, activeMCPServers, modelOverride, reasoningEffort, goal, goalBudget, e2s, reviewMode, session.WithEchoText(text), session.WithClientMessageID(clientSendID))
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	// Persist the user message with the authoritative classification: a live
	// interjection and a nudge-resume both carry is_nudge: true; a fresh task
	// — including a continuation of a completed task — does not.
	if f.store != nil {
		if classification != session.SendFresh {
			messageMetadata = mergeIsNudgeMetadata(messageMetadata)
		}
		ctx, cancel := f.storeOpCtx()
		if err := f.store.SaveMessage(ctx, session.ChatMessage{
			SessionID: id,
			Role:      "user",
			Content:   text,
			Metadata:  messageMetadata,
			// MUST be UTC (Z-suffixed) like every other timestamp writer:
			// LoadMessages orders by lexicographic TEXT comparison of
			// created_at — a local-time offset suffix would sort this row
			// after every Z-suffixed row of the same chat after a reload.
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			f.log().Error("failed to save user message to store", "error", err)
		}
		cancel()
	}

	return nil
}

// CancelTask cancels the running task in a session.
func (f *FrontendAPI) CancelTask(id string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().CancelTask(id)
}

// ResumeTask resumes an interrupted task in the given session, if any; nil
// when no unfinished task exists (safe to call on session load). The optional
// modelOverride/reasoningEffort apply the user's CURRENT selection instead of
// inheriting the interrupted task's settings.
func (f *FrontendAPI) ResumeTask(id, modelOverride, reasoningEffort string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	if f.goalResumeBlockedByModelProfiles(id) {
		return errors.New("goal mode is unavailable while the Model Profiles essential-tools profile is active — the paused goal cannot be resumed; disable the Model Profiles profile or its essential-tools narrowing to resume it")
	}
	return f.app.Manager().ResumeTask(f.ctx(), id, modelOverride, reasoningEffort, "")
}

// goalResumeBlockedByModelProfiles reports whether resuming session id would
// re-enter a goal loop while the Model Profiles essential-tools narrowing is
// active — true only when the narrowing is on AND the unfinished task carries
// a NON-terminal goal state (the exact condition Orchestrator.Resume uses to
// pick the goal loop). A task-store read failure is fail-open: the manager
// independently re-checks under the session lock and resumeGoalLoop is the
// ultimate authority. See specs/domains/model-profiles.md.
func (f *FrontendAPI) goalResumeBlockedByModelProfiles(id string) bool {
	f.seedAcquire()
	if f.store == nil || !f.modelProfilesGoalBlocked() {
		return false
	}
	adapter := session.NewTaskStoreAdapter(f.store)
	taskID, err := adapter.GetUnfinishedTaskID(id)
	if err != nil || taskID == "" {
		return false
	}
	gs, err := adapter.LoadGoalState(taskID)
	if err != nil || gs == nil {
		return false
	}
	return !gs.Status.IsTerminal()
}

// PauseSession signals the currently-running task (any mode) to pause at the
// next step boundary: the executor checks the signal cooperatively, stops
// with a paused checkpoint, and the task is persisted as paused so a later
// ResumeSession can re-enter.
func (f *FrontendAPI) PauseSession(sessionID string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().PauseSession(sessionID)
}

// ResumeSession re-enters the execution loop for a paused (or still-active)
// task, loading the persisted task state. The optional nudge is injected as a
// trailing user message into the first resumed turn (one-shot); the optional
// modelOverride/reasoningEffort apply the user's current selection.
func (f *FrontendAPI) ResumeSession(sessionID, modelOverride, reasoningEffort, nudge string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	if f.goalResumeBlockedByModelProfiles(sessionID) {
		return errors.New("goal mode is unavailable while the Model Profiles essential-tools profile is active — the paused goal cannot be resumed; disable the Model Profiles profile or its essential-tools narrowing to resume it")
	}
	return f.app.Manager().ResumeSession(f.ctx(), sessionID, modelOverride, reasoningEffort, nudge)
}

// CompactSessionContext starts a manual context compaction with the named
// strategy ("sliding_window" | "summarization" | "hierarchical").
// Asynchronous: validates and accepts (pausing a running task first, exactly
// like PauseSession, waiting for its checkpoint), reports via
// compaction_started/compaction_finished, and auto-resumes the paused task.
// See specs/domains/memory/compaction.md.
func (f *FrontendAPI) CompactSessionContext(sessionID, strategy string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().CompactSessionContext(f.ctx(), sessionID, strategy)
}

// CancelSessionCompaction aborts an in-flight manual compaction (see
// CompactSessionContext for the cancellation semantics). A no-op when no
// compaction is running.
func (f *FrontendAPI) CancelSessionCompaction(sessionID string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().CancelSessionCompaction(sessionID)
}

// GetSessionTokens returns token counts for a session: the persisted row as
// the base, overlaid when live by the manager's snapshot so a switch back to
// a running session restores the status bar's "N of M" display.
// See specs/domains/session-lifecycle.md.
func (f *FrontendAPI) GetSessionTokens(sessionID string) SessionTokensResponse {
	f.seedAcquire()
	var result SessionTokensResponse
	if f.store == nil || sessionID == "" {
		return result
	}
	info, err := f.store.LoadSession(f.ctx(), sessionID)
	if err != nil || info == nil {
		info = &session.SessionInfo{}
	}
	result.TotalInputTokens = info.TotalInputTokens
	result.TotalOutputTokens = info.TotalOutputTokens
	result.Model = info.Model
	result.Family = info.Family
	result.FillPercent = info.FillPercent

	if f.app != nil && f.app.Manager() != nil {
		if snap, ok := f.app.Manager().LiveTokenSnapshot(sessionID); ok {
			if snap.InputTokens > 0 {
				result.TotalInputTokens = snap.InputTokens
			}
			if snap.OutputTokens > 0 {
				result.TotalOutputTokens = snap.OutputTokens
			}
			if snap.Model != "" {
				result.Model = snap.Model
				result.Family = snap.Family
			}
			if snap.FillPercent > 0 {
				result.FillPercent = snap.FillPercent
			}
			result.UsedTokens = snap.UsedTokens
			result.MaxTokens = snap.MaxTokens
		}
	}
	return result
}

// CancelUnfinishedTask discards an unfinished task in the given session,
// preventing future resume prompts. Safe to call when no unfinished task
// exists; in that case it is a no-op.
func (f *FrontendAPI) CancelUnfinishedTask(id string) error {
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().CancelUnfinishedTask(id)
}

// GetSessionRuntimeStatus returns live/persisted execution state; the
// frontend calls it after loading history so the UI reflects real execution
// state. See specs/contracts/desktop-frontend.md.
func (f *FrontendAPI) GetSessionRuntimeStatus(id string) (session.SessionRuntimeStatus, error) {
	if f.appCell() == nil || f.app.Manager() == nil {
		return session.SessionRuntimeStatus{}, errors.New("session manager not initialized")
	}
	return f.app.Manager().GetSessionRuntimeStatus(id)
}

// GetSessionHistory returns the session's full content history, oldest-first.
// Deliberately unpaginated — plan-timeline restore needs the full set, so no
// numeric ceiling remains on the initial-load cost.
// See specs/domains/session-lifecycle.md.
func (f *FrontendAPI) GetSessionHistory(id string) ([]session.ChatMessage, error) {
	f.seedAcquire()
	if f.store == nil {
		return []session.ChatMessage{}, nil
	}
	ctx, cancel := f.storeOpCtx()
	defer cancel()
	return f.store.LoadSessionHistory(ctx, id)
}

// GetBlackboardState returns the current blackboard state for a session.
// Returns nil if no task state is available.
func (f *FrontendAPI) GetBlackboardState(sessionID string) (*BlackboardStateResponse, error) {
	if f.appCell() == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized")
	}

	bbState, err := f.app.Manager().GetBlackboardState(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get blackboard state: %w", err)
	}
	if bbState == nil || bbState.TaskState == nil {
		return nil, nil
	}

	return convertBlackboardState(bbState.TaskState), nil
}

// convertBlackboardState maps a core.TaskState to the frontend DTO.
func convertBlackboardState(state *core.TaskState) *BlackboardStateResponse {
	resp := &BlackboardStateResponse{
		TaskID:          state.TaskID,
		SessionID:       state.SessionID,
		Status:          state.Status,
		OriginalRequest: state.OriginalRequest,
		FinalOutput:     state.FinalOutput,
		StepResults:     make(map[string]BlackboardStepResponse, len(state.StepResults)),
		Reflections:     make([]BlackboardReflectionResponse, 0, len(state.Reflections)),
		Facts:           make([]BlackboardFactResponse, 0, len(state.Facts)),
		Attachments:     make([]BlackboardAttachmentResponse, 0, len(state.Attachments)),
	}

	// Plan
	if state.Plan != nil && len(state.Plan.Steps) > 0 {
		planResp := &BlackboardPlanResponse{
			Steps: make([]BlackboardPlanStepResponse, len(state.Plan.Steps)),
		}
		for i, s := range state.Plan.Steps {
			planResp.Steps[i] = BlackboardPlanStepResponse{
				ID:          s.ID,
				Summary:     s.Summary,
				Description: s.Description,
				DependsOn:   s.DependsOn,
			}
		}
		resp.Plan = planResp
	}

	// Step results (summary + error only; full output is fetched on demand via
	// GetStepOutput to keep this payload light).
	for stepID, sr := range state.StepResults {
		entry := BlackboardStepResponse{
			StepID:  stepID,
			Summary: sr.Summary,
		}
		if sr.Error != nil {
			entry.Error = sr.Error.Error()
		}
		resp.StepResults[stepID] = entry
	}

	// Reflections
	for _, r := range state.Reflections {
		resp.Reflections = append(resp.Reflections, BlackboardReflectionResponse{
			Summary:         r.Summary,
			Hypotheses:      r.Hypotheses,
			SuggestedAction: r.SuggestedAction,
			Reasoning:       r.Reasoning,
			FailureAnalysis: r.FailureAnalysis,
			RootCause:       r.RootCause,
			ActionPlan:      r.ActionPlan,
			Timestamp:       r.Timestamp.Format(time.RFC3339),
		})
	}

	// Facts
	for _, fact := range state.Facts {
		resp.Facts = append(resp.Facts, BlackboardFactResponse{
			Keywords: fact.Keywords,
			Content:  fact.Content,
			Author:   fact.Author,
		})
	}

	// Attachments (metadata only — markdown content is excluded)
	for _, att := range state.Attachments {
		resp.Attachments = append(resp.Attachments, BlackboardAttachmentResponse{
			ID:           att.ID,
			OriginalName: att.OriginalName,
			Format:       att.Format,
			SizeBytes:    att.SizeBytes,
			AttachedAt:   att.AttachedAt.Format(time.RFC3339),
		})
	}

	return resp
}

// taskState returns the current blackboard task state for a session, or
// (nil, nil) when no task state is available. Shared by GetStepOutput and
// SearchBlackboardStepOutputs so the restore logic stays in one place.
func (f *FrontendAPI) taskState(sessionID string) (*core.TaskState, error) {
	if f.appCell() == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized")
	}
	bbState, err := f.app.Manager().GetBlackboardState(sessionID)
	if err != nil {
		return nil, err
	}
	if bbState == nil || bbState.TaskState == nil {
		return nil, nil
	}
	return bbState.TaskState, nil
}

// GetStepOutput returns the full output (body) of a single plan step, fetched
// lazily on hover so large outputs never ride along in GetBlackboardState.
// See specs/contracts/desktop-frontend.md.
func (f *FrontendAPI) GetStepOutput(sessionID, stepID string) (string, error) {
	state, err := f.taskState(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get task state: %w", err)
	}
	if state == nil {
		return "", nil
	}
	return state.StepResults[stepID].FullOutput, nil
}

// SearchBlackboardStepOutputs returns the IDs of steps whose full output
// contains the query (case-insensitive), unioned by the viewer with its local
// filtering — content is searchable without shipping full outputs over the
// list endpoint. See specs/contracts/desktop-frontend.md.
func (f *FrontendAPI) SearchBlackboardStepOutputs(sessionID, query string) ([]string, error) {
	state, err := f.taskState(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task state: %w", err)
	}
	if state == nil || query == "" {
		return nil, nil
	}
	needle := strings.ToLower(query)
	matches := make([]string, 0)
	for stepID, sr := range state.StepResults {
		if strings.Contains(strings.ToLower(sr.FullOutput), needle) {
			matches = append(matches, stepID)
		}
	}
	return matches, nil
}

// ResolvePendingMessage patches the metadata of the most recent persisted
// message with the given role and matching field value — HITL response
// handlers mark tool_confirm / ask_user / step_limit / plan_review messages
// as resolved so they don't reappear as pending on reload.
// See specs/domains/session-lifecycle.md.
func (f *FrontendAPI) ResolvePendingMessage(sessionID, role, matchField, matchValue string, extra map[string]any) error {
	f.seedAcquire()
	if f.store == nil {
		return errors.New("session store not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.store.ResolvePendingMessage(ctx, sessionID, role, matchField, matchValue, extra)
}

// mergeIsNudgeMetadata merges is_nudge: true into a user-message metadata
// blob (nil/empty input yields a fresh blob; existing keys preserved), marking
// the persisted message as a live interjection or nudge-resume.
// See specs/contracts/event-catalog.md (message_received).
func mergeIsNudgeMetadata(raw json.RawMessage) json.RawMessage {
	merged := make(map[string]any)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &merged); err != nil {
			// Malformed metadata: start fresh rather than dropping the message.
			merged = make(map[string]any)
		}
	}
	merged["is_nudge"] = true
	out, err := json.Marshal(merged)
	if err != nil {
		return json.RawMessage(`{"is_nudge":true}`)
	}
	return out
}
