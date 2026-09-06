package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/c0wrk/core/workspace"
)

// CreateProject creates a new project. If externalPath is empty, an internal workspace is created.
func (f *FrontendAPI) CreateProject(name, externalPath string) (*project.ProjectInfo, error) {
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
	if f.projectManager == nil {
		return errors.New("project subsystem not initialized")
	}

	// Stop and clean up all in-memory sessions for the project BEFORE removing
	// the project directory. This cancels any active tasks and closes log/dump
	// file handles so no open handles remain when the project tree is deleted,
	// and removes each session's internal files. Store-only sessions have no
	// open handles; their files are removed together with the project directory
	// by DeleteProject below.
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

	// Clean up vector index data for the deleted project.
	if vm := f.getVectorManager(); vm != nil {
		_ = vm.DeleteProjectData(config.ProjectVectorIndexPath(f.agentDir, id)) // Best-effort; error is non-critical.
	}

	// Stop watcher if this was the active project
	f.watcherMu.Lock()
	if wasActive && f.watcher != nil {
		_ = f.watcher.Close() // Best-effort cleanup; error is non-critical.
		f.watcher = nil
	}
	f.watcherMu.Unlock()

	f.emitEvent(EventProjectDeleted, id)
	return nil
}

// RenameProject renames a project.
func (f *FrontendAPI) RenameProject(id, name string) error {
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
	if f.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}
	return f.projectManager.ListProjects()
}

// ActiveProjectDir returns the workspace directory of the currently active
// project, intended as the default location for user-facing native file
// dialogs (e.g. Save Message as Markdown). It returns "" when no project is
// active or when the No Project pseudo-project is active — its workspace is
// c0wrk-internal data storage (~/.c0wrk/projects/__no_project__/), not a
// directory the user considers "the project directory".
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
	// A resolution failure must not clobber the pointer: keep the original
	// value instead of normalizing to empty on a transient store error.
	resolved, err := f.resolveSavedSessionForProject(state.ProjectID, state.SavedSessionID)
	if err != nil {
		f.log().Warn("failed to resolve saved session for project UI state; keeping original value", "project", state.ProjectID, "error", err)
	} else {
		state.SavedSessionID = resolved
	}

	if err := f.projStore.SaveUIState(context.Background(), state); err != nil {
		return fmt.Errorf("failed to save project UI state: %w", err)
	}
	return nil
}

// SaveProjectActiveSession persists ONLY the selected session for a project,
// leaving any previously saved open tabs / active file untouched. Used by the
// frontend when the user switches sessions inside a project — a full
// SaveProjectUIState would race with (and clobber) the tab state owned by the
// file viewer. When no UI-state row exists yet, one is created with empty
// open tabs.
func (f *FrontendAPI) SaveProjectActiveSession(projectID, sessionID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return errors.New("project_id is required")
	}
	if f.projectManager == nil || f.projStore == nil {
		return errors.New("project subsystem not initialized")
	}

	// Validate the project exists: project_ui_state.project_id has a foreign
	// key to projects(id), so inserting for an unknown project would fail with
	// an opaque constraint error.
	p, err := f.projectManager.GetProject(projectID)
	if err != nil {
		return fmt.Errorf("failed to load project: %w", err)
	}
	if p == nil {
		return fmt.Errorf("project not found: %s", projectID)
	}

	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		// Never persist a selection that resolve would reject (unknown or
		// archived session); normalize to empty so the next project switch
		// falls back to the latest live session. A resolution ERROR is
		// propagated instead of writing anything: persisting "" after a
		// transient store failure would clobber a valid saved pointer.
		resolved, err := f.resolveSavedSessionForProject(projectID, sessionID)
		if err != nil {
			return fmt.Errorf("failed to resolve session selection: %w", err)
		}
		sessionID = resolved
	}

	if err := f.projStore.SaveSavedSessionID(context.Background(), projectID, sessionID); err != nil {
		return fmt.Errorf("failed to save project active session: %w", err)
	}
	return nil
}

