package backend

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/papers"
	"github.com/v0lka/c0wrk/core/research"
)

// seedVersionMarker is the sidecar marker filename every pack seeder writes
// into a seeded directory (unexported in both pack packages; mirrored here
// for assertions).
const seedVersionMarker = ".seed-version"

// seedGlobalPacksTestFrontend builds the minimal FrontendAPI for exercising
// the launch-time global seed: a temp agent directory plus a one-real-project
// store over a temp workspace.
func seedGlobalPacksTestFrontend(t *testing.T) (f *FrontendAPI, projectID, ws, agentDir string) {
	t.Helper()
	base := t.TempDir()
	agentDir = filepath.Join(base, "agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agentDir: %v", err)
	}
	ws = filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("create project store: %v", err)
	}
	if err := store.SaveProject(context.Background(), project.ProjectInfo{
		ID:            "proj-1",
		Name:          "Seeded",
		WorkspacePath: ws,
	}); err != nil {
		t.Fatalf("save project: %v", err)
	}
	f = &FrontendAPI{
		projectManager: project.NewManager(store, base, nil),
		projStore:      store,
		agentDir:       agentDir,
		emitEvent:      func(string, ...any) {},
	}
	return f, "proj-1", ws, agentDir
}

// TestSeedGlobalPacks_SeedsAllPacksGlobally pins the launch-time seeding: every
// c0wrk-owned pack lands in the GLOBAL agent directories —
// <agentDir>/.agents/skills (the research-* methodology skills + study-paper)
// and <agentDir>/.agents/agents (the research Subagent Profile) — carrying the
// current pack-version markers.
func TestSeedGlobalPacks_SeedsAllPacksGlobally(t *testing.T) {
	f, _, _, agentDir := seedGlobalPacksTestFrontend(t)

	f.seedGlobalPacks()

	skillsDir := config.SkillsDir(agentDir)

	// study-paper seeded with the papers pack's marker.
	spSkill := filepath.Join(skillsDir, "study-paper", "SKILL.md")
	if data, err := os.ReadFile(spSkill); err != nil {
		t.Fatalf("study-paper SKILL.md not seeded at %s: %v", spSkill, err)
	} else if len(data) == 0 {
		t.Fatal("seeded study-paper SKILL.md is empty")
	}
	if m, err := os.ReadFile(filepath.Join(skillsDir, "study-paper", seedVersionMarker)); err != nil {
		t.Fatalf("study-paper seed marker missing: %v", err)
	} else if string(m) != papers.CurrentSeedVersion {
		t.Errorf("study-paper marker = %q, want %q", m, papers.CurrentSeedVersion)
	}

	// Every research-* skill present with the research pack's marker.
	for _, name := range research.ResearchSkillNames() {
		skill := filepath.Join(skillsDir, name, "SKILL.md")
		if _, err := os.Stat(skill); err != nil {
			t.Errorf("research skill %s not seeded: %v", name, err)
			continue
		}
		m, err := os.ReadFile(filepath.Join(skillsDir, name, seedVersionMarker))
		if err != nil {
			t.Errorf("research skill %s marker missing: %v", name, err)
			continue
		}
		if string(m) != research.CurrentSeedVersion {
			t.Errorf("research skill %s marker = %q, want %q", name, m, research.CurrentSeedVersion)
		}
	}

	// The research Subagent Profile is seeded into the GLOBAL agents dir.
	for _, name := range research.ResearchAgentNames() {
		profile := filepath.Join(config.AgentsDir(agentDir), name, "AGENT.md")
		if _, err := os.Stat(profile); err != nil {
			t.Errorf("agent profile %s not seeded: %v", name, err)
		}
	}
}

