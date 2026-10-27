package backend

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/epilande/go-devicons"
	"github.com/v0lka/sp4rk/ignore"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/sp4rk/safeio"
)

// lineAnchorRe matches the trailing line-anchor fragment the viewer appends
// to file paths (specs/decisions/095-chat-file-reference-path-semantics.md);
// only numeric #L<n>[-L<m>] forms match, so "#v2"-style anchors stay verbatim.
var lineAnchorRe = regexp.MustCompile(`#L?\d+(?:-L?\d+)?$`)

// stripLineAnchor removes a trailing line-anchor fragment; every read-path
// resolver applies it first so the viewer can pass anchored paths verbatim.
func stripLineAnchor(p string) string {
	return lineAnchorRe.ReplaceAllString(p, "")
}

// resolveWorkspacePath validates that filePath is within the active project
// workspace and returns the resolved path and root
// (specs/contracts/desktop-frontend.md); session-infra dirs (plans/, temp/)
// are admitted — for those absRoot is the project DATA dir, not a git repo.
func (f *FrontendAPI) resolveWorkspacePath(filePath string) (absPath, absRoot string, err error) {
	f.seedAcquire()
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return "", "", errors.New("no active project")
	}

	absPath, err = filepath.Abs(stripLineAnchor(filePath))
	if err != nil {
		return "", "", fmt.Errorf("invalid path: %w", err)
	}

	// No Project: WorkspacePath is the project dir itself; the <sid>/workspace
	// structural rule is enforced only by ListDirectory, not here.
	absRoot, err = filepath.Abs(projectPath)
	if err != nil {
		return "", "", fmt.Errorf("invalid workspace path: %w", err)
	}
	ok, err := config.IsWithinPath(absRoot, absPath)
	if err != nil || !ok {
		if projectID != project.NoProjectID {
			if tree, managed := config.ManagedWorktreeRootForPath(absRoot, absPath); managed {
				return absPath, tree, nil
			}
		}
		// Session-infra dirs (plans/, temp/) live under the project data dir.
		projectDir := config.ProjectDir(f.agentDir, projectID)
		if !config.IsSessionInfraPath(projectDir, absPath) {
			return "", "", errors.New("path outside project workspace")
		}
		// Use the project data directory as the root for session infra paths.
		absRoot = config.ProjectDir(f.agentDir, projectID)
	}
	return absPath, absRoot, nil
}

// resolveReadablePath resolves a path for the read-path RPCs (ReadFile,
// GetFileIcon, ReadImageAsDataURL, GetFileDiff): NOT workspace-contained, no
// active project required; dialect classification and un-rooting semantics
// live in specs/decisions/095-chat-file-reference-path-semantics.md.
func (f *FrontendAPI) resolveReadablePath(filePath string) (string, error) {
	cleaned := stripLineAnchor(filePath)
	if len(cleaned) > 1 && cleaned[0] == '/' && cleaned[1] != '/' &&
		core.IsAbsolutePath(cleaned[1:]) {
		// Legacy rooted Windows spelling "/C:/x/f.md" → "C:/x/f.md".
		return cleaned[1:], nil
	}
	if core.IsAbsolutePath(cleaned) {
		return cleaned, nil
	}
	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	return absPath, nil
}

// resolveFileIcon returns the Nerd Font icon for a file, its hex color snapped
// to the nearest theme palette color.
func resolveFileIcon(info os.FileInfo) (icon, color string) {
	style := devicons.IconForInfo(info)
	return style.Icon, snapToTheme(style.Color)
}

