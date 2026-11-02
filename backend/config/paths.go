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

// noProjectID is the well-known identifier for the "No Project"
// pseudo-project, defined here to avoid importing project (circular dep) or
// core (config stays low-level).
const noProjectID = "__no_project__"

// ManagedWorktreesDir returns the LEGACY checkout-local container; new
// provisioning uses NewManagedWorktreesDir. See
// specs/domains/session-worktrees.md.
func ManagedWorktreesDir(repoRoot string) string {
	return filepath.Join(repoRoot, core.WorktreesRelativePath)
}

// ManagedWorktreePath validates a portable single-component identity and derives
// its path. Existing symlinks cannot redirect the container or tree.
func ManagedWorktreePath(repoRoot, name string) (string, error) {
	if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot {
		return "", errors.New("worktree repository root must be an absolute clean path")
	}
	if err := ValidateManagedWorktreeName(name); err != nil {
		return "", err
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

// ManagedWorktreeLegacyPath derives the historical checkout-local path
// (<repo>/.worktrees/<name>) from the portable name alone, WITHOUT
// inspecting the container — proposed/pre-durable bindings are validated
// lexically; on-disk hardening lives in ManagedWorktreePath.
func ManagedWorktreeLegacyPath(repoRoot, name string) (string, error) {
	if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot {
		return "", errors.New("worktree repository root must be an absolute clean path")
	}
	if err := ValidateManagedWorktreeName(name); err != nil {
		return "", err
	}
	return filepath.Join(ManagedWorktreesDir(repoRoot), name), nil
}

// ValidateManagedWorktreeName enforces the portable single-component
// identity rules everywhere a worktree name meets a path — the name ALONE,
// no filesystem state, so resolution is never gated by an unrelated
// container's on-disk state.
func ValidateManagedWorktreeName(name string) error {
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

// NewManagedWorktreesDir resolves the container for newly created trees only.
// The override replaces the projects base, not project identity or tree names.
func NewManagedWorktreesDir(agentDir, projectID, override string) (string, error) {
	if err := ValidateManagedWorktreeName(projectID); err != nil {
		return "", fmt.Errorf("invalid worktree project id: %w", err)
	}
	base := override
	if base == "" {
		base = ProjectsDir(agentDir)
	}
	if !filepath.IsAbs(base) || filepath.Clean(base) != base {
		return "", errors.New("git.worktrees_root must be an absolute clean path")
	}
	// Resolve existing operator/system prefixes once. Every descendant is
	// subsequently validated against this concrete container.
	base = pathutil.ResolveExistingPrefix(base)
	container := filepath.Join(base, projectID, core.ProjectWorktreesSegment)
	if err := core.ValidateWorktreeContainerPath(container, filepath.Join(container, "probe")); err != nil {
		return "", err
	}
	return container, nil
}

// CheckProjectWorktreesReleased prevents recursive project-data cleanup from
// bypassing the explicit worktree removal/dirty-loss confirmation protocol.
func CheckProjectWorktreesReleased(agentDir, projectID string) error {
	container, err := NewManagedWorktreesDir(agentDir, projectID, "")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(container)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect project worktrees before deletion: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() != core.WorktreeRepositoryMarker {
			return errors.New("project still contains managed worktrees; remove them explicitly before deleting the project")
		}
	}
	return nil
}

// WorktreePathInContainer constructs a portable tree identity within a container.
func WorktreePathInContainer(container, name string) (string, error) {
	if err := ValidateManagedWorktreeName(name); err != nil {
		return "", err
	}
	path := filepath.Join(container, name)
	if err := core.ValidateWorktreeContainerPath(container, path); err != nil {
		return "", err
	}
	return path, nil
}

// ManagedWorktreeNameFromPath recognizes legacy and repository-marked containers,
// independently of current configuration. A missing tree remains recognizable.
func ManagedWorktreeNameFromPath(repoRoot, ws string) (string, error) {
	if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot {
		return "", errors.New("worktree repository root must be an absolute clean path")
	}
	name := filepath.Base(ws)
	if err := ValidateManagedWorktreeName(name); err != nil {
		return "", err
	}
	if !core.IsManagedWorktreePath(repoRoot, ws) {
		return "", errors.New("path is not in a managed worktree container of this repository")
	}
	return name, nil
}

// ManagedWorktreeNameFromPathProject recognizes a durable managed tree of
// the given project independently of the on-disk repository marker and of
// current configuration (legacy checkout-local, repository-marked, or
// project-scoped layout): recorded identities the app wrote must stay
// decodable, so recognition never depends on a mutable marker file; foreign
// paths are still refused. See specs/domains/session-worktrees.md.
func ManagedWorktreeNameFromPathProject(repoRoot, projectID, ws string) (string, error) {
	if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot {
		return "", errors.New("worktree repository root must be an absolute clean path")
	}
	name := filepath.Base(ws)
	if err := ValidateManagedWorktreeName(name); err != nil {
		return "", err
	}
	if core.IsManagedWorktreePath(repoRoot, ws) {
		return name, nil
	}
	if ValidateManagedWorktreeName(projectID) == nil && core.IsProjectScopedWorktreePath(projectID, ws) {
		// A new-container tree is always outside the checkout (provisioning
		// enforces it); a recorded path that lands inside the repository is
		// inconsistent with every legitimate binding and is refused.
		if within, err := IsWithinPath(repoRoot, ws); err == nil && within {
			return "", errors.New("managed worktree path must be outside the repository")
		}
		return name, nil
	}
	return "", errors.New("path is not in a managed worktree container of this repository")
}

// ManagedWorktreeRootForPath finds a managed ancestor and validates the whole
// descendant path with symlink-aware containment; no foreign roots admitted.
func ManagedWorktreeRootForPath(repoRoot, path string) (string, bool) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", false
	}
	for candidate := path; ; candidate = filepath.Dir(candidate) {
		if _, err := ManagedWorktreeNameFromPath(repoRoot, candidate); err == nil {
			within, err := IsWithinPath(candidate, path)
			return candidate, err == nil && within
		}
		if filepath.Dir(candidate) == candidate {
			break
		}
	}
	return "", false
}

