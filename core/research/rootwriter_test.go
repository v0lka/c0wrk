package research

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedRootProject writes a minimal research project directory (brief only)
// under root whose brief carries the given rid and title, so the parsed
// project ID comes from the brief while the directory name may differ.
func seedRootProject(t *testing.T, root, dirName, rid, title string) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", dirName, err)
	}
	brief := fmt.Sprintf("# [%s] %s\n", rid, title)
	if err := os.WriteFile(filepath.Join(dir, "brief.md"), []byte(brief), 0o644); err != nil {
		t.Fatalf("brief %s: %v", dirName, err)
	}
	return dir
}

// writeRootIndex seeds the research root's index.md verbatim.
func writeRootIndex(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "index.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("index.md: %v", err)
	}
}

// canonicalIndex is the canonical index.md shape (see testdata/index.md): a
// project table followed by a bullet list that mirrors it. ParseIndex
// de-duplicates by first occurrence, so the TABLE order decides the active
// project; the bullet list exercises exactly that de-duplication.
const canonicalIndex = `# Research Index

Active research projects following the Iterative Engineering Research
Methodology.

| ID | Title | Status | Domain | Quarter | Researcher(s) | Brief |
|---|---|---|---|---|---|---|
| R-001 | Web Exfil Detection | Active | Web app security | 2025-Q2 | A. Researcher | [brief](R-001-web-exfil/brief.md) |
| R-002 | Empty Scaffold | Active | — | 2025-Q3 | — | [brief](R-002-empty/brief.md) |

- [R-001: Web Exfil Detection](R-001-web-exfil/brief.md)
- [R-002: Empty Scaffold](R-002-empty/brief.md)
`

// activeProjectID parses the root and returns PickActiveProject's choice
// ("" when no project exists).
func activeProjectID(t *testing.T, root string) string {
	t.Helper()
	parsed, err := ParseResearchRoot(root)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	if active := PickActiveProject(parsed); active != nil {
		return active.ID
	}
	return ""
}

// ──────────────────────────────────────────────────────────────────────────
// SetActiveResearch
// ──────────────────────────────────────────────────────────────────────────

// TestSetActiveResearch_MovesRowToEndChangesPickActiveProject pins the core
// contract: moving R-001's row to the end of the index table flips
// PickActiveProject from R-002 (the last chronological entry) to R-001 —
// the row move, not a directory rename, changes the active project. The
// bullet list stays in place and is ignored by ParseIndex's de-duplication.
func TestSetActiveResearch_MovesRowToEndChangesPickActiveProject(t *testing.T) {
	root := t.TempDir()
	seedRootProject(t, root, "R-001-web-exfil", "R-001", "Web Exfil Detection")
	seedRootProject(t, root, "R-002-empty", "R-002", "Empty Scaffold")
	writeRootIndex(t, root, canonicalIndex)

	if got := activeProjectID(t, root); got != "R-002" {
		t.Fatalf("active before = %q, want R-002 (last index entry)", got)
	}

	if err := SetActiveResearch(root, "R-001"); err != nil {
		t.Fatalf("SetActiveResearch: %v", err)
	}

	if got := activeProjectID(t, root); got != "R-001" {
		t.Fatalf("active after = %q, want R-001 (moved row is the last entry)", got)
	}

	content := string(mustRead(t, filepath.Join(root, "index.md")))
	// The moved row must come after R-002's row in the table.
	r002 := strings.Index(content, "| R-002 |")
	r001 := strings.Index(content, "| R-001 |")
	if r002 < 0 || r001 < 0 {
		t.Fatalf("index lost a project row:\n%s", content)
	}
	if r001 < r002 {
		t.Errorf("R-001 row must follow R-002's row after the move:\n%s", content)
	}
	// The bullet list is preserved verbatim (de-duplicated by the parser).
	if !strings.Contains(content, "- [R-001: Web Exfil Detection](R-001-web-exfil/brief.md)") {
		t.Errorf("bullet entry for R-001 was dropped:\n%s", content)
	}
	// Both projects still parse: nothing but the row order changed.
	parsed, err := ParseResearchRoot(root)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	if len(parsed.Projects) != 2 {
		t.Errorf("projects = %d, want 2", len(parsed.Projects))
	}
	if parsed.ActiveProjectID != "R-001" {
		t.Errorf("ActiveProjectID = %q, want R-001", parsed.ActiveProjectID)
	}
}

