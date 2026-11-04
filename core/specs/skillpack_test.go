package specs

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/seedpack"
	"github.com/v0lka/sp4rk/skills"
)

// expectedSkillNames is the canonical set bundled in the pack, sorted.
var expectedSkillNames = []string{
	"spec-check",
	"spec-consult",
	"spec-create",
	"spec-explore",
	"spec-init",
	"spec-update",
}

func TestSpecSkillNames(t *testing.T) {
	got := SpecSkillNames()
	if len(got) != 6 {
		t.Fatalf("SpecSkillNames: got %d names, want 6: %v", len(got), got)
	}
	for i, want := range expectedSkillNames {
		if got[i] != want {
			t.Errorf("SpecSkillNames[%d]: got %q, want %q", i, got[i], want)
		}
	}
}

// TestSeedSkills_EmbedsAllSix verifies the pack materializes every skill
// with a SKILL.md and that the SkillManager discovery picks them all up.
func TestSeedSkills_EmbedsAllSix(t *testing.T) {
	dest := t.TempDir()

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if len(res.Seeded) != 6 {
		t.Fatalf("Seeded: got %d, want 6: %v", len(res.Seeded), res.Seeded)
	}

	// Each skill dir must contain a SKILL.md and a seed-version marker.
	for _, name := range expectedSkillNames {
		dir := filepath.Join(dest, name)
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			t.Errorf("%s/SKILL.md missing: %v", name, err)
		}
		if ok, v := seedpack.ReadVersion(dir); !ok {
			t.Errorf("%s: missing %s marker", name, seedpack.VersionFile)
		} else if v != CurrentSeedVersion {
			t.Errorf("%s: marker version %q, want %q", name, v, CurrentSeedVersion)
		}
	}

	// Discovery: a SkillManager scanning the seeded dir must list all 6.
	sm := skills.NewSkillManager([]string{dest}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("SkillManager.Scan: %v", err)
	}
	list := sm.List()
	if len(list) != 6 {
		t.Fatalf("SkillManager.List: got %d skills, want 6: %+v", len(list), list)
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

// TestSeedSkills_DirNameMatchesFrontmatter verifies each seeded skill's
// directory name equals its SKILL.md frontmatter name — via the sp4rk
// parser, whose validateSkill enforces exactly that invariant (a mismatch
// fails the parse, so ParseSkill returning cleanly is the assertion).
func TestSeedSkills_DirNameMatchesFrontmatter(t *testing.T) {
	dest := t.TempDir()
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	for _, name := range expectedSkillNames {
		dir := filepath.Join(dest, name)
		skill, err := skills.ParseSkill(filepath.Join(dir, "SKILL.md"), dir)
		if err != nil {
			t.Errorf("%s: ParseSkill: %v", name, err)
			continue
		}
		if skill.Metadata.Name != name {
			t.Errorf("%s: frontmatter name = %q, want directory name %q", name, skill.Metadata.Name, name)
		}
		if skill.Metadata.Description == "" {
			t.Errorf("%s: frontmatter description is empty", name)
		}
	}
}

// TestSeedSkills_InitAssetsAdapted verifies spec-init ships exactly the two
// vendored templates and NOT the upstream workflow-template.md (ADR-104
// decision 2: no specs/WORKFLOW.md artifact is introduced).
func TestSeedSkills_InitAssetsAdapted(t *testing.T) {
	dest := t.TempDir()
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	for _, asset := range []string{"meta-template.md", "adr-template.md"} {
		if _, err := os.Stat(filepath.Join(dest, "spec-init", "assets", asset)); err != nil {
			t.Errorf("spec-init/assets/%s missing: %v", asset, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "spec-init", "assets", "workflow-template.md")); !os.IsNotExist(err) {
		t.Errorf("spec-init/assets/workflow-template.md must not ship (ADR-104 D2), stat err=%v", err)
	}
}

// TestSeedSkills_NoUpstreamArtifacts scans every embedded pack file and
// verifies the vendoring left no upstream traces: no "vibespec" name and no
// WORKFLOW.md artifact references (ADR-104 decisions 1 and 2).
func TestSeedSkills_NoUpstreamArtifacts(t *testing.T) {
	err := fs.WalkDir(skillPackFS, embedRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, rerr := skillPackFS.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(strings.ToLower(string(data)), "vibespec") {
			t.Errorf("%s: upstream name %q survived the adaptation", p, "vibespec")
		}
		if strings.Contains(string(data), "WORKFLOW") {
			t.Errorf("%s: WORKFLOW.md reference survived the adaptation (ADR-104 D2)", p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded pack: %v", err)
	}
}

// TestSeedSkills_IdempotentSameVersion verifies re-seeding with an unchanged
// pack version skips everything and preserves existing (possibly user-edited)
// content byte-for-byte. A same-version skill whose content diverges from the
// pack (the user edit below) is hash-diff'd against the embedded pack,
// preserved untouched, and reported as Modified — never Current (a spoofed
// marker must not masquerade as the pack) and never silently overwritten.
func TestSeedSkills_IdempotentSameVersion(t *testing.T) {
	dest := t.TempDir()

	first, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("first SeedSkills: %v", err)
	}
	if len(first.Seeded) != 6 || len(first.Current) != 0 {
		t.Fatalf("first pass: Seeded=%v Current=%v, want Seeded=6 Current=0", first.Seeded, first.Current)
	}

	// Simulate a user edit to a seeded skill (still marked at current version).
	edited := filepath.Join(dest, "spec-init", "SKILL.md")
	userContent := []byte("---\nname: spec-init\ndescription: edited\n---\n# edited body\n")
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
	if len(second.Current) != 5 {
		t.Fatalf("second pass: Current=%v, want the 5 unedited skills", second.Current)
	}
	if len(second.Modified) != 1 || second.Modified[0] != "spec-init" {
		t.Fatalf("second pass: Modified=%v, want [spec-init]", second.Modified)
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

	// Pre-create a user-owned spec-init skill with no marker.
	userDir := filepath.Join(dest, "spec-init")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	userSKILL := []byte("---\nname: spec-init\ndescription: user-owned\n---\n# mine\n")
	if err := os.WriteFile(filepath.Join(userDir, "SKILL.md"), userSKILL, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if len(res.Preserved) != 1 || res.Preserved[0] != "spec-init" {
		t.Fatalf("Preserved: got %v, want [spec-init]", res.Preserved)
	}
	if len(res.Seeded) != 5 {
		t.Fatalf("Seeded: got %d, want 5 (the other five): %v", len(res.Seeded), res.Seeded)
	}

	// User content intact, and no marker written into a user-owned dir.
	got, err := os.ReadFile(filepath.Join(userDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, userSKILL) {
		t.Errorf("user-owned skill was clobbered:\ngot:\n%s\nwant:\n%s", got, userSKILL)
	}
	if _, err := os.Stat(filepath.Join(userDir, seedpack.VersionFile)); !os.IsNotExist(err) {
		t.Errorf("user-owned dir should not gain a %s marker", seedpack.VersionFile)
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
	dir := filepath.Join(dest, "spec-update")
	if err := os.WriteFile(filepath.Join(dir, seedpack.VersionFile), []byte("0"), 0o644); err != nil {
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
	if len(res.Updated) != 1 || res.Updated[0] != "spec-update" {
		t.Fatalf("Updated: got %v, want [spec-update]", res.Updated)
	}

	// Marker restored to current version; stale file removed by full overwrite.
	ok, v := seedpack.ReadVersion(dir)
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

// TestSeedSkills_RepairsTruncatedTree verifies a marker-carrying directory
// whose files are a strict subset of the pack (an interrupted write) is
// repaired via a full Update, not mistaken for a user edit.
func TestSeedSkills_RepairsTruncatedTree(t *testing.T) {
	dest := t.TempDir()

	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}

	// Drop one nested file from spec-explore, leaving the marker intact.
	dropped := filepath.Join(dest, "spec-explore", "references", "roadmap-format.md")
	if err := os.Remove(dropped); err != nil {
		t.Fatalf("remove nested file: %v", err)
	}

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if len(res.Updated) != 1 || res.Updated[0] != "spec-explore" {
		t.Fatalf("Updated: got %v, want [spec-explore] (truncated-tree repair)", res.Updated)
	}
	if len(res.Modified) != 0 {
		t.Fatalf("Modified: got %v, want empty (truncation is not a user edit)", res.Modified)
	}
	if _, err := os.Stat(dropped); err != nil {
		t.Errorf("dropped file not restored by repair: %v", err)
	}
}

// TestSeedSkills_EmptyDestErrors verifies the guard on an empty destination.
func TestSeedSkills_EmptyDestErrors(t *testing.T) {
	if _, err := SeedSkills("", nil); err == nil {
		t.Fatal("SeedSkills with empty dest should error")
	}
}