// WorktreeVectorIndexPath returns a managed worktree's vector-index storage
// root: <project vector index>/worktrees/<name> — per-tree state stays
// isolated from concurrent sessions on other trees while remaining inside
// the project's vector dir (project deletion removes it). See
// specs/domains/session-worktrees.md.
func WorktreeVectorIndexPath(agentDir, projectID, name string) (string, error) {
	if projectID == "" {
		return "", errors.New("project id is required for a worktree vector index path")
	}
	if err := ValidateManagedWorktreeName(name); err != nil {
		return "", err
	}
	return filepath.Join(ProjectVectorIndexPath(agentDir, projectID), "worktrees", name), nil
}

// WorktreeEmbeddingCachePath returns a managed worktree's content-addressed
// embedding-cache dir, INSIDE the tree's storage root
// (<project vector index>/worktrees/<name>/embedding_cache):
// core/vectorindex enables a persistent cache only for a root inside the
// storage the service was opened for — anywhere else silently turns the
// tree's first index pass into a full cold ONNX re-run (issue #137 step 1).
// Cache roots stay disjoint subtrees (scanCacheTree walks its own root); the
// checkout's cache is seeded into fresh trees by seedWorktreeEmbeddingCache;
// project deletion removes the dir; the pre-fix sibling location is cleaned
// up via LegacyWorktreeEmbeddingCachePath. See
// specs/domains/session-worktrees.md and specs/domains/workspace.md.
func WorktreeEmbeddingCachePath(agentDir, projectID, name string) (string, error) {
	if projectID == "" {
		return "", errors.New("project id is required for a worktree embedding cache path")
	}
	if err := ValidateManagedWorktreeName(name); err != nil {
		return "", err
	}
	storage, err := WorktreeVectorIndexPath(agentDir, projectID, name)
	if err != nil {
		return "", err
	}
	return filepath.Join(storage, "embedding_cache"), nil
}

