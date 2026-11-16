package core

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/sdd"
	"github.com/v0lka/sp4rk/skills"
)

// quietLogger returns a logger whose output is discarded. The skill-catalog
// scan logs an Info line per scan; that incidental noise is not part of the
// behavior under test here (the precedence and equivalence assertions are),
// so the tests do not assert on it.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// canonicalTempRoot returns the symlink-resolved form of a t.TempDir()
// directory. The SkillManager scan canonicalizes every discovery root through
// filepath.EvalSymlinks (sp4rk/skills/manager.go), so SkillPath — and the
// resolved resource path built on top of it — always carries the canonical
// prefix. On macOS that differs from t.TempDir()'s raw form (/var/folders/…
// vs /private/var/folders/…, /var is a symlink to /private/var). Canonicalizing
// the expected roots keeps the string comparisons platform-independent (an
// identity on Linux, matching sp4rk/skills/extra_test.go's precedent).
func canonicalTempRoot(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks %s: %v", dir, err)
	}
	return resolved
}

// writeSkillWithResource creates a skill directory containing a minimal valid
// SKILL.md plus references/api.md with the given content, and returns the
// resource's absolute path.
func writeSkillWithResource(t *testing.T, skillsDir, name, resource string) string {
	t.Helper()
	skillDir := filepath.Join(skillsDir, name)
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", skillDir, err)
	}
	skillMD := "---\nname: " + name + "\ndescription: \"test skill\"\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatalf("WriteFile SKILL.md: %v", err)
	}
	resPath := filepath.Join(skillDir, "references", "api.md")
	if err := os.WriteFile(resPath, []byte(resource), 0o644); err != nil {
		t.Fatalf("WriteFile resource: %v", err)
	}
	return resPath
}

// execReadSkillResourceTool runs the real read_skill_resource tool against a
// resolver that mirrors activeSkillPathResolver's contract: the activated
// skill's DirPath comes from the session's SkillManager scan. Returns the
// content the tool actually read.
func execReadSkillResourceTool(t *testing.T, sm *skills.SkillManager, skillName, relPath string) string {
	t.Helper()
	tool := skills.NewReadSkillResourceTool(func(_ context.Context, name string) (string, bool) {
		return sm.SkillPath(name)
	})
	input, err := json.Marshal(map[string]string{"skill": skillName, "path": relPath})
	if err != nil {
		t.Fatalf("marshal tool input: %v", err)
	}
	res, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("tool.Execute: %v", err)
	}
	if strings.HasPrefix(res.Content, "Error:") || strings.HasPrefix(res.Content, "skill ") {
		t.Fatalf("tool reported failure: %q", res.Content)
	}
	return res.Content
}

