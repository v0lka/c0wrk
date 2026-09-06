package research

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/sp4rk/skills"
)

// expectedSkillNames is the canonical set bundled in the pack, sorted.
var expectedSkillNames = []string{
	"research-decision",
	"research-experiment",
	"research-hypothesis",
	"research-init",
	"research-prior-art",
	"research-status",
	"research-synthesis",
}

func TestResearchSkillNames(t *testing.T) {
	got := ResearchSkillNames()
	if len(got) != 7 {
		t.Fatalf("ResearchSkillNames: got %d names, want 7: %v", len(got), got)
	}
	for i, want := range expectedSkillNames {
		if got[i] != want {
			t.Errorf("ResearchSkillNames[%d]: got %q, want %q", i, got[i], want)
		}
	}
}

// TestSeedSkills_EmbedsAllSeven verifies the pack materializes every skill
// with a SKILL.md and that the SkillManager discovery picks them all up.
func TestSeedSkills_EmbedsAllSeven(t *testing.T) {
	dest := t.TempDir()

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if len(res.Seeded) != 7 {
		t.Fatalf("Seeded: got %d, want 7: %v", len(res.Seeded), res.Seeded)
	}

	// Each skill dir must contain a SKILL.md and a seed-version marker.
	for _, name := range expectedSkillNames {
		dir := filepath.Join(dest, name)
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			t.Errorf("%s/SKILL.md missing: %v", name, err)
		}
		if ok, v := readSeedVersion(dir); !ok {
			t.Errorf("%s: missing %s marker", name, seedVersionFile)
		} else if v != CurrentSeedVersion {
			t.Errorf("%s: marker version %q, want %q", name, v, CurrentSeedVersion)
		}
	}

	// Discovery: a SkillManager scanning the seeded dir must list all 7.
	sm := skills.NewSkillManager([]string{dest}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("SkillManager.Scan: %v", err)
	}
	list := sm.List()
	if len(list) != 7 {
		t.Fatalf("SkillManager.List: got %d skills, want 7: %+v", len(list), list)
	}
	gotNames := make(map[string]bool, len(list))
	for _, d := range list {
		gotNames[d.Name] = true
	}
	for _, want := range expectedSkillNames {
		if !gotNames[want] {
			t.Errorf("SkillManager.List missing %q", want)
		}
	}
}

// TestSeedSkills_AssetsEmbedded verifies nested assets/ files survive the
// embed + write round-trip.
func TestSeedSkills_AssetsEmbedded(t *testing.T) {
	dest := t.TempDir()
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	asset := filepath.Join(dest, "research-init", "assets", "brief-template.md")
	if _, err := os.Stat(asset); err != nil {
		t.Errorf("embedded asset missing %s: %v", asset, err)
	}
	synthAsset := filepath.Join(dest, "research-synthesis", "assets", "report-simple-template.md")
	if _, err := os.Stat(synthAsset); err != nil {
		t.Errorf("embedded asset missing %s: %v", synthAsset, err)
	}
}