// LegacyWorktreeEmbeddingCachePath returns the pre-containment-fix per-tree
// cache location — a sibling of the checkout's cache dir that
// core/vectorindex always refused, so entries there were never read or
// written. Cleanup-only (deleteWorktreeVectorIndex on tree release); never
// derive a live cache root from it.
func LegacyWorktreeEmbeddingCachePath(agentDir, projectID, name string) (string, error) {
	if projectID == "" {
		return "", errors.New("project id is required for a worktree embedding cache path")
	}
	if err := ValidateManagedWorktreeName(name); err != nil {
		return "", err
	}
	return filepath.Join(
		filepath.Dir(ProjectEmbeddingCachePath(agentDir, projectID)),
		filepath.Base(ProjectEmbeddingCachePath(agentDir, projectID))+"-worktrees",
		name,
	), nil
}

// WorkspaceSegment is the workspace directory name ("Workspace" under the
// project dir; No Project uses NoProjectWorkspaceSegment per session).
const WorkspaceSegment = "Workspace"

// NoProjectWorkspaceSegment is the directory name for per-session No Project
// workspace directories (<sessionID>/workspace/).
const NoProjectWorkspaceSegment = "workspace"

// --- Top-level ~/.c0wrk/ paths ---------------------------------------------

// AgentDir returns the canonical agent data directory (~/.c0wrk), falling
// back to "./" + DefaultAgentDir without a home. Single source of truth —
// every other helper takes the returned value as its agentDir argument;
// never recompute it inline.
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

// WebAccessCertPath returns the sidecar's app-owned self-signed certificate
// location — the only path the Generate control writes (ADR-102 decision 3);
// operator-authored certs are hand-authored via web_access.tls.cert. See
// specs/domains/web-access.md.
func WebAccessCertPath(agentDir string) string {
	return filepath.Join(agentDir, "web-access.crt")
}

