package research

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// seedOrderingProject writes a minimal research project directory (brief only)
// whose brief carries the given R-NNN id, so the parsed project ID comes from
// the brief (the parser's preference) while the directory name may differ.
func seedOrderingProject(t *testing.T, root, dirName, rid string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dirName, err)
	}
	brief := fmt.Sprintf("# [%s] Minimal project\n", rid)
	if err := os.WriteFile(filepath.Join(dir, "brief.md"), []byte(brief), 0o644); err != nil {
		t.Fatalf("brief %s: %v", dirName, err)
	}
	return dir
}

// TestParseResearchRoot_NumericProjectOrdering verifies the R-NNN sort is
// number-aware ([70]): unpadded directory names like R-2 and R-10 must sort
// numerically (R-2 first), and PickActiveProject's no-index fallback must
// therefore select R-10 — lexicographic order would put "R-2" last and mutate
// the wrong project.
func TestParseResearchRoot_NumericProjectOrdering(t *testing.T) {
	root := t.TempDir()
	seedOrderingProject(t, root, "R-10-tenth", "R-10")
	seedOrderingProject(t, root, "R-2-second", "R-2")

	parsed, err := ParseResearchRoot(root)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	if len(parsed.Projects) != 2 {
		t.Fatalf("projects = %d, want 2", len(parsed.Projects))
	}
	if parsed.Projects[0].ID != "R-2" {
		t.Errorf("first project = %q, want R-2 (numeric order)", parsed.Projects[0].ID)
	}
	if parsed.Projects[1].ID != "R-10" {
		t.Errorf("last project = %q, want R-10 (numeric order)", parsed.Projects[1].ID)
	}
	if active := PickActiveProject(parsed); active == nil || active.ID != "R-10" {
		t.Errorf("PickActiveProject = %+v, want R-10 (highest number, not lexicographic)", active)
	}
}

