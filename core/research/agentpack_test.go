package research

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/sp4rk/agents"
)

// expectedAgentNames is the canonical set bundled in the agent pack, sorted.
var expectedAgentNames = []string{"research"}

func TestResearchAgentNames(t *testing.T) {
	got := ResearchAgentNames()
	if len(got) != 1 {
		t.Fatalf("ResearchAgentNames: got %d names, want 1: %v", len(got), got)
	}
	if got[0] != expectedAgentNames[0] {
		t.Errorf("ResearchAgentNames[0]: got %q, want %q", got[0], expectedAgentNames[0])
	}
}

// TestSeedAgents_EmbedsResearchProfile verifies the pack materializes the
// research profile with an AGENT.md that parses via agents.ParseAgent, and that
// AgentManager discovery picks it up.
func TestSeedAgents_EmbedsResearchProfile(t *testing.T) {
	dest := t.TempDir()

	res, err := SeedAgents(dest, nil)
	if err != nil {
		t.Fatalf("SeedAgents: %v", err)
	}
	if len(res.Seeded) != 1 || res.Seeded[0] != "research" {
		t.Fatalf("Seeded: got %v, want [research]", res.Seeded)
	}

	// The profile dir must contain an AGENT.md and a seed-version marker.
	dir := filepath.Join(dest, "research")
	if _, err := os.Stat(filepath.Join(dir, "AGENT.md")); err != nil {
		t.Errorf("research/AGENT.md missing: %v", err)
	}
	if ok, v := readSeedVersion(dir); !ok {
		t.Errorf("research: missing %s marker", seedVersionFile)
	} else if v != AgentSeedVersion {
		t.Errorf("research: marker version %q, want %q", v, AgentSeedVersion)
	}

	// ParseAgent: name matches dir, valid tools list.
	agent, err := agents.ParseAgent(filepath.Join(dir, "AGENT.md"), dir)
	if err != nil {
		t.Fatalf("ParseAgent: unexpected error: %v", err)
	}
	if agent.Metadata.Name != "research" {
		t.Errorf("ParseAgent name = %q, want %q", agent.Metadata.Name, "research")
	}
	if agent.Metadata.Description == "" {
		t.Error("ParseAgent description is empty")
	}
	if agent.Metadata.Tools != "all" {
		t.Errorf("ParseAgent tools = %q, want %q", agent.Metadata.Tools, "all")
	}

	// Discovery: an AgentManager scanning the seeded dir must list the profile.
	am := agents.NewAgentManager([]string{dest}, nil)
	if err := am.Scan(); err != nil {
		t.Fatalf("AgentManager.Scan: %v", err)
	}
	list := am.List()
	if len(list) != 1 {
		t.Fatalf("AgentManager.List: got %d agents, want 1: %+v", len(list), list)
	}
	if list[0].Name != "research" {
		t.Errorf("AgentManager.List[0].Name = %q, want %q", list[0].Name, "research")
	}
	if _, ok := am.Get("research"); !ok {
		t.Error("AgentManager.Get(research) = not found, want found")
	}
}

// TestSeedAgents_IdempotentSameVersion verifies re-seeding with an unchanged
// pack version skips everything and preserves existing (possibly user-edited)
// content byte-for-byte.
func TestSeedAgents_IdempotentSameVersion(t *testing.T) {
	dest := t.TempDir()

	first, err := SeedAgents(dest, nil)
	if err != nil {
		t.Fatalf("first SeedAgents: %v", err)
	}
	if len(first.Seeded) != 1 || len(first.Current) != 0 {
		t.Fatalf("first pass: Seeded=%v Current=%v, want Seeded=1 Current=0", first.Seeded, first.Current)
	}

	// Simulate a user edit to the seeded profile (still marked at current version).
	edited := filepath.Join(dest, "research", "AGENT.md")
	userContent := []byte("---\nname: research\ndescription: edited\n---\n# edited body\n")
	if err := os.WriteFile(edited, userContent, 0o644); err != nil {
		t.Fatalf("write edit: %v", err)
	}

	second, err := SeedAgents(dest, nil)
	if err != nil {
		t.Fatalf("second SeedAgents: %v", err)
	}
	if len(second.Seeded) != 0 || len(second.Current) != 1 {
		t.Fatalf("second pass: Seeded=%v Current=%v, want Seeded=0 Current=1", second.Seeded, second.Current)
	}

	// The user edit must survive unchanged (same-version skip preserves edits).
	got, err := os.ReadFile(edited)
	if err != nil {
		t.Fatalf("read edited: %v", err)
	}
	if !bytes.Equal(got, userContent) {
		t.Errorf("idempotent re-seed clobbered user edit:\ngot:\n%s\nwant:\n%s", got, userContent)
	}
}

