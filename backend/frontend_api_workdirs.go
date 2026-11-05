package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/c0wrk/backend/project"
)

// Scope values for the auxiliary-work-directory RPCs; selects the persistence table.
// See specs/contracts/desktop-frontend.md (Work Directories).
const (
	workDirScopeProject = "project" // project-scoped records
	workDirScopeSession = "session" // session-scoped records
)

// resolveWorkDirPath normalizes a work-directory path to the canonical stored form
// (absolute, cleaned, symlink-resolved): stored paths feed security containment
// against resolved tool inputs, so a non-canonical root would silently fail the
// match. Must exist on disk. See specs/architecture/security-model.md (Session Roots).
func resolveWorkDirPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("cannot resolve work directory path: %w", err)
	}
	abs = filepath.Clean(abs)

	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("work directory does not exist: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("cannot resolve work directory symlinks: %w", err)
	}
	return resolved, nil
}

// ListProjectWorkDirectories returns all project-scoped auxiliary directories.
func (f *FrontendAPI) ListProjectWorkDirectories(projectID string) ([]project.WorkDirectoryRecord, error) {
	f.seedAcquire()
	if f.projStore == nil {
		return nil, errors.New("project store not initialized")
	}
	recs, err := f.projStore.ListProjectWorkDirs(f.ctx(), projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project work directories: %w", err)
	}
	return recs, nil
}

// ListSessionWorkDirectories returns all session-scoped auxiliary directories.
func (f *FrontendAPI) ListSessionWorkDirectories(sessionID string) ([]project.WorkDirectoryRecord, error) {
	f.seedAcquire()
	if f.store == nil {
		return nil, errors.New("session store not initialized")
	}
	recs, err := f.store.ListSessionWorkDirs(f.ctx(), sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list session work directories: %w", err)
	}
	return recs, nil
}

// AddWorkDirectory adds an auxiliary directory; scope selects the project- or
// session-scoped persistence. See specs/contracts/desktop-frontend.md (Work Directories).
func (f *FrontendAPI) AddWorkDirectory(scope, ownerID, path, description string) error {
	f.seedAcquire()
	path = strings.TrimSpace(path)
	description = strings.TrimSpace(description)
	if path == "" {
		return errors.New("path is required")
	}
	if description == "" {
		return errors.New("description is required")
	}
	resolved, err := resolveWorkDirPath(path)
	if err != nil {
		return err
	}
	path = resolved

	switch scope {
	case workDirScopeProject:
		if ownerID == project.NoProjectID {
			return errors.New("project-scoped directories are not available for No Project")
		}
		if f.projStore == nil {
			return errors.New("project store not initialized")
		}
		if err := f.projStore.SaveProjectWorkDir(f.ctx(), ownerID, project.WorkDirectoryRecord{
			Path:        path,
			Description: description,
		}); err != nil {
			if errors.Is(err, project.ErrWorkDirAlreadyExists) {
				return errors.New("this directory is already added for this project")
			}
			return fmt.Errorf("failed to add project work directory: %w", err)
		}
	case workDirScopeSession:
		if f.store == nil {
			return errors.New("session store not initialized")
		}
		if err := f.store.SaveSessionWorkDir(f.ctx(), ownerID, project.WorkDirectoryRecord{
			Path:        path,
			Description: description,
		}); err != nil {
			if errors.Is(err, project.ErrWorkDirAlreadyExists) {
				return errors.New("this directory is already added for this session")
			}
			return fmt.Errorf("failed to add session work directory: %w", err)
		}
	default:
		return fmt.Errorf("unknown scope %q: must be %q or %q", scope, workDirScopeProject, workDirScopeSession)
	}

	f.emitWorkDirsChanged()

	// Intake scan: warns when the added directory is a repository with a dangerous
	// .git/config (only the add path scans). See specs/decisions/033-git-subprocess-hardening.md.
	f.notifyGitConfigRisk(GitConfigRiskSourceWorkdir, path)

	return nil
}

// UpdateWorkDirectoryDescription updates an existing directory's description;
// ownerID doubles as a scope guard, so a cross-scope ID cannot mutate another owner.
func (f *FrontendAPI) UpdateWorkDirectoryDescription(scope, ownerID, id, description string) error {
	f.seedAcquire()
	description = strings.TrimSpace(description)
	if description == "" {
		return errors.New("description is required")
	}

	switch scope {
	case workDirScopeProject:
		if f.projStore == nil {
			return errors.New("project store not initialized")
		}
		if err := f.projStore.UpdateProjectWorkDirDescription(f.ctx(), ownerID, id, description); err != nil {
			return fmt.Errorf("failed to update project work directory description: %w", err)
		}
	case workDirScopeSession:
		if f.store == nil {
			return errors.New("session store not initialized")
		}
		if err := f.store.UpdateSessionWorkDirDescription(f.ctx(), ownerID, id, description); err != nil {
			return fmt.Errorf("failed to update session work directory description: %w", err)
		}
	default:
		return fmt.Errorf("unknown scope %q: must be %q or %q", scope, workDirScopeProject, workDirScopeSession)
	}

	f.emitWorkDirsChanged()
	return nil
}

// DeleteWorkDirectory removes a directory; ownerID doubles as a scope guard.
func (f *FrontendAPI) DeleteWorkDirectory(scope, ownerID, id string) error {
	f.seedAcquire()
	switch scope {
	case workDirScopeProject:
		if f.projStore == nil {
			return errors.New("project store not initialized")
		}
		if err := f.projStore.DeleteProjectWorkDir(f.ctx(), ownerID, id); err != nil {
			return fmt.Errorf("failed to delete project work directory: %w", err)
		}
	case workDirScopeSession:
		if f.store == nil {
			return errors.New("session store not initialized")
		}
		if err := f.store.DeleteSessionWorkDir(f.ctx(), ownerID, id); err != nil {
			return fmt.Errorf("failed to delete session work directory: %w", err)
		}
	default:
		return fmt.Errorf("unknown scope %q: must be %q or %q", scope, workDirScopeProject, workDirScopeSession)
	}

	f.emitWorkDirsChanged()
	return nil
}

// emitWorkDirsChanged emits the global workdirs:changed event.
// See specs/contracts/event-catalog.md.
func (f *FrontendAPI) emitWorkDirsChanged() {
	f.seedAcquire()
	if f.emitEvent != nil {
		f.emitEvent(EventWorkDirsChanged)
	}
}