// TestSetActiveResearch_CreatesMissingIndex covers "index.md отсутствует":
// a root with projects but no index gets one, seeded with a minimal row for
// rid, which becomes the single (last, therefore active) entry — overriding
// the highest-numbered-directory fallback that made R-002 active before.
func TestSetActiveResearch_CreatesMissingIndex(t *testing.T) {
	root := t.TempDir()
	seedRootProject(t, root, "R-001-web-exfil", "R-001", "Web Exfil Detection")
	seedRootProject(t, root, "R-002-empty", "R-002", "Empty Scaffold")

	if got := activeProjectID(t, root); got != "R-002" {
		t.Fatalf("active before = %q, want R-002 (highest-numbered dir fallback)", got)
	}

	if err := SetActiveResearch(root, "R-001"); err != nil {
		t.Fatalf("SetActiveResearch: %v", err)
	}

	if got := activeProjectID(t, root); got != "R-001" {
		t.Fatalf("active after = %q, want R-001", got)
	}

	content := string(mustRead(t, filepath.Join(root, "index.md")))
	for _, want := range []string{
		"# Research Index",
		indexTableHeader,
		indexTableSeparator,
		"[brief](R-001-web-exfil/brief.md)",
		"| R-001 | Web Exfil Detection |",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("created index missing %q:\n%s", want, content)
		}
	}
	// The fresh index carries exactly one entry: R-001.
	parsed, err := ParseResearchRoot(root)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	if len(parsed.Index) != 1 || parsed.Index[0].ID != "R-001" {
		t.Errorf("index entries = %+v, want exactly [R-001]", parsed.Index)
	}
}

// TestSetActiveResearch_AppendsMissingRowToExistingTable covers "строки нет":
// the index exists but carries no row for rid — a minimal row matching the
// table's column count is appended at the table's end.
func TestSetActiveResearch_AppendsMissingRowToExistingTable(t *testing.T) {
	root := t.TempDir()
	seedRootProject(t, root, "R-001-web-exfil", "R-001", "Web Exfil Detection")
	seedRootProject(t, root, "R-002-empty", "R-002", "Empty Scaffold")
	writeRootIndex(t, root, `# Research Index

| ID | Title | Status | Domain | Quarter | Researcher(s) | Brief |
|---|---|---|---|---|---|---|
| R-002 | Empty Scaffold | Active | — | 2025-Q3 | — | [brief](R-002-empty/brief.md) |
`)

	if err := SetActiveResearch(root, "R-001"); err != nil {
		t.Fatalf("SetActiveResearch: %v", err)
	}

	if got := activeProjectID(t, root); got != "R-001" {
		t.Fatalf("active after = %q, want R-001", got)
	}
	content := string(mustRead(t, filepath.Join(root, "index.md")))
	wantRow := "| R-001 | Web Exfil Detection | — | — | — | — | [brief](R-001-web-exfil/brief.md) |"
	if !strings.Contains(content, wantRow) {
		t.Errorf("minimal row missing/wrong:\n%s", content)
	}
	if !strings.HasSuffix(strings.TrimRight(content, "\n"), wantRow) {
		t.Errorf("minimal row must be the last table row:\n%s", content)
	}
}