// TestSeedAgents_PreservesUserOwnedProfile verifies a pre-existing profile
// directory without a pack marker (user-authored) is never overwritten.
func TestSeedAgents_PreservesUserOwnedProfile(t *testing.T) {
	dest := t.TempDir()

	// Pre-create a user-owned research profile with no marker.
	userDir := filepath.Join(dest, "research")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	userAGENT := []byte("---\nname: research\ndescription: user-owned\n---\n# mine\n")
	if err := os.WriteFile(filepath.Join(userDir, "AGENT.md"), userAGENT, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := SeedAgents(dest, nil)
	if err != nil {
		t.Fatalf("SeedAgents: %v", err)
	}
	if len(res.Preserved) != 1 || res.Preserved[0] != "research" {
		t.Fatalf("Preserved: got %v, want [research]", res.Preserved)
	}
	if len(res.Seeded) != 0 {
		t.Fatalf("Seeded: got %d, want 0: %v", len(res.Seeded), res.Seeded)
	}

	// User content intact, and no marker written into a user-owned dir.
	got, err := os.ReadFile(filepath.Join(userDir, "AGENT.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, userAGENT) {
		t.Errorf("user-owned profile was clobbered:\ngot:\n%s\nwant:\n%s", got, userAGENT)
	}
	if _, err := os.Stat(filepath.Join(userDir, seedVersionFile)); !os.IsNotExist(err) {
		t.Errorf("user-owned dir should not gain a %s marker", seedVersionFile)
	}
}

// TestSeedAgents_OverwritesOnVersionBump verifies a previously-seeded profile
// whose marker records an older pack version is overwritten on re-seed.
func TestSeedAgents_OverwritesOnVersionBump(t *testing.T) {
	dest := t.TempDir()

	// Seed once at the current version.
	if _, err := SeedAgents(dest, nil); err != nil {
		t.Fatalf("SeedAgents: %v", err)
	}

	// Backdate the marker to an older version and inject a stale file.
	dir := filepath.Join(dest, "research")
	if err := os.WriteFile(filepath.Join(dir, seedVersionFile), []byte("0"), 0o644); err != nil {
		t.Fatalf("backdate marker: %v", err)
	}
	stale := filepath.Join(dir, "stale-user-file.md")
	if err := os.WriteFile(stale, []byte("should be removed on update"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	res, err := SeedAgents(dest, nil)
	if err != nil {
		t.Fatalf("SeedAgents: %v", err)
	}
	if len(res.Updated) != 1 || res.Updated[0] != "research" {
		t.Fatalf("Updated: got %v, want [research]", res.Updated)
	}

	// Stale file removed, marker back at the current version.
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file should be removed on update, still present")
	}
	if ok, v := readSeedVersion(dir); !ok || v != AgentSeedVersion {
		t.Errorf("marker after update = ok=%v version=%q, want ok=true version=%q", ok, v, AgentSeedVersion)
	}
}

// TestSeedAgents_EmptyDestErrors verifies an empty destination is rejected.
func TestSeedAgents_EmptyDestErrors(t *testing.T) {
	if _, err := SeedAgents("", nil); err == nil {
		t.Fatal("SeedAgents with empty dest must return an error")
	}
}
