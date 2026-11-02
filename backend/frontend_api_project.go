package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/workspace"
)

// CreateProject creates a new project. If externalPath is empty, an internal workspace is created.
func (f *FrontendAPI) CreateProject(name, externalPath string) (*project.ProjectInfo, error) {
	f.seedAcquire()
	if f.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}
	p, err := f.projectManager.CreateProject(name, externalPath)
	if err != nil {
		return nil, err
	}

	f.emitEvent(EventProjectCreated, p)

	return p, nil
}

// DeleteProject deletes a project and all its sessions.
func (f *FrontendAPI) DeleteProject(id string) error {
	f.seedAcquire()
	if f.projectManager == nil {
		return errors.New("project subsystem not initialized")
	}
	// Validate the id BEFORE any destructive work: the manager rejects the
	// No Project pseudo-project one layer below, but the session teardown
	// loop and the vector release here must never run for it (or an empty
	// id) — a forged RPC would otherwise delete every loaded CHAT session's
	// on-disk workspace and THEN fail.
	if id == "" || id == project.NoProjectID {
		return errors.New("cannot delete the No Project pseudo-project")
	}

	if err := config.CheckProjectWorktreesReleased(f.agentDir, id); err != nil {
		return err
	}

	// Per-root vector managers (ADR-081) are released FIRST — shutdown drops
	// their open chromem handles so the directory removal below works on
	// Windows too: drop each managed worktree storage explicitly, then the
	// project root removes the rest. Releasing before removal is the
	// invariant: projectManager.DeleteProject would otherwise fail on the
	// still-open Windows handles and orphan the index.
	viRoot := config.ProjectVectorIndexPath(f.agentDir, id)
	if released := f.vectorRootsRegistry().ReleaseProject(id); len(released) > 0 {
		for _, e := range worktreeStorageNames(viRoot) {
			_ = released[0].DeleteProjectData(e) // Best-effort.
		}
		_ = released[0].DeleteProjectData(viRoot) // Best-effort; error is non-critical.
	}

	// Stop and clean up all in-memory sessions for the project BEFORE removing
	// the project directory: cancels active tasks, closes log/dump handles, and
	// removes each session's internal files so no open handles remain when the
	// project tree is deleted. Store-only sessions have no open handles; their
	// files go with the directory below.
	if f.app != nil {
		if manager := f.app.Manager(); manager != nil {
			for _, s := range manager.ListSessions() {
				if s.ProjectID != id {
					continue
				}
				if err := manager.DeleteSession(s.ID); err != nil {
					f.log().Warn("failed to clean up session while deleting project",
						"session_id", s.ID, "project_id", id, "error", err)
				}
			}
		}
	}

	if err := f.projectManager.DeleteProject(id); err != nil {
		return err
	}

	// Clear active project state only after successful deletion.
	f.activeProjectMu.Lock()
	wasActive := f.activeProjectID == id
	if wasActive {
		f.activeProjectID = ""
		f.activeProjectPath = ""
	}
	f.activeProjectMu.Unlock()

	// A stale Git-panel focus would resolve every PATHLESS git RPC into the
	// removed (or, for an external workspace, still-on-disk) tree between
	// this deletion and the frontend's asynchronous focus re-apply —
	// wrong-repo commit/push/reset. invalidateAllGitCaches is the same funnel
	// the project switch uses: cache drop + focus clear in one step.
	f.invalidateAllGitCaches()

	// Stop watcher if this was the active project
	f.watcherMu.Lock()
	if wasActive && f.watcher != nil {
		_ = f.watcher.Close() // Best-effort cleanup; error is non-critical.
		f.watcher = nil
	}
	f.watcherMu.Unlock()

	f.emitEvent(EventProjectDeleted, id)
	// The project's sessions cascaded away with it.
	f.emitSessionsChanged()
	return nil
}

// RenameProject renames a project.
func (f *FrontendAPI) RenameProject(id, name string) error {
	f.seedAcquire()
	if f.projectManager == nil {
		return errors.New("project subsystem not initialized")
	}
	if err := f.projectManager.RenameProject(id, name); err != nil {
		return fmt.Errorf("failed to rename project: %w", err)
	}
	f.emitEvent(EventProjectRenamed, map[string]string{"id": id, "name": name})
	return nil
}

// ListProjects returns all projects sorted by last activity.
func (f *FrontendAPI) ListProjects() ([]project.ProjectInfo, error) {
	f.seedAcquire()
	if f.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}
	return f.projectManager.ListProjects()
}

// ActiveProjectDir returns the workspace directory of the currently active
// project, intended as the default location for user-facing native file
// dialogs. It returns "" when no project is active or when No Project is
// active — its workspace is c0wrk-internal data storage, not a directory the
// user considers "the project directory".
func (f *FrontendAPI) ActiveProjectDir() string {
	f.activeProjectMu.RLock()
	defer f.activeProjectMu.RUnlock()
	if f.activeProjectID == "" || f.activeProjectID == project.NoProjectID {
		return ""
	}
	return f.activeProjectPath
}

// SaveProjectUIState persists project-scoped UI switch state.
func (f *FrontendAPI) SaveProjectUIState(req ProjectUIStateRequest) error {
	f.seedAcquire()
	if req.ProjectID == "" {
		return errors.New("project_id is required")
	}
	if f.projectManager == nil || f.projStore == nil {
		// Best-effort no-op when persistence dependencies are not wired.
		return nil
	}

	state := normalizeProjectUIState(project.ProjectUIState{
		ProjectID:      req.ProjectID,
		SavedSessionID: req.SavedSessionID,
		OpenTabs:       req.OpenTabs,
		ActiveFile:     req.ActiveFile,
	})
	resolved, err := f.resolveSavedSessionForProject(context.Background(), state.ProjectID, state.SavedSessionID)
	if err != nil {
		f.log().Warn("failed to resolve saved session for project UI state; keeping original value", "project", state.ProjectID, "error", err)
	} else {
		state.SavedSessionID = resolved
	}

	ctx, cancel := f.storeOpCtx()
	defer cancel()
	if err := f.projStore.SaveUIState(ctx, state); err != nil {
		return fmt.Errorf("failed to save project UI state: %w", err)
	}
	return nil
}

