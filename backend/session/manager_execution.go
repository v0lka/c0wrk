package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	goalpkg "github.com/v0lka/c0wrk/core/goal"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/ignore"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/pathutil"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// ErrNoActiveTask is returned by CancelTask when the session has no task
// currently running. Callers that don't care whether a task was actually
// cancelled (e.g. ClearGoal, which only needs the task stopped so it can
// persist a terminal goal state) can treat this as non-fatal via errors.Is.
var ErrNoActiveTask = errors.New("no active task to cancel")

// loadWorkDirectories fetches project-scoped and session-scoped auxiliary work
// directories for the given session. It is best-effort: nil stores or listing
// errors are logged and skipped. Project-scoped entries come first, then
// session-scoped entries. Loaded fresh on every call so mid-session additions
// take effect on the next task.
func (m *Manager) loadWorkDirectories(session *Session) []core.WorkDirectory {
	m.mu.RLock()
	projStore := m.projectStore
	sessStore := m.sessionStore
	m.mu.RUnlock()

	var dirs []core.WorkDirectory
	if session.ProjectID != project.NoProjectID && projStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		recs, err := projStore.ListProjectWorkDirs(ctx, session.ProjectID)
		cancel()
		if err != nil {
			m.log().Warn("failed to list project work directories", "project", session.ProjectID, "error", err)
		} else {
			for _, rec := range recs {
				dirs = append(dirs, core.WorkDirectory{Path: rec.Path, Description: rec.Description})
			}
		}
	}
	if sessStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		recs, err := sessStore.ListSessionWorkDirs(ctx, session.ID)
		cancel()
		if err != nil {
			m.log().Warn("failed to list session work directories", "session", session.ID, "error", err)
		} else {
			for _, rec := range recs {
				dirs = append(dirs, core.WorkDirectory{Path: rec.Path, Description: rec.Description})
			}
		}
	}
	return dirs
}

// injectWorkDirectories injects the session's auxiliary work directories into
// the context as both allowed roots (security containment) and the
// prompt-facing directory list. dirs must be loaded by the caller (shared with
// injectIgnoreChecker so loadWorkDirectories runs once per task rather than
// twice). Returns ctx unchanged when no directories are configured.
func (m *Manager) injectWorkDirectories(ctx context.Context, dirs []core.WorkDirectory) context.Context {
	if len(dirs) == 0 {
		return ctx
	}
	paths := make([]string, len(dirs))
	for i := range dirs {
		paths[i] = dirs[i].Path
	}
	ctx = sdktools.WithAllowedRoots(ctx, paths)
	return core.WithWorkDirectories(ctx, dirs)
}

// injectIgnoreChecker builds a multi-root ignore resolver from the session's
// workspace path plus its auxiliary work directories and attaches it to the
// context via sdktools.WithIgnoreChecker. Read-style search/listing tools
// (glob, ripgrep) consult it to honour each root's own .gitignore + .aiignore;
// read_file remains unrestricted.
//
// dirs is the work-directory slice the caller already loaded (shared with
// injectWorkDirectories) so loadWorkDirectories runs once per task rather than
// twice. For No Project sessions (no workspace and no work directories) ctx is
// returned unchanged, so behaviour is identical to before.
//
// Each root is symlink-resolved before being handed to the resolver so the
// roots match the (symlink-resolved) form that glob/ripgrep query paths in. A
// resolver is then built PER ROOT with skip-and-log on failure: a single bad
// root (e.g. a vanished or unreadable work-directory entry) only drops that
// root and must not disable ignore filtering for the healthy roots. The
// survivors are combined via ignore.NewMultiFromResolvers.
func (m *Manager) injectIgnoreChecker(ctx context.Context, session *Session, dirs []core.WorkDirectory) context.Context {
	rawRoots := make([]string, 0, 1+len(dirs))
	if session.WorkspacePath != "" {
		rawRoots = append(rawRoots, session.WorkspacePath)
	}
	for _, d := range dirs {
		if d.Path != "" {
			rawRoots = append(rawRoots, d.Path)
		}
	}
	if len(rawRoots) == 0 {
		return ctx
	}

	// Symlink-resolve + dedupe roots so they match the (resolved) form
	// glob/ripgrep query paths in.
	roots := make([]string, 0, len(rawRoots))
	seen := make(map[string]struct{}, len(rawRoots))
	for _, r := range rawRoots {
		resolved, err := filepath.EvalSymlinks(r)
		if err != nil {
			m.log().Debug("ignore checker: root symlink resolution failed, using raw path", "path", r, "error", err)
			resolved = r
		}
		if _, dup := seen[resolved]; dup {
			continue
		}
		seen[resolved] = struct{}{}
		roots = append(roots, resolved)
	}

	// Build a resolver per root from the cache. Roots that are not yet cached
	// are built asynchronously — the resolver is expensive (it walks the
	// entire directory tree for .gitignore/.aiignore files) and blocking
	// SendMessage on a large directory (e.g. the Go module cache with tens of
	// thousands of entries) causes multi-minute hangs. On the first message
	// after startup the checker may be incomplete; subsequent messages pick up
	// the cached resolver once the background walk finishes.
	resolvers := make([]*ignore.Resolver, 0, len(roots))
	for _, root := range roots {
		if cached, loaded := m.ignoreCache.Load(root); loaded {
			// The cache only ever stores *ignore.Resolver (a real resolver or
			// the building sentinel), but assert with the comma-ok form so an
			// unexpected type is skipped instead of panicking.
			r, ok := cached.(*ignore.Resolver)
			if !ok {
				continue
			}
			resolvers = append(resolvers, r)
			continue
		}
		// Kick off async build — deduplicated via LoadOrStore of a sentinel
		// so concurrent SendMessage calls don't launch duplicate walks.
		m.startIgnoreBuild(root)
	}
	if len(resolvers) == 0 {
		return ctx
	}
	checker := ignore.NewMultiFromResolvers(m.log(), resolvers...)
	return sdktools.WithIgnoreChecker(ctx, checker)
}