// WebAccessKeyPath returns the private-key half of the app-owned pair
// WebAccessCertPath names. See specs/domains/web-access.md.
func WebAccessKeyPath(agentDir string) string {
	return filepath.Join(agentDir, "web-access.key")
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

// RuntimesDir returns the embedded-LLM inference-runtime root; deliberately
// a sibling of ToolsDir, never a child — <toolsDir>/bin goes on the agent's
// PATH and the inference runtime must not become agent-invokable (ADR-066
// D3, ASI05).
func RuntimesDir(agentDir string) string {
	return filepath.Join(agentDir, core.EmbeddedRuntimesRelativePath)
}

// EmbeddedModelDir returns the embedded-LLM GGUF weights dir, a dedicated
// subdirectory of ModelsDir so removing the embedded model never touches the
// flat embedding-model files resolved by desktop/startup.go.
func EmbeddedModelDir(agentDir string) string {
	return filepath.Join(agentDir, core.EmbeddedModelRelativePath)
}

// EmbeddedMLXModelDir returns the mtplx engine's pinned MLX weight-pack
// root, separate from the llama GGUF root so scoped removals stay scoped.
func EmbeddedMLXModelDir(agentDir string) string {
	return filepath.Join(agentDir, core.EmbeddedMLXModelRelativePath)
}

// ToolsDir returns the managed external tools directory (~/.c0wrk/tools/).
func ToolsDir(agentDir string) string {
	return filepath.Join(agentDir, "tools")
}

// ThemesDir returns the user themes directory (~/.c0wrk/themes/); each theme
// is a standalone *.css file whose name doubles as its stable identifier.
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

// UpdateStagingDir returns the self-updater's staging directory: release
// archives land here atomically and are SHA256-verified before the swap.
func UpdateStagingDir(agentDir string) string {
	return filepath.Join(agentDir, "update-staging")
}

// UpdateStatePath returns the auto-checker's runtime state (last check,
// skipped version) — deliberately not config.yaml, which holds all update
// configuration under the updates section.
func UpdateStatePath(agentDir string) string {
	return filepath.Join(agentDir, "update_state.json")
}

// SingleInstanceLockPath returns the app-level lock file, flock-held for
// the process lifetime by main before any shared state is touched (ADR-075);
// runtime state, not config — there is no single-instance toggle.
func SingleInstanceLockPath(agentDir string) string {
	return filepath.Join(agentDir, "app.lock")
}

// WindowStatePath returns the persisted OS-level window geometry — runtime
// state, deliberately not config.yaml (the update_state.json split).
func WindowStatePath(agentDir string) string {
	return filepath.Join(agentDir, "window_state.json")
}

// WebviewStatePath returns the persisted webview GPU policy plus the
// watchdog's auto-mode fallback record; deliberately not config.yaml — it
// must be readable BEFORE wails.Run creates the webview, while config.yaml
// loads only in app.Startup after the window exists.
func WebviewStatePath(agentDir string) string {
	return filepath.Join(agentDir, "webview_state.json")
}

// DialogStatePath returns the last directory chosen in a native picker so
// the next dialog reopens there — runtime state, not config.yaml.
func DialogStatePath(agentDir string) string {
	return filepath.Join(agentDir, "dialog_state.json")
}

// ModelProfilesPath returns the operator-authored custom model profiles —
// user data with independent lifecycle, created lazily by the first save.
// See model_profiles_store.go for the format.
func ModelProfilesPath(agentDir string) string {
	return filepath.Join(agentDir, "model-profiles.yaml")
}

// GitConfigSnapshotsDir stores per-repository .git/config snapshots with a
// fingerprint on the trusted entry in config.yaml, so later scans detect
// config drift after the trust decision; lazily created by the writer.
func GitConfigSnapshotsDir(agentDir string) string {
	return filepath.Join(agentDir, "git-config-snapshots")
}

// --- Per-project paths (agentDir + projectID) -------------------------------

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

// --- Per-session paths (agentDir + projectID + sessionID) -------------------

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

// --- No-Project paths --------------------------------------------------------

// NoProjectSessionWorkspace returns the isolated workspace for a No-Project session.
func NoProjectSessionWorkspace(agentDir, sessionID string) string {
	return filepath.Join(ProjectDir(agentDir, noProjectID), sessionID, NoProjectWorkspaceSegment)
}

// SessionWorkspaceRoot returns a session's workspace root: the project
// workspace for regular projects, the isolated <sessionID>/workspace/ for
// No Project.
func SessionWorkspaceRoot(agentDir, projectID, sessionID string) string {
	if projectID == noProjectID {
		return NoProjectSessionWorkspace(agentDir, sessionID)
	}
	return ProjectWorkspacePath(agentDir, projectID)
}

// ValidateWithinSessionWorkspace checks absPath is inside the session's
// workspace; for No Project this enforces the <sessionID>/workspace/
// isolation boundary. Delegates to pathutil.IsWithinPath
// (symlink-resolving).
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
func ValidateNoProjectSessionPath(projectDir, absDir string) error {
	ok, err := pathutil.IsWithinPath(projectDir, absDir)
	if err != nil {
		return fmt.Errorf("cannot resolve path relative to No Project dir: %w", err)
	}
	if !ok {
		return fmt.Errorf("path %q is outside No Project directory %q", absDir, projectDir)
	}
	// filepath.Rel is for path-component analysis only — containment is
	// already verified above via pathutil.IsWithinPath.
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

// --- Path containment and session-infra helpers ------------------------------

// IsWithinPath wraps pathutil.IsWithinPath so all backend containment
// checks share a single import.
func IsWithinPath(parent, child string) (bool, error) {
	return pathutil.IsWithinPath(parent, child)
}

// IsSessionInfraPath reports whether absPath is within a session's plans/
// or temp/ subdirectory under the project data dir — outside the workspace
// but allowed for file-viewer access (plan review, temp inspection).
func IsSessionInfraPath(projectDir, absPath string) bool {
	ok, err := pathutil.IsWithinPath(projectDir, absPath)
	if err != nil || !ok {
		return false
	}
	// filepath.Rel is path-component analysis, not containment — already
	// verified above via pathutil.IsWithinPath.
	rel, err := filepath.Rel(projectDir, absPath)
	if err != nil {
		return false
	}
	// rel stays platform-native: SplitPathComponents splits on
	// filepath.Separator, a COMPILE-TIME per-OS constant — pre-converting
	// with filepath.ToSlash made the slash-joined path ONE component on
	// Windows, so every session-infra path was rejected (#146).
	parts := pathutil.SplitPathComponents(rel)
	// Structure: <sessionID>/plans/... or <sessionID>/temp/...
	return len(parts) >= 2 && (parts[1] == "plans" || parts[1] == "temp")
}

// ProjectSkillsPath returns the project-local agent skills directory.
// This is <workspacePath>/.agents/skills.
func ProjectSkillsPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".agents", "skills")
}

