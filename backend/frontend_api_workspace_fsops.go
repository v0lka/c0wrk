package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/sp4rk/safeio"
)

// Structural workspace-mutation RPCs (issue #60): create / rename / delete a
// tree entry plus reveal-in-file-manager. Every path-valued argument funnels
// through admitMutationPath — the WriteFile admission contract
// (specs/domains/workspace.md): resolveWorkspacePath admits the root, then
// containment is RE-VERIFIED against that same admitted root, and No Project
// keeps per-session isolation. Reveal additionally bounds its acceptance to
// the session workspace root (worktree included) or the session temp dir
// (specs/contracts/desktop-frontend.md); it is an app-owned UI affordance and
// is never registered as a model-facing tool.

// admitMutationPath applies the shared structural-RPC admission funnel and
// returns the resolved path with the root containment was verified against.
// Extracted verbatim from WriteFile so the mutation quartet cannot drift from
// the write path (#146: re-validation must check the SAME admitted root, not
// SessionWorkspaceRoot).
func (f *FrontendAPI) admitMutationPath(sessionID, path string) (absPath, absRoot string, err error) {
	absPath, absRoot, err = f.resolveWorkspacePath(path)
	if err != nil {
		return "", "", err
	}

	// Defence in depth: re-verify containment against the admitted root.
	if ok, err := config.IsWithinPath(absRoot, absPath); err != nil {
		return "", "", fmt.Errorf("path %q: %w", path, err)
	} else if !ok {
		return "", "", fmt.Errorf("path %q is outside session workspace", path)
	}

	f.activeProjectMu.RLock()
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()
	// No Project adds per-session isolation: a CHAT session mutates only
	// inside its own <sid>/workspace or its own plans//temp/ infra dirs.
	if projectID == project.NoProjectID {
		wsErr := config.ValidateWithinSessionWorkspace(f.agentDir, projectID, sessionID, absPath)
		infraOK := false
		if sessionID != "" {
			ownSession := config.SessionDir(f.agentDir, projectID, sessionID)
			if ownOK, ownErr := config.IsWithinPath(ownSession, absPath); ownErr == nil && ownOK {
				// IsSessionInfraPath takes the PROJECT dir; the own-session
				// check above pins <sid> to the calling session.
				infraOK = config.IsSessionInfraPath(config.ProjectDir(f.agentDir, projectID), absPath)
			}
		}
		if wsErr != nil && !infraOK {
			return "", "", fmt.Errorf("path %q is outside session workspace: %w", path, wsErr)
		}
	}
	return absPath, absRoot, nil
}

// CreateFile creates an empty file at the contained path, refusing an
// existing target instead of truncating it. The refusal is ATOMIC: the empty
// file lands through one exclusive open (O_CREATE|O_EXCL — the kernel's
// check-and-create), so a target a competing writer slips between the Lstat
// fast path and the open is refused with "already exists" and can never have
// its bytes truncated by a write-open's O_TRUNC (the SECURITY.md TOCTOU rule;
// c0wrk workspaces are written concurrently by agent sessions and UI RPCs).
// Parents are built out of real directories only (the WriteFile symlink
// discipline).
func (f *FrontendAPI) CreateFile(sessionID, path string) error {
	absPath, absRoot, err := f.admitMutationPath(sessionID, path)
	if err != nil {
		return err
	}
	// Fast path: a human-readable refusal before any directory work; the
	// authoritative guarantee is the exclusive open below.
	if _, err := os.Lstat(absPath); err == nil {
		return fmt.Errorf("path %q already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect %q: %w", path, err)
	}
	dir := filepath.Dir(absPath)
	if err := safeio.MkdirAllReal(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if err := safeio.CheckRealDirsBelow(absRoot, dir); err != nil {
		return fmt.Errorf("path %q: %w", path, err)
	}
	// Exclusive create: O_EXCL|O_CREATE fails with EEXIST when anything
	// lands at the path inside the race window above — the target is never
	// opened with O_TRUNC. OpenFileNoFollow keeps the final-component
	// symlink discipline uniform with the write path.
	file, err := safeio.OpenFileNoFollow(absPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("path %q already exists", path)
		}
		return fmt.Errorf("failed to create %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to create %q: %w", path, err)
	}
	return nil
}