// startIgnoreBuild launches a background goroutine to walk root and cache its
// ignore.Resolver. It is deduplicated: if another goroutine has already started
// (or completed) a build for the same root, this is a no-op.
func (m *Manager) startIgnoreBuild(root string) {
	// Deduplicate via a sentinel "building" marker stored in the cache map.
	// sync.Map.LoadOrStore guarantees exactly one goroutine wins the race.
	sentinel := &ignore.Resolver{}
	if actual, loaded := m.ignoreCache.LoadOrStore(root, sentinel); loaded {
		// Already cached (real resolver or in-flight sentinel) — nothing to do.
		_ = actual
		return
	}

	go func() {
		r, err := ignore.NewResolver(root)
		if err != nil {
			m.log().Debug("ignore checker: background resolver build failed", "root", root, "error", err)
			// Remove the sentinel so a future call can retry.
			m.ignoreCache.Delete(root)
			return
		}
		m.ignoreCache.Store(root, r)
	}()
}

// ignoreFileNames are the files whose change invalidates a cached resolver.
// ignore.NewResolver walks the whole root collecting every occurrence of these,
// so a change to any one of them — anywhere under a root — makes that root's
// cached resolver stale.
var ignoreFileNames = map[string]struct{}{
	".gitignore": {},
	".aiignore":  {},
	".ignore":    {}, // .ignore is honoured by ripgrep and some tools
}

// InvalidateIgnoreCache evicts cached ignore resolvers whose root contains one
// of changedPaths when that path is an ignore-rule file (.gitignore/.aiignore/
// .ignore). It is the invalidation half of the async ignore cache: without it,
// edits to ignore files would be invisible until the app restarts.
//
// It only DELETEs cache entries — it never rebuilds synchronously. The rebuild
// is triggered lazily and asynchronously by the next injectIgnoreChecker call:
// startIgnoreBuild launches a background goroutine and deduplicates concurrent
// builds via a sentinel. Because the rebuild never blocks SendMessage, this
// cannot regress the multi-minute-build case on roots with hundreds of
// thousands of files — at worst the first message after invalidation runs
// without ignore filtering (the no-checker / sentinel path), exactly like the
// first message after startup, until the background walk completes.
//
// changedPaths are expected to be absolute filesystem paths (as reported by the
// workspace watcher / fsnotify); the cache is keyed by symlink-resolved roots.
// pathutil.IsWithinPath resolves symlinks on both sides, so the match is
// correct even when the workspace lives behind an OS symlink.
func (m *Manager) InvalidateIgnoreCache(changedPaths []string) {
	affected := make(map[string]struct{})
	for _, p := range changedPaths {
		base := filepath.Base(p)
		if _, isIgnoreFile := ignoreFileNames[base]; isIgnoreFile {
			affected[p] = struct{}{}
		}
	}
	if len(affected) == 0 {
		return
	}

	// For each affected ignore file, evict every cached root that contains it.
	// The cache typically holds only a handful of roots (workspace + a few
	// auxiliary work directories), so a full Range per affected file is cheap.
	m.ignoreCache.Range(func(key, _ any) bool {
		root, ok := key.(string)
		if !ok {
			return true
		}
		for ignoreFile := range affected {
			within, err := pathutil.IsWithinPath(root, ignoreFile)
			if err != nil {
				// Resolve failure (e.g. vanished root) — evict to be safe; the
				// lazy rebuild will re-add it (or skip it) on next use.
				m.ignoreCache.Delete(root)
				m.log().Debug("ignore checker: evicted root on resolve error", "root", root, "error", err)
				continue
			}
			if within {
				m.ignoreCache.Delete(root)
				m.log().Debug("ignore checker: invalidated root after ignore-file change", "root", root, "trigger", ignoreFile)
			}
		}
		return true
	})
}

