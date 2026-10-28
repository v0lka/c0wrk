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
	// created as a REAL directory (a planted link anywhere under
	// ~/.c0wrk/projects); every operation fails closed while it is set,
	// mirroring the agent-dir refusal in backend/config resolve.
	initErr error
}

// storeOpTimeout bounds one ProjectStore round-trip on the shared SQLite
// pool (database/sql parks indefinitely on a free connection without a
// caller deadline); mirrors the session package's restoreDBReadTimeout.
// Switch-section stacking: specs/domains/workspace.md.
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

// EnsureNoProject creates the No Project pseudo-project unless it exists
// (created reports which happened). Its WorkspacePath is the project
// directory itself; per-session workspaces are the session manager's
// business. No Project semantics: specs/domains/workspace.md.
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
	wsPath := config.ProjectDir(m.agentDir, NoProjectID)
	// Store an absolute path: it must stay valid across cwd changes.
	if absPath, err := filepath.Abs(wsPath); err == nil {
		wsPath = absPath
	}
	// Canonicalize the deepest EXISTING ancestor (the workspace itself is
	// deliberately not created here) so the stored path matches the
	// resolved-form invariant CreateProject establishes; fail-soft back to
	// the lexical form.
	if resolvedParent, perr := filepath.EvalSymlinks(filepath.Dir(wsPath)); perr == nil {
		wsPath = filepath.Join(resolvedParent, filepath.Base(wsPath))
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
	// Not created here — per-session workspace creation MkdirAlls lazily.
	if err := m.store.SaveProject(ctx, info); err != nil {
		return false, err
	}
	return true, nil
}

// NewManager creates a new project Manager and ensures the projects base
// directory (~/.c0wrk/projects/) exists. A nil logger suppresses the
// non-critical failure warnings.
func NewManager(store ProjectStore, agentDir string, logger *slog.Logger) *Manager {
	projectsDir := config.ProjectsDir(agentDir)
	// MkdirAllReal: only REAL directories are created — a planted or
	// swapped-in link fails closed into initErr instead of redirecting the
	// project/session subtree writes; a pre-existing operator-symlinked
	// tree is intent, not a redirect.
	// See specs/architecture/security-model.md (Fixed-Path ~/.c0wrk State
	// Hardening).
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
		// Real-directory creation: a planted or swapped-in link at the
		// workspace root fails instead of redirecting it (same class as the
		// projects root in NewManager).
		// See specs/architecture/security-model.md (Fixed-Path ~/.c0wrk State
		// Hardening).
		if err := safeio.MkdirAllReal(info.WorkspacePath, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create internal workspace directory: %w", err)
		}
		// Canonicalize before persisting (external branch does the same):
		// security containment and the git worktree primitives compare
		// stored paths against RESOLVED forms, so a path through a symlinked
		// ancestor would silently diverge from both.
		resolved, err := filepath.EvalSymlinks(info.WorkspacePath)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve internal workspace symlinks: %w", err)
		}
		info.WorkspacePath = resolved
	} else {
		// External project: canonicalize (absolute, cleaned, resolved)
		// before persisting — containment compares stored roots against
		// resolved tool inputs, so a non-canonical root silently fails the
		// match (mirrors AddWorkDirectory).
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

	if err := config.CheckProjectWorktreesReleased(m.agentDir, id); err != nil {
		return err
	}

	// Delete from store first (FK cascade handles sessions+messages)
	if err := m.store.DeleteProject(ctx, id); err != nil {
		return fmt.Errorf("failed to delete project from store: %w", err)
	}

	if !proj.IsExternal {
		projectDir := config.ProjectDir(m.agentDir, id)
		if err := os.RemoveAll(projectDir); err != nil {
			return fmt.Errorf("failed to remove internal project directory: %w", err)
		}
	} else {
		// External project: remove a stale project dir if one somehow exists.
		// NEVER touch the external workspace.
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
	ctx, cancel := m.opCtx()
	defer cancel()
	return m.ListProjectsCtx(ctx)
}

// ListProjectsCtx is the caller-deadline variant of ListProjects: the
// project-switch critical section derives every store round-trip from ONE
// switch-scoped deadline, because individually bounded ops still stack to
// minutes of switchMu hold time on a saturated pool. See SwitchProject's
// switchSectionTimeout; specs/domains/workspace.md.
func (m *Manager) ListProjectsCtx(ctx context.Context) ([]ProjectInfo, error) {
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.ListProjects(ctx)
}

// GetProject returns a project by ID, or nil if not found.
func (m *Manager) GetProject(id string) (*ProjectInfo, error) {
	ctx, cancel := m.opCtx()
	defer cancel()
	return m.GetProjectCtx(ctx, id)
}

// GetProjectCtx is the caller-deadline variant of GetProject (see
// ListProjectsCtx for why it exists).
func (m *Manager) GetProjectCtx(ctx context.Context, id string) (*ProjectInfo, error) {
	if err := m.checkReady(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.LoadProject(ctx, id)
}
