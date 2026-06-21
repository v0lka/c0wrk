// Package config provides configuration loading and validation for the agent.
package config

import "path/filepath"

// NoProjectID is the well-known identifier for the "No Project" pseudo-project.
// Duplicated from backend/project to avoid a circular import (config ← project).
// If this value ever needs to change, update both packages.
const NoProjectID = "__no_project__"

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

// ---------------------------------------------------------------------------
// Per-project paths (agentDir + projectID)
// ---------------------------------------------------------------------------

// ProjectDir returns the per-project directory within ~/.c0wrk/projects/.
func ProjectDir(agentDir, projectID string) string {
	return filepath.Join(ProjectsDir(agentDir), projectID)
}

// ProjectWorkspacePath returns the internal workspace directory for a project.
func ProjectWorkspacePath(agentDir, projectID string) string {
	return filepath.Join(ProjectDir(agentDir, projectID), "Workspace")
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

// ---------------------------------------------------------------------------
// No-Project paths
// ---------------------------------------------------------------------------

// NoProjectSessionWorkspace returns the isolated workspace for a No-Project session.
func NoProjectSessionWorkspace(agentDir, sessionID string) string {
	return filepath.Join(ProjectDir(agentDir, NoProjectID), sessionID, "workspace")
}
