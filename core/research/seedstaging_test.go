package research

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failSeedWritesContaining swaps the seedWriteFile seam so that any write
// whose destination path contains needle fails, simulating a process kill
// mid-tree-write at a deterministic point. The seam is restored on cleanup.
// Tests using it MUST NOT run in parallel (the seam is package-global).
func failSeedWritesContaining(t *testing.T, needle string) {
	t.Helper()
	orig := seedWriteFile
	seedWriteFile = func(name string, data []byte, perm fs.FileMode) error {
		if strings.Contains(name, needle) {
			return errors.New("injected write failure: " + name)
		}
		return orig(name, data, perm)
	}
	t.Cleanup(func() { seedWriteFile = orig })
}

// restoreSeedWriteFile restores the seam early (for tests that need a second,
// successful run after the interrupted one within the same test).
func restoreSeedWriteFile(t *testing.T) {
	t.Helper()
	seedWriteFile = os.WriteFile
}

// assertDirMatchesPack verifies that the on-disk directory for one pack entry
// is byte-identical to the embedded pack subtree (marker excluded — it is not
// part of either file set).
func assertDirMatchesPack(t *testing.T, fsys fs.FS, root, name, destDir, context string) {
	t.Helper()
	pack, err := packFileSet(fsys, root, name)
	if err != nil {
		t.Fatalf("%s: packFileSet: %v", context, err)
	}
	disk, err := diskFileSet(filepath.Join(destDir, name))
	if err != nil {
		t.Fatalf("%s: diskFileSet: %v", context, err)
	}
	if len(disk) != len(pack) {
		t.Fatalf("%s: file count = %d, want %d (disk=%v)", context, len(disk), len(pack), keysOf(disk))
	}
	for rel, want := range pack {
		got, ok := disk[rel]
		if !ok {
			t.Errorf("%s: pack file %q missing on disk", context, rel)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: %q differs from the embedded pack content", context, rel)
		}
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// hiddenLeftovers returns the hidden staging/backup directories left in dir.
func hiddenLeftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			found = append(found, e.Name())
		}
	}
	return found
}

// TestSeedSkills_InterruptedWriteMidTreeLeavesNoPartialState is the
// crash-injection acceptance test for the fresh-seed path (review [58]): a
// write interrupted in the middle of a skill's tree must leave NO directory at
// the target (the staging swap guarantees a target is either absent or
// complete), and the next seeding must classify the absent entry as fresh —
// never Current — and repair it.
func TestSeedSkills_InterruptedWriteMidTreeLeavesNoPartialState(t *testing.T) {
	dest := t.TempDir()

	// Fail exactly at research-init's third staged file
	// (assets/graph-template.md): SKILL.md and assets/brief-template.md have
	// already landed in the staging dir — a literal mid-tree interruption.
	failSeedWritesContaining(t, "graph-template.md")

	if _, err := SeedSkills(dest, nil); err == nil {
		t.Fatal("SeedSkills with injected mid-tree write failure should error")
	}

	// The interrupted skill must NOT exist at the target — no partial tree.
	interrupted := filepath.Join(dest, "research-init")
	if _, err := os.Stat(interrupted); !os.IsNotExist(err) {
		t.Fatalf("interrupted skill dir must not exist; stat err = %v", err)
	}
	// The synchronous failure path cleans its own staging dir up.
	if leftovers := hiddenLeftovers(t, dest); len(leftovers) != 0 {
		t.Fatalf("failed run must not leave staging leftovers, got %v", leftovers)
	}
	// Skills completed before the interruption are intact.
	assertDirMatchesPack(t, skillPackFS, embedRoot, "research-decision", dest, "pre-interruption skill")

	// Next seeding (crash over): the interrupted skill and the never-attempted
	// tail of the pack are classified fresh (Seeded), never Current, and
	// materialized completely; the three skills completed before the
	// interruption are Current.
	restoreSeedWriteFile(t)
	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("second SeedSkills: %v", err)
	}
	wantSeeded := []string{"research-init", "research-prior-art", "research-status", "research-synthesis"}
	if len(res.Seeded) != len(wantSeeded) {
		t.Fatalf("second pass Seeded = %v, want %v", res.Seeded, wantSeeded)
	}
	for i, want := range wantSeeded {
		if res.Seeded[i] != want {
			t.Fatalf("second pass Seeded = %v, want %v", res.Seeded, wantSeeded)
		}
	}
	if len(res.Current) != 3 {
		t.Fatalf("second pass Current = %v, want the 3 skills completed before the interruption", res.Current)
	}
	assertDirMatchesPack(t, skillPackFS, embedRoot, "research-init", dest, "repaired skill")
	if ok, v := readSeedVersion(interrupted); !ok || v != CurrentSeedVersion {
		t.Errorf("repaired skill marker = (%q, %v), want %q", v, ok, CurrentSeedVersion)
	}
}