// Runs in a goroutine, results come via events.
// reviewMode, when true, marks the message as carrying code review feedback
// the agent must address (see core HandleOptions.ReviewMode).
func (m *Manager) SendMessage(ctx context.Context, id, text string, activeSkills, activeAgents []string, modelOverride, reasoningEffort string, goal bool, goalBudget string, reviewMode bool) error {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	// Check if already active (prevent double-send on the same session)
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}

	// Set active and create cancellable context with session ID
	session.active = true
	doneCh := make(chan struct{})
	session.done = doneCh
	taskCtx, cancel := context.WithCancel(ContextWithSessionID(ctx, id))
	// Enrich context with session workspace path for tool security heuristics
	taskCtx = sdktools.WithWorkspacePath(taskCtx, session.WorkspacePath)
	taskCtx = sdktools.WithTempDir(taskCtx, session.TempDir)
	taskCtx = sdktools.WithCoherence(taskCtx, m.fileTracker)
	if session.ProjectID == project.NoProjectID {
		taskCtx = coretools.WithNoProject(taskCtx)
	}
	session.cancel = cancel
	session.mu.Unlock()

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// Inject auxiliary work directories (allowed roots + prompt list), and
	// attach a multi-root ignore checker so glob/ripgrep honour each root's
	// own .gitignore + .aiignore. dirs is loaded once and shared so the
	// (DB-hitting) loadWorkDirectories runs a single time per task. Both are
	// no-ops for No Project sessions.
	dirs := m.loadWorkDirectories(session)
	taskCtx = m.injectWorkDirectories(taskCtx, dirs)
	taskCtx = m.injectIgnoreChecker(taskCtx, session, dirs)

	// Emit message received event
	m.emitFunc(Event{
		SessionID: id,
		Type:      "message_received",
		Data: MessageReceivedData{
			SessionID: id,
			Text:      text,
		},
	})

	// Check if this is the first message (session has default name)
	// and spawn title generation in background.
	session.mu.Lock()
	sessionName := session.Name
	session.mu.Unlock()
	m.mu.RLock()
	titleGen := m.titleGen
	store := m.sessionStore
	m.mu.RUnlock()
	if sessionName == "Session "+safeSessionPrefix(id) && titleGen != nil {
		dumpFile := session.DumpFile()
		go func() {
			if dumpFile != nil {
				defer func() { _ = dumpFile.Close() }()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if dumpFile != nil {
				ctx = agent.WithDumpWriter(ctx, dumpFile)
			}
			title := titleGen.Generate(ctx, text, activeSkills)
			if title == "" {
				return
			}
			if err := m.RenameSession(id, title); err != nil {
				m.log().Warn("failed to rename session with generated title", "session", id, "error", err)
				return
			}
			m.log().Info("session auto-named", "session", id, "title", title)
			// Persist rename to store
			if store != nil {
				if err := store.RenameSession(context.Background(), id, title); err != nil {
					m.log().Warn("failed to persist session title", "session", id, "error", err)
				}
			}
		}()
	}

	// Launch goroutine to handle the message
	go func(ctx context.Context, msg string, skills []string, agents []string) {
		defer close(doneCh)
		defer func() {
			session.mu.Lock()
			session.active = false
			session.cancel = nil
			session.done = nil
			session.mu.Unlock()
		}()

		// Snapshot pending attachments and clear them so they are flushed
		// exactly once into the blackboard for this SendMessage — regardless
		// of whether the message continues an interrupted task or starts a
		// fresh one. Done before the continue check so the resume path also
		// receives them (it bypasses HandleMessage, which would otherwise
		// flush them via setupBlackboard).
		session.mu.Lock()
		pendingAttachments := session.pendingAttachments
		session.pendingAttachments = nil
		pendingImages := session.pendingImageAttachments
		session.pendingImageAttachments = nil
		session.mu.Unlock()

		// Clear the pending-attachment chips now that the attachments have been
		// flushed into the blackboard for this task. Only emit when there were
		// pending attachments to avoid a spurious event on attachment-free sends.
		if len(pendingAttachments) > 0 || len(pendingImages) > 0 {
			m.emitAttachmentsChanged(id, []AttachmentInfo{}, nil)
		}

		// Convert staged image attachments into LLM image content blocks for
		// the context window. The blocks carry the base64 image data (held in
		// memory only until this snapshot); the on-disk copy at FilePath
		// persists for restart reconstruction via ChatMessage.Metadata.
		imageBlocks := imageAttachmentsToContentBlocks(pendingImages)

		// Detect goal mode (leading "/goal" command OR an explicit goal flag)
		// BEFORE the resume check. A goal request starts a fresh goal pursuit
		// rather than continuing an interrupted task, so the resume path is
		// skipped and any unfinished task is abandoned (see
		// abandonUnfinishedTaskForGoal). The two signals are OR-ed: a goal
		// flag enables goal mode regardless of the message prefix.
		goalMsg, isGoal := core.DetectAndStripGoalMode(msg)
		goalEnabled := isGoal || goal

		if goalEnabled {
			// Goal mode supersedes any interrupted task: cancel it so it does
			// not linger as resumable WIP across the new goal task, then fall
			// through to the goal dispatch below. Best-effort; a missing task
			// store or no unfinished task is a no-op.
			m.abandonUnfinishedTaskForGoal(id)
		} else if m.tryContinueInterruptedTask(ctx, id, session, msg, modelOverride, reasoningEffort, pendingAttachments) {
			// Continue an interrupted (unfinished) task if one exists: the new
			// user message is appended as a final user-nudge turn to the prior
			// trajectory and the ReAct cycle resumes — no routing, no new task,
			// no conversation-history pair. Returns true when it took the path.
			return
		}

		// No unfinished task took the resume path (or goal was requested): run
		// the normal route → plan → execute flow, or dispatch to the goal loop.
		// Get last completed task ID for continuation.
		session.mu.Lock()
		lastTaskID := session.lastCompletedTaskID
		session.mu.Unlock()

		// On a continuation (lastTaskID != ""), goal mode runs ON the restored
		// blackboard of the prior completed task: the agent keeps the inherited
		// facts and history, and a fresh goal is derived from the new message.
		// When the goal request supplanted an interrupted task, that task was
		// already cancelled above and lastTaskID refers to the last COMPLETED
		// task (or "" for a fresh goal).

		// Parse the optional budget override (JSON or empty). Empty/invalid
		// → nil (unlimited). A parse error is logged but does not block the
		// send; the goal simply runs unlimited.
		var budgetOverride *goalpkg.GoalBudget
		if goalEnabled {
			budgetOverride = parseGoalBudget(goalBudget, m.log())
		}

		// When goal mode is enabled, the orchestrator receives the /goal-stripped
		// message; otherwise it receives the original (preprocessed) message.
		// goalMsg == msg when no /goal prefix is present, so this is safe even
		// when goal mode was enabled by the flag rather than the command.
		hmMsg := msg
		if goalEnabled {
			hmMsg = goalMsg
		}

		result, err := session.orchestrator.HandleMessage(ctx, hmMsg, id, core.HandleOptions{
			TaskID:             lastTaskID,
			UserSkills:         skills,
			UserAgents:         agents,
			ModelOverride:      modelOverride,
			ReasoningEffort:    reasoningEffort,
			SessionPlansDir:    config.SessionPlansDir(m.agentDir, session.ProjectID, id),
			PendingAttachments: pendingAttachments,
			PendingImages:      imageBlocks,
			Goal:               goalEnabled,
			GoalBudgetOverride: budgetOverride,
			ReviewMode:         reviewMode,
		})

		// Distinguish partial-success (incomplete plan) from total failure.
		incomplete := err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil
		if incomplete {
			m.log().Warn("task completed with incomplete execution", "session_id", id, "task_id", lastTaskID, "error", err)
			err = nil
		}

		// Fallback: if continuation failed (restore error) and we had a TaskID, retry fresh.
		// Preserves goal mode: a failed goal-on-continuation retries as a fresh
		// goal task rather than silently dropping the flag (and, when goal was
		// enabled via the "/goal" prefix, leaking the prefix into the fresh task
		// — hmMsg is already /goal-stripped, msg is not).
		if err != nil && lastTaskID != "" {
			m.log().Warn("continuation failed, falling back to fresh workflow", "session_id", id, "task_id", lastTaskID, "error", err)
			session.mu.Lock()
			session.lastCompletedTaskID = ""
			session.mu.Unlock()
			result, err = session.orchestrator.HandleMessage(ctx, hmMsg, id, core.HandleOptions{
				TaskID:             "",
				UserSkills:         skills,
				UserAgents:         agents,
				ModelOverride:      modelOverride,
				ReasoningEffort:    reasoningEffort,
				SessionPlansDir:    config.SessionPlansDir(m.agentDir, session.ProjectID, id),
				PendingAttachments: pendingAttachments,
				PendingImages:      imageBlocks,
				Goal:               goalEnabled,
				GoalBudgetOverride: budgetOverride,
				ReviewMode:         reviewMode,
			})
			if err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil {
				m.log().Warn("task completed with incomplete execution (after fallback)", "session_id", id, "error", err)
				err = nil
			}
		}

		if err != nil {
			// Check if it was a cancellation
			if ctx.Err() == context.Canceled {
				// On shutdown, leave the task in_progress so it can be
				// resumed after restart; only user-initiated cancels are
				// persisted as cancelled.
				if m.emitTaskCancelledUnlessShuttingDown(id) {
					m.persistCancellationIfUnfinished(id)
				}
				return
			}

			// Emit error event
			m.emitFunc(Event{
				SessionID: id,
				Type:      "error",
				Data: ErrorData{
					SessionID: id,
					Error:     err.Error(),
				},
			})
			m.emitResumableIfUnfinished(id, resumableReasonFromError(err))
			return
		}

		// Store the task ID for potential continuations
		if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
			session.mu.Lock()
			session.lastCompletedTaskID = pbb.TaskID()
			session.mu.Unlock()
		}

		// Safety net: if context was cancelled but orchestrator returned no error,
		// still treat as cancellation — do not emit partial results as final.
		if ctx.Err() == context.Canceled {
			if m.emitTaskCancelledUnlessShuttingDown(id) {
				m.persistCancellationIfUnfinished(id)
			}
			return
		}

		// Emit done event with result (carries the typed success contract;
		// degraded outcomes surface a resumable action or a fallback warning).
		m.emitTaskComplete(id, result, nil)
	}(taskCtx, text, activeSkills, activeAgents)

	return nil
}

