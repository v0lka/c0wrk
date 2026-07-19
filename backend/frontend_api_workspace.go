package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/epilande/go-devicons"
	"github.com/v0lka/sp4rk/ignore"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/workspace"
)

// lineAnchorRe matches a trailing line-anchor fragment appended to a file
// path by the file viewer (e.g. ".../x.go#L20-L36" or ".../x.go#42"). The
// viewer links the chat to specific lines; the backend must strip the
// fragment before resolving the path so os.ReadFile sees the real file.
// Forms supported: "#L<n>", "#L<n>-L<m>", "#L<n>-<m>", "#<n>", "#<n>-<n>".
// Plain fragment identifiers like "#v2" or "#123" are NOT matched — only
// fragments that look like a line range.
var lineAnchorRe = regexp.MustCompile(`#L?\d+(?:-L?\d+)?$`)

// stripLineAnchor removes a trailing "#L<n>" / "#L<n>-L<m>" line anchor
// fragment from filePath. It is applied at the top of every read-path
// resolver as a defence-in-depth safety net so the viewer can pass
// line-anchored paths verbatim.
func stripLineAnchor(p string) string {
	return lineAnchorRe.ReplaceAllString(p, "")
}

// resolveWorkspacePath validates that filePath is within the active project
// workspace and returns the resolved absolute path and workspace root.
// For No Project (CHAT mode), WorkspacePath is the project directory itself
// (~/.c0wrk/projects/__no_project__/); per-session workspaces live under
// <sid>/workspace/.
//
// Additionally allows paths within session infrastructure directories
// (plans/, temp/) which live under the project directory but outside the
// workspace — these are needed for plan review and future features.
//
// IMPORTANT: for session-infra paths the returned absRoot is
// config.ProjectDir(agentDir, projectID), NOT the project workspace.
// Callers that compute relative paths from absRoot (e.g. GetFileDiff)
// must account for this — the project data dir is not a git repo and
// git-based diff operations will fall back to the no-repo variant.
func (f *FrontendAPI) resolveWorkspacePath(filePath string) (absPath, absRoot string, err error) {
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

	// For No Project, WorkspacePath points to the project directory itself
	// (~/.c0wrk/projects/__no_project__/) — no need for filepath.Dir.
	//
	// NOTE: resolveWorkspacePath does NOT enforce the structural
	// <sid>/workspace constraint — that lives in ListDirectory,
	// the user-facing entry point. ReadFile, GetFileIcon, and GetFileDiff
	// receive paths returned by ListDirectory, so the trust boundary is
	// maintained.
	absRoot, err = filepath.Abs(projectPath)
	if err != nil {
		return "", "", fmt.Errorf("invalid workspace path: %w", err)
	}
	ok, err := config.IsWithinPath(absRoot, absPath)
	if err != nil || !ok {
		// Path is outside the project workspace — check if it falls within
		// session infrastructure directories (plans/, temp/) under the
		// project's data directory (~/.c0wrk/projects/<projectID>/).
		projectDir := config.ProjectDir(f.agentDir, projectID)
		if !config.IsSessionInfraPath(projectDir, absPath) {
			return "", "", errors.New("path outside project workspace")
		}
		// Use the project data directory as the root for session infra paths.
		absRoot = config.ProjectDir(f.agentDir, projectID)
	}
	return absPath, absRoot, nil
}

// resolveReadablePath resolves a file path for the read-path RPCs (ReadFile,
// GetFileIcon, GetFileDiff). Unlike resolveWorkspacePath it does NOT enforce
// workspace containment and does NOT require an active project — the file
// viewer must be able to display any file path surfaced by the agent (e.g.
// files in the sp4rk SDK, system files referenced in chat, paths from
// external tools). Only the line-anchor fragment is stripped (defence in
// depth) and the path is made absolute. Containment for the destructive /
// trust-boundary RPCs (WriteFile, ListDirectory) remains enforced by
// resolveWorkspacePath.
func (f *FrontendAPI) resolveReadablePath(filePath string) (string, error) {
	absPath, err := filepath.Abs(stripLineAnchor(filePath))
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	return absPath, nil
}

// resolveFileIcon returns the Nerd Font icon and hex color for a file or directory.
// The color is snapped to the nearest theme palette color.
func resolveFileIcon(info os.FileInfo) (icon, color string) {
	style := devicons.IconForInfo(info)
	return style.Icon, snapToTheme(style.Color)
}