// GetSessionWorkspace returns the workspace root the UI follows for a
// session: the session's own workspace when it belongs to the active project
// (in No Project always, by per-session isolation), else the project
// workspace (specs/contracts/desktop-frontend.md,
// specs/decisions/099-research-root-follows-active-session.md).
func (f *FrontendAPI) GetSessionWorkspace(sessionID string) (string, error) {
	f.seedAcquire()
	f.activeProjectMu.RLock()
	activeProject := f.activeProjectPath
	activeProjectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if sessionID != "" && f.app != nil {
		if mgr := f.app.Manager(); mgr != nil {
			// WorkspacePathFor, never GetSession: a pure path lookup must not
			// rebuild a store-only session on this hot path; its contract
			// requires the caller to bound the session-row read. Membership
			// below keys on the store row's project ID, falling back to a
			// RESIDENT session's in-memory ProjectID (sessions never move).
			ctx, cancel := context.WithTimeout(f.ctx(), terminalPathLookupTimeout)
			ws, hasWS := mgr.WorkspacePathFor(ctx, sessionID)
			var sessProjectID string
			if hasWS && ws != "" && f.store != nil {
				if info, err := f.store.LoadSession(ctx, sessionID); err == nil && info != nil {
					sessProjectID = info.ProjectID
				} else if pid, live := mgr.SessionProjectID(sessionID); live {
					// Store row missing (best-effort SaveSession at creation
					// never landed) but the session is RESIDENT: its in-memory
					// ProjectID is authoritative, otherwise membership would
					// degrade to the project-checkout fallback for a live
					// session of the active project.
					sessProjectID = pid
				}
			}
			cancel()
			if hasWS && ws != "" {
				// Membership keys on project ID, never path comparison: a
				// managed-worktree session of the active project must still
				// resolve to its own tree, and a real project's session must
				// never leak into CHAT mode (per-session isolation).
				var belongsToActive bool
				if activeProjectID == project.NoProjectID {
					belongsToActive = sessProjectID == project.NoProjectID
				} else {
					belongsToActive = sessProjectID == activeProjectID
				}
				if belongsToActive || activeProject == "" {
					return ws, nil
				}
			}
		}
	}

	if activeProject == "" {
		return "", errors.New("no active project")
	}

	// No Project: derive the session's deterministic workspace from its ID —
	// the project-level fallback would expose all sessions' scaffolding.
	if activeProjectID == project.NoProjectID && sessionID != "" {
		wsPath := config.NoProjectSessionWorkspace(f.agentDir, sessionID)
		if absPath, absErr := filepath.Abs(wsPath); absErr == nil {
			return absPath, nil
		}
		return wsPath, nil
	}

	return activeProject, nil
}

// GetFileIcon resolves via resolveReadablePath — the uncontained read path.
func (f *FrontendAPI) GetFileIcon(filePath string) (FileIconResponse, error) {
	absPath, err := f.resolveReadablePath(filePath)
	if err != nil {
		return FileIconResponse{}, err
	}
	style := devicons.IconForPath(absPath)
	return FileIconResponse{Icon: style.Icon, IconColor: snapToTheme(style.Color)}, nil
}

// GetGitStatus returns the git status for the Git-panel focus target — the
// focused worktree, or the checkout by default (focus model:
// specs/domains/session-worktrees.md). No Project returns an empty map.
func (f *FrontendAPI) GetGitStatus(dirPath string) (map[string]GitStatusEntry, error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return nil, errors.New("no active project")
	}

	// No Project: git operations are not available.
	if projectID == project.NoProjectID {
		return map[string]GitStatusEntry{}, nil
	}

	absDir, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	absRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace path: %w", err)
	}
	focusRoot := f.resolveGitFocusRoot(absRoot)

	ok, err := config.IsWithinPath(absRoot, absDir)
	if err != nil || !ok {
		// An external linked-tree focus lies outside the project workspace;
		// containment against the focus root keeps it reachable.
		absFocus, focusErr := filepath.Abs(focusRoot)
		focusOK := focusErr == nil
		if focusOK {
			if focusWithin, fwErr := config.IsWithinPath(absFocus, absDir); fwErr != nil || !focusWithin {
				focusOK = false
			}
		}
		_, managed := config.ManagedWorktreeRootForPath(absRoot, absDir)
		if !focusOK && !managed {
			return nil, errors.New("path outside project workspace")
		}
	}

	return f.cachedGitStatus(focusRoot)
}

