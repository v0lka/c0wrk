package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/c0wrk/backend/project"
)

// Scope values accepted by the auxiliary-work-directory RPC methods. Scope
// determines which persistence table (project- or session-scoped) owns the
// record.
const (
	workDirScopeProject = "project"
	workDirScopeSession = "session"
)

// resolveWorkDirPath normalizes a work-directory path to a canonical form:
// absolute, cleaned, and symlink-resolved. Stored paths participate in
// security containment (allowed roots), which compares them against resolved
// tool inputs, so a non-canonical root (relative path, trailing slash,
// symlinked prefix) would silently fail the containment match. The path must
// exist on disk and be a directory; EvalSymlinks requires existence, so it
// runs after the existence check.
//
// Shared by the explicit AddWorkDirectory RPC and the prompt-driven auto-add
// so both produce the same canonical form.
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
	if f.store == nil {
		return nil, errors.New("session store not initialized")
	}
	recs, err := f.store.ListSessionWorkDirs(f.ctx(), sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list session work directories: %w", err)
	}
	return recs, nil
}

// AddWorkDirectory adds an auxiliary directory. scope is "project" or "session";
// ownerID is the projectID or sessionID accordingly. The directory must exist
// on disk; project-scoped directories are not available for the No Project
// pseudo-project.
func (f *FrontendAPI) AddWorkDirectory(scope, ownerID, path, description string) error {
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

	// Intake scan (text-only, exec-free): warn when the added directory is a
	// repository with a dangerous .git/config. Clean or non-git directories
	// emit nothing. Only the add path scans — description edits and removals
	// do not open a new workspace. See frontend_api_gitconfig_risk.go.
	f.notifyGitConfigRisk(GitConfigRiskSourceWorkdir, path)

	return nil
}

// UpdateWorkDirectoryDescription updates the description of an existing
// directory. scope is "project" or "session"; ownerID is the projectID or
// sessionID (used as a scope guard so a cross-scope ID cannot mutate another
// owner's record); id is the record ID.
func (f *FrontendAPI) UpdateWorkDirectoryDescription(scope, ownerID, id, description string) error {
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

// DeleteWorkDirectory removes a directory. scope is "project" or "session";
// ownerID is the projectID or sessionID (used as a scope guard); id is the
// record ID.
func (f *FrontendAPI) DeleteWorkDirectory(scope, ownerID, id string) error {
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

// emitWorkDirsChanged emits the global workdirs:changed event so the frontend
// refreshes its auxiliary-directory list. No-op when the Wails emitter has not
// been wired yet (e.g. very early in startup) — mirrors how other global events
// are dispatched through the injected emitEvent callback.
func (f *FrontendAPI) emitWorkDirsChanged() {
	if f.emitEvent != nil {
		f.emitEvent(EventWorkDirsChanged)
	}
}
