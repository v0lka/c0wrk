package core

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/sp4rk/pathutil"
	"github.com/v0lka/sp4rk/safeio"
)

// WorktreeRepositoryMarker binds a managed container to its repository; it
// survives tree removal and config changes — specs/domains/session-worktrees.md.
const WorktreeRepositoryMarker = ".c0wrk-worktree-repository"

// IsManagedWorktreePath recognizes a direct child of the legacy container or a
// marked container belonging to repoRoot, independently of current config
// (specs/domains/session-worktrees.md). Callers still validate portable names
// and live git membership before lifecycle operations.
func IsManagedWorktreePath(repoRoot, target string) bool {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return false
	}
	container := filepath.Dir(target)
	legacy := filepath.Join(repoRoot, WorktreesRelativePath)
	if pathutil.ResolveExistingPrefix(container) != pathutil.ResolveExistingPrefix(legacy) {
		if pathutil.ResolveExistingPrefix(container) != container {
			return false
		}
		marker := filepath.Join(container, WorktreeRepositoryMarker)
		fi, err := os.Lstat(marker)
		if err != nil || !fi.Mode().IsRegular() {
			return false
		}
		data, err := safeio.ReadFile(marker)
		if err != nil || strings.TrimSpace(string(data)) != pathutil.ResolveExistingPrefix(repoRoot) {
			return false
		}
	}
	return ValidateWorktreeContainerPath(container, target) == nil
}

// IsProjectScopedWorktreePath recognizes a direct child of a project-scoped
// managed container (<...>/<projectID>/<ProjectWorktreesSegment>/<name>) by
// its project-id layout alone — marker-independent, so a recorded binding
// stays decodable after the marker or app-data container is removed
// (specs/domains/session-worktrees.md); containment and symlink-safety still
// hold. Foreign (git-listed) trees keep the stricter IsManagedWorktreePath.
func IsProjectScopedWorktreePath(projectID, target string) bool {
	if projectID == "" || !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return false
	}
	container := filepath.Dir(target)
	if filepath.Base(container) != ProjectWorktreesSegment {
		return false
	}
	if filepath.Base(filepath.Dir(container)) != projectID {
		return false
	}
	return ValidateWorktreeContainerPath(container, target) == nil
}

// ValidateWorktreeContainerPath checks a direct child and refuses redirection
// below the container. System/operator ancestors are resolved by provisioning.
func ValidateWorktreeContainerPath(container, target string) error {
	if !filepath.IsAbs(container) || filepath.Clean(container) != container || filepath.Dir(target) != container {
		return errors.New("worktree must be a direct child of an absolute clean container")
	}
	for _, p := range []string{container, target} {
		fi, err := os.Lstat(p)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (!fi.IsDir() || fi.Mode()&os.ModeSymlink != 0) {
			return errors.New("worktree container and tree must be real directories")
		}
	}
	within, err := pathutil.IsWithinPath(container, target)
	if err != nil {
		return err
	}
	if !within {
		return errors.New("worktree escapes its container")
	}
	return nil
}

// MarkWorktreeContainer creates an owner-only repository marker without
// replacing a foreign marker. The caller supplies a validated configured path.
func MarkWorktreeContainer(repoRoot, container string) error {
	if err := ValidateWorktreeContainerPath(container, filepath.Join(container, "probe")); err != nil {
		return err
	}
	if pathutil.ResolveExistingPrefix(container) != container {
		return errors.New("worktree container has a symlinked ancestor")
	}
	if err := safeio.MkdirAllReal(container, 0o700); err != nil {
		return err
	}
	// Reject a link anywhere below the canonical existing ancestor: MkdirAllReal
	// resolves pre-existing links for system compatibility, not authorization.
	if pathutil.ResolveExistingPrefix(container) != container {
		return errors.New("worktree container has a symlinked ancestor")
	}
	marker := filepath.Join(container, WorktreeRepositoryMarker)
	want := pathutil.ResolveExistingPrefix(repoRoot)
	if fi, err := os.Lstat(marker); err == nil && !fi.Mode().IsRegular() {
		return errors.New("worktree repository marker must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := safeio.ReadFile(marker)
	if err == nil {
		if strings.TrimSpace(string(data)) != want {
			return errors.New("worktree container belongs to another repository")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return safeio.WriteFileAtomic(marker, []byte(want+"\n"), 0o600)
}
