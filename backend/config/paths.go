// Package config provides configuration loading and validation for the agent.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/pathutil"
)

// noProjectID is the well-known identifier for the "No Project" pseudo-project.
// Defined here rather than importing project (to avoid a circular dependency
// since project/manager.go imports config) or core (to keep the dependency
// graph lean — config is a low-level package consumed by most backend packages).
const noProjectID = "__no_project__"

// ManagedWorktreesDir returns the app-managed worktree container in a CODE repository.
// The segment literal is the shared core.WorktreesRelativePath constant.
func ManagedWorktreesDir(repoRoot string) string {
	return filepath.Join(repoRoot, core.WorktreesRelativePath)
}

// ManagedWorktreePath validates a portable single-component identity and derives
// its path. Existing symlinks cannot redirect the container or tree.
func ManagedWorktreePath(repoRoot, name string) (string, error) {
	if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot {
		return "", errors.New("worktree repository root must be an absolute clean path")
	}
	if name == "" || len(name) > 120 || name[0] == '.' || name[0] == '-' || strings.HasSuffix(name, ".") {
		return "", errors.New("invalid managed worktree name")
	}
	for _, c := range name {
		isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlnum && c != '-' && c != '_' && c != '.' {
			return "", errors.New("invalid managed worktree name")
		}
	}
	// Windows device names are invalid even on a Unix creator: persisted
	// identities must remain portable across supported desktop platforms.
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" ||
		(len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
		return "", errors.New("reserved managed worktree name")
	}
	container := ManagedWorktreesDir(repoRoot)
	path := filepath.Join(container, name)
	for _, candidate := range []string{container, path} {
		fi, err := os.Lstat(candidate)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect managed worktree path: %w", err)
		}
		if err == nil && (fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir()) {
			return "", errors.New("managed worktree path must be a directory, not a symlink")
		}
		within, err := IsWithinPath(repoRoot, candidate)
		if err != nil {
			return "", fmt.Errorf("validate managed worktree containment: %w", err)
		}
		if !within {
			return "", errors.New("managed worktree escapes repository")
		}
	}
	return path, nil
}

// validateManagedWorktreeName enforces the portable single-component identity
// rules shared by every place a worktree name is derived from or joined into
// a path.
func validateManagedWorktreeName(name string) error {
	if name == "" || len(name) > 120 || name[0] == '.' || name[0] == '-' || strings.HasSuffix(name, ".") {
		return errors.New("invalid managed worktree name")
	}
	for _, c := range name {
		isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlnum && c != '-' && c != '_' && c != '.' {
			return errors.New("invalid managed worktree name")
		}
	}
	// Windows device names are invalid even on a Unix creator: persisted
	// identities must remain portable across supported desktop platforms.
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" ||
		(len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
		return errors.New("reserved managed worktree name")
	}
	return nil
}

// ManagedWorktreeNameFromPath validates that ws is a managed worktree of
// repoRoot — exactly <repoRoot>/.worktrees/<name>, one component inside the
// container — and returns the name. Containment goes through IsWithinPath and
// the name re-derives through ManagedWorktreePath, so a returned identity
// always round-trips; the tree itself is NOT required to exist on disk (the
// restore path may not have recreated it yet).
func ManagedWorktreeNameFromPath(repoRoot, ws string) (string, error) {
	if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot {
		return "", errors.New("worktree repository root must be an absolute clean path")
	}
	if !filepath.IsAbs(ws) {
		return "", errors.New("worktree path must be absolute")
	}
	ws = filepath.Clean(ws)
	container := ManagedWorktreesDir(repoRoot)
	within, err := IsWithinPath(container, ws)
	if err != nil {
		return "", fmt.Errorf("validate managed worktree containment: %w", err)
	}
	if !within {
		return "", errors.New("path is not inside the managed worktrees container")
	}
	rel, err := filepath.Rel(container, ws)
	if err != nil {
		return "", fmt.Errorf("derive managed worktree name: %w", err)
	}
	if rel == "." || rel != filepath.Base(rel) {
		return "", errors.New("path is not a single managed worktree directory")
	}
	if _, err := ManagedWorktreePath(repoRoot, rel); err != nil {
		return "", err
	}
	return rel, nil
}