// TestSeedSkills_InterruptedUpdateKeepsOldDirIntact is the crash-injection
// acceptance test for the version-bump path (review [58]): an update whose
// staged write is interrupted must leave the OLD directory fully intact (the
// old code's RemoveAll-first ordering destroyed it), and the next seeding must
// classify it as stale (Updated), never Current, and complete the upgrade.
func TestSeedSkills_InterruptedUpdateKeepsOldDirIntact(t *testing.T) {
	dest := t.TempDir()
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("initial SeedSkills: %v", err)
	}

	// Simulate an old-pack research-init: older marker + diverged content.
	oldDir := filepath.Join(dest, "research-init")
	oldSKILL := []byte("---\nname: research-init\ndescription: old pack\n---\n# old body\n")
	if err := os.WriteFile(filepath.Join(oldDir, "SKILL.md"), oldSKILL, 0o644); err != nil {
		t.Fatalf("write old SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, seedVersionFile), []byte("1"), 0o644); err != nil {
		t.Fatalf("backdate marker: %v", err)
	}

	// Interrupt the staged rewrite of research-init at its first file.
	failSeedWritesContaining(t, ".research-init.seed-tmp-")
	if _, err := SeedSkills(dest, nil); err == nil {
		t.Fatal("SeedSkills with injected write failure should error")
	}

	// The old directory is fully intact: diverged content AND old marker.
	got, err := os.ReadFile(filepath.Join(oldDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("old SKILL.md lost after interrupted update: %v", err)
	}
	if !bytes.Equal(got, oldSKILL) {
		t.Fatalf("interrupted update must not touch the old dir content")
	}
	if ok, v := readSeedVersion(oldDir); !ok || v != "1" {
		t.Fatalf("old marker = (%q, %v), want (\"1\", true)", v, ok)
	}
	if leftovers := hiddenLeftovers(t, dest); len(leftovers) != 0 {
		t.Fatalf("failed run must not leave staging leftovers, got %v", leftovers)
	}

	// Next seeding: the old-marker entry is stale → Updated, content == pack.
	restoreSeedWriteFile(t)
	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("second SeedSkills: %v", err)
	}
	if len(res.Updated) != 1 || res.Updated[0] != "research-init" {
		t.Fatalf("second pass Updated = %v, want [research-init]", res.Updated)
	}
	if len(res.Current) != 6 {
		t.Fatalf("second pass Current = %v, want 6", res.Current)
	}
	assertDirMatchesPack(t, skillPackFS, embedRoot, "research-init", dest, "upgraded skill")
}