// SaveProjectActiveSession persists ONLY the selected session for a project,
// leaving any previously saved open tabs / active file untouched — a full
// SaveProjectUIState from the session-switch path would race with (and
// clobber) the tab state owned by the file viewer. When no UI-state row
// exists yet, one is created with empty open tabs.
func (f *FrontendAPI) SaveProjectActiveSession(projectID, sessionID string) error {
	f.seedAcquire()
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return errors.New("project_id is required")
	}
	if f.projectManager == nil || f.projStore == nil {
		return errors.New("project subsystem not initialized")
	}

	// Validate the project exists: project_ui_state.project_id has a foreign
	// key to projects(id), so inserting for an unknown project would fail
	// with an opaque constraint error.
	p, err := f.projectManager.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("failed to load project: %w", err)
	}
	if p == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		resolved, err := f.resolveSavedSessionForProject(context.Background(), projectID, sessionID)
		if err != nil {
			return fmt.Errorf("failed to resolve session selection: %w", err)
		}
		sessionID = resolved
	}

	ctx, cancel := f.storeOpCtx()
	defer cancel()
	if err := f.projStore.SaveSavedSessionID(ctx, projectID, sessionID); err != nil {
		return fmt.Errorf("failed to save project active session: %w", err)
	}
	return nil
}

// GetProjectUIState loads persisted project-scoped UI switch state.
func (f *FrontendAPI) GetProjectUIState(projectID string) (*ProjectUIStateResponse, error) {
	f.seedAcquire()
	if projectID == "" {
		return nil, errors.New("project_id is required")
	}
	if f.projectManager == nil || f.projStore == nil {
		return nil, nil
	}

	ctx, cancel := f.storeOpCtx()
	defer cancel()
	state, err := f.projStore.LoadUIState(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to load project UI state: %w", err)
	}
	if state == nil {
		return nil, nil
	}

	normalized := normalizeProjectUIState(*state)
	resolved, err := f.resolveSavedSessionForProject(context.Background(), projectID, normalized.SavedSessionID)
	if err != nil {
		f.log().Warn("failed to resolve saved session for persisted project UI state", "project", projectID, "error", err)
	} else {
		normalized.SavedSessionID = resolved
	}
	if !projectUIStateEqual(normalized, *state) {
		if saveErr := f.projStore.SaveUIState(ctx, normalized); saveErr != nil {
			f.log().Warn("failed to normalize persisted project UI state", "project", projectID, "error", saveErr)
		}
	}

	return &ProjectUIStateResponse{
		ProjectID:      normalized.ProjectID,
		SavedSessionID: normalized.SavedSessionID,
		OpenTabs:       normalized.OpenTabs,
		ActiveFile:     normalized.ActiveFile,
		UpdatedAt:      normalized.UpdatedAt,
	}, nil
}

// GetLastActiveProjectID returns the ID of the project active at the last
// SwitchProject call (persisted in app_state, including No Project); the
// frontend restores it after an app restart. Empty when nothing was
// persisted or persistence is unavailable.
func (f *FrontendAPI) GetLastActiveProjectID() string {
	f.seedAcquire()
	if f.projStore == nil {
		return ""
	}
	ctx, cancel := f.storeOpCtx()
	defer cancel()
	id, err := f.projStore.LoadAppState(ctx, project.AppStateKeyLastActiveProjectID)
	if err != nil {
		f.log().Warn("failed to load last active project id", "error", err)
		return ""
	}
	return id
}