// ReadFile returns any agent-surfaced file (uncontained, via
// resolveReadablePath). safeio refuses non-regular files — a leftover FIFO
// would hang this synchronous RPC forever — and the maxViewerFileSize cap
// bounds the one-JSON-string IPC payload.
func (f *FrontendAPI) ReadFile(filePath string) (string, error) {
	absPath, err := f.resolveReadablePath(filePath)
	if err != nil {
		return "", err
	}

	content, err := readCappedFile(absPath, maxViewerFileSize)
	if err != nil {
		return "", err
	}

	return string(content), nil
}

// readCappedFile rejects oversized files BEFORE reading (no whole-file
// buffer) and still reads through safeio (non-regular refusal).
func readCappedFile(absPath string, maxSize int64) ([]byte, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}
	if info.Size() > maxSize {
		return nil, fmt.Errorf("file too large (%d bytes, max %d bytes) — open it in an external editor instead", info.Size(), maxSize)
	}
	data, err := safeio.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	// The file may have grown between the stat and the read (an appended
	// log): enforce the cap against what was actually buffered.
	if int64(len(data)) > maxSize {
		return nil, fmt.Errorf("file too large (%d bytes, max %d bytes) — open it in an external editor instead", len(data), maxSize)
	}
	return data, nil
}

// maxDataURLSize caps data-URL payloads at 8 MiB — one base64 string over
// the IPC channel must never be unbounded.
const maxDataURLSize = 8 << 20

// maxViewerFileSize caps the ReadFile RPC at the same 8 MiB — same
// one-JSON-string IPC constraint as the data-URL path.
const maxViewerFileSize = maxDataURLSize

// imageMimeByExt pins image MIMEs the OS registry may not know — the
// octet-stream fallback would make the webview refuse to paint them.
var imageMimeByExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".svg":  "image/svg+xml",
	".avif": "image/avif",
}

// mimeByExtension resolves image extensions from imageMimeByExt first, then
// the stdlib registry, else application/octet-stream.
func mimeByExtension(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if mt, ok := imageMimeByExt[ext]; ok {
		return mt
	}
	if mt := mime.TypeByExtension(ext); mt != "" {
		return mt
	}
	return "application/octet-stream"
}

// readFileAsDataURL returns absPath as a data URL (RFC 2397), rejecting
// files larger than maxSize before reading.
func readFileAsDataURL(absPath string, maxSize int64) (string, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("failed to stat file: %w", err)
	}
	if info.Size() > maxSize {
		return "", fmt.Errorf("file too large (%d bytes, max %d)", info.Size(), maxSize)
	}

	data, err := safeio.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	return "data:" + mimeByExtension(absPath) + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// ReadFileAsDataURL embeds a local file as a data URL for the markdown
// renderer. Unlike ReadFile it is workspace-CONTAINED (resolveWorkspacePath):
// it fires during automatic rendering, so a document must not pull arbitrary
// local files into the webview DOM (specs/contracts/desktop-frontend.md).
func (f *FrontendAPI) ReadFileAsDataURL(filePath string) (string, error) {
	absPath, _, err := f.resolveWorkspacePath(filePath)
	if err != nil {
		return "", err
	}

	return readFileAsDataURL(absPath, maxDataURLSize)
}

// ReadImageAsDataURL is the image tab's display counterpart with the OPPOSITE
// containment: resolveReadablePath — uncontained, like ReadFile. Safe because
// it fires only on an explicit user open, never during auto-render. Same
// maxDataURLSize cap.
func (f *FrontendAPI) ReadImageAsDataURL(filePath string) (string, error) {
	absPath, err := f.resolveReadablePath(filePath)
	if err != nil {
		return "", err
	}
	return readFileAsDataURL(absPath, maxDataURLSize)
}