// ProjectAgentsPath returns the project-local Subagent Profiles directory
// (<workspacePath>/.agents/agents), mirroring ProjectSkillsPath.
func ProjectAgentsPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".agents", "agents")
}

// ProjectCommandsPath returns the project-local custom commands directory
// (<workspacePath>/.agents/commands), mirroring ProjectSkillsPath (ADR-096).
func ProjectCommandsPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".agents", "commands")
}

// ProjectResearchPath returns the project-local research root
// (<workspacePath>/.research) — always on for real projects, created lazily
// by the writing layer. See specs/domains/research.md.
func ProjectResearchPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".research")
}

// IsResearchPath reports whether absPath is within the research root; used
// by the workspace watcher to filter events to research artifacts.
func IsResearchPath(researchRoot, absPath string) bool {
	ok, err := pathutil.IsWithinPath(researchRoot, absPath)
	if err != nil {
		return false
	}
	return ok
}

// PaperLibraryPathIn returns the papers library inside a research root
// (<researchRoot>/papers) — a GLOBAL subdir holding every paper card
// regardless of how many R-NNN projects exist, outliving any single one;
// lazily created by the writer layer. See specs/domains/papers.md.
func PaperLibraryPathIn(researchRoot string) string {
	return filepath.Join(researchRoot, "papers")
}

// PaperLibraryPath returns the project-local papers library
// (<workspacePath>/.research/papers) — PaperLibraryPathIn applied to the
// default research root. See specs/domains/papers.md.
func PaperLibraryPath(workspacePath string) string {
	return PaperLibraryPathIn(ProjectResearchPath(workspacePath))
}

// ComparisonDirName is the research-root subdir for multi-paper comparison
// artifacts — beside papers/ because a comparison spans the library and
// belongs to no single paper. See specs/domains/papers.md.
const ComparisonDirName = "comparisons"

// ComparisonsPathIn returns the comparisons dir inside a research root
// (<researchRoot>/comparisons), one <slug>.md artifact per comparison set,
// lazily created by whoever writes it. See specs/domains/papers.md.
func ComparisonsPathIn(researchRoot string) string {
	return filepath.Join(researchRoot, ComparisonDirName)
}

// ComparisonsPath returns the project-local comparisons dir
// (<workspacePath>/.research/comparisons) — ComparisonsPathIn applied to the
// default research root. See specs/domains/papers.md.
func ComparisonsPath(workspacePath string) string {
	return ComparisonsPathIn(ProjectResearchPath(workspacePath))
}

// SessionStepDumpDir returns the per-step dump directory for a session,
// derived from the session's LLM dump path.
func SessionStepDumpDir(agentDir, projectID, sessionID string) string {
	dumpPath := SessionDumpPath(agentDir, projectID, sessionID)
	return filepath.Join(filepath.Dir(dumpPath), "steps")
}
