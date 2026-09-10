package research

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Status transition state machine
// ---------------------------------------------------------------------------

func TestValidateTransition(t *testing.T) {
	cases := []struct {
		name    string
		from    HypothesisStatus
		to      HypothesisStatus
		wantErr bool
	}{
		{"open→in-progress", StatusOpen, StatusInProgress, false},
		{"open→cancelled", StatusOpen, StatusCancelled, false},
		{"open→confirmed (skip)", StatusOpen, StatusConfirmed, true},
		{"in-progress→confirmed", StatusInProgress, StatusConfirmed, false},
		{"in-progress→refuted", StatusInProgress, StatusRefuted, false},
		{"in-progress→cancelled", StatusInProgress, StatusCancelled, false},
		{"in-progress→open (backward)", StatusInProgress, StatusOpen, true},
		{"confirmed→anything (terminal)", StatusConfirmed, StatusRefuted, true},
		{"refuted→anything (terminal)", StatusRefuted, StatusCancelled, true},
		{"cancelled→anything (terminal)", StatusCancelled, StatusOpen, true},
		{"same status (no-op)", StatusOpen, StatusOpen, false},
		{"empty→known (initial set)", "", StatusInProgress, false},
		{"unknown→known (initial set)", HypothesisStatus("bogus"), StatusOpen, false},
		{"→unknown target", StatusOpen, HypothesisStatus("bogus"), true},
		{"in_progress spelling normalized", StatusOpen, HypothesisStatus("in_progress"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTransition(tc.from, tc.to)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q→%q, got nil", tc.from, tc.to)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %q→%q: %v", tc.from, tc.to, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Pure card/graph content rewriting
// ---------------------------------------------------------------------------

func TestSetTableField_ReplaceAndInsert(t *testing.T) {
	card := "# H-001: Title\n\n| Field | Value |\n|---|---|\n| **Identifier** | H-001 |\n| **Status** | open |\n| **Timebox** | 5 days |\n\n## Statement\n\nx\n"

	got := setTableField(card, "Status", "in-progress")
	if !strings.Contains(got, "| **Status** | in-progress |") {
		t.Fatalf("Status not replaced:\n%s", got)
	}

	// Insert a field that does not yet exist.
	got = setTableField(card, "Decision", "continue")
	if !strings.Contains(got, "| **Decision** | continue |") {
		t.Fatalf("Decision not inserted:\n%s", got)
	}
}

func TestSetFinding_ReplaceAndInsert(t *testing.T) {
	card := "# H-001: Title\n\n## Result\n\n**Finding:** old\n\n**Prototype / Proof:** —\n"
	got := setFinding(card, "new finding")
	if !strings.Contains(got, "**Finding:** new finding") {
		t.Fatalf("finding not replaced:\n%s", got)
	}
	if strings.Contains(got, "**Finding:** old") {
		t.Fatalf("old finding still present:\n%s", got)
	}

	// No Result section: append one.
	card2 := "# H-001: Title\n\n## Statement\n\nx\n"
	got2 := setFinding(card2, "fresh")
	if !strings.Contains(got2, "## Result") || !strings.Contains(got2, "**Finding:** fresh") {
		t.Fatalf("Result section not appended:\n%s", got2)
	}
}

func TestSetSection_ReplaceAndInsertAndClear(t *testing.T) {
	card := "# H-001: Title\n\n## Statement\n\nold statement\n\n## Verification Criterion\n\nold criterion\n\n## Result\n\n**Finding:** —\n"

	// Replace an existing section body, multi-line, verbatim.
	got := setSection(card, "Statement", "line one\nline two")
	if !strings.Contains(got, "## Statement\n\nline one\nline two\n\n## Verification Criterion") {
		t.Fatalf("Statement not replaced:\n%s", got)
	}
	if strings.Contains(got, "old statement") {
		t.Fatalf("old statement still present:\n%s", got)
	}
	// Later sections are preserved.
	if !strings.Contains(got, "## Verification Criterion\n\nold criterion") {
		t.Fatalf("later section clobbered:\n%s", got)
	}

	// Heading match mirrors the parser's Contains semantics (decorations ok).
	got = setSection(card, "Verification Criterion", "new criterion")
	if !strings.Contains(got, "## Verification Criterion\n\nnew criterion") {
		t.Fatalf("decorated heading not matched:\n%s", got)
	}

	// Append a section that does not exist.
	got2 := setSection(card, "Experiment Notes", "notes body")
	if !strings.Contains(got2, "## Experiment Notes\n\nnotes body") {
		t.Fatalf("Experiment Notes not appended:\n%s", got2)
	}

	// Clear to empty: the heading stays, the body goes.
	got3 := setSection(card, "Statement", "")
	if !strings.Contains(got3, "## Statement") {
		t.Fatalf("Statement heading dropped:\n%s", got3)
	}
	if strings.Contains(got3, "old statement") {
		t.Fatalf("body not cleared:\n%s", got3)
	}
	if !strings.Contains(got3, "## Verification Criterion") {
		t.Fatalf("later section dropped on clear:\n%s", got3)
	}
}

func TestUpdateMermaidNode_LabelAndClass(t *testing.T) {
	graph := "```mermaid\ngraph TD\n    H001[\"H-001: Old title\"]:::open\n    H001 --> H002\n```\n"
	title := "New title"
	status := "in-progress"
	got := updateMermaidNode(graph, "H-001", &title, &status)
	if !strings.Contains(got, `H001["H-001: New title"]:::in_progress`) {
		t.Fatalf("mermaid node not updated:\n%s", got)
	}
}

func TestUpdateCatalogRow_TitleStatusDecision(t *testing.T) {
	catalog := "| ID | Hypothesis | Status | Decision | Parent(s) |\n|---|---|---|---|---|\n| [H-001](H-001.md) | Old | open | — | — |\n"
	title := "New"
	status := "confirmed"
	decision := "continue"
	got := updateCatalogRow(catalog, "H-001", &title, &status, &decision)
	if !strings.Contains(got, "| [H-001](H-001.md) | New | confirmed | continue | — |") {
		t.Fatalf("catalog row not updated:\n%s", got)
	}
}

func TestAddCatalogRow_PlaceholderAndAppend(t *testing.T) {
	// Placeholder row gets replaced.
	empty := "| ID | Hypothesis | Status | Decision | Parent(s) |\n|---|---|---|---|---|\n| *No hypotheses yet. Use the `research-hypothesis` skill to create the first one.* | | | | |\n"
	got := addCatalogRow(empty, "H-001", "First", StatusOpen, "", nil)
	if !strings.Contains(got, "| [H-001](H-001.md) | First | open | — | — |") {
		t.Fatalf("placeholder not replaced:\n%s", got)
	}
	if strings.Contains(got, "No hypotheses yet") {
		t.Fatalf("placeholder still present:\n%s", got)
	}

	// Existing row → append after it.
	existing := "| ID | Hypothesis | Status | Decision | Parent(s) |\n|---|---|---|---|---|\n| [H-001](H-001.md) | One | confirmed | continue | — |\n"
	got2 := addCatalogRow(existing, "H-002", "Two", StatusOpen, "", []string{"H-001"})
	if !strings.Contains(got2, "| [H-002](H-002.md) | Two | open | — | H-001 |") {
		t.Fatalf("row not appended:\n%s", got2)
	}
	if strings.Index(got2, "H-001") > strings.Index(got2, "H-002") {
		t.Fatalf("appended row out of order:\n%s", got2)
	}
}

func TestAddMermaidNodeAndEdges(t *testing.T) {
	graph := "```mermaid\ngraph TD\n    H001[\"H-001: One\"]:::open\n    H001 --> H002[\"H-002: Two\"]:::open\n```\n"
	got := addMermaidNodeAndEdges(graph, "H-003", "Three", StatusOpen, []string{"H-001"})
	if !strings.Contains(got, `H003["H-003: Three"]:::open`) {
		t.Fatalf("node not added:\n%s", got)
	}
	if !strings.Contains(got, "H001 --> H003") {
		t.Fatalf("edge not added:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// Round-trip: write via writer → ParseProject reflects the change
// ---------------------------------------------------------------------------

// setupProjectDir builds a minimal research project directory (nested layout)
// with a brief, an hypotheses/graph.md, and a single open hypothesis card.
// It returns the research root and the project directory under it, matching
// the mutation entry points' (researchRoot, projectDir) signature.
func setupProjectDir(t *testing.T) (root, dir string) {
	t.Helper()
	root = t.TempDir()
	dir = filepath.Join(root, "R-001-test")
	hypDir := filepath.Join(dir, "hypotheses")
	if err := os.MkdirAll(hypDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	brief := "# [R-001] Test\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.md"), []byte(brief), 0o644); err != nil {
		t.Fatalf("brief: %v", err)
	}
	graph := `# Hypothesis Graph — R-001

## Diagram

` + "```mermaid\n" + `graph TD
    classDef confirmed fill:#4CAF50,color:#fff
    classDef refuted fill:#F44336,color:#fff
    classDef in_progress fill:#FF9800,color:#fff
    classDef open fill:#2196F3,color:#fff
    classDef cancelled fill:#9E9E9E,color:#fff

    H001["H-001: Static bundle parsing"]:::open
` + "```\n" + `
## Hypothesis Catalog

| ID | Hypothesis | Status | Decision | Parent(s) |
|---|---|---|---|---|
| [H-001](H-001.md) | Static bundle parsing | open | — | — |

---

[Back to Brief](../brief.md)
`
	if err := os.WriteFile(filepath.Join(hypDir, "graph.md"), []byte(graph), 0o644); err != nil {
		t.Fatalf("graph: %v", err)
	}
	card := `# H-001: Static bundle parsing

| Field | Value |
|---|---|
| **Identifier** | H-001 |
| **Status** | open |
| **Timebox** | 5 days |
| **Parent(s)** | — |
| **Created** | 2025-04-02 |
| **Completed** | — |
| **Decision** | — |

## Statement

Static analysis of webpack bundles can recover the module graph.

## Verification Criterion

Recover >= 95% of modules.

## Experiment Notes

*Not yet started.*

## Result

**Finding:** —

---

[Back to Hypothesis Graph](graph.md) | [Back to Brief](../brief.md)
`
	if err := os.WriteFile(filepath.Join(hypDir, "H-001.md"), []byte(card), 0o644); err != nil {
		t.Fatalf("card: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hypDir, "H-001.md"), []byte(card), 0o644); err != nil {
		t.Fatalf("card: %v", err)
	}
	return root, dir
}

func TestUpdateHypothesis_RoundTrip(t *testing.T) {
	root, dir := setupProjectDir(t)

	status := "in-progress"
	title := "Refined bundle parsing"
	result := "Recovered 97% of modules."
	decision := "continue"
	timebox := "8 days"
	err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{
		Status:   &status,
		Title:    &title,
		Result:   &result,
		Decision: &decision,
		Timebox:  &timebox,
	})
	if err != nil {
		t.Fatalf("UpdateHypothesis: %v", err)
	}

	proj, err := ParseProject(dir)
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	n := proj.Graph.Node("H-001")
	if n == nil {
		t.Fatal("H-001 missing after update")
	}
	if n.Status != StatusInProgress {
		t.Errorf("status = %q, want in-progress", n.Status)
	}
	if n.Title != title {
		t.Errorf("title = %q, want %q", n.Title, title)
	}
	if n.Result != result {
		t.Errorf("result = %q, want %q", n.Result, result)
	}
	if n.Timebox != timebox {
		t.Errorf("timebox = %q, want %q", n.Timebox, timebox)
	}

	// Graph.md catalog + Mermaid must also reflect the change.
	graphRaw, _ := os.ReadFile(filepath.Join(dir, "hypotheses", "graph.md"))
	graphStr := string(graphRaw)
	if !strings.Contains(graphStr, "in_progress") || !strings.Contains(graphStr, "Refined bundle parsing") {
		t.Errorf("graph.md not updated:\n%s", graphStr)
	}
	cardRaw, _ := os.ReadFile(filepath.Join(dir, "hypotheses", "H-001.md"))
	if !strings.Contains(string(cardRaw), "| **Decision** | continue |") {
		t.Errorf("card decision not updated:\n%s", string(cardRaw))
	}
}

func TestUpdateHypothesis_InvalidTransitionLeavesFilesUnchanged(t *testing.T) {
	root, dir := setupProjectDir(t)

	// First move open → confirmed directly (illegal: must go through in-progress).
	cardBefore, _ := os.ReadFile(filepath.Join(dir, "hypotheses", "H-001.md"))
	graphBefore, _ := os.ReadFile(filepath.Join(dir, "hypotheses", "graph.md"))

	bad := "confirmed"
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{Status: &bad}); err == nil {
		t.Fatal("expected error for open→confirmed, got nil")
	}

	cardAfter, _ := os.ReadFile(filepath.Join(dir, "hypotheses", "H-001.md"))
	graphAfter, _ := os.ReadFile(filepath.Join(dir, "hypotheses", "graph.md"))
	if !bytes.Equal(cardBefore, cardAfter) {
		t.Error("card changed despite failed transition")
	}
	if !bytes.Equal(graphBefore, graphAfter) {
		t.Error("graph changed despite failed transition")
	}
}

func TestUpdateHypothesis_MissingID(t *testing.T) {
	root, dir := setupProjectDir(t)
	if err := UpdateHypothesis(root, dir, "H-999", HypothesisUpdate{}); err == nil {
		t.Fatal("expected error for missing hypothesis id")
	}
	if err := UpdateHypothesis(root, dir, "bogus", HypothesisUpdate{}); err == nil {
		t.Fatal("expected error for invalid hypothesis id")
	}
}

// writeCardOnly writes a hypothesis card without touching graph.md — a
// card-only hypothesis the reconciler still picks up (cards ∪ catalog ∪
// Mermaid), used to exercise parent updates that must ADD a Mermaid node
// definition.
func writeCardOnly(t *testing.T, dir, id, title string) {
	t.Helper()
	card := "# " + id + ": " + title + "\n\n" +
		"| Field | Value |\n|---|---|\n" +
		"| **Identifier** | " + id + " |\n" +
		"| **Status** | open |\n" +
		"| **Timebox** | — |\n" +
		"| **Parent(s)** | — |\n" +
		"| **Created** | 2025-04-02 |\n" +
		"| **Completed** | — |\n" +
		"| **Decision** | — |\n\n" +
		"## Statement\n\ns\n\n## Verification Criterion\n\nc\n\n" +
		"## Experiment Notes\n\n*Not yet started.*\n\n## Result\n\n**Finding:** —\n"
	if err := os.WriteFile(filepath.Join(dir, "hypotheses", id+".md"), []byte(card), 0o644); err != nil {
		t.Fatalf("card %s: %v", id, err)
	}
}

func TestUpdateHypothesis_LongFormSectionsRoundTrip(t *testing.T) {
	root, dir := setupProjectDir(t)

	statement := "Multi-line statement.\n\nSecond paragraph."
	criterion := "Recover >= 95% of modules\non the fixture corpus."
	notes := "Run 1: crashed.\nRun 2: passed."
	decision := "pivot"
	err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{
		Statement:             &statement,
		VerificationCriterion: &criterion,
		ExperimentNotes:       &notes,
		Decision:              &decision,
	})
	if err != nil {
		t.Fatalf("UpdateHypothesis: %v", err)
	}

	proj, err := ParseProject(dir)
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	n := proj.Graph.Node("H-001")
	if n == nil {
		t.Fatal("H-001 missing after update")
	}
	if n.Statement != statement {
		t.Errorf("Statement = %q, want %q", n.Statement, statement)
	}
	if n.VerificationCriterion != criterion {
		t.Errorf("VerificationCriterion = %q, want %q", n.VerificationCriterion, criterion)
	}
	if n.ExperimentNotes != notes {
		t.Errorf("ExperimentNotes = %q, want %q", n.ExperimentNotes, notes)
	}
	if n.Decision != decision {
		t.Errorf("Decision = %q, want %q", n.Decision, decision)
	}

	// Clearing a section round-trips to empty (placeholder semantics).
	empty := ""
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{ExperimentNotes: &empty}); err != nil {
		t.Fatalf("UpdateHypothesis clear: %v", err)
	}
	proj, _ = ParseProject(dir)
	if n := proj.Graph.Node("H-001"); n == nil || n.ExperimentNotes != "" {
		t.Errorf("cleared ExperimentNotes = %q, want empty", proj.Graph.Node("H-001").ExperimentNotes)
	}
}

func TestUpdateHypothesis_ParentsRoundTrip(t *testing.T) {
	root, dir := setupProjectDir(t)

	// Create H-002 with parent H-001 (card + Mermaid node/edge + catalog row).
	if _, err := CreateHypothesis(root, dir, NewHypothesis{Title: "Endpoint extraction", Parents: []string{"H-001"}}); err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}

	graphPath := filepath.Join(dir, "hypotheses", "graph.md")
	cardPath := filepath.Join(dir, "hypotheses", "H-002.md")

	// Clear H-002's parents: the Mermaid edge, the card row, and the catalog
	// column must all drop back to the root state.
	none := []string{}
	if err := UpdateHypothesis(root, dir, "H-002", HypothesisUpdate{Parents: &none}); err != nil {
		t.Fatalf("UpdateHypothesis clear parents: %v", err)
	}
	graphStr := string(mustRead(t, graphPath))
	cardStr := string(mustRead(t, cardPath))
	if strings.Contains(graphStr, "H001 --> H002") {
		t.Errorf("mermaid edge not removed:\n%s", graphStr)
	}
	if !strings.Contains(graphStr, "| — |") {
		t.Errorf("catalog Parent(s) column not cleared:\n%s", graphStr)
	}
	if !strings.Contains(cardStr, "| **Parent(s)** | — |") {
		t.Errorf("card Parent(s) row not cleared:\n%s", cardStr)
	}
	proj, _ := ParseProject(dir)
	if n := proj.Graph.Node("H-002"); n == nil || len(n.Parents) != 0 {
		t.Errorf("parsed parents after clear = %+v, want none", proj.Graph.Node("H-002").Parents)
	}

	// Set the parent back: edge re-added, rows updated.
	h1 := []string{"H-001"}
	if err := UpdateHypothesis(root, dir, "H-002", HypothesisUpdate{Parents: &h1}); err != nil {
		t.Fatalf("UpdateHypothesis set parents: %v", err)
	}
	graphStr = string(mustRead(t, graphPath))
	cardStr = string(mustRead(t, cardPath))
	if !strings.Contains(graphStr, "H001 --> H002") {
		t.Errorf("mermaid edge not re-added:\n%s", graphStr)
	}
	if !strings.Contains(graphStr, "| [H-002](H-002.md) | Endpoint extraction | open | — | H-001 |") {
		t.Errorf("catalog row parents not updated:\n%s", graphStr)
	}
	if !strings.Contains(cardStr, "| **Parent(s)** | H-001 |") {
		t.Errorf("card Parent(s) row not updated:\n%s", cardStr)
	}
	proj, _ = ParseProject(dir)
	if n := proj.Graph.Node("H-002"); n == nil {
		t.Fatal("H-002 missing")
	} else if !reflect.DeepEqual(n.Parents, []string{"H-001"}) {
		t.Errorf("parsed parents = %+v, want [H-001]", n.Parents)
	}
}

func TestUpdateHypothesis_ParentsCardOnlyGetsMermaidNode(t *testing.T) {
	root, dir := setupProjectDir(t)

	// H-002 exists only as a card (absent from Mermaid and the catalog);
	// making it a parent of H-001 must ADD its Mermaid node definition so
	// the new edge never references an undefined token.
	writeCardOnly(t, dir, "H-002", "Card-only sibling")

	parents := []string{"H-002"}
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{Parents: &parents}); err != nil {
		t.Fatalf("UpdateHypothesis: %v", err)
	}

	graphStr := string(mustRead(t, filepath.Join(dir, "hypotheses", "graph.md")))
	if !strings.Contains(graphStr, `H002["H-002: Card-only sibling"]:::open`) {
		t.Errorf("card-only parent's mermaid node not added:\n%s", graphStr)
	}
	if !strings.Contains(graphStr, "H002 --> H001") {
		t.Errorf("mermaid edge not added:\n%s", graphStr)
	}
	proj, _ := ParseProject(dir)
	if n := proj.Graph.Node("H-001"); n == nil {
		t.Fatal("H-001 missing")
	} else if !reflect.DeepEqual(n.Parents, []string{"H-002"}) {
		t.Errorf("parsed parents = %+v, want [H-002]", n.Parents)
	}
}

func TestUpdateHypothesis_ParentsValidation(t *testing.T) {
	root, dir := setupProjectDir(t)
	if _, err := CreateHypothesis(root, dir, NewHypothesis{Title: "Endpoint extraction", Parents: []string{"H-001"}}); err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}

	cardPath := filepath.Join(dir, "hypotheses", "H-001.md")
	graphPath := filepath.Join(dir, "hypotheses", "graph.md")
	cardBefore := mustRead(t, cardPath)
	graphBefore := mustRead(t, graphPath)

	// Self-parent.
	self := []string{"H-001"}
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{Parents: &self}); err == nil {
		t.Fatal("expected error for self-parent")
	}
	// Unknown parent.
	unknown := []string{"H-999"}
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{Parents: &unknown}); err == nil {
		t.Fatal("expected error for unknown parent")
	}
	// Cycle: H-001 → H-002 exists; making H-002 a parent of H-001 closes a loop.
	cycle := []string{"H-002"}
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{Parents: &cycle}); err == nil {
		t.Fatal("expected error for cycle-creating parent")
	}

	// A rejected parent set leaves both files byte-for-byte unchanged.
	if !bytes.Equal(cardBefore, mustRead(t, cardPath)) {
		t.Error("card changed despite rejected parents")
	}
	if !bytes.Equal(graphBefore, mustRead(t, graphPath)) {
		t.Error("graph changed despite rejected parents")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func TestCreateHypothesis_RoundTrip(t *testing.T) {
	root, dir := setupProjectDir(t)

	id, err := CreateHypothesis(root, dir, NewHypothesis{
		Title:                 "Runtime interception",
		Statement:             "Intercepting fetch captures exfil.",
		VerificationCriterion: "Captures all requests.",
		Timebox:               "6 days",
		Parents:               []string{"H-001"},
	})
	if err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}
	if id != "H-002" {
		t.Fatalf("new id = %q, want H-002", id)
	}

	proj, err := ParseProject(dir)
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	n := proj.Graph.Node("H-002")
	if n == nil {
		t.Fatal("H-002 missing after create")
	}
	if n.Title != "Runtime interception" {
		t.Errorf("title = %q", n.Title)
	}
	if n.Status != StatusOpen {
		t.Errorf("status = %q, want open", n.Status)
	}
	if len(n.Parents) != 1 || n.Parents[0] != "H-001" {
		t.Errorf("parents = %v, want [H-001]", n.Parents)
	}

	// Edge from parent must be present.
	found := false
	for _, e := range proj.Graph.Edges {
		if e.From == "H-001" && e.To == "H-002" {
			found = true
		}
	}
	if !found {
		t.Errorf("edge H-001→H-002 missing; edges=%v", proj.Graph.Edges)
	}
}