// GetFileDiff returns the unified diff of uncommitted changes for one file
// in the repository that owns it (specs/domains/session-worktrees.md).
// Uncontained like every read path; anything without a git baseline —
// outside the project, non-git, No Project — returns ("", nil) so no diff
// panel is rendered.
func (f *FrontendAPI) GetFileDiff(filePath string) (string, error) {
	// No Project: checked before path resolution to avoid misleading errors.
	f.activeProjectMu.RLock()
	isNoProject := f.activeProjectID == project.NoProjectID
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()
	if isNoProject {
		return "", nil
	}
	// Transient startup / closed-project state (distinct from No Project):
	// guard explicitly — filepath.Abs("") would resolve to the app CWD and
	// diff against an unrelated repo there.
	if projectPath == "" {
		return "", nil
	}

	absPath, err := f.resolveReadablePath(filePath)
	if err != nil {
		return "", err
	}

	absRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return "", fmt.Errorf("invalid workspace path: %w", err)
	}

	// Outside the project (and not a managed worktree): no git baseline.
	ok, err := config.IsWithinPath(absRoot, absPath)
	if err != nil {
		return "", fmt.Errorf("failed to check path containment: %w", err)
	}
	if !ok {
		if tree, managed := config.ManagedWorktreeRootForPath(absRoot, absPath); managed {
			absRoot = tree
		} else {
			return "", nil
		}
	}

	// Diff in the repository that OWNS the file, not the checkout: a
	// managed-worktree file belongs to its own repository and the checkout's
	// info/exclude hides .worktrees. ResolveWorkTreeRoot finds the nearest
	// .git, so the viewer's diff follows the session workspace.
	fileRoot := workspace.ResolveWorkTreeRoot(absPath)
	if fileRoot == "" {
		fileRoot = absRoot
	}
	if !f.isGitRepo(fileRoot) {
		// Non-git paths have no baseline; no synthetic diff panel.
		return "", nil
	}

	relPath, err := filepath.Rel(fileRoot, absPath)
	if err != nil {
		return "", fmt.Errorf("failed to compute relative path: %w", err)
	}
	// Bounded like every local git spawn: f.ctx() has no deadline, so an
	// unbounded git on a hung filesystem would park this RPC forever.
	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()
	return workspace.GetFileDiffInRepo(ctx, fileRoot, relPath)
}

// ListDirectory lists dirPath (immediate children, or a recursive flat list;
// .git excluded), flags .gitignore/.aiignore hits for subdued rendering, and
// attaches icons (specs/domains/workspace.md). A git failure computing the
// ignore set degrades to flag-less entries — only containment violations and
// an unreadable directory are fatal, so a broken repo never blanks the tree.
func (f *FrontendAPI) ListDirectory(dirPath string, recursive bool) ([]FileNode, error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return nil, errors.New("no active project")
	}

	absDir, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	absRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace path: %w", err)
	}
	ok, err := config.IsWithinPath(absRoot, absDir)
	if err != nil || !ok {
		if tree, managed := config.ManagedWorktreeRootForPath(absRoot, absDir); projectID != project.NoProjectID && managed {
			absRoot = tree
		} else {
			return nil, errors.New("path outside project workspace")
		}
	}

	// No Project: enforce that session paths follow the
	// <sessionID>/workspace/... pattern. This prevents access to
	// non-workspace subdirectories (logs/, dumps/, temp/, plans/)
	// and to other sessions' directories.
	if projectID == project.NoProjectID {
		if err := config.ValidateNoProjectSessionPath(absRoot, absDir); err != nil {
			return nil, err
		}
	}

	var ignoredPaths map[string]bool
	isRepo := f.isGitRepo(absRoot)
	if isRepo {
		ignored, gitErr := f.cachedGitIgnoredPaths(absRoot)
		if gitErr != nil {
			// Degrade, don't fail: the flags are cosmetic, and the frontend
			// caches a rejected root listing as empty with no retry. No
			// security property is relaxed by dropping them.
			f.log().Warn("failed to list git-ignored paths; listing without ignore flags",
				"dir", absDir, "error", gitErr)
			ignoredPaths = nil
		} else {
			ignoredPaths = ignored
		}
	}

	opts := []workspace.ListDirOption{
		workspace.WithIconResolver(resolveFileIcon),
		workspace.WithLogger(f.log()),
	}

	var nodes []FileNode
	if !recursive {
		nodes, err = workspace.ListDirFlat(absDir, ignoredPaths, opts...)
	} else {
		nodes, err = workspace.ListDirRecursive(absDir, ignoredPaths, opts...)
	}
	if err != nil {
		return nil, err
	}

	// ignore.Resolver layering: in a git repo only .aiignore rules are added
	// (git already honoured .gitignore with negation; OR-merging the full
	// verdict would let the resolver's negation-less matching override a git
	// "un-ignore" !pattern). Non-git: the resolver is the sole authority for
	// both files, like the indexer and search tools. Resolver construction
	// failure is non-fatal.
	if r, rErr := ignore.NewResolver(absRoot); rErr == nil {
		for i := range nodes {
			if nodes[i].GitIgnored {
				continue
			}
			ignored := r.IgnoredByAIIgnore(nodes[i].Path, nodes[i].IsDir)
			if !isRepo {
				// Non-git: resolver is the sole ignore authority.
				ignored = r.Ignored(nodes[i].Path, nodes[i].IsDir)
			}
			if ignored {
				nodes[i].GitIgnored = true
			}
		}
	}

	return nodes, nil
}

