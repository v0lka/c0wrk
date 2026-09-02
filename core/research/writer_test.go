package research

import (
	"os"
	"path/filepath"
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
func setupProjectDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "R-001-test")
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
	return dir
}

func TestUpdateHypothesis_RoundTrip(t *testing.T) {
	dir := setupProjectDir(t)

	status := "in-progress"
	title := "Refined bundle parsing"
	result := "Recovered 97% of modules."
	decision := "continue"
	timebox := "8 days"
	err := UpdateHypothesis(dir, "H-001", HypothesisUpdate{
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
	dir := setupProjectDir(t)

	// First move open → confirmed directly (illegal: must go through in-progress).
	cardBefore, _ := os.ReadFile(filepath.Join(dir, "hypotheses", "H-001.md"))
	graphBefore, _ := os.ReadFile(filepath.Join(dir, "hypotheses", "graph.md"))

	bad := "confirmed"
	if err := UpdateHypothesis(dir, "H-001", HypothesisUpdate{Status: &bad}); err == nil {
		t.Fatal("expected error for open→confirmed, got nil")
	}

	cardAfter, _ := os.ReadFile(filepath.Join(dir, "hypotheses", "H-001.md"))
	graphAfter, _ := os.ReadFile(filepath.Join(dir, "hypotheses", "graph.md"))
	if string(cardBefore) != string(cardAfter) {
		t.Error("card changed despite failed transition")
	}
	if string(graphBefore) != string(graphAfter) {
		t.Error("graph changed despite failed transition")
	}
}

func TestUpdateHypothesis_MissingID(t *testing.T) {
	dir := setupProjectDir(t)
	if err := UpdateHypothesis(dir, "H-999", HypothesisUpdate{}); err == nil {
		t.Fatal("expected error for missing hypothesis id")
	}
	if err := UpdateHypothesis(dir, "bogus", HypothesisUpdate{}); err == nil {
		t.Fatal("expected error for invalid hypothesis id")
	}
}

func TestCreateHypothesis_RoundTrip(t *testing.T) {
	dir := setupProjectDir(t)

	id, err := CreateHypothesis(dir, NewHypothesis{
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
	dir := setupProjectDir(t)
	if _, err := CreateHypothesis(dir, NewHypothesis{
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

	id, err := CreateHypothesis(dir, NewHypothesis{Title: "First"})
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
	dir := setupProjectDir(t)
	if _, err := CreateHypothesis(dir, NewHypothesis{}); err == nil {
		t.Fatal("expected error for empty title")
	}
}
