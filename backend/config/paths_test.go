package config

import (
	"path/filepath"
	"testing"
)

var testAgentDir = filepath.Join("home", "user", ".c0wrk")

func TestConfigPath(t *testing.T) {
	got := ConfigPath(testAgentDir)
	want := filepath.Join(testAgentDir, "config.yaml")
	if got != want {
		t.Errorf("ConfigPath: got %q, want %q", got, want)
	}
}

func TestDatabasePath(t *testing.T) {
	got := DatabasePath(testAgentDir)
	want := filepath.Join(testAgentDir, "database.db")
	if got != want {
		t.Errorf("DatabasePath: got %q, want %q", got, want)
	}
}

func TestLogsDir(t *testing.T) {
	got := LogsDir(testAgentDir)
	want := filepath.Join(testAgentDir, "logs")
	if got != want {
		t.Errorf("LogsDir: got %q, want %q", got, want)
	}
}

func TestSkillsDir(t *testing.T) {
	got := SkillsDir(testAgentDir)
	want := filepath.Join(testAgentDir, ".agents", "skills")
	if got != want {
		t.Errorf("SkillsDir: got %q, want %q", got, want)
	}
}

func TestProjectsDir(t *testing.T) {
	got := ProjectsDir(testAgentDir)
	want := filepath.Join(testAgentDir, "projects")
	if got != want {
		t.Errorf("ProjectsDir: got %q, want %q", got, want)
	}
}

func TestModelsDir(t *testing.T) {
	got := ModelsDir(testAgentDir)
	want := filepath.Join(testAgentDir, "models")
	if got != want {
		t.Errorf("ModelsDir: got %q, want %q", got, want)
	}
}

func TestProjectDir(t *testing.T) {
	got := ProjectDir(testAgentDir, "proj-123")
	want := filepath.Join(testAgentDir, "projects", "proj-123")
	if got != want {
		t.Errorf("ProjectDir: got %q, want %q", got, want)
	}
}

func TestProjectWorkspacePath(t *testing.T) {
	got := ProjectWorkspacePath(testAgentDir, "proj-123")
	want := filepath.Join(testAgentDir, "projects", "proj-123", "Workspace")
	if got != want {
		t.Errorf("ProjectWorkspacePath: got %q, want %q", got, want)
	}
}

func TestProjectVectorIndexPath(t *testing.T) {
	got := ProjectVectorIndexPath(testAgentDir, "proj-123")
	want := filepath.Join(testAgentDir, "projects", "proj-123", "vector_index")
	if got != want {
		t.Errorf("ProjectVectorIndexPath: got %q, want %q", got, want)
	}
}

func TestSessionDir(t *testing.T) {
	got := SessionDir(testAgentDir, "proj-123", "sess-456")
	want := filepath.Join(testAgentDir, "projects", "proj-123", "sess-456")
	if got != want {
		t.Errorf("SessionDir: got %q, want %q", got, want)
	}
}

func TestSessionLogsDir(t *testing.T) {
	got := SessionLogsDir(testAgentDir, "proj-123", "sess-456")
	want := filepath.Join(testAgentDir, "projects", "proj-123", "sess-456", "logs")
	if got != want {
		t.Errorf("SessionLogsDir: got %q, want %q", got, want)
	}
}

func TestSessionLogPath(t *testing.T) {
	got := SessionLogPath(testAgentDir, "proj-123", "sess-456")
	want := filepath.Join(testAgentDir, "projects", "proj-123", "sess-456", "logs", "session_sess-456.log")
	if got != want {
		t.Errorf("SessionLogPath: got %q, want %q", got, want)
	}
}

func TestSessionDumpPath(t *testing.T) {
	got := SessionDumpPath(testAgentDir, "proj-123", "sess-456")
	want := filepath.Join(testAgentDir, "projects", "proj-123", "sess-456", "dumps", "session_sess-456_llm_dump.jsonl")
	if got != want {
		t.Errorf("SessionDumpPath: got %q, want %q", got, want)
	}
}

func TestSessionTempDir(t *testing.T) {
	got := SessionTempDir(testAgentDir, "proj-123", "sess-456")
	want := filepath.Join(testAgentDir, "projects", "proj-123", "sess-456", "temp")
	if got != want {
		t.Errorf("SessionTempDir: got %q, want %q", got, want)
	}
}

func TestSessionPlansDir(t *testing.T) {
	got := SessionPlansDir(testAgentDir, "proj-123", "sess-456")
	want := filepath.Join(testAgentDir, "projects", "proj-123", "sess-456", "plans")
	if got != want {
		t.Errorf("SessionPlansDir: got %q, want %q", got, want)
	}
}

func TestNoProjectSessionWorkspace(t *testing.T) {
	got := NoProjectSessionWorkspace(testAgentDir, "sess-456")
	want := filepath.Join(testAgentDir, "projects", noProjectID, "sess-456", "workspace")
	if got != want {
		t.Errorf("NoProjectSessionWorkspace: got %q, want %q", got, want)
	}
}