// TestCompareResearchIDs covers the numeric comparator directly: numbers
// compare numerically, equal numbers tie-break lexicographically, and
// non-numeric IDs sort after numbered ones deterministically.
func TestCompareResearchIDs(t *testing.T) {
	cases := []struct {
		a, b string
		want int // sign of compareResearchIDs(a, b)
	}{
		{"R-2", "R-10", -1},
		{"R-10", "R-2", 1},
		{"R-002", "R-2", -1}, // equal numbers → lexicographic tie-break ("R-002" < "R-2")
		{"R-1-a", "R-1-b", -1},
		{"R-1", "not-an-id", -1},
		{"not-an-id", "R-1", 1},
		{"alpha", "beta", -1},
	}
	for _, tc := range cases {
		got := compareResearchIDs(tc.a, tc.b)
		if (got < 0) != (tc.want < 0) || (got > 0) != (tc.want > 0) {
			t.Errorf("compareResearchIDs(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestActiveProjectDir_ResolvesParsedDirDespiteBriefMismatch verifies [66]
// scenario (a): when the brief's "# [R-NNN]" header disagrees with the
// directory name (model typo, renamed/copied dir), ActiveProjectDir must
// return the directory the parser actually read (ResearchProject.Dir) instead
// of failing with "directory not found" after matching names against the
// brief-preferred ID.
func TestActiveProjectDir_ResolvesParsedDirDespiteBriefMismatch(t *testing.T) {
	root := t.TempDir()
	renamed := seedOrderingProject(t, root, "R-002-renamed-copy", "R-001")

	parsed, err := ParseResearchRoot(root)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	if len(parsed.Projects) != 1 || parsed.Projects[0].ID != "R-001" {
		t.Fatalf("parsed projects = %+v, want one R-001 (brief ID wins)", parsed.Projects)
	}
	if parsed.Projects[0].Dir != renamed {
		t.Errorf("ResearchProject.Dir = %q, want %q", parsed.Projects[0].Dir, renamed)
	}

	got, err := ActiveProjectDir(root)
	if err != nil {
		t.Fatalf("ActiveProjectDir: %v (regression: name-matching could not resolve the brief ID)", err)
	}
	if got != renamed {
		t.Errorf("ActiveProjectDir = %q, want %q (the parsed directory)", got, renamed)
	}
}

// TestActiveProjectDir_DuplicateRIDsDeterministic verifies [66] scenario (b):
// two directories normalizing to the same R-NNN (a copy without index.md)
// must yield ONE deterministic active directory — the same project the panel
// renders (PickActiveProject) and the one mutations target (ActiveProjectDir),
// never a split panel-A/writes-B state.
func TestActiveProjectDir_DuplicateRIDsDeterministic(t *testing.T) {
	root := t.TempDir()
	dirA := seedOrderingProject(t, root, "R-001-a", "R-001")
	dirB := seedOrderingProject(t, root, "R-001-b", "R-001")

	parsed, err := ParseResearchRoot(root)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	active := PickActiveProject(parsed)
	if active == nil || active.ID != "R-001" {
		t.Fatalf("PickActiveProject = %+v, want R-001", active)
	}

	got, err := ActiveProjectDir(root)
	if err != nil {
		t.Fatalf("ActiveProjectDir: %v", err)
	}
	// Deterministic tie-break: dir-name order, so the last (active) is R-001-b.
	if got != dirB {
		t.Errorf("ActiveProjectDir = %q, want %q (deterministic duplicate-ID order)", got, dirB)
	}
	if got != active.Dir || dirA == dirB {
		t.Errorf("mutation target %q diverges from the rendered project's Dir %q", got, active.Dir)
	}
}

// TestParseLog_ToleratesTrailingTokens verifies [64]: model-authored headings
// with free-form annotation after the structural tokens ("… H-001 (final
// pass)") must still parse as entries instead of being silently dropped along
// with their whole body. The annotation itself is ignored.
func TestParseLog_ToleratesTrailingTokens(t *testing.T) {
	content := `## experiment 2025-04-02T10:15:00Z H-001 (final pass)
Recovered 97% of modules on the first pass.

## note 2025-04-03T09:00:00Z some trailing annotation without hypothesis id
Free-form note body.
`
	entries := ParseLog(content)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (trailing tokens must not drop entries): %+v", len(entries), entries)
	}

	e := entries[0]
	if e.Kind != LogKindExperiment {
		t.Errorf("kind = %q, want experiment", e.Kind)
	}
	if e.CreatedAt != "2025-04-02T10:15:00Z" {
		t.Errorf("created_at = %q", e.CreatedAt)
	}
	if e.HypothesisID != "H-001" {
		t.Errorf("hypothesis id = %q, want H-001", e.HypothesisID)
	}
	if e.Message != "Recovered 97% of modules on the first pass." {
		t.Errorf("message = %q", e.Message)
	}

	n := entries[1]
	if n.Kind != LogKindNote {
		t.Errorf("kind = %q, want note", n.Kind)
	}
	if n.HypothesisID != "" {
		t.Errorf("hypothesis id = %q, want empty (trailing prose is not an id)", n.HypothesisID)
	}
	if n.Message != "Free-form note body." {
		t.Errorf("message = %q", n.Message)
	}
}

// TestProjectDir_PrefersParserActiveDuplicate verifies the ProjectDir fix:
// with two directories normalizing to the same R-NNN and NO index.md, the
// panel renders PickActiveProject's last-match duplicate (dir-name
// tie-break), so ProjectDir must resolve to that SAME directory — not the
// first sorted match — or an edit to the visible card silently writes to the
// other project's copy.
func TestProjectDir_PrefersParserActiveDuplicate(t *testing.T) {
	root := t.TempDir()
	dirA := seedOrderingProject(t, root, "R-001-a", "R-001")
	dirB := seedOrderingProject(t, root, "R-001-b", "R-001")

	parsed, err := ParseResearchRoot(root)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	active := PickActiveProject(parsed)
	if active == nil || active.ID != "R-001" || active.Dir != dirB {
		t.Fatalf("PickActiveProject = %+v, want the last-match duplicate %q", active, dirB)
	}

	got, err := ProjectDir(root, "R-001")
	if err != nil {
		t.Fatalf("ProjectDir: %v", err)
	}
	if got != dirB {
		t.Errorf("ProjectDir = %q, want %q (the parser-active duplicate, not first match %q)", got, dirB, dirA)
	}

	// The resolution must agree with ActiveProjectDir so the mutation
	// target is exactly the project the panel renders.
	activeDir, err := ActiveProjectDir(root)
	if err != nil {
		t.Fatalf("ActiveProjectDir: %v", err)
	}
	if got != activeDir {
		t.Errorf("ProjectDir %q diverges from ActiveProjectDir %q", got, activeDir)
	}
}

// TestProjectDir_IndexActiveFirstMatchDuplicate covers the index.md case,
// where PickActiveProject's semantics differ from the no-index fallback: the
// active project is the FIRST sorted match of the newest index entry's ID.
// ProjectDir must still follow the parser's selection (dir-a here), staying
// consistent with what the panel renders.
func TestProjectDir_IndexActiveFirstMatchDuplicate(t *testing.T) {
	root := t.TempDir()
	dirA := seedOrderingProject(t, root, "R-001-a", "R-001")
	dirB := seedOrderingProject(t, root, "R-001-b", "R-001")

	index := "# Research Index\n\n- [Alpha copy](R-001-a/brief.md)\n"
	if err := os.WriteFile(filepath.Join(root, "index.md"), []byte(index), 0o644); err != nil {
		t.Fatalf("index.md: %v", err)
	}

	parsed, err := ParseResearchRoot(root)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	active := PickActiveProject(parsed)
	if active == nil || active.Dir != dirA {
		t.Fatalf("PickActiveProject = %+v, want the first-match duplicate %q (index entry)", active, dirA)
	}

	got, err := ProjectDir(root, "R-001")
	if err != nil {
		t.Fatalf("ProjectDir: %v", err)
	}
	if got != dirA {
		t.Errorf("ProjectDir = %q, want %q (index-selected first match, not last match %q)", got, dirA, dirB)
	}
}

// TestProjectDir_FallsBackToFirstMatchWhenNotActive verifies the fallback
// path: when the requested R-NNN is NOT the active project (here R-002 is
// active as the highest number without an index), ProjectDir keeps its
// original first-match resolution for the duplicate ID.
func TestProjectDir_FallsBackToFirstMatchWhenNotActive(t *testing.T) {
	root := t.TempDir()
	dirA := seedOrderingProject(t, root, "R-001-a", "R-001")
	_ = seedOrderingProject(t, root, "R-001-b", "R-001")
	dirR2 := seedOrderingProject(t, root, "R-002-second", "R-002")

	parsed, err := ParseResearchRoot(root)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	if active := PickActiveProject(parsed); active == nil || active.ID != "R-002" {
		t.Fatalf("PickActiveProject = %+v, want R-002 active (highest number, no index)", active)
	}

	got, err := ProjectDir(root, "R-001")
	if err != nil {
		t.Fatalf("ProjectDir: %v", err)
	}
	if got != dirA {
		t.Errorf("ProjectDir = %q, want %q (first-match fallback for non-active id)", got, dirA)
	}

	gotR2, err := ProjectDir(root, "R-002")
	if err != nil {
		t.Fatalf("ProjectDir(R-002): %v", err)
	}
	if gotR2 != dirR2 {
		t.Errorf("ProjectDir(R-002) = %q, want %q", gotR2, dirR2)
	}
}