// WorktreeVectorIndexPath returns the vector-index storage root for a managed
// worktree of a project: <project vector index>/worktrees/<name>. A managed
// session indexes its OWN tree root, so its index state (branch-scoped
// collections, lexical index, file-hash sidecars) lives under a
// worktree-scoped root — concurrent sessions on different trees never share
// or clobber each other's persisted index state — while staying inside the
// project's vector dir so project deletion removes it together with the
// project's own index. The layout under the root (branches/, lexical/,
// sidecars) is owned by core/vectorindex; the worktrees/ container segment is
// opaque to it.
func WorktreeVectorIndexPath(agentDir, projectID, name string) (string, error) {
	if projectID == "" {
		return "", errors.New("project id is required for a worktree vector index path")
	}
	if err := validateManagedWorktreeName(name); err != nil {
		return "", err
	}
	return filepath.Join(ProjectVectorIndexPath(agentDir, projectID), "worktrees", name), nil
}

// WorktreeEmbeddingCachePath returns the content-addressed embedding cache
// directory for a managed worktree of a project: a SIBLING of the project's
// own cache dir — <project vector index>/embedding_cache-worktrees/<name> —
// never a subtree of it. The cache accounting (scanCacheTree) walks its root
// recursively, so one live cache root must never contain another: with
// per-root vector managers (ADR-080) a checkout manager and a managed-tree
// manager of the same project run concurrently, and a shared or nested cache
// root would corrupt both managers' size accounting. Per-tree caches keep
// the trees isolated at the cost of cross-tree dedup, which a shared root
// could no longer guarantee safely anyway. The dir still lives under the
// project's vector index (outside its branches/ layout, which the legacy
// migration never scans), so project deletion removes it together with the
// rest of the project's index data. Checkout keeps ProjectEmbeddingCachePath
// unchanged — no migration.
func WorktreeEmbeddingCachePath(agentDir, projectID, name string) (string, error) {
	if projectID == "" {
		return "", errors.New("project id is required for a worktree embedding cache path")
	}
	if err := validateManagedWorktreeName(name); err != nil {
		return "", err
	}
	return filepath.Join(
		filepath.Dir(ProjectEmbeddingCachePath(agentDir, projectID)),
		filepath.Base(ProjectEmbeddingCachePath(agentDir, projectID))+"-worktrees",
		name,
	), nil
}

// WorkspaceSegment is the directory name for workspace directories.
// Regular projects use "Workspace" under the project dir; No Project sessions
// use "workspace" under the per-session directory.
const WorkspaceSegment = "Workspace"

// NoProjectWorkspaceSegment is the directory name for per-session No Project
// workspace directories (<sessionID>/workspace/).
const NoProjectWorkspaceSegment = "workspace"

// ---------------------------------------------------------------------------
// Top-level ~/.c0wrk/ paths
// ---------------------------------------------------------------------------

// AgentDir returns the canonical agent data directory (~/.c0wrk). It resolves
// the user's home directory and joins it with DefaultAgentDir; if the home
// directory cannot be determined it falls back to "./" + DefaultAgentDir
// (relative to the working directory). This is the single source of truth for
// the computation — every other path helper takes the returned value as its
// agentDir argument, so callers must never recompute homeDir + DefaultAgentDir
// inline.
func AgentDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	return filepath.Join(homeDir, DefaultAgentDir)
}

// ConfigPath returns the path to config.yaml within the agent directory.
func ConfigPath(agentDir string) string {
	return filepath.Join(agentDir, "config.yaml")
}

// DatabasePath returns the fixed SQLite database path.
func DatabasePath(agentDir string) string {
	return filepath.Join(agentDir, "database.db")
}

// LogsDir returns the app-level logs directory (session-*.log files).
func LogsDir(agentDir string) string {
	return filepath.Join(agentDir, "logs")
}

// SkillsDir returns the global agent skills directory.
func SkillsDir(agentDir string) string {
	return filepath.Join(agentDir, ".agents", "skills")
}

// AgentsDir returns the global agent Subagent Profile directory
// (~/.c0wrk/.agents/agents). It mirrors SkillsDir for the agents package's
// AGENT.md discovery.
func AgentsDir(agentDir string) string {
	return filepath.Join(agentDir, ".agents", "agents")
}

// ProjectsDir returns the base projects directory.
func ProjectsDir(agentDir string) string {
	return filepath.Join(agentDir, "projects")
}