func TestCreateHypothesis_UnknownParentRejected(t *testing.T) {
	root, dir := setupProjectDir(t)
	if _, err := CreateHypothesis(root, dir, NewHypothesis{
		Title:   "Bad parent",
		Parents: []string{"H-999"},
	}); err == nil {
		t.Fatal("expected error for unknown parent")
	}
}

func TestCreateHypothesis_MissingGraphGeneratesSkeleton(t *testing.T) {
	// A project whose hypotheses/graph.md is missing must still produce a
	// well-formed graph (skeleton) rather than an empty file: the new node and
	// catalog row must be present, so the hypothesis shows up in the DAG and
	// catalog.
	dir := filepath.Join(t.TempDir(), "R-001-test")
	hypDir := filepath.Join(dir, "hypotheses")
	if err := os.MkdirAll(hypDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brief.md"), []byte("# [R-001] Test\n"), 0o644); err != nil {
		t.Fatalf("brief: %v", err)
	}

	id, err := CreateHypothesis(filepath.Dir(dir), dir, NewHypothesis{Title: "First"})
	if err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}
	if id != "H-001" {
		t.Fatalf("new id = %q, want H-001", id)
	}

	raw, err := os.ReadFile(filepath.Join(hypDir, "graph.md"))
	if err != nil {
		t.Fatalf("graph.md not created: %v", err)
	}
	graphStr := string(raw)
	if !strings.Contains(graphStr, "```mermaid") {
		t.Errorf("graph.md missing mermaid fence:\n%s", graphStr)
	}
	if !strings.Contains(graphStr, "Hypothesis Catalog") {
		t.Errorf("graph.md missing catalog:\n%s", graphStr)
	}
	if !strings.Contains(graphStr, `H001["H-001: First"]:::open`) {
		t.Errorf("graph.md missing new node:\n%s", graphStr)
	}
	if !strings.Contains(graphStr, "[H-001](H-001.md)") {
		t.Errorf("graph.md missing catalog row:\n%s", graphStr)
	}
	if strings.Contains(graphStr, "No hypotheses yet") {
		t.Errorf("graph.md still has placeholder row:\n%s", graphStr)
	}
}

func TestCreateHypothesis_EmptyTitleRejected(t *testing.T) {
	root, dir := setupProjectDir(t)
	if _, err := CreateHypothesis(root, dir, NewHypothesis{}); err == nil {
		t.Fatal("expected error for empty title")
	}
}
