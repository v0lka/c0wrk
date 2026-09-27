package config

import (
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/core/embeddedllm"
	"github.com/v0lka/c0wrk/internal/sysproc"
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

func TestAgentsDir(t *testing.T) {
	got := AgentsDir(testAgentDir)
	want := filepath.Join(testAgentDir, ".agents", "agents")
	if got != want {
		t.Errorf("AgentsDir: got %q, want %q", got, want)
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
	want := filepath.Join(testAgentDir, "projects", "proj-123", WorkspaceSegment)
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

func TestProjectEmbeddingCachePath(t *testing.T) {
	got := ProjectEmbeddingCachePath(testAgentDir, "proj-123")
	want := filepath.Join(testAgentDir, "projects", "proj-123", "vector_index", "embedding_cache")
	if got != want {
		t.Errorf("ProjectEmbeddingCachePath: got %q, want %q", got, want)
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
	want := filepath.Join(testAgentDir, "projects", noProjectID, "sess-456", NoProjectWorkspaceSegment)
	if got != want {
		t.Errorf("NoProjectSessionWorkspace: got %q, want %q", got, want)
	}
}

func TestSessionWorkspaceRoot(t *testing.T) {
	t.Run("regular project", func(t *testing.T) {
		got := SessionWorkspaceRoot(testAgentDir, "proj-1", "sess-1")
		want := filepath.Join(testAgentDir, "projects", "proj-1", WorkspaceSegment)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("no project", func(t *testing.T) {
		got := SessionWorkspaceRoot(testAgentDir, noProjectID, "sess-1")
		want := filepath.Join(testAgentDir, "projects", noProjectID, "sess-1", NoProjectWorkspaceSegment)
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestProjectResearchPath(t *testing.T) {
	ws := filepath.Join(testAgentDir, "projects", "proj-123", WorkspaceSegment)
	got := ProjectResearchPath(ws)
	want := filepath.Join(ws, ".research")
	if got != want {
		t.Errorf("ProjectResearchPath: got %q, want %q", got, want)
	}
}

func TestPaperLibraryPath(t *testing.T) {
	ws := filepath.Join(testAgentDir, "projects", "proj-123", WorkspaceSegment)
	got := PaperLibraryPath(ws)
	want := filepath.Join(ws, ".research", "papers")
	if got != want {
		t.Errorf("PaperLibraryPath: got %q, want %q", got, want)
	}
}

func TestComparisonsPath(t *testing.T) {
	ws := filepath.Join(testAgentDir, "projects", "proj-123", WorkspaceSegment)
	got := ComparisonsPath(ws)
	want := filepath.Join(ws, ".research", "comparisons")
	if got != want {
		t.Errorf("ComparisonsPath: got %q, want %q", got, want)
	}
}

func TestComparisonsPathIn(t *testing.T) {
	researchRoot := filepath.Join(testAgentDir, "projects", "proj-123", WorkspaceSegment, ".research")
	got := ComparisonsPathIn(researchRoot)
	want := filepath.Join(researchRoot, "comparisons")
	if got != want {
		t.Errorf("ComparisonsPathIn: got %q, want %q", got, want)
	}
	// The comparisons directory sits beside the paper library (both are direct
	// children of the research root), not inside any paper directory.
	if filepath.Dir(got) != researchRoot {
		t.Errorf("ComparisonsPathIn: %q is not a direct child of the research root %q", got, researchRoot)
	}
	if got == PaperLibraryPathIn(researchRoot) {
		t.Errorf("ComparisonsPathIn: must not collide with the paper library %q", got)
	}
}

func TestValidateWithinSessionWorkspace(t *testing.T) {
	t.Run("path within session workspace", func(t *testing.T) {
		wsRoot := SessionWorkspaceRoot(testAgentDir, noProjectID, "sess-1")
		absPath := filepath.Join(wsRoot, "file.txt")
		if err := ValidateWithinSessionWorkspace(testAgentDir, noProjectID, "sess-1", absPath); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("path outside session workspace", func(t *testing.T) {
		wsRoot := SessionWorkspaceRoot(testAgentDir, noProjectID, "sess-1")
		absPath := filepath.Join(wsRoot, "..", "sess-2", NoProjectWorkspaceSegment, "file.txt")
		if err := ValidateWithinSessionWorkspace(testAgentDir, noProjectID, "sess-1", absPath); err == nil {
			t.Error("expected error for path outside session workspace")
		}
	})
	t.Run("path to other session logs", func(t *testing.T) {
		absPath := filepath.Join(testAgentDir, "projects", noProjectID, "sess-1", "logs", "session.log")
		if err := ValidateWithinSessionWorkspace(testAgentDir, noProjectID, "sess-1", absPath); err == nil {
			t.Error("expected error for path to session logs")
		}
	})
	t.Run("regular project validates against project workspace", func(t *testing.T) {
		wsRoot := SessionWorkspaceRoot(testAgentDir, "proj-1", "sess-1")
		absPath := filepath.Join(wsRoot, "file.txt")
		if err := ValidateWithinSessionWorkspace(testAgentDir, "proj-1", "sess-1", absPath); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// TestDefaultAgentDirMirrorsSysproc pins the duplicated agent-dir literal in
// internal/sysproc (which cannot import backend/config) to this package's
// canonical DefaultAgentDir, so the safe git hooks directory created by
// sysproc.GitCmd always lives under ~/.c0wrk.
func TestDefaultAgentDirMirrorsSysproc(t *testing.T) {
	if sysproc.DefaultAgentDirName != DefaultAgentDir {
		t.Errorf("sysproc.DefaultAgentDirName = %q, want DefaultAgentDir %q", sysproc.DefaultAgentDirName, DefaultAgentDir)
	}
}

// TestEmbeddedLLMDirs pins the embedded-LLM storage roots to the documented
// shape (~/.c0wrk/runtimes and ~/.c0wrk/models/bonsai-2-27b) and, more
// importantly, to the agent-isolation invariant: neither root may live under
// ToolsBinDir, which Manager.PrependToPATH() exposes to the agent's bash_exec
// (ADR-066 D3, ASI05). The weights must also stay inside a dedicated
// subdirectory of ModelsDir so removing them can never touch the flat
// embedding-model files.
func TestEmbeddedLLMDirs(t *testing.T) {
	if got, want := RuntimesDir(testAgentDir), filepath.Join(testAgentDir, "runtimes"); got != want {
		t.Errorf("RuntimesDir: got %q, want %q", got, want)
	}
	if got, want := EmbeddedModelDir(testAgentDir),
		filepath.Join(testAgentDir, "models", "bonsai-2-27b"); got != want {
		t.Errorf("EmbeddedModelDir: got %q, want %q", got, want)
	}

	toolsBin := ToolsBinDir(testAgentDir)
	for name, dir := range map[string]string{
		"RuntimesDir":      RuntimesDir(testAgentDir),
		"EmbeddedModelDir": EmbeddedModelDir(testAgentDir),
	} {
		if within, err := IsWithinPath(toolsBin, dir); err != nil {
			t.Errorf("%s: containment check against the agent PATH failed: %v", name, err)
		} else if within {
			t.Errorf("%s = %q is inside the agent PATH %q — the inference runtime "+
				"would become agent-invokable", name, dir, toolsBin)
		}
		if within, err := IsWithinPath(ToolsDir(testAgentDir), dir); err != nil {
			t.Errorf("%s: containment check against the tools dir failed: %v", name, err)
		} else if within {
			t.Errorf("%s = %q is inside the managed-tools tree %q", name, dir, ToolsDir(testAgentDir))
		}
	}

	// The weights live in a dedicated subdirectory of the models root, never at
	// its top level.
	if within, err := IsWithinPath(ModelsDir(testAgentDir), EmbeddedModelDir(testAgentDir)); err != nil {
		t.Errorf("EmbeddedModelDir containment: %v", err)
	} else if !within {
		t.Errorf("EmbeddedModelDir = %q is not inside ModelsDir %q",
			EmbeddedModelDir(testAgentDir), ModelsDir(testAgentDir))
	}
	if EmbeddedModelDir(testAgentDir) == ModelsDir(testAgentDir) {
		t.Error("EmbeddedModelDir equals ModelsDir; removing the embedded model would delete the embedding model")
	}
}

// TestEmbeddedAutoUnloadDefaultMirrorsCore pins the auto-unload default that
// core/embeddedllm hands to the config sink on install. core cannot import
// this package (backend/config sits above core and already imports core
// packages, so an import back would cycle), so the value is declared on both
// sides of the boundary — the same situation TestDefaultAgentDirMirrorsSysproc
// covers for internal/sysproc.
func TestEmbeddedAutoUnloadDefaultMirrorsCore(t *testing.T) {
	if embeddedllm.DefaultAutoUnloadMinutes != EmbeddedLLMDefaultAutoUnloadMinutes {
		t.Errorf("embeddedllm.DefaultAutoUnloadMinutes = %d, want EmbeddedLLMDefaultAutoUnloadMinutes %d",
			embeddedllm.DefaultAutoUnloadMinutes, EmbeddedLLMDefaultAutoUnloadMinutes)
	}
	if !embeddedllm.DefaultAutoUnloadEnabled {
		t.Error("embeddedllm.DefaultAutoUnloadEnabled = false, want the documented default true")
	}
}