// TestSetActiveResearch_BulletsOnlyIndexMovesBulletToEnd covers an index
// without a table: the entry line (a bullet) moves to the end of the file,
// which is what makes it the last parsed entry.
func TestSetActiveResearch_BulletsOnlyIndexMovesBulletToEnd(t *testing.T) {
	root := t.TempDir()
	seedRootProject(t, root, "R-001-web-exfil", "R-001", "Web Exfil Detection")
	seedRootProject(t, root, "R-002-empty", "R-002", "Empty Scaffold")
	writeRootIndex(t, root, `# Research Index

- [R-001: Web Exfil Detection](R-001-web-exfil/brief.md)
- [R-002: Empty Scaffold](R-002-empty/brief.md)
`)

	if got := activeProjectID(t, root); got != "R-002" {
		t.Fatalf("active before = %q, want R-002", got)
	}
	if err := SetActiveResearch(root, "R-001"); err != nil {
		t.Fatalf("SetActiveResearch: %v", err)
	}
	if got := activeProjectID(t, root); got != "R-001" {
		t.Fatalf("active after = %q, want R-001", got)
	}
	content := string(mustRead(t, filepath.Join(root, "index.md")))
	if !strings.HasSuffix(strings.TrimRight(content, "\n"), "- [R-001: Web Exfil Detection](R-001-web-exfil/brief.md)") {
		t.Errorf("R-001 bullet must be the last entry line:\n%s", content)
	}
}

// TestSetActiveResearch_AlreadyLastIsNoOp verifies that activating the
// project whose row already is the last table entry rewrites nothing.
func TestSetActiveResearch_AlreadyLastIsNoOp(t *testing.T) {
	root := t.TempDir()
	seedRootProject(t, root, "R-001-web-exfil", "R-001", "Web Exfil Detection")
	seedRootProject(t, root, "R-002-empty", "R-002", "Empty Scaffold")
	writeRootIndex(t, root, canonicalIndex)

	if err := SetActiveResearch(root, "R-002"); err != nil {
		t.Fatalf("SetActiveResearch: %v", err)
	}
	if got := activeProjectID(t, root); got != "R-002" {
		t.Fatalf("active after = %q, want R-002", got)
	}
	if content := string(mustRead(t, filepath.Join(root, "index.md"))); content != canonicalIndex {
		t.Errorf("index rewritten although R-002 already was last:\n%s", content)
	}
}

// TestSetActiveResearch_UnknownRidRejected: an id that resolves to no project
// under the root is rejected before index.md is touched.
func TestSetActiveResearch_UnknownRidRejected(t *testing.T) {
	root := t.TempDir()
	seedRootProject(t, root, "R-001-web-exfil", "R-001", "Web Exfil Detection")
	writeRootIndex(t, root, canonicalIndex)

	if err := SetActiveResearch(root, "R-099"); err == nil {
		t.Fatal("expected an error for a research id with no project under the root")
	}
	if content := string(mustRead(t, filepath.Join(root, "index.md"))); content != canonicalIndex {
		t.Errorf("index.md must stay byte-for-byte unchanged:\n%s", content)
	}
}

// TestSetActiveResearch_InvalidRidRejected: input carrying no R-NNN at all is
// rejected up front.
func TestSetActiveResearch_InvalidRidRejected(t *testing.T) {
	root := t.TempDir()
	if err := SetActiveResearch(root, "not-a-research-id"); err == nil {
		t.Fatal("expected an error for a malformed research id")
	}
}

// ──────────────────────────────────────────────────────────────────────────
// DeleteResearchProject
// ──────────────────────────────────────────────────────────────────────────

