// Package config provides configuration loading and validation for the agent.
package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/v0lka/sp4rk/pathutil"
)

// noProjectID is the well-known identifier for the "No Project" pseudo-project.
// Defined here rather than importing project (to avoid a circular dependency
// since project/manager.go imports config) or core (to keep the dependency
// graph lean — config is a low-level package consumed by most backend packages).
const noProjectID = "__no_project__"

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

// ProjectsDir returns the base projects directory.
func ProjectsDir(agentDir string) string {
	return filepath.Join(agentDir, "projects")
}

// ModelsDir returns the user models directory (embedding model files).
func ModelsDir(agentDir string) string {
	return filepath.Join(agentDir, "models")
}

// ToolsDir returns the managed external tools directory (~/.c0wrk/tools/).
func ToolsDir(agentDir string) string {
	return filepath.Join(agentDir, "tools")
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
// another check is due. It is distinct from update-settings.json, which holds
// the user-facing preferences (enabled / auto-check toggles).
func UpdateStatePath(agentDir string) string {
	return filepath.Join(agentDir, "update_state.json")
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
	parts := pathutil.SplitPathComponents(filepath.ToSlash(rel))
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
// This is <workspacePath>/.research. When a project has RESEARCH mode enabled,
// its persisted ResearchRoot points here (see ProjectInfo.ResearchRoot). The
// directory is created lazily by the layer that activates RESEARCH mode, not
// by this path helper.
func ProjectResearchPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".research")
}

// SessionStepDumpDir returns the per-step dump directory for a session,
// derived from the session's LLM dump path.
func SessionStepDumpDir(agentDir, projectID, sessionID string) string {
	dumpPath := SessionDumpPath(agentDir, projectID, sessionID)
	return filepath.Join(filepath.Dir(dumpPath), "steps")
}
