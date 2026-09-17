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

// newReconcileTestFrontend builds the minimal EnableResearch-capable
// FrontendAPI: a project store with one real project over a temp workspace.
func newReconcileTestFrontend(t *testing.T) (f *FrontendAPI, projectID, ws string) {
	t.Helper()
	base := t.TempDir()
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
		Name:          "Reconcile",
		WorkspacePath: ws,
	}); err != nil {
		t.Fatalf("save project: %v", err)
	}
	f = &FrontendAPI{
		projectManager: project.NewManager(store, base, nil),
		projStore:      store,
		emitEvent:      func(string, ...any) {},
	}
	return f, "proj-1", ws
}

// TestEnableResearch_SeedsAllPacksProjectLocally pins the reconciliation
// contract at toggle time: EnableResearch must seed EVERY c0wrk-owned pack
// into the project-local agent directories — the research-* skills, the
// study-paper skill (so the project-local copy wins the same-name discovery
// chain over a ~/.agents namesake), and the research Subagent Profile — with
// the current pack-version markers, and report the skill names in the DTO.
func TestEnableResearch_SeedsAllPacksProjectLocally(t *testing.T) {
	f, projectID, ws := newReconcileTestFrontend(t)

	status, err := f.EnableResearch(projectID, "")
	if err != nil {
		t.Fatalf("EnableResearch: %v", err)
	}

	skillsDir := config.ProjectSkillsPath(ws)

	// study-paper seeded project-locally with the papers pack's marker.
	spSkill := filepath.Join(skillsDir, "study-paper", "SKILL.md")
	if data, rerr := os.ReadFile(spSkill); rerr != nil {
		t.Fatalf("study-paper SKILL.md not seeded at %s: %v", spSkill, rerr)
	} else if len(data) == 0 {
		t.Fatal("seeded study-paper SKILL.md is empty")
	}
	marker, rerr := os.ReadFile(filepath.Join(skillsDir, "study-paper", seedVersionMarker))
	if rerr != nil {
		t.Fatalf("study-paper seed marker missing: %v", rerr)
	}
	if string(marker) != papers.CurrentSeedVersion {
		t.Errorf("study-paper marker = %q, want papers.CurrentSeedVersion %q", marker, papers.CurrentSeedVersion)
	}

	// Every research-* skill present with the research pack's marker.
	for _, name := range research.ResearchSkillNames() {
		skill := filepath.Join(skillsDir, name, "SKILL.md")
		if _, serr := os.Stat(skill); serr != nil {
			t.Errorf("research skill %s not seeded: %v", name, serr)
		}
		m, merr := os.ReadFile(filepath.Join(skillsDir, name, seedVersionMarker))
		if merr != nil {
			t.Errorf("research skill %s marker missing: %v", name, merr)
			continue
		}
		if string(m) != research.CurrentSeedVersion {
			t.Errorf("research skill %s marker = %q, want %q", name, m, research.CurrentSeedVersion)
		}
	}

	// The research Subagent Profile is seeded into .agents/agents.
	for _, name := range research.ResearchAgentNames() {
		profile := filepath.Join(config.ProjectAgentsPath(ws), name, "AGENT.md")
		if _, perr := os.Stat(profile); perr != nil {
			t.Errorf("agent profile %s not seeded: %v", name, perr)
		}
	}

	// The DTO reports both packs' skill names in the seeded bucket.
	if status.SeedResult == nil {
		t.Fatal("SeedResult is nil after a first enable")
	}
	wantSeeded := map[string]bool{"study-paper": false}
	for _, name := range research.ResearchSkillNames() {
		wantSeeded[name] = false
	}
	for _, name := range status.SeedResult.Seeded {
		if _, ok := wantSeeded[name]; ok {
			wantSeeded[name] = true
		}
	}
	for name, seen := range wantSeeded {
		if !seen {
			t.Errorf("SeedResult.Seeded missing %q (got %v)", name, status.SeedResult.Seeded)
		}
	}
}

// TestSwitchProject_ReconcilesResearchPacks pins the switch-time validation:
// switching to a research-enabled project seeds missing pack entries (here:
// study-paper, the entry whose absence let a stale ~/.agents namesake win the
// discovery chain) and upgrades pack-marked outdated ones, while
// research-disabled projects reconcile nothing.
func TestSwitchProject_ReconcilesResearchPacks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH (CODE-mode SwitchProject requires it)")
	}

	// A research-enabled target project carrying an OUTDATED pack-marked
	// research-init (marker "0" < research.CurrentSeedVersion) and NO
	// study-paper at all.
	f, projectID, ws := newReconcileTestFrontend(t)
	f.builderOverride = &mockBuilder{}
	t.Cleanup(func() { closeSwitchTestWatcher(t, f) })

	root := config.ProjectResearchPath(ws)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir research root: %v", err)
	}
	proj, err := f.projectManager.GetProject(projectID)
	if err != nil || proj == nil {
		t.Fatalf("GetProject: %v", err)
	}
	proj.ResearchRoot = root
	if err := f.projStore.SaveProject(context.Background(), *proj); err != nil {
		t.Fatalf("persist ResearchRoot: %v", err)
	}

	skillsDir := config.ProjectSkillsPath(ws)
	stale := filepath.Join(skillsDir, "research-init")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir stale skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stale, "SKILL.md"), []byte("---\nname: research-init\ndescription: stale\n---\n# old\n"), 0o644); err != nil {
		t.Fatalf("write stale SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stale, seedVersionMarker), []byte("0"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}

	if err := f.SwitchProject(projectID); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}

	// Missing study-paper seeded project-locally.
	if _, serr := os.Stat(filepath.Join(skillsDir, "study-paper", "SKILL.md")); serr != nil {
		t.Fatalf("study-paper not seeded on switch: %v", serr)
	}

	// Outdated pack-marked research-init upgraded to the current pack.
	upgraded, rerr := os.ReadFile(filepath.Join(stale, "SKILL.md"))
	if rerr != nil {
		t.Fatalf("read upgraded SKILL.md: %v", rerr)
	}
	if !bytes.HasPrefix(upgraded, []byte("---\nname: research-init")) {
		// A full pack overwrite replaces the stale body; the embedded SKILL.md
		// always starts with the YAML front matter.
		t.Errorf("research-init content was not replaced by the pack copy: %q", string(upgraded[:min(60, len(upgraded))]))
	}
	if m, merr := os.ReadFile(filepath.Join(stale, seedVersionMarker)); merr != nil || string(m) != research.CurrentSeedVersion {
		t.Errorf("research-init marker not upgraded: content=%q err=%v", m, merr)
	}
}