// SwitchProject activates a project, setting it as the current workspace.
// Design invariant: exactly ONE active project at any time — the file
// watcher, vector indexer, terminal manager and session workspace resolution
// all assume single-project context; switching tears down the previous
// project's resources before activating the new one.
func (f *FrontendAPI) SwitchProject(id string) error {
	f.seedAcquire()
	if f.projectManager == nil {
		return errors.New("project subsystem not initialized")
	}

	// Serialize the whole switch (see switchMu): rapid toggles arrive on
	// separate goroutines and must not interleave teardown/activate —
	// otherwise the backend ends on the OLDER switch while the frontend
	// already moved on, a desync under which every ListDirectory fails
	// containment until restart.
	if err := f.acquireSwitchLock(); err != nil {
		// Surface the failure: the frontend's toggle swallows a rejected
		// switch and expects the global runtime_error listener to explain it
		// (error_code project_switch_busy, see
		// specs/contracts/event-catalog.md); nil-guarded like the other
		// early-startup emitters.
		if f.emitEvent != nil {
			f.emitEvent(EventRuntimeError, map[string]string{
				"id":         uuid.New().String(),
				"message":    "Another project switch is still finishing. Please try again in a moment.",
				"error_code": "project_switch_busy",
			})
		}
		f.log().Warn("SwitchProject: timed out waiting for an in-flight switch",
			"project", id, "error", err)
		// The holder has already outlived the whole acquisition budget — the
		// wedged-switch state this error exists to surface, reported in the
		// wild with no way to name the stuck step. Dump the goroutines while
		// the holder is parked; best-effort diagnostics, never alters the
		// switch outcome.
		f.dumpBusySwitchHolders(id)
		return err
	}
	switchCtx, cancelSwitchSection := context.WithTimeout(context.Background(), switchSectionTimeout)
	defer cancelSwitchSection()
	acquiredAt := time.Now()
	defer func() {
		f.switchMu.Unlock()
		if held := time.Since(acquiredAt); held >= switchHeldWarnThreshold {
			f.log().Warn("SwitchProject: held switchMu past the acquisition budget",
				"project", id, "held", held.String(),
				"budget", switchSectionTimeout.String())
		}
	}()
	if f.switchInProgressHook != nil {
		f.switchInProgressHook(id)
	}

	// Idempotency: skip the full switch if the same project is already
	// active, but STILL re-emit project:switched so a frontend whose local
	// activeProjectId went stale reconciles via its useProjectLoader
	// subscription — without the event it stays desynced (ListDirectory then
	// rejects its rootPath; @-file completions stay empty until restart).
	f.activeProjectMu.RLock()
	alreadyActive := f.activeProjectID == id
	f.activeProjectMu.RUnlock()
	if alreadyActive {
		f.log().Info("SwitchProject: project already active, skipping", "project", id)
		// Re-emit so a desynced frontend reconciles (see above). A lookup
		// failure here is rare (e.g. a race with project deletion) but must
		// be visible — warn instead of failing silently.
		p, err := func() (*project.ProjectInfo, error) {
			opCtx, cancel := f.switchStoreOpCtx(switchCtx)
			defer cancel()
			return f.projectManager.GetProjectCtx(opCtx, id)
		}()
		switch {
		case err != nil:
			f.log().Warn("SwitchProject: failed to re-emit project:switched on already-active path", "project", id, "error", err)
		case p == nil:
			f.log().Warn("SwitchProject: project not found while already active; skipped project:switched re-emit", "project", id)
		default:
			f.emitEvent(EventProjectSwitched, p)
			// Re-run the intake scan on the already-active path too: a
			// .git/config planted since the last scan must warn on the next
			// re-selection. Cheap, detection-only (see
			// frontend_api_gitconfig_risk.go).
			f.notifyGitConfigRisk(GitConfigRiskSourceProject, p.WorkspacePath)
		}
		return nil
	}

	p, err := func() (*project.ProjectInfo, error) {
		opCtx, cancel := f.switchStoreOpCtx(switchCtx)
		defer cancel()
		return f.projectManager.GetProjectCtx(opCtx, id)
	}()
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("project not found: %s", id)
	}

	// CODE mode requires git; No Project (CHAT mode) does not. Return an
	// ERROR (alongside the runtime_error toast event): the frontend treats a
	// rejected RPC as "switch did not happen" — returning nil here let it
	// flip to a project the backend never activated, a desync under which
	// ListDirectory persistently rejects its rootPath.
	if !p.IsNoProject && !gitOnPath() {
		f.emitEvent(EventRuntimeError, map[string]string{
			"id":         uuid.New().String(),
			"message":    "Git is required for CODE mode. Please install git and try again.",
			"error_code": "git_not_found",
		})
		f.log().Warn("SwitchProject blocked: git not found on PATH", "project", id)
		return errors.New("git is required for CODE mode")
	}

	// Teardown persists the PREVIOUS project's state and stays first; every
	// still-fallible step (vector setup) runs BEFORE the watcher swap and the
	// activation commit, so a failure leaves activeProjectID/activeProjectPath
	// AND the live watcher on the previous project — the backend never
	// half-switches, and the frontend (non-nil error = "switch did not
	// happen") cannot diverge.
	f.switchProjectTeardown(switchCtx, id)
	// Vector setup before the watcher swap: the steps are independent, and a
	// vector failure leaves the previous watcher untouched instead of closing
	// it and starting one for a project that never activated.
	if err := f.switchSetupVector(switchCtx, p); err != nil {
		return err
	}
	// The active session's workspace resolves ONCE, here, feeding BOTH the
	// watcher root and the tracked research/papers/comparisons roots:
	// resolving per step re-ran bounded store reads and opened a desync
	// window (watcher rooted at the session tree while the emit roots fell
	// back to the checkout, or the reverse — research/papers events unrouted
	// until the next WatchDirectory self-healed).
	sessionWS, hasSessionWS := f.switchProjectSetupWatcher(switchCtx, p)

	// All fallible steps succeeded: commit the activation. From here on the
	// switch cannot fail, so the active marker and the project:switched event
	// stay consistent with what the frontend is told.
	f.switchProjectActivate(switchCtx, p, sessionWS, hasSessionWS)
	f.applySavedProjectSwitchState(switchCtx, p.ID)

	// c0wrk-owned packs are seeded into the GLOBAL agent directories once per
	// launch (seedGlobalPacks) — a project switch seeds nothing.

	f.emitEvent(EventProjectSwitched, p)

	// Intake scan (text-only, exec-free): warn when the freshly opened
	// workspace's .git/config carries dangerous keys; silent for a clean
	// config. See frontend_api_gitconfig_risk.go.
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, p.WorkspacePath)

	// Kick the auto-fetch funnel on the REAL-switch path only (the
	// already-active early return skips it). Fire-and-forget — every gate plus
	// the quiet-failure contract live inside autoFetchOnce; app-startup
	// restoration rides along (the frontend replays the last project through
	// this same call). See specs/domains/git-auto-fetch.md.
	go f.autoFetchOnce(autoFetchTriggerSwitch)

	// Same real-switch seam, same fire-and-forget silence: sweep the vector +
	// lexical indexes of every git branch that no longer exists locally.
	// Debug logs only; never affects the switch outcome. See
	// pruneAbsentBranchVectorIndexes and specs/domains/workspace.md.
	go f.pruneAbsentBranchVectorIndexes(p)

	return nil
}

// gitOnPath reports whether git is available on PATH.
func gitOnPath() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// errSwitchLockTimeout reports a switch that could not acquire switchMu
// within switchLockTimeout — the bounded-wait sentinel.
var errSwitchLockTimeout = errors.New("project switch timed out waiting for in-flight switch")

// acquireSwitchLock acquires switchMu, waiting at most switchLockTimeout (or
// the override seam). It polls TryLock rather than blocking on Lock: a switch
// wedged behind a stuck predecessor must surface a bounded, actionable error,
// not hang the RPC goroutine (and the frontend's project-load waterfall).
func (f *FrontendAPI) acquireSwitchLock() error {
	timeout := switchLockTimeout
	if f.switchLockTimeoutOverride > 0 {
		timeout = f.switchLockTimeoutOverride
	}
	const tick = 250 * time.Millisecond
	deadline := time.Now().Add(timeout)
	for {
		if f.switchMu.TryLock() {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errSwitchLockTimeout
		}
		if remaining > tick {
			remaining = tick
		}
		time.Sleep(remaining)
	}
}

// dumpBusySwitchHolders writes a bounded all-goroutine stack dump after a
// switch failed to acquire switchMu: every section step is individually
// bounded, so a hold outliving the whole budget means deadline stacking or an
// unreachable blocking call — only the holder's stack distinguishes them.
func (f *FrontendAPI) dumpBusySwitchHolders(projectID string) {
	buf := make([]byte, maxBusySwitchDumpBytes)
	n := runtime.Stack(buf, true)
	f.log().Warn("SwitchProject: switchMu still held; goroutine dump for offline diagnosis",
		"project", projectID,
		"dump_truncated", n == len(buf),
		"stacks", string(buf[:n]))
}

