package core

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"github.com/v0lka/c0wrk/core/sdd"
	"github.com/v0lka/sp4rk/skills"
)

// writeTestSkill creates <dir>/<name>/SKILL.md with a minimal valid frontmatter
// and returns the skill directory.
func writeTestSkill(t *testing.T, dir, name, desc string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", skillDir, err)
	}
	content := "---\nname: " + name + "\ndescription: \"" + desc + "\"\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile SKILL.md: %v", err)
	}
	return skillDir
}

func skillNamesFor(t *testing.T, descriptors []skills.SkillDescriptor) []string {
	t.Helper()
	names := make([]string, 0, len(descriptors))
	for _, d := range descriptors {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	return names
}

// TestProjectSkillDirs_NoFramework pins the negative case: a workspace that
// matches no framework declares no extra directories, so discovery stays
// exactly the .agents/skills + base-dir set it was before SDD detection.
func TestProjectSkillDirs_NoFramework(t *testing.T) {
	ws := t.TempDir()
	if got := ProjectSkillDirs(ws, quietLogger()); got != nil {
		t.Fatalf("ProjectSkillDirs(empty workspace) = %v, want nil", got)
	}
	// The workspace argument is required: no workspace, no framework dirs.
	if got := ProjectSkillDirs("", quietLogger()); got != nil {
		t.Fatalf("ProjectSkillDirs(\"\") = %v, want nil", got)
	}
}

// TestProjectSkillDirs_FrameworkSkillOutsideAgents is the core of the
// "installed outside .agents/skills" behavior: a framework skill sitting in
// .claude/skills — with none of the framework's marker files written — is
// enough for the framework to be present, so .claude/skills enters the
// discovery set.
func TestProjectSkillDirs_FrameworkSkillOutsideAgents(t *testing.T) {
	ws := t.TempDir()
	writeTestSkill(t, filepath.Join(ws, ".claude", "skills"), "openspec-propose", "Propose a change")

	got := ProjectSkillDirs(ws, quietLogger())
	want := []string{
		filepath.Join(ws, ".agents", "skills"),
		filepath.Join(ws, ".claude", "skills"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProjectSkillDirs = %v, want %v", got, want)
	}
}

// TestProjectSkillDirs_MarkerOnly verifies the marker signal: a workspace
// carrying the framework's marker files (and no installed skill at all) still
// contributes the framework's declared skill directories, so a skill installed
// later is picked up on the next scan without a detection change.
func TestProjectSkillDirs_MarkerOnly(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "specs"), 0o755); err != nil {
		t.Fatalf("MkdirAll specs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "specs", "META.md"), []byte("# Meta\n"), 0o644); err != nil {
		t.Fatalf("WriteFile META.md: %v", err)
	}

	got := ProjectSkillDirs(ws, quietLogger())
	want := []string{
		filepath.Join(ws, ".agents", "skills"),
		filepath.Join(ws, ".claude", "skills"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProjectSkillDirs = %v, want %v", got, want)
	}
}

// TestProjectSkillDirs_SpecKitMarkers covers a second framework so the set is
// not accidentally hard-wired to the sdd.yaml document.
func TestProjectSkillDirs_SpecKitMarkers(t *testing.T) {
	ws := t.TempDir()
	memory := filepath.Join(ws, ".specify", "memory")
	if err := os.MkdirAll(memory, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", memory, err)
	}
	if err := os.WriteFile(filepath.Join(memory, "constitution.md"), []byte("# Constitution\n"), 0o644); err != nil {
		t.Fatalf("WriteFile constitution.md: %v", err)
	}

	got := ProjectSkillDirs(ws, quietLogger())
	want := []string{
		filepath.Join(ws, ".agents", "skills"),
		filepath.Join(ws, ".claude", "skills"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProjectSkillDirs = %v, want %v", got, want)
	}
}

// TestProjectSkillDirsFrom_ExplicitCatalog covers the catalog seam (a merged
// built-in + operator catalog can be passed explicitly) and the declared
// skill-set probe: AllOf satisfied is a presence signal on its own, while an
// unsatisfied set over a directory holding no skill contributes nothing.
func TestProjectSkillDirsFrom_ExplicitCatalog(t *testing.T) {
	ws := t.TempDir()
	writeTestSkill(t, filepath.Join(ws, "custom", "skills"), "op-skill", "Operator skill")

	fw := sdd.Framework{
		ID:    "operator",
		Label: "Operator",
		Detect: sdd.Detect{Skills: sdd.Skills{
			Dirs:  []string{"custom/skills"},
			AllOf: []string{"op-skill"},
		}},
	}
	got := ProjectSkillDirsFrom(ws, []sdd.Framework{fw})
	want := []string{filepath.Join(ws, "custom", "skills")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProjectSkillDirsFrom = %v, want %v", got, want)
	}

	// AnyOf is not satisfied and the directory holds no skill: absent.
	empty := t.TempDir()
	unmatched := sdd.Framework{
		ID:    "operator",
		Label: "Operator",
		Detect: sdd.Detect{Skills: sdd.Skills{
			Dirs:  []string{"custom/skills"},
			AnyOf: []string{"op-apply", "op-archive"},
		}},
	}
	if got := ProjectSkillDirsFrom(empty, []sdd.Framework{unmatched}); got != nil {
		t.Fatalf("ProjectSkillDirsFrom (unmatched) = %v, want nil", got)
	}

	// No workspace, no set — even with a matching framework.
	if got := ProjectSkillDirsFrom("", []sdd.Framework{fw}); got != nil {
		t.Fatalf("ProjectSkillDirsFrom(\"\") = %v, want nil", got)
	}
}

// TestResolveSkillDirEntry pins the path semantics of a framework-declared
// directory: clean workspace-relative entries resolve under the workspace,
// and every shape the DSL loader rejects at load ("~"-relative, absolute) or
// that would escape the workspace (traversal, a symlink pointing outside) is
// rejected here too — the runtime resolver never widens discovery beyond the
// workspace on a shape the declaration could not legally express.
func TestResolveSkillDirEntry(t *testing.T) {
	// The spelling is platform-produced (filepath.Join / a genuinely absolute
	// path of the running OS), not a literal: filepath.Clean and Join emit the
	// OS separator, and an "absolute" entry is only absolute on its own
	// platform (a POSIX spelling is a relative path on Windows).
	ws := filepath.Join(string(os.PathSeparator), "work", "project")
	absolute := filepath.Join(t.TempDir(), "skills")
	tests := []struct {
		name   string
		entry  string
		ws     string
		want   string
		wantOK bool
	}{
		{name: "workspace relative", entry: filepath.Join(".claude", "skills"), ws: ws,
			want: filepath.Join(ws, ".claude", "skills"), wantOK: true},
		{name: "nested workspace relative", entry: filepath.Join(".foo", "bar", "skills"), ws: ws,
			want: filepath.Join(ws, ".foo", "bar", "skills"), wantOK: true},
		{name: "home relative", entry: "~/" + filepath.Join(".claude", "skills"), ws: ws,
			wantOK: false},
		{name: "home alone", entry: "~", ws: ws, wantOK: false},
		{name: "absolute", entry: absolute, ws: ws, wantOK: false},
		{name: "absolute needs no workspace", entry: absolute, ws: "", wantOK: false},
		{name: "traversal", entry: filepath.Join("..", "escape", "skills"), ws: ws, wantOK: false},
		{name: "embedded traversal", entry: "a/../../b/skills", ws: ws, wantOK: false},
		{name: "empty", entry: "   ", ws: ws, wantOK: false},
		{name: "relative without workspace", entry: filepath.Join(".claude", "skills"), ws: "",
			wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveSkillDirEntry(tt.entry, tt.ws)
			if ok != tt.wantOK {
				t.Fatalf("resolveSkillDirEntry(%q) ok = %v, want %v (path %q)", tt.entry, ok, tt.wantOK, got)
			}
			if ok && got != tt.want {
				t.Errorf("resolveSkillDirEntry(%q) = %q, want %q", tt.entry, got, tt.want)
			}
		})
	}
}

// TestResolveSkillDirEntry_SymlinkEscape pins the containment property the
// segment check alone could not give: a declared directory that resolves
// through a workspace-internal symlink pointing OUTSIDE the workspace is
// rejected, so a framework document cannot smuggle an outside directory into
// discovery through a link it planted (or found) inside the tree.
func TestResolveSkillDirEntry_SymlinkEscape(t *testing.T) {
	if os.Getenv("CGO_ENABLED") == "0" && runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, ".claude", "escape")); err != nil {
		t.Fatal(err)
	}
	if got, ok := resolveSkillDirEntry(filepath.Join(".claude", "escape", "skills"), ws); ok {
		t.Fatalf("resolveSkillDirEntry through an outside-pointing symlink = %q, want rejected", got)
	}
	// A link that stays inside the workspace resolves fine.
	if err := os.Symlink(filepath.Join(ws, ".claude"), filepath.Join(ws, ".claude-link")); err != nil {
		t.Fatal(err)
	}
	got, ok := resolveSkillDirEntry(filepath.Join(".claude-link", "skills"), ws)
	if !ok || got != filepath.Join(ws, ".claude-link", "skills") {
		t.Fatalf("resolveSkillDirEntry internal link = %q ok = %v, want %q ok", got, ok, filepath.Join(ws, ".claude-link", "skills"))
	}
}

