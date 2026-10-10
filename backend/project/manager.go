package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/sp4rk/safeio"
)

// Manager provides high-level project lifecycle operations.
type Manager struct {
	store    ProjectStore
	agentDir string // ~/.c0wrk
	logger   *slog.Logger
	mu       sync.RWMutex
	// initErr is non-nil when the projects base directory could not be
	// created as a REAL directory (a symlink or non-directory planted at any
	// component of ~/.c0wrk/projects). Every operation fails closed while it
	// is set: os.MkdirAll would silently follow the link and materialize
	// project workspaces, session logs/dumps/temp and vector indexes inside
	// the link target — outside ~/.c0wrk. Mirrors the loud agent-dir refusal
	// in backend/config resolve.
	initErr error
}

// storeOpTimeout bounds one ProjectStore round-trip. The store rides the
// app's single shared SQLite connection pool; database/sql waits for a free
// pooled connection with no deadline when the caller context carries none
// (busy_timeout bounds only SQLite lock retry). Mirrors the session
// package's restoreDBReadTimeout so a contended pool yields a prompt,
// retryable error instead of an indefinitely parked RPC.
const storeOpTimeout = 15 * time.Second

// opCtx returns a bounded context for one store round-trip.
func (m *Manager) opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), storeOpTimeout)
}

// checkReady reports the manager's initialization failure, if any.
func (m *Manager) checkReady() error {
	if m.initErr != nil {
		return fmt.Errorf("projects directory unusable: %w", m.initErr)
	}
	return nil
}

// EnsureNoProject creates the No Project pseudo-project if it does not already exist.
// It is safe to call multiple times (idempotent). Returns created=true when
// the project was newly created (false when it already existed).
//
// For No Project, WorkspacePath points to the project directory itself
// (~/.c0wrk/projects/__no_project__/) rather than a shared Workspace/
// subdirectory. Per-session workspaces live under <session-uuid>/workspace/
// and are created lazily by the session manager.
func (m *Manager) EnsureNoProject() (created bool, err error) {
	if err := m.checkReady(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	ctx, cancel := m.opCtx()
	defer cancel()
	proj, err := m.store.LoadProject(ctx, NoProjectID)
	if err != nil {
		return false, fmt.Errorf("checking No Project: %w", err)
	}
	if proj != nil {
		return false, nil // already exists
	}

	now := time.Now().UTC().Format(time.RFC3339)
	// No Project workspace is the project directory itself; per-session
	// workspaces live under <uuid>/workspace/ and are created by the
	// session manager on demand.
	wsPath := config.ProjectDir(m.agentDir, NoProjectID)
	// Ensure the stored path is always absolute so it remains valid
	// regardless of runtime working directory changes.
	if absPath, err := filepath.Abs(wsPath); err == nil {
		wsPath = absPath
	}
	info := ProjectInfo{
		ID:            NoProjectID,
		Name:          "No Project",
		WorkspacePath: wsPath,
		IsExternal:    false,
		IsNoProject:   true,
		CreatedAt:     now,
		LastActiveAt:  now,
	}
	// Do not eagerly create the directory — per-session workspace
	// creation will create parent directories lazily via MkdirAll.
	if err := m.store.SaveProject(ctx, info); err != nil {
		return false, err
	}
	return true, nil
}

// NewManager creates a new project Manager and ensures the projects base
// directory (~/.c0wrk/projects/) exists.
//
// logger is used to emit warnings on non-critical directory-creation failures.
// If nil, warnings are suppressed.
func NewManager(store ProjectStore, agentDir string, logger *slog.Logger) *Manager {
	// Ensure the projects base directory exists so downstream project
	// creation (internal workspaces, session directories) can proceed
	// without the caller needing to manage directory layout.
	projectsDir := config.ProjectsDir(agentDir)
	// MkdirAllReal creates only real directories: a dangling link, a
	// non-directory component, or a link swapped into a created component is
	// refused loudly and fail-closed — initErr makes every subsequent
	// operation fail rather than write through it — while a pre-existing
	// operator-symlinked tree resolves as intent rather than as a planted
	// redirect of the project/session subtree writes (workspaces, session
	// logs/dumps/temp/plans, the vector index).
	initErr := safeio.MkdirAllReal(projectsDir, 0o755)
	if initErr != nil && logger != nil {
		logger.Error("projects directory is not a real directory — refusing to create or operate projects through it",
			"path", projectsDir, "error", initErr)
	}

	return &Manager{
		store:    store,
		agentDir: agentDir,
		logger:   logger,
		initErr:  initErr,
	}
}

// CreateProject creates a new project with either an internal or external workspace.
// If externalPath is empty, an internal workspace directory is created under the agentDir.
// If externalPath is non-empty, the path is validated and used as-is (external project).
func (m *Manager) CreateProject(name, externalPath string) (*ProjectInfo, error) {
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	info := ProjectInfo{
		ID:           id,
		Name:         name,
		CreatedAt:    now,
		LastActiveAt: now,
	}

	if externalPath == "" {
		// Internal project: create workspace directory under ~/.c0wrk/projects/<id>/Workspace
		info.WorkspacePath = config.ProjectWorkspacePath(m.agentDir, id)
		info.IsExternal = false
		// Real-directory creation: a dangling or swapped-in symlink at the
		// workspace root fails instead of redirecting it (same
		// directory-vector class as the projects root guarded in
		// NewManager).
		if err := safeio.MkdirAllReal(info.WorkspacePath, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create internal workspace directory: %w", err)
		}
	} else {
		// External project: canonicalize the path (absolute, cleaned,
		// symlink-resolved) before persisting. Stored paths participate in
		// security containment (allowed roots), which compares them against
		// resolved tool inputs, so a non-canonical root would silently fail
		// the containment match — mirrors AddWorkDirectory.
		abs, err := filepath.Abs(externalPath)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve external workspace path: %w", err)
		}
		abs = filepath.Clean(abs)
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("external path does not exist: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve external workspace symlinks: %w", err)
		}
		info.WorkspacePath = resolved
		info.IsExternal = true
	}

	ctx, cancel := m.opCtx()
	defer cancel()
	if err := m.store.SaveProject(ctx, info); err != nil {
		return nil, fmt.Errorf("failed to persist project: %w", err)
	}

	return &info, nil
}