// parseGoalBudget parses a goal-budget override string into a *goal.GoalBudget.
// The string is expected to be a JSON object with the goal.GoalBudget field
// (max_turns). An empty string returns nil (unlimited). An unparseable string
// is logged and returns nil so a malformed budget never blocks the send — the
// goal simply falls back to unlimited.
func parseGoalBudget(raw string, log *slog.Logger) *goalpkg.GoalBudget {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var b goalpkg.GoalBudget
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		log.Warn("goal budget override is not valid JSON; falling back to unlimited", "raw", raw, "error", err)
		return nil
	}
	return &b
}

// tryContinueInterruptedTask checks whether the session has an unfinished
// (interrupted) task and, if so, continues its ReAct cycle by appending the
// user's new message as a final seeded user-nudge turn. It returns true when
// it took the resume path (the caller must NOT proceed with the normal
// HandleMessage flow), and false when there is no unfinished task (the caller
// runs the normal route → plan → execute flow).
//
// The user message is rendered as a user turn in the LLM context (via the
// trajectory's Step.UserNudge, which stepsToMessages emits as a {role:user}
// message), so the agent sees the new instruction immediately after the prior
// trajectory. The task is NOT re-routed, NO new task is created, and NO new
// conversation-history pair is recorded — the task lifecycle simply continues.
// (Resume's recordResumeOutcome records the assistant side only, mirroring the
// app-restart ResumeTask path.)
//
// Because this path bypasses HandleMessage (step 0 there applies them), the
// per-request model/reasoning overrides are applied here via
// orchestrator.ApplyRequestOverrides, and pending attachments are flushed into
// the restored blackboard (mirroring HandleMessage's setupBlackboard
// continuation path). Both are no-ops when empty.
//
// The message_received event has already been emitted by SendMessage, so the
// UI shows the user message; the resumed task emits its own task lifecycle
// events (routing/context_fill/steps/task_complete) on top.
func (m *Manager) tryContinueInterruptedTask(
	ctx context.Context,
	id string,
	session *Session,
	message string,
	modelOverride, reasoningEffort string,
	pendingAttachments []orchestration.Attachment,
) bool {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return false
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(id)
	if err != nil {
		m.log().Warn("continue-interrupted-task: failed to look up unfinished task; falling back to fresh task", "session", id, "error", err)
		return false
	}
	if taskID == "" {
		return false
	}

	// Restore the blackboard (facts / step results) for the interrupted task.
	bb, err := RestoreBlackboard(taskID, id, adapter, nil)
	if err != nil {
		m.log().Warn("continue-interrupted-task: failed to restore blackboard; falling back to fresh task", "session", id, "error", err)
		return false
	}
	if bb == nil {
		return false
	}

	// Apply per-request model/reasoning overrides. This path bypasses
	// HandleMessage, where step 0 would otherwise apply them, so the resumed
	// task picks up the model/reasoning the user selected for this message
	// instead of silently inheriting the prior task's settings. No-op when
	// both overrides are empty.
	session.orchestrator.ApplyRequestOverrides(ctx, modelOverride, reasoningEffort)

	// Flush attachments staged by AttachFiles into the restored blackboard
	// (mirrors HandleMessage's setupBlackboard continuation path). bb is a
	// *PersistentBlackboard, so AddAttachment persists to the store.
	for _, a := range pendingAttachments {
		bb.AddAttachment(a)
	}

	// Load the persisted trajectory and append the user message as a final
	// nudge step so it renders as a {role:user} turn after the prior steps.
	resumeSteps, err := adapter.LoadTrajectory(taskID)
	if err != nil {
		m.log().Warn("continue-interrupted-task: failed to load trajectory; falling back to fresh task", "session", id, "error", err)
		return false
	}
	resumeSteps = append(resumeSteps, agent.Step{UserNudge: message})

	// Resolve the persisted routing decision (optional — Resume defaults to
	// the general domain when nil). The task is never re-routed.
	var routing *router.RoutingDecision
	if state, stateErr := adapter.LoadTaskState(taskID); stateErr != nil {
		m.log().Warn("continue-interrupted-task: failed to load task state; resuming without routing", "session", id, "error", stateErr)
	} else if state != nil {
		routing = state.RoutingDecision
	}

	// Resolve the prior task_failed_resumable banner so it does not linger
	// after the resumed execution finishes.
	m.resolveResumableTaskMessage(id, taskID, "resumed")

	result, err := session.orchestrator.Resume(ctx, bb, routing, config.SessionPlansDir(m.agentDir, session.ProjectID, id), resumeSteps, nil)

	// Shared completion handling (mirrors ResumeTask's goroutine tail).
	if err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil {
		m.log().Warn("continued task completed with incomplete execution", "session_id", id, "error", err)
		err = nil
	}

	if err != nil {
		if ctx.Err() == context.Canceled {
			if m.emitTaskCancelledUnlessShuttingDown(id) {
				bb.CancelTask()
			}
			return true
		}
		m.emitFunc(Event{
			SessionID: id,
			Type:      "error",
			Data: ErrorData{
				SessionID: id,
				Error:     err.Error(),
			},
		})
		m.emitResumableIfUnfinished(id, resumableReasonFromError(err))
		return true
	}

	// Store the task ID for potential further continuations.
	if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
		session.mu.Lock()
		session.lastCompletedTaskID = pbb.TaskID()
		session.mu.Unlock()
	}

	m.emitTaskComplete(id, result, nil)
	return true
}