// GetSessionWorkspace returns the workspace directory path for a given session.
// When the session is known to the session manager, its specific WorkspacePath
// is returned if it belongs to the active project — this guards against
// returning a stale workspace from a session that belongs to a different
// project the user switched away from.
// For No Project (CHAT mode), each session has its own isolated workspace
// (~/.c0wrk/projects/__no_project__/<sid>/workspace/) which differs
// from the project-level workspace path. In this case the session workspace
// is always preferred.
// Falls back to the active project workspace path if the session is not yet
// registered, the manager is unavailable, or the session belongs to a
// different project.
func (f *FrontendAPI) GetSessionWorkspace(sessionID string) (string, error) {
	f.activeProjectMu.RLock()
	activeProject := f.activeProjectPath
	activeProjectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if sessionID != "" && f.app != nil {
		if mgr := f.app.Manager(); mgr != nil {
			if wsPath, ok := mgr.GetSessionWorkspacePath(sessionID); ok && wsPath != "" {
				// Return the session workspace if it belongs to the active project.
				// For No Project, session and project workspaces differ by design
				// (per-session isolation), so match by project ID instead.
				if wsPath == activeProject || activeProject == "" || activeProjectID == project.NoProjectID {
					return wsPath, nil
				}
			}
		}
	}

	if activeProject == "" {
		return "", errors.New("no active project")
	}

	// For No Project, per-session workspaces are deterministic:
	// __no_project__/<sessionID>/workspace. When the session is not yet
	// in memory (e.g. after a project switch where the session was not
	// created by applySavedProjectSwitchState), derive the path from the
	// session ID instead of falling back to the project-level directory
	// which would expose all sessions' scaffolding.
	if activeProjectID == project.NoProjectID && sessionID != "" {
		wsPath := config.NoProjectSessionWorkspace(f.agentDir, sessionID)
		if absPath, absErr := filepath.Abs(wsPath); absErr == nil {
			return absPath, nil
		}
		return wsPath, nil
	}

	return activeProject, nil
}

// GetFileIcon returns the Nerd Font icon and hex color for a file path. The
// path is not constrained to the active project workspace — the viewer may
// request an icon for any file path surfaced by the agent.
func (f *FrontendAPI) GetFileIcon(filePath string) (FileIconResponse, error) {
	absPath, err := f.resolveReadablePath(filePath)
	if err != nil {
		return FileIconResponse{}, err
	}
	style := devicons.IconForPath(absPath)
	return FileIconResponse{Icon: style.Icon, IconColor: snapToTheme(style.Color)}, nil
}

// GetGitStatus returns a map of absolute file paths to their git status for the
// active project. Delegates to core/workspace.GitStatus after path validation.
// Returns an empty map for No Project (no git operations).
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
	ok, err := config.IsWithinPath(absRoot, absDir)
	if err != nil || !ok {
		return nil, errors.New("path outside project workspace")
	}

	return workspace.GitStatus(f.ctx(), absRoot)
}

// ReadFile returns the content of a file. The path is not constrained to the
// active project workspace — the viewer may surface any file path surfaced by
// the agent (e.g. SDK files, system files referenced in chat). Only the
// line-anchor fragment is stripped and the path is made absolute.
func (f *FrontendAPI) ReadFile(filePath string) (string, error) {
	absPath, err := f.resolveReadablePath(filePath)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	return string(content), nil
}

// GetFileDiff returns the unified diff of uncommitted changes for a single
// file. Uses the cached isGitRepo check to avoid redundant git rev-parse
// calls, then delegates to GetFileDiffInRepo for git repositories.
//
// The read path is not constrained to the workspace — the viewer may surface
// any file path surfaced by the agent. A diff requires a git baseline, so for
// files outside the active project root OR outside a git repository the RPC
// returns ("", nil) (no error, no baseline): the hunk-staging panel and
// synthetic diff view are not rendered. Returns ("", nil) early for No Project
// (no git operations) as well.
func (f *FrontendAPI) GetFileDiff(filePath string) (string, error) {
	// No Project: git diff is not available. Check before any path resolution
	// to avoid misleading path-resolution errors.
	f.activeProjectMu.RLock()
	isNoProject := f.activeProjectID == project.NoProjectID
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()
	if isNoProject {
		return "", nil
	}
	// No active project loaded at all (transient startup / closed-project
	// state, distinct from No Project): there is no baseline to diff against.
	// Guard explicitly because resolveReadablePath is path-agnostic and
	// filepath.Abs("") would otherwise resolve to the app CWD, risking a
	// diff against an unrelated git repo at the CWD.
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

	// Files outside the active project root have no git baseline to diff
	// against. Return ("", nil) so the frontend does not render a diff panel.
	ok, err := config.IsWithinPath(absRoot, absPath)
	if err != nil {
		return "", fmt.Errorf("failed to check path containment: %w", err)
	}
	if !ok {
		return "", nil
	}

	if !f.isGitRepo(absRoot) {
		// Non-git paths (non-git workspaces, session-infra directories) have no
		// git baseline to diff against. Return an empty string so the frontend
		// does not render a synthetic diff or the hunk-staging panel.
		return "", nil
	}

	relPath, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", fmt.Errorf("failed to compute relative path: %w", err)
	}
	return workspace.GetFileDiffInRepo(f.ctx(), absRoot, relPath)
}