// TestSeedSkills_IdempotentSameVersion verifies re-seeding with an unchanged
// pack version skips everything and preserves existing (possibly user-edited)
// content byte-for-byte. A same-version skill whose content diverges from the
// pack (the user edit below) is hash-diff'd against the embedded pack,
// preserved untouched, and reported as Modified — never Current (a spoofed
// marker must not masquerade as the pack, review finding [44]) and never
// silently overwritten (acceptance: an edit on top of the pack is not
// re-clobbered).
func TestSeedSkills_IdempotentSameVersion(t *testing.T) {
	dest := t.TempDir()

	first, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("first SeedSkills: %v", err)
	}
	if len(first.Seeded) != 7 || len(first.Current) != 0 {
		t.Fatalf("first pass: Seeded=%v Current=%v, want Seeded=7 Current=0", first.Seeded, first.Current)
	}

	// Simulate a user edit to a seeded skill (still marked at current version).
	edited := filepath.Join(dest, "research-init", "SKILL.md")
	userContent := []byte("---\nname: research-init\ndescription: edited\n---\n# edited body\n")
	if err := os.WriteFile(edited, userContent, 0o644); err != nil {
		t.Fatalf("write edit: %v", err)
	}

	second, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("second SeedSkills: %v", err)
	}
	if len(second.Seeded) != 0 || len(second.Updated) != 0 {
		t.Fatalf("second pass: Seeded=%v Updated=%v, want both empty", second.Seeded, second.Updated)
	}
	if len(second.Current) != 6 {
		t.Fatalf("second pass: Current=%v, want the 6 unedited skills", second.Current)
	}
	if len(second.Modified) != 1 || second.Modified[0] != "research-init" {
		t.Fatalf("second pass: Modified=%v, want [research-init]", second.Modified)
	}

	// The user edit must survive unchanged (same-version divergence is never
	// silently overwritten).
	got, err := os.ReadFile(edited)
	if err != nil {
		t.Fatalf("read edited: %v", err)
	}
	if !bytes.Equal(got, userContent) {
		t.Errorf("idempotent re-seed clobbered user edit:\ngot:\n%s\nwant:\n%s", got, userContent)
	}
}

// TestSeedSkills_PreservesUserOwnedSkill verifies a pre-existing skill
// directory without a pack marker (user-authored) is never overwritten.
func TestSeedSkills_PreservesUserOwnedSkill(t *testing.T) {
	dest := t.TempDir()

	// Pre-create a user-owned research-init skill with no marker.
	userDir := filepath.Join(dest, "research-init")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	userSKILL := []byte("---\nname: research-init\ndescription: user-owned\n---\n# mine\n")
	if err := os.WriteFile(filepath.Join(userDir, "SKILL.md"), userSKILL, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if len(res.Preserved) != 1 || res.Preserved[0] != "research-init" {
		t.Fatalf("Preserved: got %v, want [research-init]", res.Preserved)
	}
	if len(res.Seeded) != 6 {
		t.Fatalf("Seeded: got %d, want 6 (the other six): %v", len(res.Seeded), res.Seeded)
	}

	// User content intact, and no marker written into a user-owned dir.
	got, err := os.ReadFile(filepath.Join(userDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, userSKILL) {
		t.Errorf("user-owned skill was clobbered:\ngot:\n%s\nwant:\n%s", got, userSKILL)
	}
	if _, err := os.Stat(filepath.Join(userDir, seedVersionFile)); !os.IsNotExist(err) {
		t.Errorf("user-owned dir should not gain a %s marker", seedVersionFile)
	}
}

// TestSeedSkills_OverwritesOnVersionBump verifies a previously-seeded skill
// whose marker records an older pack version is overwritten on re-seed.
func TestSeedSkills_OverwritesOnVersionBump(t *testing.T) {
	dest := t.TempDir()

	// Seed once at the current version.
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}

	// Backdate the marker to an older version and inject a stale asset.
	dir := filepath.Join(dest, "research-status")
	if err := os.WriteFile(filepath.Join(dir, seedVersionFile), []byte("0"), 0o644); err != nil {
		t.Fatalf("backdate marker: %v", err)
	}
	stale := filepath.Join(dir, "stale-user-file.md")
	if err := os.WriteFile(stale, []byte("should be removed on update"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if len(res.Updated) != 1 || res.Updated[0] != "research-status" {
		t.Fatalf("Updated: got %v, want [research-status]", res.Updated)
	}

	// Marker restored to current version; stale file removed by full overwrite.
	ok, v := readSeedVersion(dir)
	if !ok || v != CurrentSeedVersion {
		t.Errorf("after update: marker %q (ok=%v), want %q", v, ok, CurrentSeedVersion)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file should have been removed on version-bump overwrite")
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing after update: %v", err)
	}
}

// TestSeedSkills_EmptyDestErrors verifies the guard on an empty destination.
func TestSeedSkills_EmptyDestErrors(t *testing.T) {
	if _, err := SeedSkills("", nil); err == nil {
		t.Fatal("SeedSkills with empty dest should error")
	}
}
