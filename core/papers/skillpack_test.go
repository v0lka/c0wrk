package papers

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/skills"
)

// expectedSkillNames is the canonical set bundled in the pack, sorted.
var expectedSkillNames = []string{"study-paper"}

func TestSkillNames(t *testing.T) {
	got := SkillNames()
	if len(got) != len(expectedSkillNames) {
		t.Fatalf("SkillNames: got %d names, want %d: %v", len(got), len(expectedSkillNames), got)
	}
	for i, want := range expectedSkillNames {
		if got[i] != want {
			t.Errorf("SkillNames[%d]: got %q, want %q", i, got[i], want)
		}
	}
}

// TestSeedSkills_EmbedsAll verifies the pack materializes every skill with a
// SKILL.md, a current seed-version marker, and that the SkillManager discovery
// picks them all up (mirrors core/research TestSeedSkills_EmbedsAllSeven).
func TestSeedSkills_EmbedsAll(t *testing.T) {
	dest := t.TempDir()

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if len(res.Seeded) != len(expectedSkillNames) {
		t.Fatalf("Seeded: got %d, want %d: %v", len(res.Seeded), len(expectedSkillNames), res.Seeded)
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

	// Discovery: a SkillManager scanning the seeded dir must list the skill.
	sm := skills.NewSkillManager([]string{dest}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("SkillManager.Scan: %v", err)
	}
	list := sm.List()
	if len(list) != len(expectedSkillNames) {
		t.Fatalf("SkillManager.List: got %d skills, want %d: %+v", len(list), len(expectedSkillNames), list)
	}
	gotNames := make(map[string]bool, len(list))
	for _, d := range list {
		gotNames[d.Name] = true
		if strings.TrimSpace(d.Description) == "" {
			t.Errorf("SkillManager.List: skill %q has an empty description", d.Name)
		}
	}
	for _, want := range expectedSkillNames {
		if !gotNames[want] {
			t.Errorf("SkillManager.List missing %q", want)
		}
	}
}

// linkTargetRe matches Markdown link targets that point at a bundled resource
// relative to the skill root (references/…, assets/…, scripts/…).
var linkTargetRe = regexp.MustCompile(`\]\(((?:references|assets|scripts)/[^)\s]+)\)`)

// TestSeedSkills_SkillMDFileMapResolves verifies every resource link in the
// SKILL.md file map points at a file that is actually bundled in the embedded
// pack (acceptance: "все .md-ресурсы ссылаются корректно"). It checks the
// embed FS directly, so a renamed/moved/removed resource is caught before it
// can ship as a dangling link.
func TestSeedSkills_SkillMDFileMapResolves(t *testing.T) {
	for _, name := range expectedSkillNames {
		skillMD := filepath.ToSlash(filepath.Join(embedRoot, name, "SKILL.md"))
		data, err := skillPackFS.ReadFile(skillMD)
		if err != nil {
			t.Fatalf("read embedded %s: %v", skillMD, err)
		}
		matches := linkTargetRe.FindAllStringSubmatch(string(data), -1)
		if len(matches) == 0 {
			t.Fatalf("%s: no bundled-resource links found — file map missing?", skillMD)
		}
		seen := make(map[string]bool, len(matches))
		for _, m := range matches {
			target := m[1]
			if seen[target] {
				continue
			}
			seen[target] = true
			embedded := filepath.ToSlash(filepath.Join(embedRoot, name, target))
			if _, err := skillPackFS.ReadFile(embedded); err != nil {
				t.Errorf("%s references %q but it is not embedded (%s): %v", skillMD, target, embedded, err)
			}
		}
	}
}

// TestSeedSkills_AssetsAndScriptsEmbedded verifies the nested references/,
// assets/ and scripts/ subtrees survive the embed + write round-trip, and that
// the dropped PDF converter (to_markdown.py) is NOT shipped (c0wrk uses the
// managed markitdown CLI instead).
func TestSeedSkills_AssetsAndScriptsEmbedded(t *testing.T) {
	dest := t.TempDir()
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	root := filepath.Join(dest, "study-paper")
	want := []string{
		filepath.Join(root, "references", "appraisal.md"),
		filepath.Join(root, "references", "extraction-and-fidelity.md"),
		filepath.Join(root, "references", "genres.md"),
		filepath.Join(root, "references", "literature-context.md"),
		filepath.Join(root, "references", "stats-and-benchmarks.md"),
		filepath.Join(root, "assets", "note-template.md"),
		filepath.Join(root, "assets", "appraisal-template.md"),
		filepath.Join(root, "assets", "comparison-matrix.md"),
		filepath.Join(root, "assets", "flashcards.md"),
		filepath.Join(root, "assets", "explainer-outline.md"),
		filepath.Join(root, "scripts", "fetch_paper.py"),
		filepath.Join(root, "scripts", "literature.py"),
	}
	for _, p := range want {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("embedded resource missing %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "scripts", "to_markdown.py")); !os.IsNotExist(err) {
		t.Errorf("to_markdown.py must not be shipped (markitdown is the PDF path); stat err = %v", err)
	}
}

// TestSeedSkills_IdempotentSameVersion verifies re-seeding with an unchanged
// pack version skips everything and preserves existing (possibly user-edited)
// content byte-for-byte. A same-version skill whose content diverges from the
// pack (the user edit below) is hash-diff'd against the embedded pack,
// preserved untouched, and reported as Modified — never silently overwritten.
func TestSeedSkills_IdempotentSameVersion(t *testing.T) {
	dest := t.TempDir()

	first, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("first SeedSkills: %v", err)
	}
	if len(first.Seeded) != 1 || len(first.Current) != 0 {
		t.Fatalf("first pass: Seeded=%v Current=%v, want Seeded=1 Current=0", first.Seeded, first.Current)
	}

	// Simulate a user edit to the seeded skill (still marked at current version).
	edited := filepath.Join(dest, "study-paper", "SKILL.md")
	userContent := []byte("---\nname: study-paper\ndescription: edited\n---\n# edited body\n")
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
	if len(second.Modified) != 1 || second.Modified[0] != "study-paper" {
		t.Fatalf("second pass: Modified=%v, want [study-paper]", second.Modified)
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

// TestSeedSkills_PreservesUserOwnedSkill verifies a pre-existing skill
// directory without a pack marker (user-authored) is never overwritten.
func TestSeedSkills_PreservesUserOwnedSkill(t *testing.T) {
	dest := t.TempDir()

	// Pre-create a user-owned study-paper skill with no marker.
	userDir := filepath.Join(dest, "study-paper")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	userSKILL := []byte("---\nname: study-paper\ndescription: user-owned\n---\n# mine\n")
	if err := os.WriteFile(filepath.Join(userDir, "SKILL.md"), userSKILL, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if len(res.Preserved) != 1 || res.Preserved[0] != "study-paper" {
		t.Fatalf("Preserved: got %v, want [study-paper]", res.Preserved)
	}
	if len(res.Seeded) != 0 {
		t.Fatalf("Seeded: got %v, want none", res.Seeded)
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

	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}

	dir := filepath.Join(dest, "study-paper")
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
	if len(res.Updated) != 1 || res.Updated[0] != "study-paper" {
		t.Fatalf("Updated: got %v, want [study-paper]", res.Updated)
	}

	ok, v := readSeedVersion(dir)
	if !ok || v != CurrentSeedVersion {
		t.Errorf("after update: marker %q (ok=%v), want %q", v, ok, CurrentSeedVersion)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale file should have been removed on version-bump overwrite")
	}
}

// TestSeedSkills_EmptyDestErrors verifies the guard on an empty destination.
func TestSeedSkills_EmptyDestErrors(t *testing.T) {
	if _, err := SeedSkills("", nil); err == nil {
		t.Fatal("SeedSkills with empty dest should error")
	}
}

// TestEmbeddedScriptsStdlibOnly guards the pack's scripts against introducing
// third-party imports: c0wrk runs them on its managed Python without extra
// installs, so only the standard library may be imported.
func TestEmbeddedScriptsStdlibOnly(t *testing.T) {
	allowed := map[string]bool{
		"argparse": true, "html": true, "json": true, "os": true, "re": true,
		"shutil": true, "socket": true, "ssl": true, "sys": true, "textwrap": true,
		"time": true, "typing": true, "urllib": true, "xml": true, "dataclasses": true,
		"collections": true, "itertools": true, "functools": true, "pathlib": true,
		"hashlib": true, "tempfile": true, "gzip": true, "io": true, "csv": true,
		"unittest": true, "datetime": true, "math": true, "string": true, "base64": true,
		"http": true, "email": true, "contextlib": true, "traceback": true, "warnings": true,
		"__future__": true, "subprocess": true, "unicodedata": true, "difflib": true,
		"statistics": true, "operator": true, "types": true, "abc": true,
	}
	importRe := regexp.MustCompile(`(?m)^\s*(?:import|from)\s+([A-Za-z_][A-Za-z0-9_]*)`)
	for _, script := range []string{"fetch_paper.py", "literature.py"} {
		p := filepath.ToSlash(filepath.Join(embedRoot, "study-paper", "scripts", script))
		data, err := skillPackFS.ReadFile(p)
		if err != nil {
			t.Fatalf("read embedded %s: %v", p, err)
		}
		for _, m := range importRe.FindAllStringSubmatch(string(data), -1) {
			mod := m[1]
			if !allowed[mod] {
				t.Errorf("%s imports non-stdlib module %q", script, mod)
			}
		}
	}
}

// TestSeedSkills_SkillMDNoLicenseFrontMatter pins the study-paper front matter
// to the research-skill convention (name/description/metadata): the vendored MIT
// `license:` field is gone and a `metadata:` block is present. It also pins the
// seed-version bump that a pack-content change requires.
func TestSeedSkills_SkillMDNoLicenseFrontMatter(t *testing.T) {
	if CurrentSeedVersion != "4" {
		t.Errorf("CurrentSeedVersion = %q, want %q (pack content changed)", CurrentSeedVersion, "4")
	}
	licenseRe := regexp.MustCompile(`(?m)^license:`)
	metadataRe := regexp.MustCompile(`(?m)^metadata:`)
	for _, name := range expectedSkillNames {
		p := filepath.ToSlash(filepath.Join(embedRoot, name, "SKILL.md"))
		data, err := skillPackFS.ReadFile(p)
		if err != nil {
			t.Fatalf("read embedded %s: %v", p, err)
		}
		if licenseRe.Match(data) {
			t.Errorf("%s front matter still carries a license: field", name)
		}
		if !metadataRe.Match(data) {
			t.Errorf("%s front matter is missing the metadata: block", name)
		}
	}
}