// ResumeTask checks for an unfinished task in the given session and resumes it.
// Returns nil if no unfinished task exists or if the task store is not configured.
// Invoked both by the manual Resume button (with the user's current model/reasoning
// selection) and on app restart to resume interrupted tasks. The optional
// modelOverride/reasoningEffort are applied (same as a fresh SendMessage) so a
// model/reasoning switch the user made before resuming is honored instead of
// silently inheriting the interrupted task's settings.
func (m *Manager) ResumeTask(ctx context.Context, id, modelOverride, reasoningEffort string) error {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()

	if ts == nil {
		return nil // no task persistence — nothing to resume
	}

	session, restoreErr := m.getOrRestoreSession(id)
	if restoreErr != nil {
		return fmt.Errorf("failed to restore session: %w", restoreErr)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(id)
	if err != nil {
		return fmt.Errorf("failed to check unfinished tasks: %w", err)
	}
	if taskID == "" {
		return nil // no unfinished task
	}

	// Load task state and restore blackboard.
	bb, err := RestoreBlackboard(taskID, id, adapter, nil)
	if err != nil {
		return fmt.Errorf("failed to restore blackboard: %w", err)
	}
	if bb == nil {
		return nil // task record not found (race condition or cleanup)
	}

	// Load task state. The routing decision and plan are OPTIONAL for resume:
	// a persisted routing decision is reused (the task is not re-routed), and
	// a plan is not required — the Conductor handles plan-less tasks via a
	// standalone checklist. state may be nil when no state was persisted.
	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		return fmt.Errorf("failed to load task state: %w", err)
	}

	// Resolve the persisted routing decision (may be nil — Resume defaults to
	// the general domain in that case) and original request (may be empty).
	var routing *router.RoutingDecision
	originalRequest := bb.GetOriginalRequest()
	if state != nil {
		routing = state.RoutingDecision
		if state.OriginalRequest != "" {
			originalRequest = state.OriginalRequest
		}
	}

	// Load the persisted trajectory so the resumed executor continues from the
	// checkpoint instead of starting fresh. An empty/nil trajectory is valid —
	// the Conductor simply starts a fresh ReAct loop.
	resumeSteps, err := adapter.LoadTrajectory(taskID)
	if err != nil {
		return fmt.Errorf("failed to load trajectory: %w", err)
	}

	// Load the persisted goal state. When present and non-terminal (paused or
	// still active), the orchestrator re-enters the goal loop instead of the
	// plain Conductor path. A nil goal state (non-goal task) resumes normally.
	goalState, err := adapter.LoadGoalState(taskID)
	if err != nil {
		return fmt.Errorf("failed to load goal state: %w", err)
	}

	session.mu.Lock()
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}
	session.active = true
	resumeDoneCh := make(chan struct{})
	session.done = resumeDoneCh
	taskCtx, cancel := context.WithCancel(ContextWithSessionID(ctx, id))
	taskCtx = sdktools.WithWorkspacePath(taskCtx, session.WorkspacePath)
	taskCtx = sdktools.WithTempDir(taskCtx, session.TempDir)
	taskCtx = sdktools.WithCoherence(taskCtx, m.fileTracker)
	if session.ProjectID == project.NoProjectID {
		taskCtx = coretools.WithNoProject(taskCtx)
	}
	session.cancel = cancel
	session.mu.Unlock()

	// Apply per-request model/reasoning overrides. This path bypasses
	// HandleMessage, where step 0 would otherwise apply them, so the resumed
	// task picks up the model/reasoning the user selected instead of silently
	// inheriting the interrupted task's settings. No-op when both are empty.
	// Run after the active-check (so overrides never leak into a concurrently
	// running task) but before launching the Resume goroutine, so the emitter's
	// cached model is synchronized before the initial context_fill is emitted.
	session.orchestrator.ApplyRequestOverrides(ctx, modelOverride, reasoningEffort)

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// Inject auxiliary work directories (allowed roots + prompt list), and
	// attach a multi-root ignore checker so glob/ripgrep honour each root's
	// own .gitignore + .aiignore. dirs is loaded once and shared so the
	// (DB-hitting) loadWorkDirectories runs a single time per task. Both are
	// no-ops for No Project sessions.
	dirs := m.loadWorkDirectories(session)
	taskCtx = m.injectWorkDirectories(taskCtx, dirs)
	taskCtx = m.injectIgnoreChecker(taskCtx, session, dirs)

	// Emit resume event so the frontend knows a task is resuming.
	m.emitFunc(Event{
		SessionID: id,
		Type:      "task_resumed",
		Data: MessageReceivedData{
			SessionID: id,
			Text:      originalRequest,
		},
	})

	// Mark the prior task_failed_resumable banner as resolved so it does not
	// reappear as pending after the resume goroutine finishes. Done here (at
	// the committed point, right before launching) so a failed restore still
	// leaves the banner actionable for a retry.
	m.resolveResumableTaskMessage(id, taskID, "resumed")

	// Launch goroutine (same pattern as SendMessage).
	go func() {
		defer close(resumeDoneCh)
		defer func() {
			session.mu.Lock()
			session.active = false
			session.cancel = nil
			session.done = nil
			session.mu.Unlock()
		}()

		result, err := session.orchestrator.Resume(taskCtx, bb, routing, config.SessionPlansDir(m.agentDir, session.ProjectID, id), resumeSteps, goalState)

		// Treat partial execution like the SendMessage path — deliver best-effort
		// output and rely on emitResumableIfUnfinished to expose resumability.
		if err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil {
			m.log().Warn("resumed task completed with incomplete execution", "session_id", id, "error", err)
			err = nil
		}

		if err != nil {
			if taskCtx.Err() == context.Canceled {
				// On shutdown, leave the restored task in_progress so it can
				// be resumed after restart; only user-initiated cancels mark
				// the task as cancelled.
				if m.emitTaskCancelledUnlessShuttingDown(id) {
					bb.CancelTask()
				}
				return
			}

			m.emitFunc(Event{
				SessionID: id,
				Type:      "error",
				Data: ErrorData{
					SessionID: id,
					Error:     err.Error(),
				},
			})
			m.emitResumableIfUnfinished(id, resumableReasonFromError(err))
			return
		}

		// Store the task ID for potential continuations
		if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
			session.mu.Lock()
			session.lastCompletedTaskID = pbb.TaskID()
			session.mu.Unlock()
		}

		m.emitTaskComplete(id, result, nil)
	}()

	return nil
}

