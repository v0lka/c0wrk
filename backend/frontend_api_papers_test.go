package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/sp4rk/skills"
)

// TestSeedPapersSkillPack_SeedsGlobalSkillsDir verifies the startup seeding of
// the built-in paper-study skill-pack: after seedPapersSkillPack(agentDir) the
// `study-paper` skill is present in the GLOBAL agent skills directory
// (config.SkillsDir(agentDir) = ~/.c0wrk/.agents/skills) and is discoverable
// from that directory alone — i.e. independently of any project or RESEARCH
// mode, which is exactly how ListSkills (builder.GetSkillDescriptors) sees it.
func TestSeedPapersSkillPack_SeedsGlobalSkillsDir(t *testing.T) {
	agentDir := t.TempDir()

	f := &FrontendAPI{}
	f.seedPapersSkillPack(agentDir)

	globalSkills := config.SkillsDir(agentDir)
	skillDir := filepath.Join(globalSkills, "study-paper")
	skillMD := filepath.Join(skillDir, "SKILL.md")
	if _, err := os.Stat(skillMD); err != nil {
		t.Fatalf("expected seeded SKILL.md at %s: %v", skillMD, err)
	}
	if _, err := skills.ParseSkill(skillMD, skillDir); err != nil {
		t.Fatalf("seeded SKILL.md does not parse: %v", err)
	}

	// Discovery from the global dir alone — no project dir, no RESEARCH mode.
	sm := skills.NewSkillManager([]string{globalSkills}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("SkillManager.Scan: %v", err)
	}
	found := false
	for _, d := range sm.List() {
		if d.Name == "study-paper" {
			found = true
			if d.Description == "" {
				t.Error("study-paper descriptor has an empty description")
			}
		}
	}
	if !found {
		t.Fatalf("study-paper not discoverable in %s: %+v", globalSkills, sm.List())
	}

	// Idempotent: a second seeding pass must not disturb the seeded tree.
	before, err := os.ReadFile(skillMD)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	f.seedPapersSkillPack(agentDir)
	after, err := os.ReadFile(skillMD)
	if err != nil {
		t.Fatalf("re-read SKILL.md: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("re-seeding changed the seeded SKILL.md")
	}
}

// TestSeedPapersSkillPack_EmptyAgentDirIsNoop pins the guard: an empty agentDir
// (the test/default case) must be a silent no-op, never a write to the real
// user home.
func TestSeedPapersSkillPack_EmptyAgentDirIsNoop(t *testing.T) {
	f := &FrontendAPI{}
	f.seedPapersSkillPack("")
}
