package research

// Regression tests for the review findings on the research package: the
// fail-closed read of unreadable mutation targets (log.md / index.md),
// CreateHypothesis's hypDir containment, hypothesis-ID canonicalization
// (zero padding + numeric ordering), and the literal "<br>" escape
// round-trip — see the c1 review report.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requireNonRoot skips permission-bit tests when running as root (root reads
// through 0o000 modes, so the fail-closed path cannot be exercised).
func requireNonRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits do not block reads")
	}
}

// ---------------------------------------------------------------------------
// Fail-closed reads of unreadable mutation targets
// ---------------------------------------------------------------------------

// TestAppendLogEntry_UnreadableLogFailsClosed pins the data-loss guard: an
// existing but unreadable log.md must abort the append with an error instead
// of being treated as missing (which would replace the whole research
// history with a log containing only the new entry).
func TestAppendLogEntry_UnreadableLogFailsClosed(t *testing.T) {
	requireNonRoot(t)
	root, dir := setupProjectDir(t)
	logPath := filepath.Join(dir, "log.md")
	original := "# Research Log\n\n## experiment 2025-04-01T10:00:00Z [H-001]\n\nFirst entry.\n"
	if err := os.WriteFile(logPath, []byte(original), 0o644); err != nil {
		t.Fatalf("writing log: %v", err)
	}
	restoreLog := makeUnreadable(t, logPath)

	err := AppendLogEntry(root, dir, ResearchLogEntry{
		Kind:         LogKindNote,
		HypothesisID: "H-001",
		Message:      "must not be written",
	})
	if err == nil {
		t.Fatal("AppendLogEntry succeeded against an unreadable log.md")
	}
	if !strings.Contains(err.Error(), "log") {
		t.Errorf("error does not mention the log: %v", err)
	}

	// Restore readability (chmod on Unix, handle release on Windows) before
	// the byte-for-byte comparison re-reads the file.
	restoreLog()
	got, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("re-reading log: %v", readErr)
	}
	if string(got) != original {
		t.Errorf("log.md was modified despite the abort:\n%s", got)
	}
}

// TestUpdateHypothesis_UnreadableLogFailsClosed pins the same guard on the
// UpdateHypothesis status-change path: the whole mutation (card + graph +
// log) must abort and leave every file byte-for-byte unchanged when log.md
// cannot be read.
func TestUpdateHypothesis_UnreadableLogFailsClosed(t *testing.T) {
	requireNonRoot(t)
	root, dir := setupProjectDir(t)
	hypDir := filepath.Join(dir, "hypotheses")
	cardPath := filepath.Join(hypDir, "H-001.md")
	graphPath := filepath.Join(hypDir, "graph.md")
	logPath := filepath.Join(dir, "log.md")

	cardBefore, _ := os.ReadFile(cardPath)
	graphBefore, _ := os.ReadFile(graphPath)
	if err := os.WriteFile(logPath, []byte("# Research Log\n\n## note 2025-04-01T10:00:00Z [H-001]\n\nPrior.\n"), 0o644); err != nil {
		t.Fatalf("writing log: %v", err)
	}
	makeUnreadable(t, logPath)

	status := "in-progress"
	err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{Status: &status})
	if err == nil {
		t.Fatal("UpdateHypothesis succeeded against an unreadable log.md")
	}
	if !strings.Contains(err.Error(), "log") {
		t.Errorf("error does not mention the log: %v", err)
	}

	// The re-reads below report their errors instead of ignoring them: a
	// failed read returns nil content, which would masquerade as "modified"
	// and hide the actual I/O problem.
	cardAfter, cardErr := os.ReadFile(cardPath)
	if cardErr != nil {
		t.Fatalf("re-reading card: %v", cardErr)
	}
	graphAfter, graphErr := os.ReadFile(graphPath)
	if graphErr != nil {
		t.Fatalf("re-reading graph: %v", graphErr)
	}
	if !bytes.Equal(cardAfter, cardBefore) {
		t.Error("card was modified despite the log-read abort")
	}
	if !bytes.Equal(graphAfter, graphBefore) {
		t.Error("graph was modified despite the log-read abort")
	}
}