// maxBusySwitchDumpBytes caps the diagnostic goroutine dump against the
// pathological thousands-of-parked-goroutines case flooding the log.
const maxBusySwitchDumpBytes = 512 * 1024

// switchSetupVector runs the vector-index setup for a project switch,
// honoring the test-only switchProjectSetupVectorFn seam (nil in production)
// that drives the fallible pre-watcher step to verify the switch stays
// atomic on failure. ctx is the switch-section deadline.
func (f *FrontendAPI) switchSetupVector(ctx context.Context, p *project.ProjectInfo) error {
	if f.switchProjectSetupVectorFn != nil {
		return f.switchProjectSetupVectorFn(ctx, p)
	}
	return f.switchProjectSetupVector(ctx, p)
}

// switchProjectTeardown persists the previous project state and cancels in-flight work.
func (f *FrontendAPI) switchProjectTeardown(ctx context.Context, newID string) {
	f.activeProjectMu.RLock()
	previousProjectID := f.activeProjectID
	f.activeProjectMu.RUnlock()
	if previousProjectID != "" && previousProjectID != newID {
		f.persistCurrentProjectSwitchState(ctx, previousProjectID)
	}

	// Per-root managers (ADR-081): no indexing is cancelled here. Every live
	// manager serves its own tree — a background session on the outgoing
	// project keeps searching its own index — and the focus move below does
	// not kill the outgoing tree's in-flight build. Manager teardown happens
	// only on release/deletion. See specs/decisions/081-vector-per-root-registry.md.
	_ = previousProjectID
}

// trackedSessionRoots derives the research/papers/comparisons roots a project
// switch tracks (ADR-099), from the ALREADY-RESOLVED active-session workspace
// (the same snapshot that rooted the watcher): <session-workspace>/.research
// when one resolved, <checkout>/.research otherwise. The papers/comparisons
// derivation stays GUARDED: an empty effective research root must leave both
// roots EMPTY — the watcher emit gates treat "" as "not tracked", while
// filepath.Join("", "papers") would silently produce a relative path and
// break that contract.
func trackedSessionRoots(p *project.ProjectInfo, sessionWS string, hasSessionWS bool) (researchRoot, papersRoot, comparisonsRoot string) {
	if p == nil || p.IsNoProject || p.WorkspacePath == "" {
		return "", "", ""
	}
	if hasSessionWS && sessionWS != "" {
		researchRoot = config.ProjectResearchPath(sessionWS)
	} else {
		researchRoot = config.ProjectResearchPath(p.WorkspacePath)
	}
	if researchRoot == "" {
		return "", "", ""
	}
	return researchRoot, config.PaperLibraryPathIn(researchRoot), config.ComparisonsPathIn(researchRoot)
}

// switchProjectActivate sets the new project as active and updates related
// state. sessionWS/hasSessionWS carry the active-session workspace resolution
// switchProjectSetupWatcher already made, so the session-following roots
// derive from the SAME snapshot the watcher root was chosen from (ADR-099) —
// resolving independently here would re-run the bounded store reads and open
// a desync window between the watcher root and the tracked emit roots.
func (f *FrontendAPI) switchProjectActivate(switchCtx context.Context, p *project.ProjectInfo, sessionWS string, hasSessionWS bool) {
	f.seedAcquire()
	// Derive the session-following roots BEFORE the lock: the tracked-root
	// fields are read by the watcher callbacks under activeProjectMu, so no
	// work beyond cheap derivations may run while it is held. The roots
	// follow the active session (see trackedSessionRoots).
	researchRoot, papersRoot, comparisonsRoot := trackedSessionRoots(p, sessionWS, hasSessionWS)

	f.activeProjectMu.Lock()
	f.activeProjectID = p.ID
	f.activeProjectPath = p.WorkspacePath
	// The three roots follow the active session (ADR-099 — see the field
	// docs in frontend_api.go); always assigned (including "") so switching
	// to No Project clears a stale root and stops cross-project events.
	f.activeResearchRoot = researchRoot
	f.activePapersRoot = papersRoot
	f.activeComparisonsRoot = comparisonsRoot
	f.activeProjectMu.Unlock()

	// Invalidate cached skill list since project-local skills may differ.
	f.invalidateSkillCache()
	// Invalidate cached agent list since project-local agents may differ.
	f.invalidateAgentCache()
	// Invalidate both per-repo git caches (status / ignored paths) so no
	// snapshot from the previous workspace can leak into the new one. Runs for
	// every real switch (CODE and No Project). See frontend_api_gitcache.go.
	f.invalidateAllGitCaches()

	// Set MCP working directory to the new project workspace
	if b := f.builder(); b != nil {
		b.SetMCPWorkDir(p.WorkspacePath)
	}

	// Update project activity timestamp.
	if f.projStore != nil {
		ctx, cancel := f.switchStoreOpCtx(switchCtx)
		_ = f.projStore.UpdateProjectActivity(ctx, p.ID) // Best-effort; error is non-critical.
		// Persist the destination project (including the No Project
		// pseudo-project) so the last active project survives an app restart.
		// Best-effort: a failed write must not abort the switch.
		if err := f.projStore.SaveAppState(ctx, project.AppStateKeyLastActiveProjectID, p.ID); err != nil {
			f.log().Warn("failed to persist last active project id", "project", p.ID, "error", err)
		}
		cancel()
	}
}

