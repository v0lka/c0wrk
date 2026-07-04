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
	if wasActive && f.watcher != nil {
		_ = f.watcher.Close() // Best-effort cleanup; error is non-critical.
		f.watcher = nil
	}

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
	state.SavedSessionID = f.resolveSavedSessionForProject(state.ProjectID, state.SavedSessionID)

	if err := f.projStore.SaveUIState(context.Background(), state); err != nil {
		return fmt.Errorf("failed to save project UI state: %w", err)
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
	normalized.SavedSessionID = f.resolveSavedSessionForProject(projectID, normalized.SavedSessionID)
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

	// Idempotency: skip if the same project is already active.
	f.activeProjectMu.RLock()
	alreadyActive := f.activeProjectID == id
	f.activeProjectMu.RUnlock()
	if alreadyActive {
		f.log().Info("SwitchProject: project already active, skipping", "project", id)
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
	if !p.IsNoProject && !gitOnPath() {
		f.emitEvent(EventRuntimeError, map[string]string{
			"id":         uuid.New().String(),
			"message":    "Git is required for CODE mode. Please install git and try again.",
			"error_code": "git_not_found",
		})
		f.log().Warn("SwitchProject blocked: git not found on PATH", "project", id)
		return nil // error already reported via event; no-op the switch
	}

	f.switchProjectTeardown(id)
	f.switchProjectActivate(p)
	f.switchProjectSetupWatcher(p)
	f.recoverPlanReviewSessions(p)

	if err := f.switchProjectSetupVector(p); err != nil {
		return err
	}

	f.applySavedProjectSwitchState(p.ID)
	f.emitEvent(EventProjectSwitched, p)

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
	f.activeProjectMu.Unlock()

	// Invalidate cached skill list since project-local skills may differ.
	f.invalidateSkillCache()

	// Set MCP working directory to the new project workspace
	if b := f.builder(); b != nil {
		b.SetMCPWorkDir(p.WorkspacePath)
	}

	// Update project activity timestamp.
	if f.projStore != nil {
		_ = f.projStore.UpdateProjectActivity(context.Background(), p.ID) // Best-effort; error is non-critical.
	}
}

// switchProjectSetupWatcher creates a file watcher for the new project workspace.
// For No Project, the watcher is scoped to the most recently active session's
// workspace directory to avoid cross-session tree_changed noise. Falls back to
// the project workspace path if no session workspace can be determined.
func (f *FrontendAPI) switchProjectSetupWatcher(p *project.ProjectInfo) {
	// Always tear down the previous watcher, regardless of target project.
	if f.watcher != nil {
		_ = f.watcher.Close() // Best-effort cleanup; error is non-critical.
		f.watcher = nil
	}

	// Determine the watcher root. For No Project, scope to the active
	// session's workspace directory so file changes in session A don't
	// trigger tree refreshes while viewing session B.
	watcherRoot := p.WorkspacePath
	if p.IsNoProject {
		if sessionWS := f.resolveNoProjectSessionWorkspace(); sessionWS != "" {
			watcherRoot = sessionWS
		}
	}

	watcher, err := workspace.NewWatcher(watcherRoot, func() {
		f.emitEvent(EventWorkspaceTreeChanged, nil)

		// No Project: skip vector index notification (no indexing).
		if !p.IsNoProject {
			if vm := f.getVectorManager(); vm != nil {
				vm.NotifyFileChange(p.WorkspacePath)
			}
		}
	})
	if err != nil {
		f.log().Warn("failed to start workspace file watcher", "project", p.ID, "error", err)
	} else {
		f.watcher = watcher
	}
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

	branch, branchErr := vectorindex.CurrentBranch(f.ctx(), p.WorkspacePath)
	if branchErr != nil {
		return fmt.Errorf("detecting git branch for project %s: %w", p.ID, branchErr)
	}
	capturedBranch := branch

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
				Branch:       capturedBranch,
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
	normalized.SavedSessionID = f.resolveSavedSessionForProject(projectID, normalized.SavedSessionID)
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

	resolvedSessionID, changed := f.resolveSessionFallback(projectID, normalized.SavedSessionID)
	normalized.ProjectID = strings.TrimSpace(projectID)
	normalized.SavedSessionID = resolvedSessionID

	shouldPersist := changed || state == nil || !projectUIStateEqual(normalized, *state)
	if shouldPersist {
		if saveErr := f.projStore.SaveUIState(context.Background(), normalized); saveErr != nil {
			f.log().Warn("failed to persist resolved project switch state", "project", projectID, "error", saveErr)
		}
	}
}

func (f *FrontendAPI) resolveSessionFallback(projectID, savedSessionID string) (string, bool) {
	resolvedSaved := f.resolveSavedSessionForProject(projectID, savedSessionID)
	if resolvedSaved != "" {
		return resolvedSaved, strings.TrimSpace(savedSessionID) != resolvedSaved
	}

	latest := f.resolveLatestSessionForProject(projectID)
	if latest != "" {
		return latest, strings.TrimSpace(savedSessionID) != latest
	}

	created := f.createSessionForProject(projectID)
	if created != "" {
		return created, strings.TrimSpace(savedSessionID) != created
	}

	return "", strings.TrimSpace(savedSessionID) != ""
}

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
	if len(sessions) == 0 {
		return ""
	}

	latest := sessions[0]
	latestTS := parseSessionActivityTimestamp(latest)
	for i := 1; i < len(sessions); i++ {
		candidate := sessions[i]
		candidateTS := parseSessionActivityTimestamp(candidate)
		if candidateTS.After(latestTS) {
			latest = candidate
			latestTS = candidateTS
		}
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

func (f *FrontendAPI) resolveSavedSessionForProject(projectID, savedSessionID string) string {
	savedSessionID = strings.TrimSpace(savedSessionID)
	if savedSessionID == "" {
		return ""
	}
	if f.app == nil || f.app.Manager() == nil {
		return ""
	}

	sessions, err := f.app.Manager().ListSessionsByProject(projectID)
	if err != nil {
		f.log().Warn("failed to list sessions for project switch state resolution", "project", projectID, "error", err)
		return ""
	}
	for _, s := range sessions {
		if s.ID == savedSessionID {
			return savedSessionID
		}
	}

	// If session is not listed but a store+resolver may lazily restore it,
	// validate ownership directly via manager.GetSession.
	if restored, ok := f.app.Manager().GetSession(savedSessionID); ok && restored != nil {
		if restored.ProjectID == projectID {
			return savedSessionID
		}
	}

	return ""
}

// recoverPlanReviewSessions queries sessions in plan review for the given
// project, restores them, validates the .md file still exists, and re-emits
// plan_review_ready events so the frontend re-opens the review panel after
// app restart. Stale state (missing .md file) is cleared to prevent stuck sessions.
func (f *FrontendAPI) recoverPlanReviewSessions(p *project.ProjectInfo) {
	if f.store == nil {
		return
	}
	manager := f.app.Manager()
	if manager == nil {
		return
	}

	ctx := f.ctx()
	sessions, err := f.store.GetSessionsInPlanReview(ctx, p.ID)
	if err != nil {
		f.log().Warn("failed to query plan review sessions on recovery", "project", p.ID, "error", err)
		return
	}

	for _, info := range sessions {
		// For awaiting_feedback sessions, emit the feedback prompt instead
		// of plan_review_ready so the user knows to type a message.
		if info.PlanReviewPhase == string(session.PlanReviewAwaitingFeedback) {
			manager.EmitSessionEvent(info.ID, "plan_review_awaiting_feedback",
				map[string]string{"session_id": info.ID})
			continue
		}

		// Plan review path may be empty — stale state without a path.
		if info.PlanReviewPath == "" {
			// Stale state without a path — clear it.
			if clrErr := f.store.UpdateSessionPlanReview(ctx, info.ID, "", ""); clrErr != nil {
				f.log().Warn("failed to clear stale plan review state", "session", info.ID, "error", clrErr)
			}
			continue
		}
		if _, statErr := os.Stat(info.PlanReviewPath); os.IsNotExist(statErr) {
			// Plan file missing — clear stale state.
			f.log().Info("clearing stale plan review state (plan file missing)", "session", info.ID, "path", info.PlanReviewPath)
			if clrErr := f.store.UpdateSessionPlanReview(ctx, info.ID, "", ""); clrErr != nil {
				f.log().Warn("failed to clear stale plan review state", "session", info.ID, "error", clrErr)
			}
			continue
		}

		// Check if the plan was already resolved (accepted or rejected).
		// A resolved plan_review message in chat history means the plan was
		// handled and we should not re-emit plan_review_ready.
		if resolved, rErr := f.store.HasResolvedPlanReviewMessage(ctx, info.ID); rErr != nil {
			f.log().Warn("failed to check resolved plan review, skipping recovery", "session", info.ID, "error", rErr)
			continue
		} else if resolved {
			f.log().Info("clearing stale plan review state (plan already resolved)", "session", info.ID)
			if clrErr := f.store.UpdateSessionPlanReview(ctx, info.ID, "", ""); clrErr != nil {
				f.log().Warn("failed to clear stale plan review state", "session", info.ID, "error", clrErr)
			}
			continue
		}

		// Restore the session into memory (getOrRestoreSession also restores planReviewBB/Route from persistence).
		sess, ok := manager.GetSession(info.ID)
		if !ok || sess == nil {
			f.log().Warn("failed to restore session for plan review recovery", "session", info.ID)
			continue
		}

		// Read plan content and emit plan_review_ready as a session-scoped event.
		planContent, readErr := os.ReadFile(info.PlanReviewPath)
		if readErr != nil {
			f.log().Warn("failed to read plan file for recovery", "session", info.ID, "path", info.PlanReviewPath, "error", readErr)
			// Clear stale state and notify the frontend so the session isn't stuck.
			if clrErr := f.store.UpdateSessionPlanReview(ctx, info.ID, "", ""); clrErr != nil {
				f.log().Warn("failed to clear stale plan review state", "session", info.ID, "error", clrErr)
			}
			manager.EmitSessionEvent(info.ID, "plan_validation_failed", session.PlanValidationFailedData{
				SessionID: info.ID,
				Issues:    []session.ValidationIssue{{Severity: "error", Description: "Plan file could not be read after restart: " + readErr.Error()}},
			})
			continue
		}
		// Emit plan_review_ready through the session manager's emitFunc
		// so the event persists and reaches frontend listeners correctly.
		// Use a session-scoped emit via the manager directly.
		manager.EmitSessionEvent(info.ID, "plan_review_ready", session.PlanReviewReadyData{
			SessionID:   info.ID,
			PlanPath:    info.PlanReviewPath,
			PlanContent: string(planContent),
		})
	}
}