// CancelUnfinishedTask discards any unfinished task in the given session by
// marking it as cancelled in the task store. After this returns successfully,
// the session no longer has a resumable task and emitResumableIfUnfinished
// will not emit a "task_failed_resumable" event for it.
// Returns nil if no task store is configured or no unfinished task exists.
func (m *Manager) CancelUnfinishedTask(sessionID string) error {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return nil
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		return fmt.Errorf("failed to look up unfinished task: %w", err)
	}
	if taskID == "" {
		return nil
	}
	if err := adapter.PersistCancellation(taskID); err != nil {
		return fmt.Errorf("failed to mark task as cancelled: %w", err)
	}
	// Mark the prior task_failed_resumable banner as resolved so it does not
	// reappear as pending on session reload after the user discards the task.
	m.resolveResumableTaskMessage(sessionID, taskID, "cancelled")
	return nil
}

// SessionRuntimeStatus describes the live and persisted execution state of a
// session, so the frontend can reconstruct "is something running / resumable"
// after app restart or session switch instead of assuming idle.
type SessionRuntimeStatus struct {
	Active            bool   `json:"active"`
	HasUnfinishedTask bool   `json:"has_unfinished_task"`
	UnfinishedTaskID  string `json:"unfinished_task_id,omitempty"`
}

// GetSessionRuntimeStatus returns whether a task is currently running in the
// session (in-memory) and whether an unfinished (resumable) task is persisted
// in the task store. It never restores a session as a side effect.
func (m *Manager) GetSessionRuntimeStatus(sessionID string) (SessionRuntimeStatus, error) {
	var status SessionRuntimeStatus

	// Memory-only lookup: a session that is not in memory cannot be active,
	// and restoring it here would be an unwanted side effect for a status poll.
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess != nil {
		status.Active = sess.IsActive()
	}

	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts != nil {
		adapter := NewTaskStoreAdapter(ts)
		taskID, err := adapter.GetUnfinishedTaskID(sessionID)
		if err != nil {
			return status, fmt.Errorf("failed to look up unfinished task: %w", err)
		}
		if taskID != "" {
			status.HasUnfinishedTask = true
			status.UnfinishedTaskID = taskID
		}
	}

	return status, nil
}