// TestDedupeSkillDirs verifies the set normalizer: empty entries are dropped,
// paths are cleaned, and the first (highest-priority) occurrence wins.
func TestDedupeSkillDirs(t *testing.T) {
	a := filepath.Join(string(os.PathSeparator), "a")
	b := filepath.Join(string(os.PathSeparator), "b")
	got := dedupeSkillDirs([]string{a, filepath.Join(a, "."), "", "  ", b, filepath.Join(a, "."), b})
	want := []string{a, b}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupeSkillDirs = %v, want %v", got, want)
	}
	if got := dedupeSkillDirs(nil); got != nil {
		t.Fatalf("dedupeSkillDirs(nil) = %v, want nil", got)
	}
	if got := dedupeSkillDirs([]string{"", "  "}); got != nil {
		t.Fatalf("dedupeSkillDirs(empties) = %v, want nil", got)
	}
}

// TestOrchestratorBuilder_SkillDirsForWorkspace pins the ordering of the whole
// discovery set — project .agents/skills, the detected framework's declared
// dirs, then the shared base dirs — and its deduplication when a base dir
// repeats a workspace dir.
func TestOrchestratorBuilder_SkillDirsForWorkspace(t *testing.T) {
	ws := t.TempDir()
	writeTestSkill(t, filepath.Join(ws, ".claude", "skills"), "openspec-propose", "Propose a change")
	globalSkills := t.TempDir()

	b := &OrchestratorBuilder{
		logger:        quietLogger(),
		baseSkillDirs: []string{filepath.Join(ws, ".claude", "skills"), globalSkills},
	}

	got := b.SkillDirsForWorkspace(ws, ws)
	want := []string{
		filepath.Join(ws, ".agents", "skills"),
		filepath.Join(ws, ".claude", "skills"),
		globalSkills,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SkillDirsForWorkspace = %v, want %v", got, want)
	}

	// CHAT mode (no workspace): the shared base dirs alone, deduplicated.
	if got := b.SkillDirsForWorkspace("", ""); !reflect.DeepEqual(got, want[1:]) {
		t.Fatalf("SkillDirsForWorkspace(\"\") = %v, want %v", got, want[1:])
	}

	// A builder with nothing configured yields no set at all (nil, not [""])
	// so the callers can distinguish "no discovery" from "one empty dir".
	if got := (&OrchestratorBuilder{logger: quietLogger()}).SkillDirsForWorkspace("", ""); got != nil {
		t.Fatalf("empty builder SkillDirsForWorkspace = %v, want nil", got)
	}
}