// ModelsDir returns the user models directory (embedding model files).
func ModelsDir(agentDir string) string {
	return filepath.Join(agentDir, "models")
}

// RuntimesDir returns the embedded-LLM inference-runtime root
// (~/.c0wrk/runtimes/). Each pinned runtime occupies its own
// "llama-<tag>-<backend>" subdirectory, derived by core/embeddedllm's Layout.
//
// It is deliberately a sibling of ToolsDir, never a child of it:
// Manager.PrependToPATH() puts <toolsDir>/bin on the agent's PATH, and the
// inference runtime must not be agent-invokable (ADR-066 D3, ASI05).
func RuntimesDir(agentDir string) string {
	return filepath.Join(agentDir, core.EmbeddedRuntimesRelativePath)
}

// EmbeddedModelDir returns the embedded-LLM weights directory
// (~/.c0wrk/models/bonsai-2-27b/), the dedicated subdirectory of ModelsDir
// that holds the GGUF files and manifest.json. Nesting keeps the flat
// embedding-model files resolved by desktop/startup.go resolveModelPath
// untouched, so removing the embedded model can never delete them.
func EmbeddedModelDir(agentDir string) string {
	return filepath.Join(agentDir, core.EmbeddedModelRelativePath)
}

// ToolsDir returns the managed external tools directory (~/.c0wrk/tools/).
func ToolsDir(agentDir string) string {
	return filepath.Join(agentDir, "tools")
}

// ThemesDir returns the user themes directory (~/.c0wrk/themes/) holding
// custom CSS theme files. Each theme is a standalone *.css file whose name
// doubles as its stable identifier; the directory is created lazily by the
// importer, this helper only names it.
func ThemesDir(agentDir string) string {
	return filepath.Join(agentDir, "themes")
}

// ToolsBinDir returns the directory for static binaries managed by the
// tool-manager (~/.c0wrk/tools/bin/).
func ToolsBinDir(agentDir string) string {
	return filepath.Join(ToolsDir(agentDir), "bin")
}

// ToolsPythonDir returns the directory for Python installations managed by
// the tool-manager (~/.c0wrk/tools/python/).
func ToolsPythonDir(agentDir string) string {
	return filepath.Join(ToolsDir(agentDir), "python")
}

// UpdateStagingDir returns the staging directory used by the self-updater to
// download and verify release archives before they are swapped into place
// (~/.c0wrk/update-staging/). Archives land here atomically (tmp+rename) and
// are integrity-checked (SHA256SUMS) before the update is applied.
func UpdateStagingDir(agentDir string) string {
	return filepath.Join(agentDir, "update-staging")
}

// UpdateStatePath returns the path to update_state.json inside the agent
// directory. This file holds the ephemeral self-update runtime state (the
// timestamp of the last automatic check and the currently-skipped version) and
// is deliberately NOT part of config.yaml: it is written by the background
// auto-checker at runtime and read back on the next startup to decide whether
// another check is due. All update *configuration* (the enabled /
// auto-check toggles and the check interval) lives in config.yaml under the
// updates section.
func UpdateStatePath(agentDir string) string {
	return filepath.Join(agentDir, "update_state.json")
}

// SingleInstanceLockPath returns the path to the app-level single-instance
// lock file inside the agent directory. The file is held with an exclusive
// non-blocking OS lock (flock on darwin/linux, LockFileEx on Windows) for the
// process lifetime by main before any shared state is touched; a process that
// finds it already held knows a first instance is alive and must not mutate
// crash-capture liveness markers (see ADR-075). Like window_state.json this is
// runtime state, deliberately NOT part of config.yaml — there is no
// single-instance toggle to misconfigure.
func SingleInstanceLockPath(agentDir string) string {
	return filepath.Join(agentDir, "app.lock")
}

// WindowStatePath returns the path to window_state.json inside the agent
// directory. This file holds the persisted OS-level window geometry (width,
// height, maximized flag) so the application window reopens at the size the
// user left it. It is deliberately NOT part of config.yaml: window geometry is
// runtime state (written on resize/shutdown, read on startup), not an
// operator-facing setting — mirroring the update_state.json split.
func WindowStatePath(agentDir string) string {
	return filepath.Join(agentDir, "window_state.json")
}