// TestSeedSkills_MarkerPlusTruncatedTreeIsStaleNotCurrent pins the rejection
// of the marker-first ([58]a) ordering: a directory carrying a CURRENT-version
// marker over a truncated strict subset of the pack (the state "marker first"
// would leave behind on a crash) must be classified stale and repaired by the
// next seeding — never Current.
func TestSeedSkills_MarkerPlusTruncatedTreeIsStaleNotCurrent(t *testing.T) {
	dest := t.TempDir()

	// Plant research-init: current marker + only SKILL.md (assets missing),
	// with the present file byte-identical to the pack — a pure truncation.
	dir := filepath.Join(dest, "research-init")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	packSKILL, err := skillPackFS.ReadFile(filepath.ToSlash(filepath.Join(embedRoot, "research-init", "SKILL.md")))
	if err != nil {
		t.Fatalf("read pack SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), packSKILL, 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, seedVersionFile), []byte(CurrentSeedVersion), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if contains(res.Current, "research-init") {
		t.Fatalf("marker + truncated tree must NOT be classified Current; Current=%v", res.Current)
	}
	if len(res.Updated) != 1 || res.Updated[0] != "research-init" {
		t.Fatalf("Updated = %v, want [research-init] (stale → repaired)", res.Updated)
	}
	assertDirMatchesPack(t, skillPackFS, embedRoot, "research-init", dest, "repaired truncated tree")
	if ok, v := readSeedVersion(dir); !ok || v != CurrentSeedVersion {
		t.Errorf("marker after repair = (%q, %v), want %q", v, ok, CurrentSeedVersion)
	}
}

// TestSeedSkills_SpoofedMarkerWithAlienContentNotCurrent pins review [44]: a
// repo-shipped directory whose spoofed .seed-version equals the current pack
// version but whose content is NOT the pack must not be reported as Current
// (the false "current pack" provenance signal). It is preserved untouched,
// exactly like a marker-less user directory, and reported as Modified.
func TestSeedSkills_SpoofedMarkerWithAlienContentNotCurrent(t *testing.T) {
	dest := t.TempDir()

	dir := filepath.Join(dest, "research-hypothesis")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	alien := []byte("---\nname: research-hypothesis\ndescription: attacker content\n---\n# not the pack\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), alien, 0o644); err != nil {
		t.Fatalf("write alien SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, seedVersionFile), []byte(CurrentSeedVersion), 0o644); err != nil {
		t.Fatalf("write spoofed marker: %v", err)
	}

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if contains(res.Current, "research-hypothesis") {
		t.Fatalf("spoofed marker must NOT be classified Current; Current=%v", res.Current)
	}
	if len(res.Modified) != 1 || res.Modified[0] != "research-hypothesis" {
		t.Fatalf("Modified = %v, want [research-hypothesis]", res.Modified)
	}
	got, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, alien) {
		t.Errorf("alien content must be preserved, not overwritten")
	}
}

// TestSeedSkills_MarkerlessExactPackContentReclaimed pins the [58]c repair of
// the legacy crash window: a complete pack-content directory whose marker is
// missing (the old write order could be killed between tree write and marker
// write) is recognized as pack-owned via its content hash, reported Current,
// and only the marker is re-stamped — the content itself is not rewritten.
func TestSeedSkills_MarkerlessExactPackContentReclaimed(t *testing.T) {
	dest := t.TempDir()
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	dir := filepath.Join(dest, "research-status")
	skillMD := filepath.Join(dir, "SKILL.md")
	before, err := os.ReadFile(skillMD)
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, seedVersionFile)); err != nil {
		t.Fatalf("remove marker: %v", err)
	}

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("second SeedSkills: %v", err)
	}
	if !contains(res.Current, "research-status") {
		t.Fatalf("marker-less pack-content dir must be reclaimed as Current; Current=%v", res.Current)
	}
	if len(res.Preserved) != 0 || len(res.Modified) != 0 || len(res.Updated) != 0 {
		t.Fatalf("unexpected buckets: Preserved=%v Modified=%v Updated=%v", res.Preserved, res.Modified, res.Updated)
	}
	if ok, v := readSeedVersion(dir); !ok || v != CurrentSeedVersion {
		t.Errorf("marker after reclaim = (%q, %v), want %q", v, ok, CurrentSeedVersion)
	}
	after, err := os.ReadFile(skillMD)
	if err != nil {
		t.Fatalf("re-read SKILL.md: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("reclaim must not rewrite content")
	}
}

// TestSeedSkills_DotfilesDoNotAffectClassification verifies OS noise (.DS_Store
// and friends) never flips a Current directory into Modified: dot entries are
// excluded from the content hash on both sides.
func TestSeedSkills_DotfilesDoNotAffectClassification(t *testing.T) {
	dest := t.TempDir()
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "research-status", ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("write .DS_Store: %v", err)
	}

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("second SeedSkills: %v", err)
	}
	if len(res.Current) != 7 || len(res.Modified) != 0 {
		t.Fatalf("dotfile noise must not change classification: Current=%v Modified=%v", res.Current, res.Modified)
	}
}

// TestSeedSkills_SweepsCrashLeftoverStagingDirs verifies the sweep of hidden
// staging/backup directories a hard kill leaves behind (where the synchronous
// cleanup never ran): the next seeding removes them and proceeds normally.
func TestSeedSkills_SweepsCrashLeftoverStagingDirs(t *testing.T) {
	dest := t.TempDir()

	leftoverStaging := filepath.Join(dest, ".research-init.seed-tmp-999")
	if err := os.MkdirAll(leftoverStaging, 0o755); err != nil {
		t.Fatalf("mkdir staging leftover: %v", err)
	}
	if err := os.WriteFile(filepath.Join(leftoverStaging, "SKILL.md"), []byte("partial"), 0o644); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	leftoverBackup := filepath.Join(dest, ".research-status.seed-old")
	if err := os.MkdirAll(leftoverBackup, 0o755); err != nil {
		t.Fatalf("mkdir backup leftover: %v", err)
	}

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	if len(res.Seeded) != 7 {
		t.Fatalf("Seeded = %d, want 7: %v", len(res.Seeded), res.Seeded)
	}
	if leftovers := hiddenLeftovers(t, dest); len(leftovers) != 0 {
		t.Fatalf("crash leftovers must be swept, got %v", leftovers)
	}
	assertDirMatchesPack(t, skillPackFS, embedRoot, "research-init", dest, "skill after sweep")
}