// TestOrchestratorBuilder_GetSkillDescriptors_TakesFullSet verifies the
// descriptor scan consumes the whole set: a skill living only in the
// framework-declared directory (the ListSkills RPC path) is listed, and an
// empty set falls back to the shared base dirs.
func TestOrchestratorBuilder_GetSkillDescriptors_TakesFullSet(t *testing.T) {
	ws := t.TempDir()
	writeTestSkill(t, filepath.Join(ws, ".claude", "skills"), "openspec-propose", "Propose a change")
	writeTestSkill(t, filepath.Join(ws, ".agents", "skills"), "project-skill", "Project local")
	globalSkills := t.TempDir()
	writeTestSkill(t, globalSkills, "global-skill", "Global")

	b := &OrchestratorBuilder{logger: quietLogger(), baseSkillDirs: []string{globalSkills}}

	got := skillNamesFor(t, b.GetSkillDescriptors(b.SkillDirsForWorkspace(ws, ws)))
	want := []string{"global-skill", "openspec-propose", "project-skill"} // SkillManager.List is name-sorted
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetSkillDescriptors(SkillDirsForWorkspace) = %v, want %v", got, want)
	}

	// An empty set keeps the pre-SDD behavior: the shared base dirs are still
	// scanned for a caller with no workspace.
	got = skillNamesFor(t, b.GetSkillDescriptors(nil))
	if !reflect.DeepEqual(got, []string{"global-skill"}) {
		t.Fatalf("GetSkillDescriptors(nil) = %v, want [global-skill]", got)
	}
}