// emitTaskCancelledUnlessShuttingDown emits the "task_cancelled" event for the
// session unless the manager is shutting down. It returns true when the caller
// should persist the cancellation (user-initiated cancel), and false when the
// manager is shutting down — in that case the task must stay in_progress so it
// remains resumable after restart, so the caller skips persistence entirely.
func (m *Manager) emitTaskCancelledUnlessShuttingDown(id string) bool {
	if m.shuttingDown.Load() {
		return false
	}
	m.emitFunc(Event{
		SessionID: id,
		Type:      "task_cancelled",
		Data:      TaskCancelledData{SessionID: id},
	})
	return true
}

// persistCancellationIfUnfinished marks the session's unfinished task (if any)
// as cancelled in the task store. Best-effort: errors are logged only.
func (m *Manager) persistCancellationIfUnfinished(sessionID string) {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return
	}
	adapter := NewTaskStoreAdapter(ts)
	tid, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		m.log().Warn("failed to look up unfinished task on cancel", "session", sessionID, "error", err)
		return
	}
	if tid == "" {
		return
	}
	if err := adapter.PersistCancellation(tid); err != nil {
		m.log().Warn("failed to persist cancellation", "task", tid, "error", err)
	}
}

// abandonUnfinishedTaskForGoal cancels the session's unfinished task (if any)
// so a goal request can start a fresh goal pursuit instead of resuming the
// interrupted task. It persists the cancellation, resolves any pending
// task_failed_resumable banner so it does not linger, and emits a service
// event so the user sees the interrupted task was abandoned for the goal.
// Best-effort: errors are logged only. No-op when there is no task store or
// no unfinished task.
func (m *Manager) abandonUnfinishedTaskForGoal(id string) {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return
	}
	adapter := NewTaskStoreAdapter(ts)
	tid, err := adapter.GetUnfinishedTaskID(id)
	if err != nil {
		m.log().Warn("goal-on-resume: failed to look up unfinished task to abandon", "session", id, "error", err)
		return
	}
	if tid == "" {
		return
	}
	if err := adapter.PersistCancellation(tid); err != nil {
		m.log().Warn("goal-on-resume: failed to cancel unfinished task", "task", tid, "error", err)
	}
	m.resolveResumableTaskMessage(id, tid, "abandoned_for_goal")
	m.emitFunc(Event{
		SessionID: id,
		Type:      "service",
		Data: map[string]any{
			"content": "Interrupted task abandoned to start goal mode.",
			"phase":   "orchestration",
		},
	})
}

// unfinishedTaskID returns the ID of the most recent unfinished (in_progress
// or failed) task for the session, or "" if there is none, no task store is
// configured, or the lookup errors. It is a pure lookup — it never emits.
func (m *Manager) unfinishedTaskID(sessionID string) string {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return ""
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		m.log().Warn("failed to get unfinished task ID", "session", sessionID, "error", err)
		return ""
	}
	return taskID
}

// emitResumableIfUnfinished checks whether the session has an unfinished task
// in the task store and, if so, emits a "task_failed_resumable" event so the
// frontend can offer a Resume button. It returns true when the event was
// emitted, so callers that KNOW the execution was degraded can surface a
// fallback warning when the resumable safety net is unavailable (nil task
// store, lookup error, or no unfinished record). reason is a concise,
// contextual cause prepended to the banner message and carried in the
// structured Reason field; pass "" for the generic message.
func (m *Manager) emitResumableIfUnfinished(sessionID, reason string) bool {
	taskID := m.unfinishedTaskID(sessionID)
	if taskID == "" {
		return false
	}

	message := "Plan execution failed. You can resume to retry from where it left off."
	if reason != "" {
		message = reason + " You can resume to retry from where it left off."
	}

	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "task_failed_resumable",
		Data: TaskFailedResumableData{
			Message: message,
			TaskID:  taskID,
			Reason:  reason,
		},
	})
	return true
}

// completionReason maps a completion outcome to a concise, readable reason
// sentence for the resume banner. Returns "" for a full success (the caller
// must not call emitResumableIfUnfinished for successes — see emitTaskComplete).
func completionReason(completion string) string {
	switch completion {
	case "partial":
		return "Execution reached a partial state."
	case "failed":
		return "Execution failed."
	case "aborted":
		return "Execution was aborted."
	default:
		return ""
	}
}

// resumableReasonFromError derives a concise, user-facing reason for the
// resume banner from a terminal execution error. It trims to the first line,
// capitalizes the first letter, and caps the length so the banner stays
// readable. Returns "" when err is nil, leaving the generic message intact.
func resumableReasonFromError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return ""
	}
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	const maxReasonRunes = 140
	if r := []rune(msg); len(r) > maxReasonRunes {
		msg = string(r[:maxReasonRunes-1]) + "…"
	}
	if r := []rune(msg); len(r) > 0 {
		msg = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	return msg
}

// resolveResumableTaskMessage marks the persisted "task_failed_resumable"
// message for the given task as resolved, so the Resume/Cancel banner does
// not reappear as pending on session reload after the user resumes or cancels.
// Best-effort: errors are logged only — the in-memory UI is already updated
// optimistically by the frontend, and a missed persist is self-healing via
// stale reconciliation on the next reload.
func (m *Manager) resolveResumableTaskMessage(sessionID, taskID, decision string) {
	m.mu.RLock()
	store := m.sessionStore
	m.mu.RUnlock()
	if store == nil || taskID == "" {
		return
	}
	extra := map[string]any{"resolved": true}
	if decision != "" {
		extra["decision"] = decision
	}
	if err := store.ResolvePendingMessage(context.Background(), sessionID, "task_failed_resumable", "task_id", taskID, extra); err != nil {
		m.log().Warn("failed to resolve persisted task_failed_resumable message",
			"session", sessionID, "task", taskID, "decision", decision, "error", err)
	}
}