// DeleteProject removes a project. For internal projects, the workspace directory is also removed.
// External workspace directories are never touched. The No Project pseudo-project cannot be deleted.
func (m *Manager) DeleteProject(id string) error {
	if id == NoProjectID {
		return errors.New("cannot delete the No Project pseudo-project")
	}
	if err := m.checkReady(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	ctx, cancel := m.opCtx()
	defer cancel()
	proj, err := m.store.LoadProject(ctx, id)
	if err != nil {
		return fmt.Errorf("failed to load project for deletion: %w", err)
	}
	if proj == nil {
		return fmt.Errorf("project %q not found", id)
	}

	// Delete from store first (FK cascade handles sessions+messages)
	if err := m.store.DeleteProject(ctx, id); err != nil {
		return fmt.Errorf("failed to delete project from store: %w", err)
	}

	if !proj.IsExternal {
		// Internal project: remove the project directory tree
		projectDir := config.ProjectDir(m.agentDir, id)
		if err := os.RemoveAll(projectDir); err != nil {
			return fmt.Errorf("failed to remove internal project directory: %w", err)
		}
	} else {
		// External project: only clean up the project directory under the agentDir if it
		// somehow exists (it shouldn't, but be safe). NEVER touch the external workspace.
		projectDir := config.ProjectDir(m.agentDir, id)
		if _, err := os.Stat(projectDir); err == nil {
			if err := os.RemoveAll(projectDir); err != nil {
				return fmt.Errorf("failed to remove stale project directory: %w", err)
			}
		}
	}

	return nil
}

// RenameProject updates a project's display name.
func (m *Manager) RenameProject(id, name string) error {
	if id == NoProjectID {
		return errors.New("cannot rename the No Project pseudo-project")
	}
	if err := m.checkReady(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := m.opCtx()
	defer cancel()
	return m.store.RenameProject(ctx, id, name)
}

// ListProjects returns all projects ordered by last activity.
func (m *Manager) ListProjects() ([]ProjectInfo, error) {
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ctx, cancel := m.opCtx()
	defer cancel()
	return m.store.ListProjects(ctx)
}

// GetProject returns a project by ID, or nil if not found.
func (m *Manager) GetProject(id string) (*ProjectInfo, error) {
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	ctx, cancel := m.opCtx()
	defer cancel()
	return m.store.LoadProject(ctx, id)
}
