package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/backend/vectorindex"
	"github.com/v0lka/c0wrk/backend/workspace"
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
	// If deleting the active project, clear active state
	f.activeProjectMu.Lock()
	wasActive := f.activeProjectID == id
	if wasActive {
		f.activeProjectID = ""
		f.activeProjectPath = ""
	}
	f.activeProjectMu.Unlock()

	if err := f.projectManager.DeleteProject(id); err != nil {
		return err
	}

	// Clean up vector index data for the deleted project.
	if vm := f.getVectorManager(); vm != nil {
		_ = vm.DeleteProjectData(id) // Best-effort; error is non-critical.
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

	// Persist current project's switch state before changing active project.
	f.activeProjectMu.RLock()
	previousProjectID := f.activeProjectID
	f.activeProjectMu.RUnlock()
	if previousProjectID != "" && previousProjectID != id {
		f.persistCurrentProjectSwitchState(previousProjectID)
	}

	// Cancel any in-flight indexing from a previous project.
	if vm := f.getVectorManager(); vm != nil {
		vm.CancelIndexing()
	}

	f.activeProjectMu.Lock()
	f.activeProjectID = p.ID
	f.activeProjectPath = p.WorkspacePath
	f.activeProjectMu.Unlock()

	// Set MCP working directory to the new project workspace
	if f.app != nil {
		f.app.Builder().SetMCPWorkDir(p.WorkspacePath)
	}

	// Update project activity timestamp.
	if f.projStore != nil {
		_ = f.projStore.UpdateProjectActivity(context.Background(), id) // Best-effort; error is non-critical.
	}

	// Recreate file watcher for the new project workspace
	if f.watcher != nil {
		_ = f.watcher.Close() // Best-effort cleanup; error is non-critical.
		f.watcher = nil
	}

	watcher, err := workspace.NewWatcher(p.WorkspacePath, func() {
		// Existing behavior: emit workspace tree change.
		f.emitEvent(EventWorkspaceTreeChanged, nil)

		// Trigger debounced incremental indexing via Manager.
		if vm := f.getVectorManager(); vm != nil {
			vm.NotifyFileChange(p.WorkspacePath)
		}
	})
	if err != nil {
		f.log().Warn("failed to start workspace file watcher", "project", id, "error", err)
	} else {
		f.watcher = watcher
	}

	// --- Vector index wiring ---
	// Git is a hard dependency (verified at startup), so any branch
	// detection failure is a real error — not an excuse to silently fall
	// back to DefaultBranch and potentially mis-partition the index.
	if vm := f.getVectorManager(); vm != nil {
		branch, branchErr := vectorindex.CurrentBranch(f.ctx(), p.WorkspacePath)
		if branchErr != nil {
			return fmt.Errorf("detecting git branch for project %s: %w", p.ID, branchErr)
		}
		capturedBranch := branch

		if switchErr := vm.SwitchProject(p.ID, p.WorkspacePath, vectorindex.ProjectCallbacks{
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
	}

	f.applySavedProjectSwitchState(p.ID)
	f.emitEvent(EventProjectSwitched, p)

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