// taskCompletionInfo derives the frontend-facing success contract from the
// typed execution status on a HandleResult.
func taskCompletionInfo(result *core.HandleResult) (success bool, completion string) {
	if result == nil {
		return true, "full"
	}
	switch result.Status {
	case orchestration.ExecutionStatusPartial, orchestration.ExecutionStatusCancelled:
		return false, "partial"
	case orchestration.ExecutionStatusFailed:
		return false, "failed"
	case orchestration.ExecutionStatusAborted:
		return false, "aborted"
	default:
		return true, "full"
	}
}

// emitTaskComplete emits the "task_complete" event with the typed success
// contract and guarantees the degraded-outcome surfacing: for non-successful
// completions it either emits "task_failed_resumable" or, when the resumable
// safety net cannot deliver (no task store, lookup failure, no unfinished
// record), a visible service warning — never a silent visual success.
func (m *Manager) emitTaskComplete(sessionID string, result *core.HandleResult, plan *orchestration.Plan) {
	success, completion := taskCompletionInfo(result)
	data := TaskCompleteData{
		SessionID:  sessionID,
		Success:    success,
		Completion: completion,
	}
	if result != nil {
		data.Output = result.Output
		data.RoutingDecision = result.RoutingDecision
		data.Plan = result.Plan
		data.Reflections = result.Reflections
	}
	if plan != nil {
		data.Plan = plan
	}
	m.emitFunc(Event{SessionID: sessionID, Type: "task_complete", Data: data})

	// On a genuinely successful completion, NEVER offer a resume action — the
	// result above is final. If the task that just ran is still marked
	// unfinished, the completion write raced or was dropped; surface a
	// persistence warning instead of a misleading "resume" banner (see
	// warnIfCompletionDidNotPersist).
	if success {
		m.warnIfCompletionDidNotPersist(sessionID, result)
		return
	}

	resumableEmitted := m.emitResumableIfUnfinished(sessionID, completionReason(completion))
	if !resumableEmitted {
		m.log().Warn("degraded task completion without resumable safety net", "session", sessionID, "completion", completion)
		m.emitFunc(Event{
			SessionID: sessionID,
			Type:      "service",
			Data: map[string]any{
				"content": fmt.Sprintf("Task finished with %s execution, but it cannot be resumed. Review the output above.", completion),
				"phase":   "orchestration",
			},
		})
	}
}

// warnIfCompletionDidNotPersist checks whether the task that just completed
// successfully is still marked unfinished. When it is, the completion write
// (CompleteTask) raced or was dropped, so a service warning is emitted so the
// user knows a persistence issue occurred — but the result above is valid and
// no misleading "resume" banner is offered. A different (stale) unfinished
// task is ignored, as it predates this successful execution.
func (m *Manager) warnIfCompletionDidNotPersist(sessionID string, result *core.HandleResult) {
	var completedTaskID string
	if result != nil {
		if pbb, ok := result.Blackboard.(core.PersistableBlackboard); ok {
			completedTaskID = pbb.TaskID()
		}
	}
	if completedTaskID == "" {
		return
	}
	if unfinished := m.unfinishedTaskID(sessionID); unfinished == completedTaskID {
		m.log().Error("task succeeded but completion did not persist", "session", sessionID, "task", completedTaskID)
		m.emitFunc(Event{
			SessionID: sessionID,
			Type:      "service",
			Data: map[string]any{
				"content": "Task completed successfully, but a persistence issue was detected. The result above is valid.",
				"phase":   "persistence",
			},
		})
	}
}

// CancelTask cancels the currently running task in a session.
// It signals cancellation and waits (with timeout) for the task goroutine to finish.
func (m *Manager) CancelTask(id string) error {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()

	if !session.active {
		session.mu.Unlock()
		return ErrNoActiveTask
	}

	doneCh := session.done
	if session.cancel != nil {
		session.cancel()
	}
	session.mu.Unlock()

	// Wait for the goroutine to finish so the task_cancelled event is emitted
	// before this method returns to the frontend.
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(m.stopTimeout):
			m.log().Warn("timed out waiting for task goroutine to stop on cancel", "session_id", id)
		}
	}

	return nil
}

// GetBlackboardState returns the current blackboard state for a session.
// It uses the in-memory lastCompletedTaskID if available, otherwise falls back
// to the most recent task ID from the database.
// Returns nil, nil if no task state is available.
func (m *Manager) GetBlackboardState(sessionID string) (*BlackboardState, error) {
	sess, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to restore session: %w", err)
	}
	if sess == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()

	if ts == nil {
		return nil, nil // no task persistence — no blackboard state
	}

	// Try in-memory lastCompletedTaskID first.
	sess.mu.Lock()
	taskID := sess.lastCompletedTaskID
	sess.mu.Unlock()

	// Fallback: query the database for the latest task.
	if taskID == "" {
		dbTaskID, dbErr := ts.GetLatestTaskID(context.Background(), sessionID)
		if dbErr != nil {
			return nil, fmt.Errorf("failed to get latest task ID: %w", dbErr)
		}
		if dbTaskID == "" {
			return nil, nil // no tasks for this session
		}
		taskID = dbTaskID
	}

	adapter := NewTaskStoreAdapter(ts)
	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to load task state: %w", err)
	}
	if state == nil {
		return nil, nil
	}

	return &BlackboardState{TaskState: state}, nil
}

// BlackboardState wraps a core.TaskState for the GetBlackboardState API.
type BlackboardState struct {
	TaskState *core.TaskState
}
