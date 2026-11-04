package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/specs"
	"github.com/v0lka/sp4rk/agents"
	"github.com/v0lka/sp4rk/skills"
)

// The spec-* skillset and the specs Subagent Profile (ADR-105) ride the same
// launch-time global seed as the research/paper packs (ADR-056):
// seedGlobalPacks materializes them into the GLOBAL agent directories
// (~/.c0wrk/.agents/{skills,agents}) so the standard discovery set — whose
// first entry is exactly config.SkillsDir/AgentsDir(agentDir)
// (config.defaultSkillDirs/defaultAgentDirs) — picks them up with no catalog
// change. These tests mirror frontend_api_research_reconcile_test.go.

// TestSeedGlobalPacks_SeedsSpecPacksGlobally pins the launch-time seeding of
// the vendored spec packs: every spec-* skill lands in the GLOBAL skills dir
// with a non-empty SKILL.md and the spec pack's version marker, and the specs
// Subagent Profile lands in the GLOBAL agents dir with the agent pack's own
// marker (the two packs version independently).
func TestSeedGlobalPacks_SeedsSpecPacksGlobally(t *testing.T) {
	f, _, _, agentDir := seedGlobalPacksTestFrontend(t)

	f.seedGlobalPacks()

	skillsDir := config.SkillsDir(agentDir)
	for _, name := range specs.SpecSkillNames() {
		skill := filepath.Join(skillsDir, name, "SKILL.md")
		data, err := os.ReadFile(skill)
		if err != nil {
			t.Errorf("spec skill %s not seeded: %v", name, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("seeded spec skill %s SKILL.md is empty", name)
		}
		m, err := os.ReadFile(filepath.Join(skillsDir, name, seedVersionMarker))
		if err != nil {
			t.Errorf("spec skill %s marker missing: %v", name, err)
			continue
		}
		if string(m) != specs.CurrentSeedVersion {
			t.Errorf("spec skill %s marker = %q, want %q", name, m, specs.CurrentSeedVersion)
		}
	}

	agentsDir := config.AgentsDir(agentDir)
	for _, name := range specs.SpecAgentNames() {
		profile := filepath.Join(agentsDir, name, "AGENT.md")
		data, err := os.ReadFile(profile)
		if err != nil {
			t.Errorf("agent profile %s not seeded: %v", name, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("seeded agent profile %s AGENT.md is empty", name)
		}
		m, err := os.ReadFile(filepath.Join(agentsDir, name, seedVersionMarker))
		if err != nil {
			t.Errorf("agent profile %s marker missing: %v", name, err)
			continue
		}
		if string(m) != specs.AgentSeedVersion {
			t.Errorf("agent profile %s marker = %q, want %q", name, m, specs.AgentSeedVersion)
		}
	}
}

// TestSeedGlobalPacks_PreservesUserOwnedSpecPacks pins the non-destructive
// half of the launch seed for BOTH spec packs: a marker-less, diverging
// directory is USER-OWNED and must survive the seed untouched — even though
// same-named pack entries exist.
func TestSeedGlobalPacks_PreservesUserOwnedSpecPacks(t *testing.T) {
	f, _, _, agentDir := seedGlobalPacksTestFrontend(t)

	userSkill := filepath.Join(config.SkillsDir(agentDir), "spec-init")
	if err := os.MkdirAll(userSkill, 0o755); err != nil {
		t.Fatalf("mkdir user skill: %v", err)
	}
	userSkillBody := []byte("---\nname: spec-init\ndescription: mine\n---\n# my own spec-init\n")
	if err := os.WriteFile(filepath.Join(userSkill, "SKILL.md"), userSkillBody, 0o644); err != nil {
		t.Fatalf("write user SKILL.md: %v", err)
	}

	userAgent := filepath.Join(config.AgentsDir(agentDir), "specs")
	if err := os.MkdirAll(userAgent, 0o755); err != nil {
		t.Fatalf("mkdir user agent: %v", err)
	}
	userAgentBody := []byte("---\nname: specs\ndescription: my own steward\n---\n# my own specs profile\n")
	if err := os.WriteFile(filepath.Join(userAgent, "AGENT.md"), userAgentBody, 0o644); err != nil {
		t.Fatalf("write user AGENT.md: %v", err)
	}

	f.seedGlobalPacks()

	gotSkill, err := os.ReadFile(filepath.Join(userSkill, "SKILL.md"))
	if err != nil {
		t.Fatalf("read user SKILL.md: %v", err)
	}
	if !bytes.Equal(gotSkill, userSkillBody) {
		t.Error("user-owned spec-init was modified by the launch seed")
	}
	if _, err := os.Stat(filepath.Join(userSkill, seedVersionMarker)); !os.IsNotExist(err) {
		t.Errorf("user-owned spec-init gained a pack marker (stat err=%v)", err)
	}

	gotAgent, err := os.ReadFile(filepath.Join(userAgent, "AGENT.md"))
	if err != nil {
		t.Fatalf("read user AGENT.md: %v", err)
	}
	if !bytes.Equal(gotAgent, userAgentBody) {
		t.Error("user-owned specs profile was modified by the launch seed")
	}
	if _, err := os.Stat(filepath.Join(userAgent, seedVersionMarker)); !os.IsNotExist(err) {
		t.Errorf("user-owned specs profile gained a pack marker (stat err=%v)", err)
	}
}

// TestSeedGlobalPacks_SpecPacksIdempotent pins that a repeated launch seed
// is a no-op for the spec packs: every seeded file's bytes and every marker
// survive a second run unchanged, and no pack entry disappears.
func TestSeedGlobalPacks_SpecPacksIdempotent(t *testing.T) {
	f, _, _, agentDir := seedGlobalPacksTestFrontend(t)

	f.seedGlobalPacks()

	snapshot := map[string]string{}
	skillsDir := config.SkillsDir(agentDir)
	for _, name := range specs.SpecSkillNames() {
		skillMD, err := os.ReadFile(filepath.Join(skillsDir, name, "SKILL.md"))
		if err != nil {
			t.Fatalf("spec skill %s SKILL.md missing after first seed: %v", name, err)
		}
		marker, err := os.ReadFile(filepath.Join(skillsDir, name, seedVersionMarker))
		if err != nil {
			t.Fatalf("spec skill %s marker missing after first seed: %v", name, err)
		}
		snapshot[filepath.Join(name, "SKILL.md")] = string(skillMD)
		snapshot[filepath.Join(name, seedVersionMarker)] = string(marker)
	}
	agentsDir := config.AgentsDir(agentDir)
	for _, name := range specs.SpecAgentNames() {
		agentMD, err := os.ReadFile(filepath.Join(agentsDir, name, "AGENT.md"))
		if err != nil {
			t.Fatalf("agent profile %s AGENT.md missing after first seed: %v", name, err)
		}
		marker, err := os.ReadFile(filepath.Join(agentsDir, name, seedVersionMarker))
		if err != nil {
			t.Fatalf("agent profile %s marker missing after first seed: %v", name, err)
		}
		snapshot[filepath.Join(name, "AGENT.md")] = string(agentMD)
		snapshot[filepath.Join(name, seedVersionMarker)] = string(marker)
	}

	f.seedGlobalPacks()

	for _, name := range specs.SpecSkillNames() {
		for _, file := range []string{"SKILL.md", seedVersionMarker} {
			got, err := os.ReadFile(filepath.Join(skillsDir, name, file))
			if err != nil {
				t.Fatalf("spec skill %s %s missing after repeat seed: %v", name, file, err)
			}
			if want := snapshot[filepath.Join(name, file)]; string(got) != want {
				t.Errorf("spec skill %s %s changed on repeat seed", name, file)
			}
		}
	}
	for _, name := range specs.SpecAgentNames() {
		for _, file := range []string{"AGENT.md", seedVersionMarker} {
			got, err := os.ReadFile(filepath.Join(agentsDir, name, file))
			if err != nil {
				t.Fatalf("agent profile %s %s missing after repeat seed: %v", name, file, err)
			}
			if want := snapshot[filepath.Join(name, file)]; string(got) != want {
				t.Errorf("agent profile %s %s changed on repeat seed", name, file)
			}
		}
	}
}

// TestSeedGlobalPacks_SpecPacksInCatalog pins the discovery half of ADR-105
// decision 4: the seeded spec packs enter ListSkills/ListAgents WITHOUT any
// catalog change. The sp4rk SkillManager/AgentManager — the exact discovery
// the real builder runs over the global dir (config.SkillsDir/AgentsDir, the
// first entry of the compiled-in default dir lists) — surface every seeded
// entry, and the descriptors flow through ListSkills/ListAgents unmodified.
func TestSeedGlobalPacks_SpecPacksInCatalog(t *testing.T) {
	f, _, _, agentDir := seedGlobalPacksTestFrontend(t)

	f.seedGlobalPacks()

	// Real discovery over the seeded GLOBAL dirs (what the builder scans).
	skillsDir := config.SkillsDir(agentDir)
	sm := skills.NewSkillManager([]string{skillsDir}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("SkillManager.Scan: %v", err)
	}
	agentsDir := config.AgentsDir(agentDir)
	am := agents.NewAgentManager([]string{agentsDir}, nil)
	if err := am.Scan(); err != nil {
		t.Fatalf("AgentManager.Scan: %v", err)
	}

	// Serve the discovered descriptors through the builder seam the two
	// catalog RPCs read (mockBuilder stands in for the OrchestratorBuilder).
	f.builderOverride = &mockBuilder{
		getSkillDescriptorsRes: sm.List(),
		getAgentDescriptorsRes: am.List(),
	}

	skillNames := map[string]bool{}
	for _, d := range f.ListSkills() {
		skillNames[d.Name] = true
	}
	for _, want := range specs.SpecSkillNames() {
		if !skillNames[want] {
			t.Errorf("ListSkills missing seeded spec skill %q (got %v)", want, skillNames)
		}
	}

	agentNames := map[string]bool{}
	for _, d := range f.ListAgents() {
		agentNames[d.Name] = true
	}
	for _, want := range specs.SpecAgentNames() {
		if !agentNames[want] {
			t.Errorf("ListAgents missing seeded agent profile %q (got %v)", want, agentNames)
		}
	}
}