// switchProjectSetupWatcher creates a file watcher for the new project; the
// root FOLLOWS the ACTIVE SESSION in both modes (No Project: the isolated
// per-session workspace; CODE: the bound managed worktree, else the checkout)
// — see specs/domains/workspace.md (Filesystem Watcher). fsnotify is NOT
// recursive, so the root choice decides what is watched at all. Creation
// happens here for the active session and re-scopes on every session switch
// via WatchDirectory (lazily deferred when no session exists yet). The CODE
// branch RETURNS the session-workspace snapshot it resolved, and the caller
// threads it into switchProjectActivate so the tracked research/papers/
// comparisons roots derive from ONE resolution (ADR-099); No Project returns
// ("", false) — those roots are unavailable by protocol.
func (f *FrontendAPI) switchProjectSetupWatcher(switchCtx context.Context, p *project.ProjectInfo) (sessionWS string, hasSessionWS bool) {
	f.seedAcquire()
	// For No Project, resolve the session workspace before acquiring the lock
	// to avoid holding watcherMu during session-manager queries.
	if p.IsNoProject {
		noProjectWS := f.resolveNoProjectSessionWorkspace(switchCtx)
		f.watcherMu.Lock()
		defer f.watcherMu.Unlock()
		if noProjectWS == "" {
			// No session yet: tear down any previous watcher and defer
			// creation until the first session is activated (WatchDirectory
			// re-scopes then).
			if f.watcher != nil {
				_ = f.watcher.Close()
				f.watcher = nil
			}
			return "", false
		}
		if err := f.reScopeNoProjectWatcherLocked(noProjectWS); err != nil {
			f.log().Warn("failed to start workspace file watcher", "project", p.ID, "error", err)
		}
		return "", false
	}

	// CODE mode: the watcher root follows the ACTIVE SESSION — the bound
	// managed worktree, else the checkout. Resolved BEFORE watcherMu (the
	// resolver queries the session manager/store, which must not hold it)
	// and before switchProjectActivate commits the new project, so the
	// tracked emit roots still hold the PREVIOUS project's values here; the
	// callback snapshots them per batch under activeProjectMu.
	watchRoot := p.WorkspacePath
	sessionWS, hasSessionWS = f.activeSessionWorkspace(p.ID)
	if hasSessionWS && sessionWS != "" {
		watchRoot = sessionWS
	}
	f.watcherMu.Lock()
	defer f.watcherMu.Unlock()
	if err := f.reScopeCodeWatcherLocked(watchRoot, config.ProjectResearchPath(p.WorkspacePath)); err != nil {
		f.log().Warn("failed to start workspace file watcher", "project", p.ID, "error", err)
	}
	return sessionWS, hasSessionWS
}

// reScopeCodeWatcherLocked tears down the current watcher and re-roots it at
// watchRoot — the CODE-mode counterpart of the No-Project re-scope. The root
// follows the active session (specs/domains/workspace.md), and the callback
// routes the per-root consumers (git status/ignore caches, vector
// NotifyFileChange) at watchRoot — the tree the events actually come from —
// never at the checkout. checkoutResearchRoot is the checkout's canonical
// <workspace>/.research, watched recursively wherever the root moved (see
// watchResearchTreesLocked). Caller holds f.watcherMu and must have resolved
// watchRoot WITHOUT holding it (the resolver queries the session manager/store).
func (f *FrontendAPI) reScopeCodeWatcherLocked(watchRoot, checkoutResearchRoot string) error {
	f.seedAcquire()
	if watchRoot == "" {
		return errors.New("cannot re-scope watcher to empty path")
	}
	if f.watcher != nil {
		_ = f.watcher.Close()
		f.watcher = nil
	}
	watcher, err := workspace.NewWatcher(watchRoot, func(changedPaths []string) {
		// Snapshot the active project fields under the lock: this callback
		// runs on the fsnotify goroutine against main-thread switches —
		// unlocked reads violated Go's memory model.
		f.activeProjectMu.RLock()
		snapProjectID := f.activeProjectID
		snapResearchRoot := f.activeResearchRoot
		snapPapersRoot := f.activePapersRoot
		snapComparisonsRoot := f.activeComparisonsRoot
		f.activeProjectMu.RUnlock()

		// research:file_changed for paths inside the tracked research root;
		// the flag annotates workspace:tree_changed so the frontend can skip
		// the full refetch when the incremental path handles the update.
		researchScoped := f.emitResearchFileChanged(snapResearchRoot, snapProjectID, changedPaths)
		// papers:changed for the paper library / comparisons roots — tracked
		// independently of the active R-NNN (ADR-099).
		f.emitPapersChanged(snapPapersRoot, snapComparisonsRoot, snapProjectID, changedPaths)
		f.emitEvent(EventWorkspaceTreeChanged, map[string]bool{
			"research_scoped": researchScoped,
		})

		// An ignore-rule file change anywhere under a watched root makes the
		// cached ignore resolver stale; evict the affected roots so the next
		// task rebuilds them (async, lazy) — else ignore edits are invisible
		// until restart.
		if f.app != nil && f.app.Manager() != nil {
			f.app.Manager().InvalidateIgnoreCache(changedPaths)
		}

		// Git status/ignore caches are keyed by repository root: route the
		// invalidation at the WATCHED root, so a managed tree's churn
		// refreshes that tree's Git panel state. See frontend_api_gitcache.go.
		f.invalidateGitCachesOnWatcher(watchRoot, changedPaths)

		// Project-local skills/agents/commands live under the watched
		// workspace's .agents/ — invalidate the caches so the next
		// ListSkills / ListAgents / ListCommands rescans.
		f.invalidateSkillCache()
		f.invalidateAgentCache()
		f.invalidateCommandCache()

		// Only trigger vector re-indexing when at least one changed path is
		// indexable — churn inside ignored locations must not wake the
		// indexer into a wasted ValidateCollection pass (including a wasted
		// ONNX inference). The notification targets the WATCHED root's
		// manager only (per-root registry, ADR-081): a managed tree's churn
		// never wakes the checkout's manager, and vice versa.
		if vm := f.vectorRootsRegistry().LiveManager(watchRoot); vm != nil && vm.IsAnyIndexablePath(changedPaths) {
			vm.NotifyFileChange()
		}
	})
	if err != nil {
		return err
	}
	f.watcher = watcher
	f.watchResearchTreesLocked(watcher, watchRoot, checkoutResearchRoot)
	return nil
}