// TestOrchestratorBuilder_ResolveSkillResourcePath verifies the builder
// method behind the frontend ResolveSkillResourcePath RPC:
//
//  1. Precedence — with the same skill present in the project-local and a
//     shared base directory, the project-local occurrence wins (highest
//     priority first), matching what the tool's per-session SkillManager
//     hands to the read_skill_resource tool.
//  2. Equivalence — the returned path's content is byte-identical to what
//     the real tool read for the same (skill, path) arguments.
//  3. A skill that exists only in the base dirs still resolves when the
//     project-local dir is set.
//  4. Traversal outside the skill directory is refused (SafeResolvePath),
//     unknown skills are refused, and an empty discovery set is refused.
func TestOrchestratorBuilder_ResolveSkillResourcePath(t *testing.T) {
	workspace := canonicalTempRoot(t, t.TempDir())
	projectSkills := filepath.Join(workspace, ".agents", "skills")
	frameworkSkills := filepath.Join(workspace, ".claude", "skills")
	globalSkills := canonicalTempRoot(t, t.TempDir())

	const (
		projectResource = "project-local api reference"
		globalResource  = "global api reference"
	)
	projectRes := writeSkillWithResource(t, projectSkills, "dup-skill", projectResource)
	globalRes := writeSkillWithResource(t, globalSkills, "dup-skill", globalResource)
	onlyGlobalRes := writeSkillWithResource(t, globalSkills, "base-only", "base-only reference")
	// A skill installed ONLY in a framework-declared directory: activation
	// and resource resolution must see it through the same discovery set.
	onlyFrameworkRes := writeSkillWithResource(t, frameworkSkills, "fw-only", "framework api reference")

	b := &OrchestratorBuilder{
		logger:        quietLogger(),
		baseSkillDirs: []string{globalSkills},
	}
	// A framework present in the workspace declares .claude/skills, so the
	// discovery set joins it between the project-local and the base dirs.
	b.SetSDDCatalog(func(string) []sdd.Framework {
		return []sdd.Framework{{
			ID:    "probe",
			Label: "Probe",
			Detect: sdd.Detect{
				Markers: []string{".specify/marker"},
				Skills:  sdd.Skills{Dirs: []string{".claude/skills"}, AnyOf: []string{"fw-only"}},
			},
		}}
	})
	if err := os.MkdirAll(filepath.Join(workspace, ".specify"), 0o755); err != nil {
		t.Fatalf("MkdirAll marker dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".specify", "marker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile marker: %v", err)
	}

	// 1+2. Precedence and tool equivalence for the skill present in both
	// catalogs: the project-local copy must win, and the RPC path must be the
	// file the tool actually read.
	got, err := b.ResolveSkillResourcePath(workspace, "dup-skill", "references/api.md")
	if err != nil {
		t.Fatalf("ResolveSkillResourcePath: %v", err)
	}
	if got != projectRes {
		t.Errorf("resolved %q, want project-local %q (not global %q)", got, projectRes, globalRes)
	}
	gotBytes, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("ReadFile resolved path: %v", err)
	}
	if string(gotBytes) != projectResource {
		t.Errorf("resolved file content = %q, want %q", gotBytes, projectResource)
	}
	sm := skills.NewSkillManager(b.SkillDirsForWorkspace(workspace, workspace), quietLogger())
	if err := sm.Scan(); err != nil {
		t.Fatalf("SkillManager.Scan: %v", err)
	}
	if toolRead := execReadSkillResourceTool(t, sm, "dup-skill", "references/api.md"); toolRead != string(gotBytes) {
		t.Errorf("tool read %q but RPC path carries %q", toolRead, gotBytes)
	}

	// 3. Base-only skill resolves through the shared dirs even with a
	// project-local dir in front.
	got, err = b.ResolveSkillResourcePath(workspace, "base-only", "references/api.md")
	if err != nil {
		t.Fatalf("ResolveSkillResourcePath base-only: %v", err)
	}
	if got != onlyGlobalRes {
		t.Errorf("base-only resolved %q, want %q", got, onlyGlobalRes)
	}

	// 3b. A skill installed ONLY in a framework-declared directory resolves:
	// the RPC shares the session discovery set (project-local, framework
	// dirs, base dirs) rather than .agents/skills plus the base dirs alone.
	got, err = b.ResolveSkillResourcePath(workspace, "fw-only", "references/api.md")
	if err != nil {
		t.Fatalf("ResolveSkillResourcePath fw-only: %v", err)
	}
	if got != onlyFrameworkRes {
		t.Errorf("fw-only resolved %q, want %q", got, onlyFrameworkRes)
	}

	// 4a. Path traversal out of the skill directory is refused.
	if _, err := b.ResolveSkillResourcePath(workspace, "dup-skill", "../../escape.md"); err == nil {
		t.Error("expected traversal outside the skill directory to be refused")
	}

	// 4b. Unknown skill is refused.
	if _, err := b.ResolveSkillResourcePath(workspace, "no-such-skill", "references/api.md"); err == nil {
		t.Error("expected unknown skill to be refused")
	}

	// 4c. Empty arguments are refused.
	if _, err := b.ResolveSkillResourcePath(workspace, "", "references/api.md"); err == nil {
		t.Error("expected empty skill name to be refused")
	}
	if _, err := b.ResolveSkillResourcePath(workspace, "dup-skill", ""); err == nil {
		t.Error("expected empty resource path to be refused")
	}

	// 4d. No discovery set at all is refused.
	empty := &OrchestratorBuilder{logger: quietLogger()}
	if _, err := empty.ResolveSkillResourcePath("", "dup-skill", "references/api.md"); err == nil {
		t.Error("expected empty discovery set to be refused")
	}
}