// GetProjectUIState loads persisted project-scoped UI switch state.
func (f *FrontendAPI) GetProjectUIState(projectID string) (*ProjectUIStateResponse, error) {
	if projectID == "" {
		return nil, errors.New("project_id is required")
	}
	if f.projectManager == nil || f.projStore == nil {
		return nil, nil
	}

	state, err := f.projStore.LoadUIState(context.Background(), projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to load project UI state: %w", err)
	}
	if state == nil {
		return nil, nil
	}

	normalized := normalizeProjectUIState(*state)
	resolved, err := f.resolveSavedSessionForProject(projectID, normalized.SavedSessionID)
	if err != nil {
		// Read path: a transient resolution failure must not blank the saved
		// pointer in the response (nor trigger the normalization write-back).
		f.log().Warn("failed to resolve saved session for persisted project UI state", "project", projectID, "error", err)
	} else {
		normalized.SavedSessionID = resolved
	}
	if !projectUIStateEqual(normalized, *state) {
		if saveErr := f.projStore.SaveUIState(context.Background(), normalized); saveErr != nil {
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

// GetLastActiveProjectID returns the ID of the project that was active at the
// last SwitchProject call (persisted in app_state, including the No Project
// pseudo-project ID). The frontend uses it after an app restart to restore the
// previously active project. Returns an empty string when nothing was
// persisted yet or persistence is unavailable.
func (f *FrontendAPI) GetLastActiveProjectID() string {
	if f.projStore == nil {
		return ""
	}
	id, err := f.projStore.LoadAppState(context.Background(), project.AppStateKeyLastActiveProjectID)
	if err != nil {
		f.log().Warn("failed to load last active project id", "error", err)
		return ""
	}
	return id
}

// SwitchProject activates a project, setting it as the current workspace.
//
// Design invariant: the application operates with a single active project at any
// time. The file watcher, vector indexer, terminal manager, and session workspace
// resolution all assume single-project context. Concurrent project access is not
// supported — switching tears down the previous project's resources before
// activating the new one.
func (f *FrontendAPI) SwitchProject(id string) error {
	if f.projectManager == nil {
		return errors.New("project subsystem not initialized")
	}

	// Serialize the entire switch (see switchMu in frontend_api.go): rapid
	// toggles arrive on separate goroutines and must not interleave
	// teardown/activate — otherwise the backend can end on the OLDER switch
	// while the frontend's serialized chain already moved on, a desync under
	// which every ListDirectory (and with it @-file completion) fails
	// containment until an app restart.
	f.switchMu.Lock()
	defer f.switchMu.Unlock()
	if f.switchInProgressHook != nil {
		f.switchInProgressHook(id)
	}

	// Idempotency: skip the full switch if the same project is already
	// active. The project:switched event is still emitted so a frontend
	// whose local activeProjectId went stale (e.g. a concurrent toggle
	// interleave or a switch RPC that failed after backend-side
	// activation) reconciles to the backend's authoritative state via its
	// useProjectLoader subscription. Without the event, a desynced
	// frontend stays desynced: every later toggle that reaches the
	// backend takes this early return, ListDirectory then rejects the
	// frontend's rootPath ("path outside project workspace") and @-file
	// completions stay empty until an app restart.
	f.activeProjectMu.RLock()
	alreadyActive := f.activeProjectID == id
	f.activeProjectMu.RUnlock()
	if alreadyActive {
		f.log().Info("SwitchProject: project already active, skipping", "project", id)
		// Re-emit so a desynced frontend reconciles (see the comment above).
		// A lookup failure here is rare (e.g. a race with project deletion),
		// but without a log it is undiagnosable why the frontend never
		// repaired itself — so warn instead of failing silently.
		p, err := f.projectManager.GetProject(id)
		switch {
		case err != nil:
			f.log().Warn("SwitchProject: failed to re-emit project:switched on already-active path", "project", id, "error", err)
		case p == nil:
			f.log().Warn("SwitchProject: project not found while already active; skipped project:switched re-emit", "project", id)
		default:
			f.emitEvent(EventProjectSwitched, p)
			// Re-run the intake scan on the already-active path too
			// (review [57]): a .git/config planted since the last scan
			// must warn on the next re-selection, not only after a real
			// switch. Cheap (a bounded text parse) and detection-only —
			// the spawn layer re-scans per invocation regardless.
			f.notifyGitConfigRisk(GitConfigRiskSourceProject, p.WorkspacePath)
		}
		return nil
	}

	p, err := f.projectManager.GetProject(id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("project not found: %s", id)
	}

	// CODE mode requires git; No Project (CHAT mode) does not.
	// Return an ERROR (alongside the runtime_error event that drives the
	// UI toast): switchProjectWithState treats a rejected RPC as "switch
	// did not happen" and leaves the frontend's activeProjectId on the
	// previous project. Returning nil here used to let the frontend flip
	// its own store to a project the backend never activated — a
	// frontend/backend desync under which ListDirectory persistently
	// rejects the frontend's rootPath and @-file completions die until
	// an app restart.
	if !p.IsNoProject && !gitOnPath() {
		f.emitEvent(EventRuntimeError, map[string]string{
			"id":         uuid.New().String(),
			"message":    "Git is required for CODE mode. Please install git and try again.",
			"error_code": "git_not_found",
		})
		f.log().Warn("SwitchProject blocked: git not found on PATH", "project", id)
		return errors.New("git is required for CODE mode")
	}

	f.switchProjectTeardown(id)
	f.switchProjectActivate(p)
	f.switchProjectSetupWatcher(p)

	if err := f.switchProjectSetupVector(p); err != nil {
		return err
	}

	f.applySavedProjectSwitchState(p.ID)
	f.emitEvent(EventProjectSwitched, p)

	// Intake scan (text-only, exec-free): warn the user when the freshly
	// opened workspace's .git/config carries dangerous keys. Emits nothing
	// for a clean config. See frontend_api_gitconfig_risk.go.
	f.notifyGitConfigRisk(GitConfigRiskSourceProject, p.WorkspacePath)

	return nil
}

// gitOnPath reports whether git is available on PATH.
func gitOnPath() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// switchProjectTeardown persists the previous project state and cancels in-flight work.
func (f *FrontendAPI) switchProjectTeardown(newID string) {
	f.activeProjectMu.RLock()
	previousProjectID := f.activeProjectID
	f.activeProjectMu.RUnlock()
	if previousProjectID != "" && previousProjectID != newID {
		f.persistCurrentProjectSwitchState(previousProjectID)
	}

	// Cancel any in-flight indexing from a previous project.
	if vm := f.getVectorManager(); vm != nil {
		vm.CancelIndexing()
	}
}

// switchProjectActivate sets the new project as active and updates related state.
func (f *FrontendAPI) switchProjectActivate(p *project.ProjectInfo) {
	f.activeProjectMu.Lock()
	f.activeProjectID = p.ID
	f.activeProjectPath = p.WorkspacePath
	// Track the active research root so the workspace watcher callback can
	// emit research:file_changed events for file modifications inside it.
	// This mirrors what EnableResearch does, ensuring the incremental update
	// path works even when switching to a project that already has research
	// enabled (e.g. after a restart or project switch).
	// Always assign (including "") so switching to a project with research
	// disabled — or to No Project — clears a stale research root from the
	// previously-active project and stops cross-project research:file_changed
	// events.
	f.activeResearchRoot = p.ResearchRoot
	f.activeProjectMu.Unlock()

	// Invalidate cached skill list since project-local skills may differ.
	f.invalidateSkillCache()
	// Invalidate cached agent list since project-local agents may differ.
	f.invalidateAgentCache()

	// Set MCP working directory to the new project workspace
	if b := f.builder(); b != nil {
		b.SetMCPWorkDir(p.WorkspacePath)
	}

	// Update project activity timestamp.
	if f.projStore != nil {
		_ = f.projStore.UpdateProjectActivity(context.Background(), p.ID) // Best-effort; error is non-critical.
		// Persist the destination project (including the No Project
		// pseudo-project) so the last active project survives an app restart.
		// Best-effort: a failed write must not abort the switch.
		if err := f.projStore.SaveAppState(context.Background(), project.AppStateKeyLastActiveProjectID, p.ID); err != nil {
			f.log().Warn("failed to persist last active project id", "project", p.ID, "error", err)
		}
	}
}

// switchProjectSetupWatcher creates a file watcher for the new project workspace.
// For No Project (CHAT mode), each session has an isolated workspace
// (~/.c0wrk/projects/__no_project__/<sid>/workspace/). The watcher is scoped to
// the active session's workspace so only that session's file changes emit
// workspace:tree_changed — other sessions and session-infra directories
// (plans/, temp/, logs) that live outside workspace/ are excluded. fsnotify is
// NOT recursive (it only reports events for explicitly-added directories, even
// on macOS), so scoping to the session workspace — rather than the shared
// project dir — is what prevents cross-session noise.
//
// The watcher root must follow the active session. It is created here for the
// most recently active session (if any) and re-scoped on every session switch
// via WatchDirectory. When no session exists yet (fresh start), creation is
// deferred until the first session is activated — WatchDirectory creates the
// watcher lazily.
func (f *FrontendAPI) switchProjectSetupWatcher(p *project.ProjectInfo) {
	// For No Project, resolve the session workspace before acquiring the lock
	// to avoid holding watcherMu during session-manager queries.
	if p.IsNoProject {
		sessionWS := f.resolveNoProjectSessionWorkspace()
		f.watcherMu.Lock()
		defer f.watcherMu.Unlock()
		if sessionWS == "" {
			// No session yet: tear down any previous watcher and defer
			// creation until the first session is activated (WatchDirectory
			// re-scopes then).
			if f.watcher != nil {
				_ = f.watcher.Close()
				f.watcher = nil
			}
			return
		}
		if err := f.reScopeNoProjectWatcherLocked(sessionWS); err != nil {
			f.log().Warn("failed to start workspace file watcher", "project", p.ID, "error", err)
		}
		return
	}

	// CODE mode: tear down the previous watcher and create a new one scoped
	// to the project workspace.
	// Read the active research root BEFORE acquiring watcherMu so the lock
	// order stays activeProjectMu → watcherMu (switchProjectActivate runs
	// first and has already set it for a project with research enabled).
	f.activeProjectMu.RLock()
	researchRoot := f.activeResearchRoot
	f.activeProjectMu.RUnlock()

	f.watcherMu.Lock()
	defer f.watcherMu.Unlock()
	if f.watcher != nil {
		_ = f.watcher.Close()
		f.watcher = nil
	}
	watcher, err := workspace.NewWatcher(p.WorkspacePath, func(changedPaths []string) {
		// Snapshot the active project fields under the lock to avoid a data
		// race between this callback (fsnotify goroutine) and project
		// switches / research toggles (main thread). Reading these fields
		// without the lock violated Go's memory model.
		f.activeProjectMu.RLock()
		snapProjectID := f.activeProjectID
		snapResearchRoot := f.activeResearchRoot
		f.activeProjectMu.RUnlock()

		// Emit research:file_changed for any changed path inside the research
		// directory. The helper returns whether the change set was
		// research-scoped so we can annotate workspace:tree_changed — the
		// frontend's full-refetch path skips when the incremental path will
		// handle the update, avoiding a redundant double fetch.
		researchScoped := f.emitResearchFileChanged(snapResearchRoot, snapProjectID, changedPaths)
		f.emitEvent(EventWorkspaceTreeChanged, map[string]bool{
			"research_scoped": researchScoped,
		})

		// An ignore-rule file (.gitignore/.aiignore/.ignore) change anywhere
		// under a watched root makes the cached ignore resolver stale. Evict
		// the affected roots so the next task rebuilds them (async, lazy) —
		// without this, ignore edits are invisible until restart.
		if f.app != nil && f.app.Manager() != nil {
			f.app.Manager().InvalidateIgnoreCache(changedPaths)
		}

		// Project-local skills live under <workspace>/.agents/skills, so a
		// workspace change may have added/removed/modified them. Invalidate
		// the skill cache so the next ListSkills call rescans.
		f.invalidateSkillCache()
		// Project-local agents live under <workspace>/.agents/agents — same
		// reasoning. Invalidate the agent cache so ListAgents rescans.
		f.invalidateAgentCache()

		// Only trigger vector re-indexing when at least one changed path is
		// indexable. Churn inside ignored locations (.git maintenance: gc,
		// repack, reflog cleanup; .DS_Store; node_modules) must not wake the
		// indexer — it would run a full ValidateCollection pass (including a
		// wasted ONNX inference) only to find "no changes detected". The
		// indexer's cached .gitignore patterns are reused so the watcher does
		// not re-read .gitignore on every debounce flush.
		vm := f.getVectorManager()
		if vm != nil && vm.IsAnyIndexablePath(changedPaths) {
			vm.NotifyFileChange()
		}
	})
	if err != nil {
		f.log().Warn("failed to start workspace file watcher", "project", p.ID, "error", err)
		return
	}
	f.watcher = watcher

	// Recursively watch the research artifact tree so edits to hypothesis
	// cards, the brief, prior-art, or graph files (which live in nested
	// subdirectories like .research/R-NNN/hypotheses/) are detected. The
	// workspace watcher is NOT recursive (fsnotify only reports events for
	// explicitly-added directories), so without this the research panel
	// never receives research:file_changed and does not auto-update. New
	// subdirectories created inside the tree are auto-added by the watcher.
	if researchRoot != "" {
		if err := watcher.WatchTree(researchRoot); err != nil {
			f.log().Debug("failed to watch research tree", "root", researchRoot, "error", err)
		}
	}
}

// emitResearchFileChanged checks whether any of the changed paths fall inside
// the research directory and, if so, emits a research:file_changed event
// carrying the project ID and comma-separated paths. It returns true when at
// least one path was research-scoped, so the caller can annotate
// workspace:tree_changed and the frontend can skip a redundant full status
// refetch (the incremental path via useResearchFileWatcher handles it).
//
// The caller must pass already-snapshotted researchRoot and projectID values
// (read under activeProjectMu) to avoid the data race between the fsnotify
// callback goroutine and project switches / research toggles on the main
// thread. DRY-extracted from the CODE-mode and No-Project watcher callbacks.
func (f *FrontendAPI) emitResearchFileChanged(researchRoot, projectID string, changedPaths []string) bool {
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

// reScopeNoProjectWatcher tears down the current watcher (if any) and creates a
// new one scoped to root, which must be a No Project session workspace. Each
// chat session has an isolated workspace, so the watcher root must follow the
// active session to detect its file changes while ignoring other sessions and
// session-infra directories. The directory is created if missing so the watcher
// can be set up even before the session workspace exists on disk (e.g. fresh
// start, or a session whose workspace has not been materialized yet).
func (f *FrontendAPI) reScopeNoProjectWatcher(root string) error {
	f.watcherMu.Lock()
	defer f.watcherMu.Unlock()
	return f.reScopeNoProjectWatcherLocked(root)
}

// reScopeNoProjectWatcherLocked is the lock-held implementation of
// reScopeNoProjectWatcher. The caller must hold f.watcherMu.
func (f *FrontendAPI) reScopeNoProjectWatcherLocked(root string) error {
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
		// Snapshot the active project fields under the lock (see CODE-mode
		// callback for rationale).
		f.activeProjectMu.RLock()
		snapProjectID := f.activeProjectID
		snapResearchRoot := f.activeResearchRoot
		f.activeProjectMu.RUnlock()

		researchScoped := f.emitResearchFileChanged(snapResearchRoot, snapProjectID, changedPaths)
		f.emitEvent(EventWorkspaceTreeChanged, map[string]bool{
			"research_scoped": researchScoped,
		})

		// Invalidate skill cache in case project-local skills changed.
		f.invalidateSkillCache()
		// Invalidate agent cache in case project-local agents changed.
		f.invalidateAgentCache()
		// Same ignore-cache invalidation as CODE mode (see switchProjectSetupWatcher).
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

// switchProjectSetupVector configures vector indexing for the new project.
// For No Project (CHAT mode), vector indexing is fully disabled: the manager
// is reset to an empty state (clearing any stale CODE-project collection) and
// a disabled status is emitted, but no index is built and no embedder is loaded.
func (f *FrontendAPI) switchProjectSetupVector(p *project.ProjectInfo) error {
	vm := f.getVectorManager()
	if vm == nil {
		return nil
	}

	// No Project (CHAT mode): vector indexing is disabled. Delegate to the
	// manager, which short-circuits without running git branch detection,
	// loading the embedder, creating a persistent index, or starting a
	// background indexing goroutine. Emit a disabled status so the frontend
	// reflects the dormant state rather than the previous project's status.
	if p.IsNoProject {
		if switchErr := vm.SwitchProject(p.ID, p.WorkspacePath, config.ProjectVectorIndexPath(f.agentDir, p.ID), vectorindex.ProjectCallbacks{}); switchErr != nil {
			return fmt.Errorf("switching vector index project: %w", switchErr)
		}
		f.emitEvent(EventVectorIndexStatus, VectorIndexStatus{State: "unavailable", Indices: []string{}})
		return nil
	}

	// Vector init (chromem DB open, branch detect, branch-collection switch,
	// background indexing, git monitor) runs asynchronously inside the
	// manager's initProject goroutine, so SwitchProject returns at once. The
	// branch for the progress callback's display field is detected by
	// initProject and surfaced via vm.GetIndexStatus().Branch — no duplicate
	// synchronous CurrentBranch call here (it would block the RPC path the
	// async refactor unblocks and run git twice per switch).
	//
	// OnFailure covers the init-fatal paths (DB open / branch detect / branch
	// switch): init has already returned soft-nil, so without this the UI
	// would keep showing a stale prior state. Emit "unavailable" so the
	// frontend's deriveDotStatus renders the dormant pill the No-Project path
	// already uses.
	if switchErr := vm.SwitchProject(p.ID, p.WorkspacePath, config.ProjectVectorIndexPath(f.agentDir, p.ID), vectorindex.ProjectCallbacks{
		OnProgress: func(phase vectorindex.IndexPhase, state vectorindex.IndexState, indexed, total int, file string) {
			f.emitEvent(EventVectorIndexStatus, VectorIndexStatus{
				State:        string(state),
				Phase:        string(phase),
				Indices:      []string{"vector", "lexical"},
				Progress:     progressPercent(indexed, total),
				FilesIndexed: indexed,
				TotalFiles:   total,
				CurrentFile:  file,
				Branch:       vm.GetIndexStatus().Branch,
			})
		},
		OnFailure: func(err error) {
			f.log().Warn("vector index init failed for project; search unavailable",
				"project", p.ID, "error", err)
			f.emitEvent(EventVectorIndexStatus, VectorIndexStatus{
				State:   string(vectorindex.IndexStateUnavailable),
				Indices: []string{},
			})
		},
	}); switchErr != nil {
		return fmt.Errorf("switching vector index project: %w", switchErr)
	}

	return nil
}

// progressPercent calculates a percentage value for indexing progress.
func progressPercent(indexed, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(indexed) / float64(total) * 100
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

// persistCurrentProjectSwitchState stores switch state for the current project.
// Backend is not authoritative for open tabs/selected session, so this method
// only validates and normalizes already persisted state (non-destructive).
func (f *FrontendAPI) persistCurrentProjectSwitchState(projectID string) {
	if strings.TrimSpace(projectID) == "" {
		return
	}
	if f.projStore == nil {
		return
	}

	state, err := f.projStore.LoadUIState(context.Background(), projectID)
	if err != nil {
		f.log().Warn("failed to load current project switch state", "project", projectID, "error", err)
		return
	}
	if state == nil {
		return
	}

	normalized := normalizeProjectUIState(*state)
	resolved, err := f.resolveSavedSessionForProject(projectID, normalized.SavedSessionID)
	if err != nil {
		// Keep the original pointer: normalizing to empty after a transient
		// store failure would clobber a valid saved selection.
		f.log().Warn("failed to resolve saved session for current project switch state", "project", projectID, "error", err)
	} else {
		normalized.SavedSessionID = resolved
	}
	if !projectUIStateEqual(normalized, *state) {
		if saveErr := f.projStore.SaveUIState(context.Background(), normalized); saveErr != nil {
			f.log().Warn("failed to normalize current project switch state", "project", projectID, "error", saveErr)
		}
	}
}

// applySavedProjectSwitchState resolves and persists deterministic session fallback
// for the destination project after switching:
// 1) use restored saved session when valid
// 2) otherwise select latest session for project
// 3) otherwise create a new project-scoped session
func (f *FrontendAPI) applySavedProjectSwitchState(projectID string) {
	if strings.TrimSpace(projectID) == "" {
		return
	}
	if f.projStore == nil {
		return
	}

	state, err := f.projStore.LoadUIState(context.Background(), projectID)
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

	resolvedSessionID, changed, err := f.resolveSessionFallback(projectID, normalized.SavedSessionID)
	if err != nil {
		// Persist nothing on a resolution error: falling back to the latest
		// session after a transient store failure would overwrite a valid
		// saved pointer with the wrong value.
		f.log().Warn("failed to resolve project switch state; leaving persisted state untouched", "project", projectID, "error", err)
		return
	}
	normalized.ProjectID = strings.TrimSpace(projectID)
	normalized.SavedSessionID = resolvedSessionID

	shouldPersist := changed || state == nil || !projectUIStateEqual(normalized, *state)
	if shouldPersist {
		if saveErr := f.projStore.SaveUIState(context.Background(), normalized); saveErr != nil {
			f.log().Warn("failed to persist resolved project switch state", "project", projectID, "error", saveErr)
		}
	}
}

func (f *FrontendAPI) resolveSessionFallback(projectID, savedSessionID string) (resolvedID string, changed bool, err error) {
	resolvedSaved, err := f.resolveSavedSessionForProject(projectID, savedSessionID)
	if err != nil {
		return "", false, err
	}
	if resolvedSaved != "" {
		return resolvedSaved, strings.TrimSpace(savedSessionID) != resolvedSaved, nil
	}

	latest := f.resolveLatestSessionForProject(projectID)
	if latest != "" {
		return latest, strings.TrimSpace(savedSessionID) != latest, nil
	}

	created := f.createSessionForProject(projectID)
	if created != "" {
		return created, strings.TrimSpace(savedSessionID) != created, nil
	}

	return "", strings.TrimSpace(savedSessionID) != "", nil
}

// resolveLatestSessionForProject returns the session with the most recent
// effective activity. ListSessionsByProject exposes that effective activity as
// SessionInfo.LastActiveAt: the newest persisted session event (chat message
// or terminal command), falling back to the stored last_active_at, then to
// created_at.
func (f *FrontendAPI) resolveLatestSessionForProject(projectID string) string {
	if strings.TrimSpace(projectID) == "" {
		return ""
	}
	if f.app == nil || f.app.Manager() == nil {
		return ""
	}

	sessions, err := f.app.Manager().ListSessionsByProject(projectID)
	if err != nil {
		f.log().Warn("failed to list sessions for latest fallback resolution", "project", projectID, "error", err)
		return ""
	}

	// Archived sessions are read-only and hidden from the session picker;
	// they must never be auto-selected as the restore target.
	latest := session.SessionInfo{}
	latestTS := time.Time{}
	found := false
	for _, candidate := range sessions {
		if candidate.Archived {
			continue
		}
		candidateTS := parseSessionActivityTimestamp(candidate)
		if !found || candidateTS.After(latestTS) {
			latest = candidate
			latestTS = candidateTS
			found = true
		}
	}
	if !found {
		return ""
	}

	return latest.ID
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

func (f *FrontendAPI) createSessionForProject(projectID string) string {
	if strings.TrimSpace(projectID) == "" {
		return ""
	}
	if f.projectManager == nil || f.app == nil || f.app.Manager() == nil {
		return ""
	}

	proj, err := f.projectManager.GetProject(projectID)
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
		if err := f.store.SaveSession(context.Background(), *created); err != nil {
			f.log().Warn("failed to persist fallback session", "project", projectID, "session_id", created.ID, "error", err)
		}
	}

	return created.ID
}

// resolveNoProjectSessionWorkspace returns the workspace path of the most
// recently active session for the No Project, or an empty string if no
// session exists yet. Used to scope the file watcher to the session-specific
// directory instead of the shared __no_project__/ base.
func (f *FrontendAPI) resolveNoProjectSessionWorkspace() string {
	if f.app == nil || f.app.Manager() == nil {
		return ""
	}
	sessions, err := f.app.Manager().ListSessionsByProject(project.NoProjectID)
	if err != nil || len(sessions) == 0 {
		return ""
	}
	// ListSessionsByProject returns sessions sorted by last_active_at desc,
	// so sessions[0] is the most recently active.
	ws, ok := f.app.Manager().GetSessionWorkspacePath(sessions[0].ID)
	if !ok || ws == "" {
		return ""
	}
	return ws
}

// resolveSavedSessionForProject validates a saved session pointer against the
// project's sessions: it resolves only when the session still exists, belongs
// to the project, and is not archived. A resolution ERROR is distinct from a
// negative answer: callers must not treat a transient store failure as
// "pointer is stale", because normalizing to empty in that case would clobber
// a perfectly valid saved selection.
func (f *FrontendAPI) resolveSavedSessionForProject(projectID, savedSessionID string) (string, error) {
	savedSessionID = strings.TrimSpace(savedSessionID)
	if savedSessionID == "" {
		return "", nil
	}
	if f.app == nil || f.app.Manager() == nil {
		return "", nil
	}

	sessions, err := f.app.Manager().ListSessionsByProject(projectID)
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

	// If session is not listed but a store+resolver may lazily restore it,
	// validate ownership directly via manager.GetSession.
	if restored, ok := f.app.Manager().GetSession(savedSessionID); ok && restored != nil {
		if restored.ProjectID == projectID && !restored.Archived {
			return savedSessionID, nil
		}
	}

	return "", nil
}