// watchResearchTreesLocked registers the recursive research-tree watches on a
// freshly built CODE watcher: the checkout's canonical <workspace>/.research
// and the watched root's own .research when they differ. Either tree may sit
// outside the watcher root, so each is admitted via AllowRoot (inert for
// in-root trees). Registration creates NOTHING on disk (Issue 52: a switch
// must not materialize .research in the user's repository) — a missing tree
// is only marked recursive; its first write auto-adds it.
func (f *FrontendAPI) watchResearchTreesLocked(w *workspace.Watcher, watchRoot, checkoutResearchRoot string) {
	roots := make([]string, 0, 2)
	if checkoutResearchRoot != "" {
		roots = append(roots, checkoutResearchRoot)
	}
	if sessionRoot := config.ProjectResearchPath(watchRoot); sessionRoot != "" && sessionRoot != checkoutResearchRoot {
		roots = append(roots, sessionRoot)
	}
	for _, root := range roots {
		if err := w.AllowRoot(root); err != nil {
			f.log().Debug("failed to admit research root", "root", root, "error", err)
		}
		if err := w.WatchTree(root); err != nil {
			f.log().Debug("failed to watch research tree", "root", root, "error", err)
		}
	}
}

// emitResearchFileChanged emits research:file_changed for changed paths
// inside the tracked research root, returning true when at least one path
// was research-scoped so the caller can annotate workspace:tree_changed and
// the frontend can skip a redundant full refetch (the incremental
// useResearchFileWatcher path handles it). Caller passes researchRoot and
// projectID already snapshotted under activeProjectMu (fsnotify callback vs
// main-thread switches).
func (f *FrontendAPI) emitResearchFileChanged(researchRoot, projectID string, changedPaths []string) bool {
	f.seedAcquire()
	if researchRoot == "" {
		return false
	}
	var researchPaths []string
	for _, p := range changedPaths {
		if config.IsResearchPath(researchRoot, p) {
			researchPaths = append(researchPaths, p)
		}
	}
	if len(researchPaths) == 0 {
		return false
	}
	f.log().Debug("research: file changed in research dir, emitting event",
		"project_id", projectID,
		"research_root", researchRoot,
		"paths", researchPaths,
	)
	f.emitEvent(EventResearchFileChanged, map[string]string{
		"project_id": projectID,
		"paths":      strings.Join(researchPaths, ","),
	})
	return true
}

// reScopeNoProjectWatcher tears down the current watcher (if any) and scopes
// a new one to root, which must be a No Project session workspace (each chat
// session has an isolated workspace). The directory is created if missing so
// the watcher can be set up before the workspace exists on disk.
func (f *FrontendAPI) reScopeNoProjectWatcher(root string) error {
	f.watcherMu.Lock()
	defer f.watcherMu.Unlock()
	return f.reScopeNoProjectWatcherLocked(root)
}

// reScopeNoProjectWatcherLocked is the lock-held implementation of
// reScopeNoProjectWatcher. The caller must hold f.watcherMu.
func (f *FrontendAPI) reScopeNoProjectWatcherLocked(root string) error {
	f.seedAcquire()
	if root == "" {
		return errors.New("cannot re-scope watcher to empty path")
	}
	if f.watcher != nil {
		_ = f.watcher.Close()
		f.watcher = nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("failed to create session workspace for watcher: %w", err)
	}
	watcher, err := workspace.NewWatcher(root, func(changedPaths []string) {
		// Snapshot the active project fields under the lock (see the
		// CODE-mode callback).
		f.activeProjectMu.RLock()
		snapProjectID := f.activeProjectID
		snapResearchRoot := f.activeResearchRoot
		snapPapersRoot := f.activePapersRoot
		snapComparisonsRoot := f.activeComparisonsRoot
		f.activeProjectMu.RUnlock()

		researchScoped := f.emitResearchFileChanged(snapResearchRoot, snapProjectID, changedPaths)
		// No Project has no paper library or comparisons dir (both roots
		// empty) — a no-op, kept for parity with the CODE-mode callback.
		f.emitPapersChanged(snapPapersRoot, snapComparisonsRoot, snapProjectID, changedPaths)
		f.emitEvent(EventWorkspaceTreeChanged, map[string]bool{
			"research_scoped": researchScoped,
		})

		// Invalidate the skill/agent/command caches — project-local entries
		// live under the session workspace's .agents/.
		f.invalidateSkillCache()
		f.invalidateAgentCache()
		f.invalidateCommandCache()
		// Same ignore-cache invalidation as CODE mode.
		if f.app != nil && f.app.Manager() != nil {
			f.app.Manager().InvalidateIgnoreCache(changedPaths)
		}
	})
	if err != nil {
		return fmt.Errorf("failed to start workspace file watcher: %w", err)
	}
	f.watcher = watcher
	return nil
}

// switchProjectSetupVector aligns the vector index with the switched-to
// project (ADR-081): the Git-panel FOCUS moves to the project's effective
// target — the saved session's managed tree, else the checkout — creating
// that root's manager on demand; no live manager is torn down (background
// sessions keep searching their own trees). No Project (CHAT mode) disables
// vector indexing entirely: focus cleared, unavailable status emitted. See
// specs/decisions/081-vector-per-root-registry.md.
func (f *FrontendAPI) switchProjectSetupVector(switchCtx context.Context, p *project.ProjectInfo) error {
	f.seedAcquire()
	// Handshake with the background ONNX init: the factory is usually NOT
	// ready when the frontend fires its first SwitchProject on backend:ready;
	// record the project so InitVectorIndexForActiveProject drains it once
	// the factory lands, instead of silently dropping the setup. Read-and-
	// record is atomic under vectorSetupMu (lock order: vectorSetupMu → the
	// registry's internal lock).
	vr := f.vectorRootsRegistry()
	f.vectorSetupMu.Lock()
	if !vr.FactoryReady() {
		if p.IsNoProject {
			// CHAT mode never indexes: drop any pending setup.
			f.deferredVectorProject = nil
		} else {
			f.deferredVectorProject = p
		}
		f.vectorSetupMu.Unlock()
		return nil
	}
	// Factory is ready: this switch supersedes any deferred setup.
	f.deferredVectorProject = nil
	f.vectorSetupMu.Unlock()

	// No Project (CHAT mode): clear the focus and reflect the dormant
	// subsystem in the status stream.
	if p.IsNoProject {
		vr.LeaveFocus()
		st := VectorIndexStatus{State: "unavailable", Indices: []string{}}
		f.applyEmbedderInfo(&st)
		f.emitEvent(EventVectorIndexStatus, st)
		return nil
	}

	// Fail-soft: a focus that cannot be indexed must not fail the project
	// switch — the vector_index:status stream carries the state instead.
	// switchCtx flows into ApplyFocus so the vector leg's one store
	// round-trip rides the section deadline like every other switch store op;
	// the manager build and the eviction-drain wait stay time-bounded, not
	// ctx-bounded (see switchSectionTimeout).
	target := f.resolveVectorIndexTarget(switchCtx, p)
	if err := vr.ApplyFocus(switchCtx, canonicalRoot(target.workspacePath)); err != nil {
		f.log().Warn("vector focus unavailable for project target",
			"project", p.ID, "root", target.workspacePath, "error", err)
	}
	return nil
}