// WatchDirectory re-scopes the watcher to the requested workspace root (the
// active session's workspace, checkout as fallback; tracked roots follow —
// specs/domains/session-worktrees.md). A re-scope applies only while the
// root still matches the active-session authority; late requests are skipped.
func (f *FrontendAPI) WatchDirectory(dirPath string) error {
	f.seedAcquire()
	if f.isNoProject() {
		// dirPath is renderer-supplied: gate it exactly like ListDirectory
		// (containment + the <sessionID>/workspace structural rule), or the
		// re-scope would re-root the watcher at any path.
		f.activeProjectMu.RLock()
		projectPath := f.activeProjectPath
		f.activeProjectMu.RUnlock()
		if projectPath == "" {
			return errors.New("no active project")
		}
		absDir, err := filepath.Abs(dirPath)
		if err != nil {
			return fmt.Errorf("invalid path: %w", err)
		}
		absRoot, err := filepath.Abs(projectPath)
		if err != nil {
			return fmt.Errorf("invalid workspace path: %w", err)
		}
		if err := config.ValidateNoProjectSessionPath(absRoot, absDir); err != nil {
			return err
		}
		return f.reScopeNoProjectWatcher(absDir)
	}
	// CODE mode: renderer-supplied path must belong to this project (active
	// workspace or a managed worktree) before influencing watcher state.
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()
	if projectPath == "" {
		return errors.New("no active project")
	}
	absDir, err := filepath.Abs(dirPath)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	absRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return fmt.Errorf("invalid workspace path: %w", err)
	}
	within, withinErr := config.IsWithinPath(absRoot, absDir)
	managedRoot, managed := config.ManagedWorktreeRootForPath(absRoot, absDir)
	if (withinErr != nil || !within) && !managed {
		return errors.New("path outside project workspace")
	}

	// Only a workspace root (checkout or managed tree) may re-root the
	// watcher; a deeper path keeps the additive single-directory watch. An
	// unchanged root keeps the live watcher (FileTreePanel re-issues on
	// every mount).
	scopeRoot := ""
	switch {
	case absDir == absRoot:
		scopeRoot = absDir
	case managed:
		scopeRoot = managedRoot
	}
	f.watcherMu.Lock()
	var watchErr error
	rescoped := false
	switch {
	case scopeRoot != "":
		if f.watcher == nil || f.watcher.Root() != scopeRoot {
			// Freshness re-check: re-scope calls race, and a LATE request for
			// an older tree must not re-root the watcher (or the tracked
			// roots). Atomic under watcherMu; the authority lookup never takes
			// watcherMu. A degraded lookup skips (the frontend re-issues) and
			// a skipped request moves nothing.
			if f.scopeRootMatchesActiveSessionLocked(scopeRoot, absRoot) {
				watchErr = f.reScopeCodeWatcherLocked(scopeRoot, config.ProjectResearchPath(absRoot))
				rescoped = watchErr == nil
			} else {
				f.log().Debug("skipping stale watcher re-scope", "requested", scopeRoot)
			}
		} else {
			// Same-root re-issue: idempotent; heals a roots/watcher drift
			// from an earlier skipped request.
			rescoped = true
		}
	case f.watcher == nil:
		watchErr = errors.New("no active file watcher")
	default:
		// Admit before watching: a managed tree lives outside the checkout
		// the watcher is rooted at (a no-op for in-checkout paths).
		if allowErr := f.watcher.AllowRoot(absDir); allowErr != nil {
			f.log().Debug("failed to admit session workspace root", "dir", absDir, "error", allowErr)
		}
		watchErr = f.watcher.WatchDir(absDir)
	}
	f.watcherMu.Unlock()
	if watchErr != nil {
		return watchErr
	}

	// Only an APPLIED re-scope moves the tracked emit roots: a skipped one
	// must leave them on the tree the watcher is rooted at, and the project
	// guard keeps a stale RPC from poisoning a project that switched.
	if scopeRoot != "" && rescoped {
		f.activeProjectMu.Lock()
		if f.activeProjectPath == absRoot {
			sessionRoot := config.ProjectResearchPath(scopeRoot)
			f.activeResearchRoot = sessionRoot
			f.activePapersRoot = config.PaperLibraryPathIn(sessionRoot)
			f.activeComparisonsRoot = config.ComparisonsPathIn(sessionRoot)
		}
		f.activeProjectMu.Unlock()
	}
	return nil
}