// TestSetActiveResearch_UnreadableIndexFailsClosed pins the same guard on the
// root index: an unreadable index.md must abort activation instead of being
// rewritten as a minimal single-row skeleton (which would drop every other
// project's row). R-001's row deliberately sits FIRST: activation must need a
// real index rewrite, so the guard fires on both platform dialects of
// "unreadable" — where the read fails (Unix permission bits), and where only
// the rename onto the locked file fails (a Windows handle with no sharing can
// be read through by some runner/file-system combinations, but replacing the
// file it holds is still denied). With R-001 already last, the no-op early
// return would mask the guard on such platforms.
func TestSetActiveResearch_UnreadableIndexFailsClosed(t *testing.T) {
	requireNonRoot(t)
	root, dir := setupProjectDir(t)
	indexPath := filepath.Join(root, "index.md")
	original := "# Research Index\n\n" +
		indexTableHeader + "\n" + indexTableSeparator + "\n" +
		"| [R-001](R-001-test/brief.md) | Test | | | | | [brief](R-001-test/brief.md) |\n" +
		"| [R-002](R-002-other/brief.md) | Other | | | | | [brief](R-002-other/brief.md) |\n"
	if err := os.WriteFile(indexPath, []byte(original), 0o644); err != nil {
		t.Fatalf("writing index: %v", err)
	}
	restoreIndex := makeUnreadable(t, indexPath)

	if err := SetActiveResearch(root, "R-001"); err == nil {
		t.Fatal("SetActiveResearch succeeded against an unreadable index.md")
	}

	// Restore readability (chmod on Unix, handle release on Windows) before
	// the byte-for-byte comparison re-reads the file.
	restoreIndex()
	got, readErr := os.ReadFile(indexPath)
	if readErr != nil {
		t.Fatalf("re-reading index: %v", readErr)
	}
	if string(got) != original {
		t.Errorf("index.md was modified despite the abort:\n%s", got)
	}
	// The project directory itself must be untouched by the failed path.
	if _, err := os.Stat(filepath.Join(dir, "brief.md")); err != nil {
		t.Errorf("project brief missing after failed activation: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CreateHypothesis hypDir containment
// ---------------------------------------------------------------------------

// TestCreateHypothesis_HypDirOutsideRootRejected pins the defense-in-depth
// gate: the hypotheses directory creation runs through the same containment
// check as the file writes, so a projectDir outside the research root is
// rejected before MkdirAll can create real directories outside the
// workspace.
func TestCreateHypothesis_HypDirOutsideRootRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // exists, but is not under root

	_, err := CreateHypothesis(root, outside, NewHypothesis{Title: "Escaping"})
	if err == nil {
		t.Fatal("CreateHypothesis accepted a projectDir outside the research root")
	}
	if !strings.Contains(err.Error(), "outside the research root") && !strings.Contains(err.Error(), "containment") {
		t.Errorf("error is not a containment rejection: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "hypotheses")); !os.IsNotExist(statErr) {
		t.Error("hypotheses directory was created outside the research root")
	}
}

// ---------------------------------------------------------------------------
// Hypothesis-ID canonicalization: zero padding + numeric ordering
// ---------------------------------------------------------------------------

// TestNormalizeID_HandWrittenUnpaddedMergesWithSeeded pins the H-1 → H-001
// canonicalization: a hand-named unpadded card and the seeded padded card
// are ONE hypothesis — one graph node, one maxHypothesisNumber identity.
func TestNormalizeID_HandWrittenUnpaddedMergesWithSeeded(t *testing.T) {
	root, dir := setupProjectDir(t)
	hypDir := filepath.Join(dir, "hypotheses")

	handCard := "# H-1: Hand duplicate\n\n" +
		"| Field | Value |\n|---|---|\n| **Identifier** | H-1 |\n| **Status** | open |\n\n" +
		"## Statement\n\nHand-written spelling of H-001.\n"
	if err := os.WriteFile(filepath.Join(hypDir, "H-1.md"), []byte(handCard), 0o644); err != nil {
		t.Fatalf("writing hand card: %v", err)
	}

	// maxHypothesisNumber treats H-1 and H-001 as the same number.
	if got := maxHypothesisNumber(dir); got != 1 {
		t.Errorf("maxHypothesisNumber = %d with H-1.md + H-001.md + graph H-001, want 1", got)
	}

	graphContent, _ := readFile(filepath.Join(hypDir, "graph.md"))
	mnodes, medges := ParseMermaidGraph(graphContent)
	g := BuildGraph(mnodes, medges, ParseCatalog(graphContent), loadCards(hypDir))
	count := 0
	for _, n := range g.Nodes {
		if n.ID == "H-001" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("hand-written H-1 and seeded H-001 coexist as %d nodes, want 1 merged node", count)
	}

	// The next created hypothesis skips the shared number, not duplicates it.
	id, err := CreateHypothesis(root, dir, NewHypothesis{Title: "Second"})
	if err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}
	if id != "H-002" {
		t.Errorf("CreateHypothesis id = %q, want H-002 (H-1 and H-001 share number 1)", id)
	}
}

// TestBuildGraph_NumericIDOrdering pins the numeric H-NNN ordering: H-2
// sorts before H-10, whether or not the spellings are padded.
func TestBuildGraph_NumericIDOrdering(t *testing.T) {
	mnodes := []mermaidNode{
		{id: "H-010", title: "Ten"},
		{id: "H-002", title: "Two"},
		{id: "H-001", title: "One"},
	}
	g := BuildGraph(mnodes, nil, nil, nil)
	want := []string{"H-001", "H-002", "H-010"}
	if len(g.Nodes) != len(want) {
		t.Fatalf("nodes = %d, want %d", len(g.Nodes), len(want))
	}
	for i, id := range want {
		if g.Nodes[i].ID != id {
			t.Errorf("node[%d] = %q, want %q (numeric order)", i, g.Nodes[i].ID, id)
		}
	}

	// Parents and normalizeParents order numerically too.
	if got := normalizeParents([]string{"H-10", "H-2", "H-001"}); len(got) != 3 || got[0] != "H-001" || got[1] != "H-002" || got[2] != "H-010" {
		t.Errorf("normalizeParents = %v, want [H-001 H-002 H-010]", got)
	}
}

// TestUpdateHypothesis_UnpaddedCardFilenameResolved pins cardPathForID: a
// card file named with the unpadded spelling (H-1.md) is still found and
// updated for its canonical ID, instead of being reported missing after the
// zero-padding canonicalization.
func TestUpdateHypothesis_UnpaddedCardFilenameResolved(t *testing.T) {
	root, dir := setupProjectDir(t)
	hypDir := filepath.Join(dir, "hypotheses")
	if err := os.Rename(filepath.Join(hypDir, "H-001.md"), filepath.Join(hypDir, "H-1.md")); err != nil {
		t.Fatalf("rename: %v", err)
	}

	status := "in-progress"
	if err := UpdateHypothesis(root, dir, "H-1", HypothesisUpdate{Status: &status}); err != nil {
		t.Fatalf("UpdateHypothesis on a hand-named H-1.md card: %v", err)
	}

	content, readErr := os.ReadFile(filepath.Join(hypDir, "H-1.md"))
	if readErr != nil {
		t.Fatalf("reading card: %v", readErr)
	}
	if !strings.Contains(string(content), "# H-001: Static bundle parsing") {
		t.Errorf("card heading not canonicalized to H-001:\n%s", content)
	}
	if !strings.Contains(string(content), "| **Status** | in-progress |") {
		t.Errorf("card status not updated:\n%s", content)
	}

	proj, err := ParseProject(dir)
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	n := proj.Graph.Node("H-001")
	if n == nil {
		t.Fatal("H-001 node missing after update")
	}
	if n.Status != StatusInProgress {
		t.Errorf("status = %q, want in-progress", n.Status)
	}
}

// ---------------------------------------------------------------------------
// Literal "<br>" escape round-trip
// ---------------------------------------------------------------------------

// TestEscapeLiteralBR_RoundTrip pins the shield: a value that legitimately
// contains the literal text "<br>" is written as "&lt;br&gt;" and parses
// back as "<br>", while real newlines still fold/unfold exactly.
func TestEscapeLiteralBR_RoundTrip(t *testing.T) {
	cases := []string{
		"uses a literal <br> tag",
		"first<br>line\nsecond<br>line",
		"mixed <br> and pipe | and\nnewline",
		// A value that literally contains the HTML-entity spelling must also
		// survive (single-level shield).
		"an entity spelling &lt;br&gt; inline",
		"both <br> and &lt;br&gt; together",
	}
	for _, raw := range cases {
		escaped := escapeCell(raw)
		if strings.ContainsAny(escaped, "\n\r") {
			t.Errorf("escapeCell(%q) still contains a newline: %q", raw, escaped)
		}
		// When the raw value has no newlines of its own, the escaped form must
		// contain no "<br>" at all (any occurrence would be an unshielded
		// literal). With real newlines, folded "<br>" markers are expected.
		if !strings.ContainsAny(raw, "\n\r") && strings.Contains(escaped, "<br>") {
			t.Errorf("escapeCell(%q) left an unshielded <br>: %q", raw, escaped)
		}
		row := "| **X** | " + escaped + " |"
		cells := splitCells(row)
		if len(cells) < 2 {
			t.Fatalf("splitCells(%q) = %v, want >= 2 cells", row, cells)
		}
		if got := cells[1]; got != raw {
			t.Errorf("round-trip mismatch: raw=%q got=%q (escaped=%q)", raw, got, escaped)
		}
		if again := escapeCell(cells[1]); again != escaped {
			t.Errorf("re-escape mismatch: first=%q second=%q", escaped, again)
		}
	}

	// Pure newlines still fold and unfold exactly.
	if got := unescapeCell(escapeCell("a\nb")); got != "a\nb" {
		t.Errorf("plain newline round-trip = %q, want %q", got, "a\nb")
	}
}

// TestEscapeMermaidLabel_LiteralBRRoundTrip pins the same shield on Mermaid
// labels: a title containing a literal "<br>" keeps matching
// mermaidNodeLineRe and parses back to the literal text, not a newline.
func TestEscapeMermaidLabel_LiteralBRRoundTrip(t *testing.T) {
	raw := "literal <br> in title"
	escaped := escapeMermaidLabel(raw)
	if strings.Contains(escaped, "<br>") || strings.Contains(escaped, "\"") || strings.ContainsAny(escaped, "\n\r") {
		t.Fatalf("escapeMermaidLabel produced an unparseable label: %q", escaped)
	}
	line := `    H001["H-001: ` + escaped + `"]:::open`
	m := mermaidNodeLineRe.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("written mermaid line does not match mermaidNodeLineRe: %s", line)
	}
	label := unescapeMermaidLabel(m[3])
	if idx := strings.Index(label, ":"); idx >= 0 {
		label = strings.TrimSpace(label[idx+1:])
	}
	if label != raw {
		t.Errorf("round-trip mismatch: raw=%q got=%q (escaped=%q)", raw, label, escaped)
	}
	if again := escapeMermaidLabel(label); again != escaped {
		t.Errorf("re-escape mismatch: first=%q second=%q", escaped, again)
	}
}

// TestSetFinding_LiteralBRRoundTrip pins the shield on the **Finding:** line:
// a finding containing a literal "<br>" reads back verbatim.
func TestSetFinding_LiteralBRRoundTrip(t *testing.T) {
	card := "# H-001: T\n\n## Result\n\n**Finding:** —\n"
	raw := "found a literal <br> in the output"
	got := setFinding(card, raw)
	if !strings.Contains(got, "**Finding:** found a literal &lt;br&gt; in the output") {
		t.Fatalf("finding line not shielded:\n%s", got)
	}
	if back := extractFinding(got); back != raw {
		t.Errorf("extractFinding = %q, want %q", back, raw)
	}
	if again := setFinding(got, raw); again != got {
		t.Errorf("setFinding not idempotent under the shield:\n%s\n---\n%s", got, again)
	}
}