// CreateDirectory creates the directory and any missing parents at the
// contained path, refusing an existing target.
func (f *FrontendAPI) CreateDirectory(sessionID, path string) error {
	absPath, absRoot, err := f.admitMutationPath(sessionID, path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(absPath); err == nil {
		return fmt.Errorf("path %q already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect %q: %w", path, err)
	}
	// MkdirAllRealWithin creates the missing tail out of real directories and
	// re-verifies (pre + post) that the landed tree stayed inside the
	// admitted root — a swapped-in link cannot redirect the creation.
	return safeio.MkdirAllRealWithin(absRoot, absPath, 0o755)
}

// RenameEntry renames (or moves) a contained entry to a contained destination.
// Both endpoints must resolve to the SAME admitted root — no cross-container
// moves, which also keeps the rename on one filesystem. os.Rename operates on
// the final component without following it, so an admitted in-root symlink is
// moved as the link itself; the parents must be real directories.
//
// The destination-must-not-exist guarantee is ADVISORY, not atomic: rename(2)
// silently replaces an existing target, and no portable no-replace rename
// exists (renameat2 RENAME_NOREPLACE — Linux-only, renamex_np — macOS-only,
// MoveFileEx without MOVEFILE_REPLACE_EXISTING — Windows-only). The Lstat
// refusal shrinks the race window to the microseconds between the check and
// the os.Rename — the safeio shrink-the-window discipline, as for the
// MkdirAllReal + CheckRealDirsBelow pair — but a destination a competing
// writer lands inside that window is still replaced (both entries lost).
// This residual window is an ACCEPTED RISK under the single-user
// concurrent-writer boundary SECURITY.md draws for a workspace; closing it
// fully needs the platform-specific syscalls and is deliberately out of
// scope (specs/domains/workspace.md, "Tree mutations and reveal").
func (f *FrontendAPI) RenameEntry(sessionID, oldPath, newPath string) error {
	oldAbs, oldRoot, err := f.admitMutationPath(sessionID, oldPath)
	if err != nil {
		return fmt.Errorf("rename source: %w", err)
	}
	newAbs, newRoot, err := f.admitMutationPath(sessionID, newPath)
	if err != nil {
		return fmt.Errorf("rename destination: %w", err)
	}
	if oldRoot != newRoot {
		return fmt.Errorf("rename must stay within one workspace (%q vs %q)", oldRoot, newRoot)
	}
	if oldAbs == oldRoot {
		return errors.New("cannot rename the workspace root")
	}
	if oldAbs == newAbs {
		return fmt.Errorf("rename source and destination are the same path %q", oldPath)
	}
	if _, err := os.Lstat(oldAbs); err != nil {
		return fmt.Errorf("failed to inspect %q: %w", oldPath, err)
	}
	if _, err := os.Lstat(newAbs); err == nil {
		return fmt.Errorf("path %q already exists", newPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect %q: %w", newPath, err)
	}
	if err := safeio.CheckRealDirsBelow(oldRoot, filepath.Dir(oldAbs)); err != nil {
		return fmt.Errorf("rename source %q: %w", oldPath, err)
	}
	if _, err := os.Lstat(filepath.Dir(newAbs)); err != nil {
		return fmt.Errorf("rename destination directory %q does not exist: %w", filepath.Dir(newPath), err)
	}
	if err := safeio.CheckRealDirsBelow(newRoot, filepath.Dir(newAbs)); err != nil {
		return fmt.Errorf("rename destination %q: %w", newPath, err)
	}
	if err := os.Rename(oldAbs, newAbs); err != nil {
		return fmt.Errorf("failed to rename %q to %q: %w", oldPath, newPath, err)
	}
	return nil
}

// DeleteEntry permanently removes a contained file or directory tree. The
// admitted root itself is never deletable. os.RemoveAll unlinks symlinks
// without descending into them; a final-component symlink whose target lies
// outside the root is refused at admission (containment resolves symlinks —
// fail-closed, matching the write path).
func (f *FrontendAPI) DeleteEntry(sessionID, path string) error {
	absPath, absRoot, err := f.admitMutationPath(sessionID, path)
	if err != nil {
		return err
	}
	if absPath == absRoot {
		return errors.New("cannot delete the workspace root")
	}
	if err := safeio.CheckRealDirsBelow(absRoot, filepath.Dir(absPath)); err != nil {
		return fmt.Errorf("path %q: %w", path, err)
	}
	if err := os.RemoveAll(absPath); err != nil {
		return fmt.Errorf("failed to delete %q: %w", path, err)
	}
	return nil
}

// RevealTarget names a semantic reveal destination resolved SERVER-SIDE, so
// raw app-internal paths (the session temp dir under the project data dir)
// never cross the wire (specs/contracts/desktop-frontend.md).
const (
	RevealTargetWorkspace = "workspace"
	RevealTargetTemp      = "temp"
)

// RevealOpener spawns the OS file manager for a reveal: revealDir opens
// plainly, selectPath (when non-empty) is additionally highlighted where the
// platform supports selection. Production uses DefaultRevealOpener; tests
// inject a fake (the DefaultBrowserOpener seam pattern).
type RevealOpener func(revealDir, selectPath string) error

// DefaultRevealOpener runs the per-OS reveal matrix: darwin `open -R` reveals
// AND selects (plain `open` only opens), windows `explorer.exe /select,`
// selects (plain `explorer` only opens; its non-zero exit code is a
// documented quirk, so the child is never Run), linux `xdg-open` opens only —
// no selection support. The launcher is started detached and reaped in the
// background: a wedged file manager must not park this synchronous RPC.
func DefaultRevealOpener(revealDir, selectPath string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
		if selectPath != "" {
			args = []string{"-R", selectPath}
		} else {
			args = []string{revealDir}
		}
	case "windows":
		command = "explorer.exe"
		if selectPath != "" {
			args = []string{"/select," + selectPath}
		} else {
			args = []string{revealDir}
		}
	default:
		command, args = "xdg-open", []string{revealDir}
	}
	cmd := exec.CommandContext(context.Background(), command, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening the file manager (%s): %w", command, err)
	}
	// Reap the launcher child: Start without Wait leaks a zombie per reveal.
	// The Wait goroutine detaches on purpose — the launcher must not be
	// killed when the RPC returns, and its error carries no actionable signal.
	go func() { _ = cmd.Wait() }()
	return nil
}

// revealEntry validates entryPath (absolute, existing) within root and
// returns the directory to open plus the entry to select. An empty entryPath
// means the root itself: its parent lies outside the workspace, so the root
// is revealed without selection.
func revealEntry(root, entryPath string) (revealDir, selectPath string, err error) {
	if entryPath == "" {
		return root, "", nil
	}
	absPath, err := filepath.Abs(entryPath)
	if err != nil {
		return "", "", fmt.Errorf("invalid path %q: %w", entryPath, err)
	}
	if ok, err := config.IsWithinPath(root, absPath); err != nil {
		return "", "", fmt.Errorf("path %q: %w", entryPath, err)
	} else if !ok {
		return "", "", fmt.Errorf("path %q is outside session workspace", entryPath)
	}
	if _, err := os.Lstat(absPath); err != nil {
		return "", "", fmt.Errorf("failed to inspect %q: %w", entryPath, err)
	}
	if absPath == root {
		return root, "", nil
	}
	return filepath.Dir(absPath), absPath, nil
}

// sessionProjectIDFor resolves which project a session belongs to: the store
// row first, a RESIDENT session's in-memory ProjectID when the row is missing
// (sessions never move). ok=false means neither source answered; callers
// decide their own fallback. Same membership key GetSessionWorkspace keys on.
func (f *FrontendAPI) sessionProjectIDFor(sessionID string) (projectID string, ok bool) {
	if f.app == nil || f.store == nil {
		return "", false
	}
	mgr := f.app.Manager()
	if mgr == nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(f.ctx(), terminalPathLookupTimeout)
	defer cancel()
	if info, err := f.store.LoadSession(ctx, sessionID); err == nil && info != nil {
		return info.ProjectID, true
	} else if pid, live := mgr.SessionProjectID(sessionID); live {
		return pid, true
	}
	return "", false
}

// RevealInFileManager opens the OS file manager at a sanctioned location:
// target "workspace" resolves through GetSessionWorkspace (the session-first
// root rule — managed worktree for bound sessions, per-session No Project
// workspace, checkout otherwise) with entryPath contained inside it, and
// target "temp" resolves the session temp dir under the project data dir,
// creating it lazily so opening works. Only these two targets are accepted;
// everything else is rejected (specs/contracts/desktop-frontend.md).
func (f *FrontendAPI) RevealInFileManager(sessionID, target, entryPath string) error {
	f.seedAcquire()
	switch target {
	case RevealTargetWorkspace:
		root, err := f.GetSessionWorkspace(sessionID)
		if err != nil {
			return err
		}
		revealDir, selectPath, err := revealEntry(root, entryPath)
		if err != nil {
			return err
		}
		return f.revealLauncher()(revealDir, selectPath)
	case RevealTargetTemp:
		if sessionID == "" {
			return errors.New("a session id is required for the temp target")
		}
		f.activeProjectMu.RLock()
		projectID := f.activeProjectID
		f.activeProjectMu.RUnlock()
		if projectID == "" {
			return errors.New("no active project")
		}
		// Strict membership: a foreign session's temp dir belongs to another
		// project's data tree and is never revealed. An unanswered lookup
		// (no store/manager wired) falls through — the caller is the UI,
		// which only ever holds an active-project session.
		if sessProjectID, ok := f.sessionProjectIDFor(sessionID); ok && sessProjectID != projectID {
			return fmt.Errorf("session %q does not belong to the active project", sessionID)
		}
		tempDir := config.SessionTempDir(f.agentDir, projectID, sessionID)
		if err := safeio.MkdirAllReal(tempDir, 0o755); err != nil {
			return fmt.Errorf("failed to create session temp dir: %w", err)
		}
		revealDir, selectPath, err := revealEntry(tempDir, entryPath)
		if err != nil {
			return err
		}
		return f.revealLauncher()(revealDir, selectPath)
	default:
		return fmt.Errorf("unknown reveal target %q", target)
	}
}

// revealLauncher returns the injected reveal opener, defaulting to the real
// OS launcher when no test seam is installed.
func (f *FrontendAPI) revealLauncher() RevealOpener {
	if f.revealOpener != nil {
		return f.revealOpener
	}
	return DefaultRevealOpener
}