// TestDeleteResearchProject_RemovesRowAndDirectory pins the delete contract:
// the index loses EVERY R-001 entry line (table row and bullet duplicate),
// the R-NNN-* directory tree is removed in full, and the remaining project
// takes over as active.
func TestDeleteResearchProject_RemovesRowAndDirectory(t *testing.T) {
	root := t.TempDir()
	dir1 := seedRootProject(t, root, "R-001-web-exfil", "R-001", "Web Exfil Detection")
	dir2 := seedRootProject(t, root, "R-002-empty", "R-002", "Empty Scaffold")
	// Give R-001 a non-empty tree so RemoveAll has real work.
	if err := os.MkdirAll(filepath.Join(dir1, "hypotheses"), 0o755); err != nil {
		t.Fatalf("MkdirAll hypotheses: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir1, "hypotheses", "graph.md"), []byte("# Hypothesis Graph\n"), 0o644); err != nil {
		t.Fatalf("seed graph: %v", err)
	}
	writeRootIndex(t, root, canonicalIndex)

	if err := DeleteResearchProject(root, "R-001"); err != nil {
		t.Fatalf("DeleteResearchProject: %v", err)
	}

	if _, err := os.Stat(dir1); !os.IsNotExist(err) {
		t.Errorf("project directory %q still exists (stat err: %v)", dir1, err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "brief.md")); err != nil {
		t.Errorf("sibling project must survive: %v", err)
	}
	content := string(mustRead(t, filepath.Join(root, "index.md")))
	if strings.Contains(content, "R-001") {
		t.Errorf("index still references R-001:\n%s", content)
	}
	if !strings.Contains(content, "R-002") {
		t.Errorf("index lost R-002:\n%s", content)
	}
	if got := activeProjectID(t, root); got != "R-002" {
		t.Errorf("active after delete = %q, want R-002", got)
	}
}

// TestDeleteResearchProject_NoIndexJustRemovesDir: a root without index.md
// still deletes the project directory cleanly.
func TestDeleteResearchProject_NoIndexJustRemovesDir(t *testing.T) {
	root := t.TempDir()
	dir1 := seedRootProject(t, root, "R-001-web-exfil", "R-001", "Web Exfil Detection")
	seedRootProject(t, root, "R-002-empty", "R-002", "Empty Scaffold")

	if err := DeleteResearchProject(root, "R-001"); err != nil {
		t.Fatalf("DeleteResearchProject: %v", err)
	}
	if _, err := os.Stat(dir1); !os.IsNotExist(err) {
		t.Errorf("project directory %q still exists (stat err: %v)", dir1, err)
	}
	if got := activeProjectID(t, root); got != "R-002" {
		t.Errorf("active after delete = %q, want R-002", got)
	}
}

// TestDeleteResearchProject_UnknownRidRejected: deleting an id with no project
// under the root is an error and removes nothing.
func TestDeleteResearchProject_UnknownRidRejected(t *testing.T) {
	root := t.TempDir()
	dir1 := seedRootProject(t, root, "R-001-web-exfil", "R-001", "Web Exfil Detection")
	writeRootIndex(t, root, canonicalIndex)

	if err := DeleteResearchProject(root, "R-099"); err == nil {
		t.Fatal("expected an error for a research id with no project under the root")
	}
	if _, err := os.Stat(dir1); err != nil {
		t.Errorf("project directory must survive a rejected delete: %v", err)
	}
	if content := string(mustRead(t, filepath.Join(root, "index.md"))); content != canonicalIndex {
		t.Errorf("index.md must stay byte-for-byte unchanged:\n%s", content)
	}
}

// TestDeleteResearchProject_SymlinkedRootStillContained verifies the
// resolution logic does not false-reject a research root reached through a
// symlink (macOS /var → /private/var and user-managed links alike): root and
// project directory resolve consistently, and the delete lands on the real
// location.
func TestDeleteResearchProject_SymlinkedRootStillContained(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real-root")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll real root: %v", err)
	}
	seedRootProject(t, realRoot, "R-001-web-exfil", "R-001", "Web Exfil Detection")
	seedRootProject(t, realRoot, "R-002-empty", "R-002", "Empty Scaffold")
	writeRootIndex(t, realRoot, canonicalIndex)
	link := filepath.Join(base, "root-link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatalf("Symlink root: %v", err)
	}

	if err := DeleteResearchProject(link, "R-001"); err != nil {
		t.Fatalf("DeleteResearchProject through a symlinked root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(realRoot, "R-001-web-exfil")); !os.IsNotExist(err) {
		t.Errorf("project directory must be gone under the real root (stat err: %v)", err)
	}
	if got := activeProjectID(t, realRoot); got != "R-002" {
		t.Errorf("active after delete = %q, want R-002", got)
	}
}