// TestSeedAgents_MarkerPlusTruncatedTreeIsRepaired mirrors the marker-first
// trap for the agent pack (the original [58] filing): an empty profile
// directory carrying a current-version marker is a truncated strict subset of
// the pack and must be repaired, never classified Current.
func TestSeedAgents_MarkerPlusTruncatedTreeIsRepaired(t *testing.T) {
	dest := t.TempDir()

	dir := filepath.Join(dest, "research")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, seedVersionFile), []byte(AgentSeedVersion), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	res, err := SeedAgents(dest, nil)
	if err != nil {
		t.Fatalf("SeedAgents: %v", err)
	}
	if contains(res.Current, "research") {
		t.Fatalf("marker + empty tree must NOT be classified Current; Current=%v", res.Current)
	}
	if len(res.Updated) != 1 || res.Updated[0] != "research" {
		t.Fatalf("Updated = %v, want [research]", res.Updated)
	}
	assertDirMatchesPack(t, agentPackFS, agentEmbedRoot, "research", dest, "repaired profile")
	if ok, v := readSeedVersion(dir); !ok || v != AgentSeedVersion {
		t.Errorf("marker after repair = (%q, %v), want %q", v, ok, AgentSeedVersion)
	}
}

// TestSeedAgents_InterruptedWriteLeavesNoPartialState is the crash-injection
// test for the agent pack's fresh-seed path.
func TestSeedAgents_InterruptedWriteLeavesNoPartialState(t *testing.T) {
	dest := t.TempDir()

	failSeedWritesContaining(t, "AGENT.md")
	if _, err := SeedAgents(dest, nil); err == nil {
		t.Fatal("SeedAgents with injected write failure should error")
	}
	if _, err := os.Stat(filepath.Join(dest, "research")); !os.IsNotExist(err) {
		t.Fatalf("interrupted profile dir must not exist; stat err = %v", err)
	}
	if leftovers := hiddenLeftovers(t, dest); len(leftovers) != 0 {
		t.Fatalf("failed run must not leave staging leftovers, got %v", leftovers)
	}

	restoreSeedWriteFile(t)
	res, err := SeedAgents(dest, nil)
	if err != nil {
		t.Fatalf("second SeedAgents: %v", err)
	}
	if len(res.Seeded) != 1 || res.Seeded[0] != "research" {
		t.Fatalf("Seeded = %v, want [research]", res.Seeded)
	}
	assertDirMatchesPack(t, agentPackFS, agentEmbedRoot, "research", dest, "re-seeded profile")
}

// TestSeedSkills_CleanSeedHashMatchesPack pins the symmetry the hash
// classification depends on: after a clean seed, diskFileSet and packFileSet
// produce identical (path → bytes) sets, so the next run classifies every
// skill Current.
func TestSeedSkills_CleanSeedHashMatchesPack(t *testing.T) {
	dest := t.TempDir()
	if _, err := SeedSkills(dest, nil); err != nil {
		t.Fatalf("SeedSkills: %v", err)
	}
	for _, name := range ResearchSkillNames() {
		assertDirMatchesPack(t, skillPackFS, embedRoot, name, dest, name)
	}

	res, err := SeedSkills(dest, nil)
	if err != nil {
		t.Fatalf("second SeedSkills: %v", err)
	}
	if len(res.Current) != 7 || len(res.Modified) != 0 || len(res.Updated) != 0 {
		t.Fatalf("clean re-seed must be all Current: %+v", res)
	}
}

// TestHashFileSet_Framing verifies the length-framed hash: path/content
// boundaries are unambiguous and the hash is deterministic.
func TestHashFileSet_Framing(t *testing.T) {
	if hashFileSet(map[string][]byte{"a": []byte("bc")}) == hashFileSet(map[string][]byte{"ab": []byte("c")}) {
		t.Fatal("path/content boundary must be framed: {a:bc} vs {ab:c} must differ")
	}
	first := map[string][]byte{"x": []byte("y")}
	second := map[string][]byte{"x": []byte("y")}
	if hashFileSet(first) != hashFileSet(second) {
		t.Fatal("hash must be deterministic for identical sets")
	}
	if hashFileSet(map[string][]byte{"x": []byte("y")}) == hashFileSet(map[string][]byte{"x": []byte("z")}) {
		t.Fatal("content change must change the hash")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
