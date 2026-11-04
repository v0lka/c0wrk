package specs

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/v0lka/c0wrk/core/seedpack"
	"github.com/v0lka/sp4rk/agents"
)

// expectedAgentNames is the canonical set bundled in the agent pack, sorted.
var expectedAgentNames = []string{"specs"}

// expectedProfileTools is the least-privilege tool budget ADR-105 decision 3
// pins for the spec-steward profile: Markdown read/write only, never execute.
const expectedProfileTools = "local-read, local-write"

// expectedProfileSkills is the declared `skills:` list, in declared order.
var expectedProfileSkills = []string{"spec-consult", "spec-create", "spec-update", "spec-check"}

func TestSpecAgentNames(t *testing.T) {
	got := SpecAgentNames()
	if len(got) != 1 {
		t.Fatalf("SpecAgentNames: got %d names, want 1: %v", len(got), got)
	}
	if got[0] != expectedAgentNames[0] {
		t.Errorf("SpecAgentNames[0]: got %q, want %q", got[0], expectedAgentNames[0])
	}
}

// TestSeedAgents_EmbedsSpecsProfile verifies the pack materializes the
// specs profile with an AGENT.md that parses via agents.ParseAgent (the
// fail-closed sp4rk parser: unknown tool groups, malformed or duplicate
// skill names fail the parse), and that AgentManager discovery picks it up.
func TestSeedAgents_EmbedsSpecsProfile(t *testing.T) {
	dest := t.TempDir()

	res, err := SeedAgents(dest, nil)
	if err != nil {
		t.Fatalf("SeedAgents: %v", err)
	}
	if len(res.Seeded) != 1 || res.Seeded[0] != "specs" {
		t.Fatalf("Seeded: got %v, want [specs]", res.Seeded)
	}

	// The profile dir must contain an AGENT.md and a seed-version marker.
	dir := filepath.Join(dest, "specs")
	if _, err := os.Stat(filepath.Join(dir, "AGENT.md")); err != nil {
		t.Errorf("specs/AGENT.md missing: %v", err)
	}
	if ok, v := seedpack.ReadVersion(dir); !ok {
		t.Errorf("specs: missing %s marker", seedpack.VersionFile)
	} else if v != AgentSeedVersion {
		t.Errorf("specs: marker version %q, want %q", v, AgentSeedVersion)
	}

	// ParseAgent: name matches dir, least-privilege tools, declared skills.
	agent, err := agents.ParseAgent(filepath.Join(dir, "AGENT.md"), dir)
	if err != nil {
		t.Fatalf("ParseAgent: unexpected error: %v", err)
	}
	if agent.Metadata.Name != "specs" {
		t.Errorf("ParseAgent name = %q, want %q", agent.Metadata.Name, "specs")
	}
	if agent.Metadata.Description == "" {
		t.Error("ParseAgent description is empty")
	}
	if agent.Metadata.Tools != expectedProfileTools {
		t.Errorf("ParseAgent tools = %q, want %q", agent.Metadata.Tools, expectedProfileTools)
	}
	if got, err := agent.RequiredSkills(); err != nil {
		t.Fatalf("RequiredSkills: %v", err)
	} else if !slices.Equal(got, expectedProfileSkills) {
		t.Errorf("RequiredSkills = %v, want %v", got, expectedProfileSkills)
	}

	// The declared skills must all be members of the bundled skill pack —
	// the launch-time fail-closed catalog resolution must succeed for the
	// seeded profile (the pack ships every skill it requires).
	packSkills := SpecSkillNames()
	for _, name := range expectedProfileSkills {
		if !slices.Contains(packSkills, name) {
			t.Errorf("profile skill %q is not bundled in the spec skill pack %v", name, packSkills)
		}
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
	if list[0].Name != "specs" {
		t.Errorf("AgentManager.List[0].Name = %q, want %q", list[0].Name, "specs")
	}
	if _, ok := am.Get("specs"); !ok {
		t.Error("AgentManager.Get(specs) = not found, want found")
	}
}

// TestSeedAgents_IdempotentSameVersion verifies re-seeding with an unchanged
// pack version preserves existing (possibly user-edited) content byte-for-byte:
// a same-version profile whose content diverges from the pack is hash-diff'd,
// preserved, and reported as Modified — never Current, never overwritten.
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
	edited := filepath.Join(dest, "specs", "AGENT.md")
	userContent := []byte("---\nname: specs\ndescription: edited\n---\n# edited body\n")
	if err := os.WriteFile(edited, userContent, 0o644); err != nil {
		t.Fatalf("write edit: %v", err)
	}

	second, err := SeedAgents(dest, nil)
	if err != nil {
		t.Fatalf("second SeedAgents: %v", err)
	}
	if len(second.Seeded) != 0 || len(second.Updated) != 0 {
		t.Fatalf("second pass: Seeded=%v Updated=%v, want both empty", second.Seeded, second.Updated)
	}
	if len(second.Current) != 0 {
		t.Fatalf("second pass: Current=%v, want empty", second.Current)
	}
	if len(second.Modified) != 1 || second.Modified[0] != "specs" {
		t.Fatalf("second pass: Modified=%v, want [specs]", second.Modified)
	}

	// The user edit must survive unchanged.
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

	// Pre-create a user-owned specs profile with no marker.
	userDir := filepath.Join(dest, "specs")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	userAGENT := []byte("---\nname: specs\ndescription: user-owned\n---\n# mine\n")
	if err := os.WriteFile(filepath.Join(userDir, "AGENT.md"), userAGENT, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := SeedAgents(dest, nil)
	if err != nil {
		t.Fatalf("SeedAgents: %v", err)
	}
	if len(res.Preserved) != 1 || res.Preserved[0] != "specs" {
		t.Fatalf("Preserved: got %v, want [specs]", res.Preserved)
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
	if _, err := os.Stat(filepath.Join(userDir, seedpack.VersionFile)); !os.IsNotExist(err) {
		t.Errorf("user-owned dir should not gain a %s marker", seedpack.VersionFile)
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
	dir := filepath.Join(dest, "specs")
	if err := os.WriteFile(filepath.Join(dir, seedpack.VersionFile), []byte("0"), 0o644); err != nil {
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
	if len(res.Updated) != 1 || res.Updated[0] != "specs" {
		t.Fatalf("Updated: got %v, want [specs]", res.Updated)
	}

	// Stale file removed, marker back at the current version.
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file should be removed on update, still present")
	}
	if ok, v := seedpack.ReadVersion(dir); !ok || v != AgentSeedVersion {
		t.Errorf("marker after update = ok=%v version=%q, want ok=true version=%q", ok, v, AgentSeedVersion)
	}
}

// TestSeedAgents_EmptyDestErrors verifies an empty destination is rejected.
func TestSeedAgents_EmptyDestErrors(t *testing.T) {
	if _, err := SeedAgents("", nil); err == nil {
		t.Fatal("SeedAgents with empty dest must return an error")
	}
}