// TestOrchestratorBuilder_BuildSessionSkillManager_FrameworkDirs is the
// activation half of the feature: a skill installed only in .claude/skills
// resolves by name from the per-session SkillManager, so a
// sendMessage(activeSkills: ["openspec-propose"]) activates it. Precedence over
// the base dirs is preserved — the workspace copy wins.
func TestOrchestratorBuilder_BuildSessionSkillManager_FrameworkDirs(t *testing.T) {
	ws := t.TempDir()
	frameworkSkills := filepath.Join(ws, ".claude", "skills")
	writeTestSkill(t, frameworkSkills, "openspec-propose", "Propose a change")
	wantDir := filepath.Join(frameworkSkills, "openspec-propose")
	globalSkills := t.TempDir()
	writeTestSkill(t, globalSkills, "openspec-propose", "Stale global copy")
	writeTestSkill(t, globalSkills, "global-only", "Global")

	b := &OrchestratorBuilder{logger: quietLogger(), baseSkillDirs: []string{globalSkills}}
	sm := b.buildSessionSkillManager(ws, ws, quietLogger())
	if sm == nil {
		t.Fatal("buildSessionSkillManager returned nil for a workspace with framework skills")
	}

	s, ok := sm.Get("openspec-propose")
	if !ok {
		t.Fatal("openspec-propose (only in .claude/skills) did not resolve — activeSkills activation would drop it")
	}
	if s.Metadata.Description != "Propose a change" {
		t.Errorf("resolved skill description = %q, want the workspace copy", s.Metadata.Description)
	}
	if gotPath, ok := sm.SkillPath("openspec-propose"); !ok || gotPath != wantDir {
		t.Errorf("SkillPath = %q (ok=%v), want the .claude/skills copy %q", gotPath, ok, wantDir)
	}
	// The base dirs stay reachable through the same manager.
	if _, ok := sm.Get("global-only"); !ok {
		t.Error("global-only (shared base dir) did not resolve")
	}

	// The three stock roots keep their precedence: .agents/skills outranks the
	// framework dir, which outranks the shared base dirs.
	projectSkills := filepath.Join(ws, ".agents", "skills")
	writeTestSkill(t, projectSkills, "openspec-propose", "Project local copy")
	projectDir := filepath.Join(projectSkills, "openspec-propose")
	sm = b.buildSessionSkillManager(ws, ws, quietLogger())
	if gotPath, ok := sm.SkillPath("openspec-propose"); !ok || gotPath != projectDir {
		t.Errorf("SkillPath = %q (ok=%v), want the project copy %q", gotPath, ok, projectDir)
	}
}