// DialogStatePath returns the path to dialog_state.json inside the agent
// directory. This file holds the last directory chosen in a native picker
// dialog (e.g. PickDirectory for adding a project) so the next dialog opens
// where the user left off. Like window_state.json, it is runtime state and
// deliberately NOT part of config.yaml.
func DialogStatePath(agentDir string) string {
	return filepath.Join(agentDir, "dialog_state.json")
}

// ModelProfilesPath returns the path to model-profiles.yaml inside the agent
// directory. This file holds the operator-authored custom model-profile
// profiles in a versioned format separate from config.yaml: profiles are
// user data with independent lifecycle (create/edit/delete from the
// settings UI), not machine-managed runtime state and not static config.
// The file is created lazily by the first save; absence means "no custom
// profiles". See modelProfiles_profiles_store.go for the format.
func ModelProfilesPath(agentDir string) string {
	return filepath.Join(agentDir, "model-profiles.yaml")
}

// GitConfigSnapshotsDir returns the directory where per-repository git-config
// snapshots are stored (~/.c0wrk/git-config-snapshots/). When a repository is
// trusted, the scan of its .git/config is snapshotted here and a fingerprint
// (a hash of the snapshot content) is recorded on the trusted entry in
// config.yaml (security.trusted_git_repos[].fingerprint) so a later scan can
// diff against the stored snapshot and detect config drift after the trust
// decision. The directory is lazily created by the writer; this helper only
// names it.
func GitConfigSnapshotsDir(agentDir string) string {
	return filepath.Join(agentDir, "git-config-snapshots")
}

// ---------------------------------------------------------------------------
// Per-project paths (agentDir + projectID)
// ---------------------------------------------------------------------------

// ProjectDir returns the per-project directory within ~/.c0wrk/projects/.
func ProjectDir(agentDir, projectID string) string {
	return filepath.Join(ProjectsDir(agentDir), projectID)
}

// ProjectWorkspacePath returns the internal workspace directory for a project.
func ProjectWorkspacePath(agentDir, projectID string) string {
	return filepath.Join(ProjectDir(agentDir, projectID), WorkspaceSegment)
}

// ProjectVectorIndexPath returns the vector index storage for a project.
func ProjectVectorIndexPath(agentDir, projectID string) string {
	return filepath.Join(ProjectDir(agentDir, projectID), "vector_index")
}

// ProjectEmbeddingCachePath returns the content-addressed embedding cache
// directory inside a project's vector-index storage.
func ProjectEmbeddingCachePath(agentDir, projectID string) string {
	return filepath.Join(ProjectVectorIndexPath(agentDir, projectID), "embedding_cache")
}

// ---------------------------------------------------------------------------
// Per-session paths (agentDir + projectID + sessionID)
// ---------------------------------------------------------------------------

// SessionDir returns the per-session directory.
func SessionDir(agentDir, projectID, sessionID string) string {
	return filepath.Join(ProjectDir(agentDir, projectID), sessionID)
}

// SessionLogsDir returns the session-specific logs directory.
func SessionLogsDir(agentDir, projectID, sessionID string) string {
	return filepath.Join(SessionDir(agentDir, projectID, sessionID), "logs")
}

// SessionLogPath returns the session log file path.
func SessionLogPath(agentDir, projectID, sessionID string) string {
	return filepath.Join(SessionLogsDir(agentDir, projectID, sessionID),
		"session_"+sessionID+".log")
}

// SessionDumpPath returns the LLM dump file path.
func SessionDumpPath(agentDir, projectID, sessionID string) string {
	return filepath.Join(SessionDir(agentDir, projectID, sessionID), "dumps",
		"session_"+sessionID+"_llm_dump.jsonl")
}

// SessionTempDir returns the session temp directory.
func SessionTempDir(agentDir, projectID, sessionID string) string {
	return filepath.Join(SessionDir(agentDir, projectID, sessionID), "temp")
}

// SessionPlansDir returns the plans directory for a session.
func SessionPlansDir(agentDir, projectID, sessionID string) string {
	return filepath.Join(SessionDir(agentDir, projectID, sessionID), "plans")
}

// SessionImagesDir returns the per-session images directory used to store
// processed image attachments (resized base64-encoded copies and thumbnails).
func SessionImagesDir(agentDir, projectID, sessionID string) string {
	return filepath.Join(SessionDir(agentDir, projectID, sessionID), "images")
}