// progressFraction calculates indexing progress as a fraction in [0, 1].
// It deliberately mirrors the Progress value of GetVectorIndexStatus and the
// frontend contract (IndexingStatus.tsx renders progress * 100 percent) — a
// 0–100 percentage would pin the bar at 100% after ~1% of files.
func progressFraction(indexed, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(indexed) / float64(total)
}

// SaveProjectSwitchState persists project-scoped UI switch state.
// Backward-compatible alias for SaveProjectUIState.
func (f *FrontendAPI) SaveProjectSwitchState(req ProjectUIStateRequest) error {
	return f.SaveProjectUIState(req)
}

// GetProjectSwitchState loads persisted project-scoped UI switch state.
// Backward-compatible alias for GetProjectUIState.
func (f *FrontendAPI) GetProjectSwitchState(projectID string) (*ProjectUIStateResponse, error) {
	return f.GetProjectUIState(projectID)
}

func normalizeProjectUIState(state project.ProjectUIState) project.ProjectUIState {
	state.ProjectID = strings.TrimSpace(state.ProjectID)
	state.SavedSessionID = strings.TrimSpace(state.SavedSessionID)
	state.ActiveFile = strings.TrimSpace(state.ActiveFile)

	uniqueTabs := make([]string, 0, len(state.OpenTabs))
	seen := make(map[string]struct{}, len(state.OpenTabs))
	for _, tab := range state.OpenTabs {
		tab = strings.TrimSpace(tab)
		if tab == "" {
			continue
		}
		if _, exists := seen[tab]; exists {
			continue
		}
		seen[tab] = struct{}{}
		uniqueTabs = append(uniqueTabs, tab)
	}
	state.OpenTabs = uniqueTabs

	if state.ActiveFile != "" {
		if _, ok := seen[state.ActiveFile]; !ok {
			state.ActiveFile = ""
		}
	}

	return state
}

func projectUIStateEqual(a, b project.ProjectUIState) bool {
	if a.ProjectID != b.ProjectID ||
		a.SavedSessionID != b.SavedSessionID ||
		a.ActiveFile != b.ActiveFile ||
		a.UpdatedAt != b.UpdatedAt {
		return false
	}
	if len(a.OpenTabs) != len(b.OpenTabs) {
		return false
	}
	for i := range a.OpenTabs {
		if a.OpenTabs[i] != b.OpenTabs[i] {
			return false
		}
	}
	return true
}

// persistCurrentProjectSwitchState re-normalizes the current project's
// already persisted switch state (non-destructive — the backend is not
// authoritative for open tabs/selected session). switchCtx is the
// switch-section deadline.
func (f *FrontendAPI) persistCurrentProjectSwitchState(switchCtx context.Context, projectID string) {
	f.seedAcquire()
	if strings.TrimSpace(projectID) == "" {
		return
	}
	if f.projStore == nil {
		return
	}

	ctx, cancel := f.switchStoreOpCtx(switchCtx)
	defer cancel()
	state, err := f.projStore.LoadUIState(ctx, projectID)
	if err != nil {
		f.log().Warn("failed to load current project switch state", "project", projectID, "error", err)
		return
	}
	if state == nil {
		return
	}

	normalized := normalizeProjectUIState(*state)
	resolved, err := f.resolveSavedSessionForProject(switchCtx, projectID, normalized.SavedSessionID)
	if err != nil {
		f.log().Warn("failed to resolve saved session for current project switch state", "project", projectID, "error", err)
	} else {
		normalized.SavedSessionID = resolved
	}
	if !projectUIStateEqual(normalized, *state) {
		if saveErr := f.projStore.SaveUIState(ctx, normalized); saveErr != nil {
			f.log().Warn("failed to normalize current project switch state", "project", projectID, "error", saveErr)
		}
	}
}

// applySavedProjectSwitchState resolves the destination project's
// deterministic session fallback (saved → latest → create; see
// resolveSessionFallback) and persists the result. switchCtx is the
// switch-section deadline (see switchSectionTimeout).
func (f *FrontendAPI) applySavedProjectSwitchState(switchCtx context.Context, projectID string) {
	f.seedAcquire()
	if strings.TrimSpace(projectID) == "" {
		return
	}
	if f.projStore == nil {
		return
	}

	ctx, cancel := f.switchStoreOpCtx(switchCtx)
	defer cancel()
	state, err := f.projStore.LoadUIState(ctx, projectID)
	if err != nil {
		f.log().Warn("failed to load saved project switch state", "project", projectID, "error", err)
		return
	}

	var normalized project.ProjectUIState
	if state != nil {
		normalized = normalizeProjectUIState(*state)
	} else {
		normalized = project.ProjectUIState{ProjectID: strings.TrimSpace(projectID)}
	}

	resolvedSessionID, changed, err := f.resolveSessionFallback(switchCtx, projectID, normalized.SavedSessionID)
	if err != nil {
		f.log().Warn("failed to resolve project switch state; leaving persisted state untouched", "project", projectID, "error", err)
		return
	}
	normalized.ProjectID = strings.TrimSpace(projectID)
	normalized.SavedSessionID = resolvedSessionID

	shouldPersist := changed || state == nil || !projectUIStateEqual(normalized, *state)
	if shouldPersist {
		if saveErr := f.projStore.SaveUIState(ctx, normalized); saveErr != nil {
			f.log().Warn("failed to persist resolved project switch state", "project", projectID, "error", saveErr)
		}
	}
}