// TestSeedGlobalPacks_PreservesUserOwnedSkill pins the non-destructive half of
// the launch seed: a marker-less, diverging directory is USER-OWNED and must
// survive the seed untouched — even when a same-named pack skill exists.
func TestSeedGlobalPacks_PreservesUserOwnedSkill(t *testing.T) {
	f, _, _, agentDir := seedGlobalPacksTestFrontend(t)

	userSkill := filepath.Join(config.SkillsDir(agentDir), "study-paper")
	if err := os.MkdirAll(userSkill, 0o755); err != nil {
		t.Fatalf("mkdir user skill: %v", err)
	}
	userBody := []byte("---\nname: study-paper\ndescription: mine\n---\n# my own copy\n")
	if err := os.WriteFile(filepath.Join(userSkill, "SKILL.md"), userBody, 0o644); err != nil {
		t.Fatalf("write user SKILL.md: %v", err)
	}

	f.seedGlobalPacks()

	got, err := os.ReadFile(filepath.Join(userSkill, "SKILL.md"))
	if err != nil {
		t.Fatalf("read user SKILL.md: %v", err)
	}
	if !bytes.Equal(got, userBody) {
		t.Error("user-owned study-paper was modified by the launch seed")
	}
	if _, err := os.Stat(filepath.Join(userSkill, seedVersionMarker)); !os.IsNotExist(err) {
		t.Errorf("user-owned study-paper gained a pack marker (stat err=%v)", err)
	}
}

// TestSeedGlobalPacks_Idempotent pins that a repeated launch seed is a no-op
// (the pack copies are current) and leaves the packs in place.
func TestSeedGlobalPacks_Idempotent(t *testing.T) {
	f, _, _, agentDir := seedGlobalPacksTestFrontend(t)

	f.seedGlobalPacks()
	f.seedGlobalPacks()

	if _, err := os.Stat(filepath.Join(config.SkillsDir(agentDir), "study-paper", "SKILL.md")); err != nil {
		t.Fatalf("study-paper missing after a repeated seed: %v", err)
	}
	entries, err := os.ReadDir(config.AgentsDir(agentDir))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no research agent profiles after a repeated seed: %v", err)
	}
}

// TestSwitchProject_DoesNotSeedProjectLocalPacks pins that a project switch no
// longer writes any pack into the PROJECT-LOCAL .agents directories — seeding
// is now a global, launch-time concern (seedGlobalPacks).
func TestSwitchProject_DoesNotSeedProjectLocalPacks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH (CODE-mode SwitchProject requires it)")
	}

	f, projectID, ws, _ := seedGlobalPacksTestFrontend(t)
	f.builderOverride = &mockBuilder{}
	t.Cleanup(func() { closeSwitchTestWatcher(t, f) })

	if err := f.SwitchProject(projectID); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}

	if _, err := os.Stat(config.ProjectSkillsPath(ws)); !os.IsNotExist(err) {
		t.Errorf("project-local skills dir created by SwitchProject (stat err=%v)", err)
	}
	if _, err := os.Stat(config.ProjectAgentsPath(ws)); !os.IsNotExist(err) {
		t.Errorf("project-local agents dir created by SwitchProject (stat err=%v)", err)
	}
}

// TestLiteratureScriptPath_Global pins the global resolver: the study-paper
// literature.py helper is looked up in c0wrk's GLOBAL agent skills directory
// (config.SkillsDir(agentDir)), never in a project-local one; a missing helper
// (or missing agentDir) yields "" — the explicit no_script degradation.
func TestLiteratureScriptPath_Global(t *testing.T) {
	f := &FrontendAPI{}
	if got := f.literatureScriptPath(); got != "" {
		t.Errorf("literatureScriptPath() with no agentDir = %q, want empty", got)
	}

	agentDir := t.TempDir()
	f = &FrontendAPI{agentDir: agentDir}
	if got := f.literatureScriptPath(); got != "" {
		t.Errorf("literatureScriptPath() missing = %q, want empty", got)
	}

	script := filepath.Join(config.SkillsDir(agentDir), studyPaperSkillName, literatureScriptRelPath)
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(script, []byte("# stub\n"), 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	if got := f.literatureScriptPath(); got != script {
		t.Errorf("literatureScriptPath() = %q, want %q", got, script)
	}
}