// TestSwitchProject_PreservesUserOwnedSkillOnReconcile pins the
// non-destructive half of the switch-time reconciliation: a marker-less,
// diverging directory is USER-OWNED and must survive a switch untouched —
// even when a same-named pack skill exists.
func TestSwitchProject_PreservesUserOwnedSkillOnReconcile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH (CODE-mode SwitchProject requires it)")
	}

	f, projectID, ws := newReconcileTestFrontend(t)
	f.builderOverride = &mockBuilder{}
	t.Cleanup(func() { closeSwitchTestWatcher(t, f) })

	root := config.ProjectResearchPath(ws)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir research root: %v", err)
	}
	proj, err := f.projectManager.GetProject(projectID)
	if err != nil || proj == nil {
		t.Fatalf("GetProject: %v", err)
	}
	proj.ResearchRoot = root
	if err := f.projStore.SaveProject(context.Background(), *proj); err != nil {
		t.Fatalf("persist ResearchRoot: %v", err)
	}

	// User-owned study-paper: no marker, diverging content.
	userSkill := filepath.Join(config.ProjectSkillsPath(ws), "study-paper")
	if err := os.MkdirAll(userSkill, 0o755); err != nil {
		t.Fatalf("mkdir user skill: %v", err)
	}
	userBody := []byte("---\nname: study-paper\ndescription: mine\n---\n# my own copy\n")
	if err := os.WriteFile(filepath.Join(userSkill, "SKILL.md"), userBody, 0o644); err != nil {
		t.Fatalf("write user SKILL.md: %v", err)
	}

	if err := f.SwitchProject(projectID); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}

	got, rerr := os.ReadFile(filepath.Join(userSkill, "SKILL.md"))
	if rerr != nil {
		t.Fatalf("read user SKILL.md: %v", rerr)
	}
	if !bytes.Equal(got, userBody) {
		t.Error("user-owned study-paper was modified by switch-time reconciliation")
	}
	if _, merr := os.Stat(filepath.Join(userSkill, seedVersionMarker)); !os.IsNotExist(merr) {
		t.Errorf("user-owned study-paper gained a pack marker (stat err=%v)", merr)
	}
}

// TestSwitchProject_NoReconcileWithoutResearch pins the gate: switching to a
// project WITHOUT a persisted research root must not touch the project-local
// agent directories at all.
func TestSwitchProject_NoReconcileWithoutResearch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH (CODE-mode SwitchProject requires it)")
	}

	f, projectID, ws := newReconcileTestFrontend(t)
	f.builderOverride = &mockBuilder{}
	t.Cleanup(func() { closeSwitchTestWatcher(t, f) })

	if err := f.SwitchProject(projectID); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}

	if _, serr := os.Stat(config.ProjectSkillsPath(ws)); !os.IsNotExist(serr) {
		t.Errorf("project skills dir created for a research-disabled project (stat err=%v)", serr)
	}
	if _, aerr := os.Stat(config.ProjectAgentsPath(ws)); !os.IsNotExist(aerr) {
		t.Errorf("project agents dir created for a research-disabled project (stat err=%v)", aerr)
	}
}

// TestLiteratureScriptPath_ProjectLocal pins the retargeted resolver: the
// study-paper literature.py helper is looked up in the REQUESTING project's
// project-local skills directory (the copy seeded by the research pack
// reconciliation), never in a global one.
func TestLiteratureScriptPath_ProjectLocal(t *testing.T) {
	f := &FrontendAPI{}

	if got := f.literatureScriptPath(""); got != "" {
		t.Errorf("literatureScriptPath(\"\") = %q, want empty", got)
	}

	ws := t.TempDir()
	if got := f.literatureScriptPath(ws); got != "" {
		t.Errorf("literatureScriptPath(missing) = %q, want empty", got)
	}

	script := filepath.Join(config.ProjectSkillsPath(ws), "study-paper", "scripts", "literature.py")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(script, []byte("# stub\n"), 0o644); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	if got := f.literatureScriptPath(ws); got != script {
		t.Errorf("literatureScriptPath(ws) = %q, want %q", got, script)
	}
}