func (f *FrontendAPI) resolveSessionFallback(switchCtx context.Context, projectID, savedSessionID string) (resolvedID string, changed bool, err error) {
	resolvedSaved, err := f.resolveSavedSessionForProject(switchCtx, projectID, savedSessionID)
	if err != nil {
		return "", false, err
	}
	if resolvedSaved != "" {
		return resolvedSaved, strings.TrimSpace(savedSessionID) != resolvedSaved, nil
	}

	latest := f.resolveLatestSessionForProject(switchCtx, projectID)
	if latest != "" {
		return latest, strings.TrimSpace(savedSessionID) != latest, nil
	}

	created := f.createSessionForProject(switchCtx, projectID)
	if created != "" {
		return created, strings.TrimSpace(savedSessionID) != created, nil
	}

	return "", strings.TrimSpace(savedSessionID) != "", nil
}

// resolveLatestSessionForProject returns the session with the most recent
// effective activity — SessionInfo.LastActiveAt (the newest persisted session
// event), falling back to the stored last_active_at, then created_at.
func (f *FrontendAPI) resolveLatestSessionForProject(switchCtx context.Context, projectID string) string {
	if strings.TrimSpace(projectID) == "" {
		return ""
	}
	if f.appCell() == nil || f.app.Manager() == nil {
		return ""
	}

	sessions := f.app.Manager()
	opCtx, cancel := f.switchStoreOpCtx(switchCtx)
	defer cancel()
	latest, err := f.latestSessionID(opCtx, sessions, projectID)
	if err != nil {
		f.log().Warn("failed to list sessions for latest fallback resolution", "project", projectID, "error", err)
		return ""
	}
	return latest
}

func parseSessionActivityTimestamp(info session.SessionInfo) time.Time {
	if ts, err := time.Parse(time.RFC3339, info.LastActiveAt); err == nil {
		return ts
	}
	if ts, err := time.Parse(time.RFC3339, info.CreatedAt); err == nil {
		return ts
	}
	return time.Time{}
}

func (f *FrontendAPI) createSessionForProject(switchCtx context.Context, projectID string) string {
	if strings.TrimSpace(projectID) == "" {
		return ""
	}
	if f.projectManager == nil || f.appCell() == nil || f.app.Manager() == nil {
		return ""
	}
	if err := switchCtx.Err(); err != nil {
		// CreateSession builds a full orchestrator with no internal deadline
		// and could hold switchMu far past the section budget; skip and let
		// the frontend fall back to its own session selection.
		f.log().Warn("switch section deadline spent; skipping fallback session creation",
			"project", projectID, "error", err)
		return ""
	}

	proj, err := func() (*project.ProjectInfo, error) {
		opCtx, cancel := f.switchStoreOpCtx(switchCtx)
		defer cancel()
		return f.projectManager.GetProjectCtx(opCtx, projectID)
	}()
	if err != nil {
		f.log().Warn("failed to load project for session creation fallback", "project", projectID, "error", err)
		return ""
	}
	if proj == nil {
		return ""
	}

	created, err := f.app.Manager().CreateSession(projectID, proj.WorkspacePath)
	if err != nil {
		f.log().Warn("failed to create fallback session for project switch", "project", projectID, "error", err)
		return ""
	}
	if created == nil || strings.TrimSpace(created.ID) == "" {
		return ""
	}

	if f.store != nil {
		ctx, cancel := f.switchStoreOpCtx(switchCtx)
		if err := f.store.SaveSession(ctx, *created); err != nil {
			f.log().Warn("failed to persist fallback session", "project", projectID, "session_id", created.ID, "error", err)
		}
		cancel()
	}

	f.emitSessionsChanged()
	return created.ID
}

// resolveNoProjectSessionWorkspace returns the most recently active No
// Project session's workspace ("" when none) so the file watcher scopes to
// the session directory instead of the shared __no_project__/ base.
func (f *FrontendAPI) resolveNoProjectSessionWorkspace(switchCtx context.Context) string {
	if f.appCell() == nil || f.app.Manager() == nil {
		return ""
	}
	sessions, err := func() ([]session.SessionInfo, error) {
		opCtx, cancel := f.switchStoreOpCtx(switchCtx)
		defer cancel()
		return f.app.Manager().ListSessionsByProjectCtx(opCtx, project.NoProjectID)
	}()
	if err != nil || len(sessions) == 0 {
		return ""
	}
	// Sessions arrive sorted by last_active_at desc, so sessions[0] is the
	// most recent. Resolve via the READ-ONLY WorkspacePathFor, never
	// GetSessionWorkspacePath: the latter lazily restores the session (full
	// orchestrator build) under switchMu — the hang it exists to avoid.
	ctx, cancel := context.WithTimeout(switchCtx, terminalPathLookupTimeout)
	defer cancel()
	ws, ok := f.app.Manager().WorkspacePathFor(ctx, sessions[0].ID)
	if !ok || ws == "" {
		return ""
	}
	return ws
}

// resolveSavedSessionForProject resolves a saved session pointer only when
// the session exists, belongs to the project, and is not archived. A
// resolution ERROR is distinct from a negative answer — callers must not
// treat a transient store failure as "pointer is stale" (normalizing to
// empty would clobber a valid selection). switchCtx bounds the store call.
func (f *FrontendAPI) resolveSavedSessionForProject(switchCtx context.Context, projectID, savedSessionID string) (string, error) {
	savedSessionID = strings.TrimSpace(savedSessionID)
	if savedSessionID == "" {
		return "", nil
	}
	if f.appCell() == nil || f.app.Manager() == nil {
		return "", nil
	}

	sessions, err := func() ([]session.SessionInfo, error) {
		opCtx, cancel := f.switchStoreOpCtx(switchCtx)
		defer cancel()
		return f.app.Manager().ListSessionsByProjectCtx(opCtx, projectID)
	}()
	if err != nil {
		return "", fmt.Errorf("failed to list sessions for saved session resolution: %w", err)
	}
	for _, s := range sessions {
		// Archived sessions are read-only; a saved selection pointing at one
		// must not be restored (fallback resolution picks a live session).
		if s.ID == savedSessionID && !s.Archived {
			return savedSessionID, nil
		}
	}

	// No manager-level fallback: ListSessionsByProject is authoritative for
	// "exists, belongs, not archived". The old GetSession fallback restored
	// (full orchestrator build) exactly the rows the loop rejects, under
	// switchMu on the switch path. Store-only sessions resolve in the loop.

	return "", nil
}