// ---------------------------------------------------------------------------
// No-Project paths
// ---------------------------------------------------------------------------

// NoProjectSessionWorkspace returns the isolated workspace for a No-Project session.
func NoProjectSessionWorkspace(agentDir, sessionID string) string {
	return filepath.Join(ProjectDir(agentDir, noProjectID), sessionID, NoProjectWorkspaceSegment)
}

// SessionWorkspaceRoot returns the workspace root directory for a session.
// For regular projects this is the project workspace; for No Project it is
// the isolated per-session workspace (<sessionID>/workspace/).
func SessionWorkspaceRoot(agentDir, projectID, sessionID string) string {
	if projectID == noProjectID {
		return NoProjectSessionWorkspace(agentDir, sessionID)
	}
	return ProjectWorkspacePath(agentDir, projectID)
}

// ValidateWithinSessionWorkspace checks that absPath is contained within
// the session's workspace directory. For No Project sessions this enforces
// the <sessionID>/workspace/ isolation boundary.
//
// Containment is delegated to pathutil.IsWithinPath, which resolves
// symlinks via ResolveExistingPrefix on the longest existing path prefix
// and uses a separator-terminated prefix comparison.
func ValidateWithinSessionWorkspace(agentDir, projectID, sessionID, absPath string) error {
	wsRoot := SessionWorkspaceRoot(agentDir, projectID, sessionID)
	ok, err := pathutil.IsWithinPath(wsRoot, absPath)
	if err != nil {
		return fmt.Errorf("cannot resolve path relative to workspace: %w", err)
	}
	if !ok {
		return fmt.Errorf("path %q is outside session workspace %q", absPath, wsRoot)
	}
	return nil
}

// ValidateNoProjectSessionPath checks that absDir is either the No Project
// project directory itself or a <sessionID>/workspace/... subdirectory.
// Returns an error if absDir falls outside the allowed No Project tree.
func ValidateNoProjectSessionPath(projectDir, absDir string) error {
	// Containment check via the centralized API (see AGENTS.md path rules).
	ok, err := pathutil.IsWithinPath(projectDir, absDir)
	if err != nil {
		return fmt.Errorf("cannot resolve path relative to No Project dir: %w", err)
	}
	if !ok {
		return fmt.Errorf("path %q is outside No Project directory %q", absDir, projectDir)
	}
	// filepath.Rel is used here for path-component analysis (splitting into
	// <sessionID>/workspace/... segments), not for containment — containment
	// is already verified above via pathutil.IsWithinPath.
	rel, err := filepath.Rel(projectDir, absDir)
	if err != nil {
		return fmt.Errorf("cannot compute relative path: %w", err)
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	// Paths directly under the project dir (UUID session directories)
	// must follow the <sessionID>/workspace/... pattern.
	if len(parts) >= 1 && parts[0] != "." {
		if len(parts) < 2 || parts[1] != NoProjectWorkspaceSegment {
			return errors.New("access denied: path under session must be <sessionID>/workspace")
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Path containment and session-infra helpers
// ---------------------------------------------------------------------------

// IsWithinPath returns true if child is equal to or a descendant of parent.
// Wraps pathutil.IsWithinPath so all backend path-containment checks use a
// single import. See github.com/v0lka/sp4rk/pathutil.IsWithinPath for full semantics.
func IsWithinPath(parent, child string) (bool, error) {
	return pathutil.IsWithinPath(parent, child)
}

// IsSessionInfraPath returns true if absPath falls within a session's plans/
// or temp/ subdirectory under the project data directory. These directories
// live outside the workspace but are allowed for file viewer access
// (plan review, temp file inspection).
//
// Expected structure: <projectDir>/<sessionID>/plans/... or
// <projectDir>/<sessionID>/temp/...
func IsSessionInfraPath(projectDir, absPath string) bool {
	ok, err := pathutil.IsWithinPath(projectDir, absPath)
	if err != nil || !ok {
		return false
	}
	// Containment is verified above via pathutil.IsWithinPath.
	// filepath.Rel is used here for path-component analysis (splitting into
	// <sessionID>/plans/... or <sessionID>/temp/... segments), not for
	// containment.
	rel, err := filepath.Rel(projectDir, absPath)
	if err != nil {
		return false
	}
	// rel stays in its platform-native form: SplitPathComponents splits on
	// filepath.Separator, a COMPILE-TIME per-OS constant. Pre-converting with
	// filepath.ToSlash made the slash-joined path split into a SINGLE
	// component on Windows (where the separator is '\'), so every
	// session-infra path was classified as non-infra and rejected (#146).
	parts := pathutil.SplitPathComponents(rel)
	// Structure: <sessionID>/plans/... or <sessionID>/temp/...
	return len(parts) >= 2 && (parts[1] == "plans" || parts[1] == "temp")
}

// ProjectSkillsPath returns the project-local agent skills directory.
// This is <workspacePath>/.agents/skills.
func ProjectSkillsPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".agents", "skills")
}

// ProjectAgentsPath returns the project-local Subagent Profiles directory.
// This is <workspacePath>/.agents/agents. Mirrors ProjectSkillsPath for the
// agents package's AGENT.md discovery.
func ProjectAgentsPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".agents", "agents")
}