// scopeRootMatchesActiveSessionLocked reports whether scopeRoot is still what
// the active-session authority resolves to: the session workspace, else the
// checkout. Caller holds watcherMu; the authority reads never take watcherMu,
// so the nested activeProjectMu read cannot deadlock.
func (f *FrontendAPI) scopeRootMatchesActiveSessionLocked(scopeRoot, checkout string) bool {
	f.activeProjectMu.RLock()
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()
	if ws, ok := f.activeSessionWorkspace(projectID); ok && ws != "" {
		return ws == scopeRoot
	}
	return scopeRoot == checkout
}

// UnwatchDirectory is a no-op: the watcher is fully backend-managed (scoped
// by switchProjectSetupWatcher / WatchDirectory). FileTreePanel calls this
// from unmount cleanup — every tab switch, StrictMode remount — and an
// RPC-triggered unwatch there killed watching until remount.
func (f *FrontendAPI) UnwatchDirectory(_ string) error {
	return nil
}

// WriteFile: resolveWorkspacePath is the containment decision; the
// re-validation below re-checks the SAME admitted root — never
// SessionWorkspaceRoot, which disagrees by construction (plans/ and temp/
// are siblings of the workspace; external projects' workspaces are their
// own checkouts) and used to swallow every admitted write.
func (f *FrontendAPI) WriteFile(sessionID, path, content string) error {
	f.seedAcquire()
	absPath, absRoot, err := f.resolveWorkspacePath(path)
	if err != nil {
		return err
	}

	// Defence in depth: re-verify containment against the admitted root.
	if ok, err := config.IsWithinPath(absRoot, absPath); err != nil {
		return fmt.Errorf("path %q: %w", path, err)
	} else if !ok {
		return fmt.Errorf("path %q is outside session workspace", path)
	}

	f.activeProjectMu.RLock()
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()
	// No Project adds per-session isolation: a CHAT session writes only
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
			return fmt.Errorf("path %q is outside session workspace: %w", path, wsErr)
		}
	}

	dir := filepath.Dir(absPath)
	// Symlink safety: MkdirAllReal refuses a dangling or swapped-in link but
	// resolves pre-existing operator/system ancestors (/var → /private/var),
	// so a link shipped INSIDE the workspace is caught here: every component
	// below the admitted root must be real (CheckRealDirsBelow), enforced
	// before the write. The no-follow open protects the final component.
	if err := safeio.MkdirAllReal(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if err := safeio.CheckRealDirsBelow(absRoot, dir); err != nil {
		return fmt.Errorf("path %q: %w", path, err)
	}
	return safeio.WriteFile(absPath, []byte(content), 0o644)
}