// TestResolveProjectDirWithinRoot unit-tests the delete containment gate
// directly: a project directory outside the root and one equal to the root
// itself are rejected; a direct child resolves and is returned resolved.
func TestResolveProjectDirWithinRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "research-root")
	project := filepath.Join(root, "R-001-x")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, project, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
	}

	if _, err := resolveProjectDirWithinRoot(root, outside); err == nil {
		t.Error("expected a directory outside the root to be rejected")
	}
	if _, err := resolveProjectDirWithinRoot(root, root); err == nil {
		t.Error("expected the research root itself to be rejected (RemoveAll would wipe the tree)")
	}
	resolved, err := resolveProjectDirWithinRoot(root, project)
	if err != nil {
		t.Fatalf("valid project directory rejected: %v", err)
	}
	if resolvedRoot, rerr := resolveExistingDir(root); rerr != nil || resolved != filepath.Join(resolvedRoot, "R-001-x") {
		t.Errorf("resolved = %q, want %q (err: %v)", resolved, filepath.Join(resolvedRoot, "R-001-x"), rerr)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// index.md pure helpers
// ──────────────────────────────────────────────────────────────────────────

// TestMoveIndexRowToTableEnd_TableRowMovesAfterLaterRows verifies the pure
// move: a mid-table row lands after every other table row, and the move is
// reported.
func TestMoveIndexRowToTableEnd_TableRowMovesAfterLaterRows(t *testing.T) {
	content := `# Research Index

| ID | Title | Brief |
|---|---|---|
| R-001 | Alpha | [brief](R-001-a/brief.md) |
| R-002 | Beta | [brief](R-002-b/brief.md) |
| R-003 | Gamma | [brief](R-003-c/brief.md) |

- [R-003: Gamma](R-003-c/brief.md)
`
	next, moved := moveIndexRowToTableEnd(content, "R-001")
	if !moved {
		t.Fatal("moved = false, want true")
	}
	lines := strings.Split(next, "\n")
	var rowIdx, r003Idx int
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "| R-001 |"):
			rowIdx = i
		case strings.HasPrefix(l, "| R-003 |"):
			r003Idx = i
		}
	}
	if rowIdx < r003Idx {
		t.Errorf("R-001 row (line %d) must come after R-003's row (line %d):\n%s", rowIdx, r003Idx, next)
	}
	if !strings.Contains(next, "- [R-003: Gamma](R-003-c/brief.md)") {
		t.Errorf("unrelated bullet entry must be preserved:\n%s", next)
	}
}

// TestMoveIndexRowToTableEnd_UnknownRidUnchanged: no row for rid means the
// content is returned verbatim and the move is reported as absent.
func TestMoveIndexRowToTableEnd_UnknownRidUnchanged(t *testing.T) {
	content := "| R-001 | Alpha | [brief](R-001-a/brief.md) |\n"
	next, moved := moveIndexRowToTableEnd(content, "R-099")
	if moved {
		t.Error("moved = true, want false")
	}
	if next != content {
		t.Errorf("content changed:\n%s", next)
	}
}

// TestRemoveIndexEntryLines_RemovesEveryReference: both the table row and the
// bullet duplicate of rid disappear; other lines survive verbatim.
func TestRemoveIndexEntryLines_RemovesEveryReference(t *testing.T) {
	next := removeIndexEntryLines(canonicalIndex, "R-001")
	if strings.Contains(next, "R-001") {
		t.Errorf("R-001 still referenced:\n%s", next)
	}
	for _, want := range []string{"# Research Index", "| R-002 |", "- [R-002: Empty Scaffold](R-002-empty/brief.md)"} {
		if !strings.Contains(next, want) {
			t.Errorf("lost %q:\n%s", want, next)
		}
	}
	if again := removeIndexEntryLines(next, "R-001"); again != next {
		t.Errorf("second removal must be a no-op:\n%s", again)
	}
}