// ListDirectory returns the children of a directory. When recursive is false,
// only the immediate children are listed, sorted directories first then
// alphabetically. When recursive is true, a flat list of all files and
// directories found recursively under dirPath is returned. The .git directory
// and its contents are excluded. Files and directories ignored by .gitignore
// are included but flagged with GitIgnored=true so the frontend can render
// them with a subdued color.
//
// Delegates to core/workspace.ListDirFlat / ListDirRecursive and attaches icons.
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

	// For No Project, WorkspacePath is the project dir itself
	// (~/.c0wrk/projects/__no_project__/) — use it directly.
	absRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace path: %w", err)
	}
	ok, err := config.IsWithinPath(absRoot, absDir)
	if err != nil || !ok {
		return nil, errors.New("path outside project workspace")
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
		ignoredPaths, err = workspace.GitIgnoredPaths(f.ctx(), absRoot)
		if err != nil {
			return nil, err
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

	// Layer ignore-file flagging on top of the git-derived set. A single-root
	// ignore.Resolver over the workspace compiles both .gitignore and .aiignore
	// patterns. Two cases:
	//
	//   - Git repository: git already honoured .gitignore (with negation and
	//     the global gitignore that the resolver does not), so only .aiignore-
	//     sourced rules are layered on top via IgnoredByAIIgnore. OR-merging
	//     the full resolver verdict here would let the resolver's negation-less
	//     .gitignore matching override a git "un-ignore" (!pattern).
	//   - Non-git workspace: there is no git to honour .gitignore, so the
	//     resolver is the sole authority for both files (matching how the
	//     indexer and search tools treat non-git workspaces). The full Ignored
	//     verdict is applied.
	//
	// The resolver is therefore built unconditionally — No Project / non-git
	// workspaces now honour .aiignore (and .gitignore) too, consistent with the
	// indexer and glob/ripgrep tools. A construction failure is non-fatal: the
	// listing still returns with whatever flags were already computed.
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

// WatchDirectory adds a directory to the file watcher.
//
// For No Project (CHAT mode), it re-scopes the watcher to the given session
// workspace: each chat session is isolated, so the watcher root must follow the
// active session to detect its file changes. The frontend calls this with the
// active session's workspace on every session switch, so re-scoping here keeps
// the watcher in sync without requiring a separate RPC or frontend/binding
// changes. For CODE mode, the directory is added to the existing project
// watcher.
func (f *FrontendAPI) WatchDirectory(dirPath string) error {
	if f.isNoProject() {
		return f.reScopeNoProjectWatcher(dirPath)
	}
	f.watcherMu.Lock()
	defer f.watcherMu.Unlock()
	if f.watcher == nil {
		return errors.New("no active file watcher")
	}
	return f.watcher.WatchDir(dirPath)
}

// UnwatchDirectory is a no-op in both CHAT and CODE modes.
//
// The workspace watcher is fully managed by the backend: it is created and
// scoped to the project workspace (CODE mode) or the active session's workspace
// (CHAT mode) by switchProjectSetupWatcher / WatchDirectory, and torn down on
// project switch. The frontend calls this from FileTreePanel's effect cleanup,
// which runs whenever FileTreePanel unmounts — and in CODE mode FileTreePanel
// unmounts when the user switches the sidebar tab (Explorer → Git/Search),
// collapses the sidebar, or during React StrictMode's mount/unmount/remount
// cycle in development.
//
// Removing the watched workspace root here would tear down the backend-managed
// watcher and break file-change detection until the panel remounts. In CODE
// mode this is exactly what happened: switching to the Git tab unmounted
// FileTreePanel, UnwatchDirectory removed the project root, and the file tree
// stopped updating. A StrictMode race between the async unwatch/watch RPCs
// could even leave the root permanently un-watched. CHAT mode was already a
// no-op; this extends the same treatment to CODE mode so the tree auto-refreshes
// in both modes.
func (f *FrontendAPI) UnwatchDirectory(_ string) error {
	return nil
}

// WriteFile writes content to a file within the session's workspace.
func (f *FrontendAPI) WriteFile(sessionID, path, content string) error {
	absPath, _, err := f.resolveWorkspacePath(path)
	if err != nil {
		return err
	}

	f.activeProjectMu.RLock()
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()
	if err := config.ValidateWithinSessionWorkspace(f.agentDir, projectID, sessionID, absPath); err != nil {
		return fmt.Errorf("path %q is outside session workspace: %w", path, err)
	}

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return os.WriteFile(absPath, []byte(content), 0o644)
}