// ProjectResearchPath returns the project-local research workspace directory.
// This is <workspacePath>/.research — the canonical research root for every
// real project (RESEARCH is always on for real projects). The directory is
// created lazily by the layer that writes research artifacts, not by this
// path helper.
func ProjectResearchPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".research")
}

// IsResearchPath returns true if absPath is within the research directory
// rooted at researchRoot. Used by the workspace watcher callback to filter
// file-change events to only those affecting research artifacts.
func IsResearchPath(researchRoot, absPath string) bool {
	ok, err := pathutil.IsWithinPath(researchRoot, absPath)
	if err != nil {
		return false
	}
	return ok
}

// PaperLibraryPathIn returns the literature ("papers") library directory
// nested inside a research root: <researchRoot>/papers. The library is a
// global subdirectory of the research root — it holds every paper card
// regardless of how many R-NNN research projects exist (and even when none
// does), so it outlives any single research project. Callers pass the
// project's effective research root (ProjectResearchPath for a real project)
// so a custom research root carries its own library. The directory is created
// lazily by the writer layer (core/papers) and by the watcher setup, not by
// this path helper.
func PaperLibraryPathIn(researchRoot string) string {
	return filepath.Join(researchRoot, "papers")
}

// PaperLibraryPath returns the project-local literature ("papers") library
// directory: <workspacePath>/.research/papers — i.e. PaperLibraryPathIn applied
// to the default research root (ProjectResearchPath). It is the well-known
// location of the library. The directory is created lazily by the writer
// layer (core/papers), not by this path helper.
func PaperLibraryPath(workspacePath string) string {
	return PaperLibraryPathIn(ProjectResearchPath(workspacePath))
}

// ComparisonDirName is the research-root subdirectory holding multi-paper
// comparison artifacts. It sits beside the paper library (papers/) because a
// comparison spans papers from across the library and therefore belongs to no
// single paper directory.
const ComparisonDirName = "comparisons"

// ComparisonsPathIn returns the multi-paper comparison directory nested inside
// a research root: <researchRoot>/comparisons. It holds one Markdown artifact
// per comparison set (<slug>.md), written by the study-paper Compare intent
// from its comparison-matrix template. Like the paper library, it is a global
// subdirectory of the research root and is created lazily by whoever writes the
// artifact (the agent's skill), not by this path helper. Callers pass the
// project's effective research root so a custom research root carries its own
// comparisons.
func ComparisonsPathIn(researchRoot string) string {
	return filepath.Join(researchRoot, ComparisonDirName)
}

// ComparisonsPath returns the project-local multi-paper comparison directory:
// <workspacePath>/.research/comparisons — i.e. ComparisonsPathIn applied to the
// default research root (ProjectResearchPath). It is the well-known location
// used when the project has no custom research root (RESEARCH off, or enabled
// with the default root).
func ComparisonsPath(workspacePath string) string {
	return ComparisonsPathIn(ProjectResearchPath(workspacePath))
}

// SessionStepDumpDir returns the per-step dump directory for a session,
// derived from the session's LLM dump path.
func SessionStepDumpDir(agentDir, projectID, sessionID string) string {
	dumpPath := SessionDumpPath(agentDir, projectID, sessionID)
	return filepath.Join(filepath.Dir(dumpPath), "steps")
}
