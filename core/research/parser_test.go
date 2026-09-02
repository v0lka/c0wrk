package research

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ──────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────

// floatEq reports whether two floats are equal within a small tolerance, for
// comparing computed rates (e.g. 2/3) without floating-point noise.
func floatEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestNormalizeID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"H001", "H-001"},              // Mermaid node-ID spelling
		{"H-001", "H-001"},             // card/catalog spelling
		{"h-001", "H-001"},             // case-insensitive
		{"[H-002](H-002.md)", "H-002"}, // markdown-link cell
		{"H-12", "H-12"},
		{"", ""},
		{"no id here", ""},
	}
	for _, tc := range cases {
		if got := NormalizeID(tc.in); got != tc.want {
			t.Errorf("NormalizeID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeStatus_LowercasesAndMapsInProgress(t *testing.T) {
	cases := []struct {
		in   string
		want HypothesisStatus
	}{
		{"open", StatusOpen},
		{"in-progress", StatusInProgress},
		{"in_progress", StatusInProgress}, // Mermaid CSS spelling
		{"Confirmed", StatusConfirmed},    // mixed case
		{"REFUTED", StatusRefuted},
		{"cancelled", StatusCancelled},
		{"bogus", "bogus"}, // unknown preserved untyped
		{"", ""},
	}
	for _, tc := range cases {
		got := NormalizeStatus(tc.in)
		if got != tc.want {
			t.Errorf("NormalizeStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHypothesisStatus_Predicates(t *testing.T) {
	terminal := []HypothesisStatus{StatusConfirmed, StatusRefuted, StatusCancelled}
	active := []HypothesisStatus{StatusOpen, StatusInProgress}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("%q should be terminal", s)
		}
		if s.IsActive() {
			t.Errorf("%q should not be active", s)
		}
	}
	for _, s := range active {
		if s.IsTerminal() {
			t.Errorf("%q should not be terminal", s)
		}
		if !s.IsActive() {
			t.Errorf("%q should be active", s)
		}
	}
	// Unknown statuses are non-terminal (parser does not assume finished).
	if HypothesisStatus("bogus").IsTerminal() {
		t.Error("unknown status should be non-terminal")
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Mermaid graph parsing (pure)
// ──────────────────────────────────────────────────────────────────────────

func TestParseMermaidGraph_NodesEdgesAndCommentStripping(t *testing.T) {
	content := "# Hypothesis Graph\n\n## Diagram\n\n```mermaid\n" +
		"graph TD\n" +
		"classDef confirmed fill:#4CAF50,color:#fff\n" +
		"    %% H099[\"H-099: commented example\"]:::confirmed\n" +
		"    %% H099 --> H100\n" +
		"    H001[\"H-001: Static bundle parsing\"]:::open\n" +
		"    H002[\"H-002: Endpoint extraction\"]:::confirmed\n" +
		"    H001 --> H002\n" +
		"    H001 --> H003[\"H-003: Runtime interception\"]:::in_progress\n" +
		"```\n"

	nodes, edges := ParseMermaidGraph(content)

	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes (commented H099/H100 ignored), got %d: %+v", len(nodes), nodes)
	}
	wantStatus := map[string]HypothesisStatus{
		"H-001": StatusOpen,
		"H-002": StatusConfirmed,
		"H-003": StatusInProgress,
	}
	wantTitle := map[string]string{
		"H-001": "Static bundle parsing",
		"H-002": "Endpoint extraction",
		"H-003": "Runtime interception",
	}
	for _, n := range nodes {
		if n.status != wantStatus[n.id] {
			t.Errorf("node %s status = %q, want %q", n.id, n.status, wantStatus[n.id])
		}
		if n.title != wantTitle[n.id] {
			t.Errorf("node %s title = %q, want %q", n.id, n.title, wantTitle[n.id])
		}
	}

	wantEdges := []HypothesisEdge{
		{From: "H-001", To: "H-002"},
		{From: "H-001", To: "H-003"},
	}
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("edges = %+v, want %+v", edges, wantEdges)
	}
}

func TestParseMermaidGraph_EmptyAndMissing(t *testing.T) {
	// No mermaid block at all.
	if nodes, edges := ParseMermaidGraph("# just a heading\n"); nodes != nil || edges != nil {
		t.Errorf("missing block should yield nil/nil, got %+v / %+v", nodes, edges)
	}
	// Empty graph template (only classDefs + comments) yields nothing.
	empty := "## Diagram\n\n```mermaid\ngraph TD\nclassDef open fill:#2196F3\n" +
		"    %% H001[\"H-001: x\"]:::open\n```\n"
	if nodes, edges := ParseMermaidGraph(empty); len(nodes) != 0 || len(edges) != 0 {
		t.Errorf("empty template should yield 0 nodes/edges, got %d / %d", len(nodes), len(edges))
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Catalog table parsing (pure)
// ──────────────────────────────────────────────────────────────────────────

func TestParseCatalog_SkipsHeaderSeparatorPlaceholder(t *testing.T) {
	content := `## Hypothesis Catalog

| ID | Hypothesis | Status | Decision | Parent(s) |
|---|---|---|---|---|
| *No hypotheses yet. Use the skill to create the first one.* | | | | |
| [H-001](H-001.md) | Static parsing | confirmed | continue | — |
| H-002 | Endpoint extraction | in-progress | — | H-001, H-003 |
`
	rows := ParseCatalog(content)
	if len(rows) != 2 {
		t.Fatalf("expected 2 data rows (header/sep/placeholder skipped), got %d", len(rows))
	}
	if rows[0].id != "H-001" || rows[0].status != StatusConfirmed || len(rows[0].parents) != 0 {
		t.Errorf("row0 = %+v", rows[0])
	}
	if rows[1].id != "H-002" || rows[1].status != StatusInProgress {
		t.Errorf("row1 id/status = %+v", rows[1])
	}
	wantParents := []string{"H-001", "H-003"}
	if !reflect.DeepEqual(rows[1].parents, wantParents) {
		t.Errorf("row1 parents = %+v, want %+v", rows[1].parents, wantParents)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Card parsing (pure)
// ──────────────────────────────────────────────────────────────────────────

func TestParseCard_FullCard(t *testing.T) {
	content := `# H-001: Static bundle parsing

| Field | Value |
|---|---|
| **Identifier** | H-001 |
| **Status** | confirmed |
| **Timebox** | 5 days |
| **Parent(s)** | H-009, H-010 |
| **Created** | 2025-04-02 |

## Statement

Bundles can be parsed.

## Result

**Finding:** Recovered 97% of modules.
`
	node, err := ParseCard(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node.ID != "H-001" {
		t.Errorf("ID = %q, want H-001", node.ID)
	}
	if node.Title != "Static bundle parsing" {
		t.Errorf("Title = %q", node.Title)
	}
	if node.Status != StatusConfirmed {
		t.Errorf("Status = %q", node.Status)
	}
	if node.Timebox != "5 days" {
		t.Errorf("Timebox = %q", node.Timebox)
	}
	wantParents := []string{"H-009", "H-010"}
	if !reflect.DeepEqual(node.Parents, wantParents) {
		t.Errorf("Parents = %+v, want %+v", node.Parents, wantParents)
	}
	if node.Result != "Recovered 97% of modules." {
		t.Errorf("Result = %q", node.Result)
	}
}

func TestParseCard_PartialCardAndDashPlaceholders(t *testing.T) {
	// Partial: no Status, no Timebox, no Result; dash placeholders everywhere.
	content := `# H-004: Service-worker hooking

| Field | Value |
|---|---|
| **Identifier** | H-004 |
| **Status** | open |
| **Created** | 2025-04-20 |

## Result

*Filled upon completion.*

**Finding:** —
`
	node, err := ParseCard(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node.Status != StatusOpen {
		t.Errorf("Status = %q, want open", node.Status)
	}
	if node.Timebox != "" {
		t.Errorf("Timebox should be empty, got %q", node.Timebox)
	}
	if len(node.Parents) != 0 {
		t.Errorf("Parents should be empty, got %+v", node.Parents)
	}
	if node.Result != "" {
		t.Errorf("Result should be empty for dash placeholder, got %q", node.Result)
	}
}

func TestParseCard_FreeFormResultWithoutFindingMarker(t *testing.T) {
	content := `# H-010: Free form

| Field | Value |
|---|---|
| **Identifier** | H-010 |
| **Status** | confirmed |

## Result

The experiment showed a clear signal on the second run.
*This is an italic placeholder line that must be dropped.*

More detail here.
`
	node, err := ParseCard(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "The experiment showed a clear signal on the second run.\nMore detail here."
	if node.Result != want {
		t.Errorf("Result = %q, want %q", node.Result, want)
	}
}

func TestParseCard_NoIdentifierReturnsError(t *testing.T) {
	content := "# Not a hypothesis\n\nNo identifier anywhere.\n"
	if _, err := ParseCard(content); err == nil {
		t.Fatal("expected error for card without identifier, got nil")
	}
}

func TestParseCard_IdentifierFieldFallback(t *testing.T) {
	// Non-standard heading but an Identifier field is present.
	content := `# Some other heading

| Field | Value |
|---|---|
| **Identifier** | H-042 |
| **Status** | open |
`
	node, err := ParseCard(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if node.ID != "H-042" {
		t.Errorf("ID = %q, want H-042", node.ID)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Brief parsing (pure)
// ──────────────────────────────────────────────────────────────────────────

func TestParseBrief(t *testing.T) {
	content := `# [R-001] Web Exfil Detection

| Field | Value |
|---|---|
| **Identifier** | R-001 |
| **Status** | Active |
| **Problem domain** | Web application security |
| **Quarter** | 2025-Q2 |
| **Researcher(s)** | A. Researcher |
| **Related researches** | R-000 |

## Research Question

Can we detect exfil client-side?

## Success Criteria

Coverage >= 90%.
`
	b := ParseBrief(content)
	if b.ID != "R-001" {
		t.Errorf("ID = %q", b.ID)
	}
	if b.Title != "Web Exfil Detection" {
		t.Errorf("Title = %q", b.Title)
	}
	if b.Status != "Active" || b.ProblemDomain != "Web application security" ||
		b.Quarter != "2025-Q2" || b.Researchers != "A. Researcher" ||
		b.RelatedResearches != "R-000" {
		t.Errorf("metadata fields wrong: %+v", b)
	}
	if b.ResearchQuestion != "Can we detect exfil client-side?" {
		t.Errorf("ResearchQuestion = %q", b.ResearchQuestion)
	}
	if b.SuccessCriteria != "Coverage >= 90%." {
		t.Errorf("SuccessCriteria = %q", b.SuccessCriteria)
	}
}

// TestParseBrief_DescriptiveH1 covers a brief whose H1 is NOT the canonical
// "# [R-NNN] Title" form but a descriptive "# Research Brief: <Title>", with
// the ID carried only in the Identifier table field. The parser must recover
// both the ID (from the field) and a clean title (prefix-stripped H1).
func TestParseBrief_DescriptiveH1(t *testing.T) {
	content := `# Research Brief: Aurora — Sample Integration Service

| Field | Value |
|---|---|
| **Identifier** | R-002 |
| **Status** | Active |
`
	b := ParseBrief(content)
	if b.ID != "R-002" {
		t.Errorf("ID = %q, want R-002 (from Identifier field)", b.ID)
	}
	if b.Title != "Aurora — Sample Integration Service" {
		t.Errorf("Title = %q, want prefix-stripped H1", b.Title)
	}
	if b.Status != "Active" {
		t.Errorf("Status = %q", b.Status)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Index parsing (pure)
// ──────────────────────────────────────────────────────────────────────────

func TestParseIndex_LinksAndFallback(t *testing.T) {
	t.Run("links", func(t *testing.T) {
		content := "- [R-001: Web Exfil](R-001-web/brief.md)\n- [R-002: Other](R-002-other/brief.md)\n"
		entries := ParseIndex(content)
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(entries))
		}
		if entries[0].ID != "R-001" || entries[0].Path != "R-001-web/brief.md" {
			t.Errorf("entry0 = %+v", entries[0])
		}
		if entries[1].ID != "R-002" {
			t.Errorf("entry1 = %+v", entries[1])
		}
	})
	t.Run("fallback bare tokens", func(t *testing.T) {
		entries := ParseIndex("Some prose mentioning R-007 and R-003.\n")
		if len(entries) != 2 {
			t.Fatalf("expected 2 fallback entries, got %d", len(entries))
		}
		// Order of appearance preserved.
		if entries[0].ID != "R-007" || entries[1].ID != "R-003" {
			t.Errorf("entries = %+v", entries)
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────
// Prior-art count (pure)
// ──────────────────────────────────────────────────────────────────────────

func TestParsePriorArtCount(t *testing.T) {
	t.Run("numbered entries", func(t *testing.T) {
		content := `## Entries

| # | Source | Type | Annotation | Relevance |
|---|---|---|---|---|
| 1 | Paper A | paper | x | High |
| 2 | Tool B | tool | y | Medium |
| 3 | CVE C | cve | z | High |
`
		if got := ParsePriorArtCount(content); got != 3 {
			t.Errorf("got %d, want 3", got)
		}
	})
	t.Run("placeholder only", func(t *testing.T) {
		content := "| # | Source |\n|---|---|\n| *No entries yet.* | |\n"
		if got := ParsePriorArtCount(content); got != 0 {
			t.Errorf("got %d, want 0", got)
		}
	})
	t.Run("grouped-prose headings fallback", func(t *testing.T) {
		// No numbered table; entries are "### N.M Name" subsections.
		content := "# Prior Art\n\n" +
			"## 1. Proxy Infrastructure\n\n" +
			"### 1.1 goproxy\n\n- Go library\n\n" +
			"### 1.2 ReverseProxy\n\n- stdlib\n\n" +
			"### 1.3 mitmproxy\n\n- tool\n\n" +
			"## 2. AI Safety\n\n" +
			"### 2.1 NeMo Guardrails\n\n- framework\n"
		if got := ParsePriorArtCount(content); got != 4 {
			t.Errorf("got %d, want 4 subsection entries", got)
		}
	})
	t.Run("table wins over stray headings", func(t *testing.T) {
		// A canonical numbered table is present; a stray "### 1.1" must NOT be
		// added on top of the table count.
		content := "| # | Source |\n|---|---|\n| 1 | Paper A |\n\n### 1.1 stray\n"
		if got := ParsePriorArtCount(content); got != 1 {
			t.Errorf("got %d, want 1 (table only)", got)
		}
	})
}

// ──────────────────────────────────────────────────────────────────────────
// BuildGraph (pure) — merge priority + parent reconciliation
// ──────────────────────────────────────────────────────────────────────────

func TestBuildGraph_CardOverridesCatalogAndMermaid(t *testing.T) {
	mermaid := []mermaidNode{
		{id: "H-001", title: "mermaid-title", status: StatusOpen}, // lowest priority
	}
	catalog := []catalogRow{
		{id: "H-001", title: "catalog-title", status: StatusRefuted}, // middle
	}
	cards := []HypothesisNode{
		{ID: "H-001", Title: "card-title", Status: StatusConfirmed, Timebox: "3 days", Result: "won"}, // highest
	}
	g := BuildGraph(mermaid, nil, catalog, cards)
	n := g.Node("H-001")
	if n == nil {
		t.Fatal("expected node H-001")
	}
	if n.Title != "card-title" {
		t.Errorf("Title = %q, want card-title", n.Title)
	}
	if n.Status != StatusConfirmed {
		t.Errorf("Status = %q, want confirmed", n.Status)
	}
	if n.Timebox != "3 days" || n.Result != "won" {
		t.Errorf("Timebox/Result = %q / %q", n.Timebox, n.Result)
	}
}

func TestBuildGraph_ParentReconciledFromEdge(t *testing.T) {
	// Card omits parents; catalog omits parents; only the Mermaid edge exists.
	mermaid := []mermaidNode{{id: "H-001"}, {id: "H-002"}}
	edges := []HypothesisEdge{{From: "H-001", To: "H-002"}}
	cards := []HypothesisNode{{ID: "H-001"}, {ID: "H-002"}} // no parents declared
	g := BuildGraph(mermaid, edges, nil, cards)

	child := g.Node("H-002")
	if child == nil {
		t.Fatal("expected node H-002")
	}
	wantParents := []string{"H-001"}
	if !reflect.DeepEqual(child.Parents, wantParents) {
		t.Errorf("Parents = %+v, want %+v (derived from edge)", child.Parents, wantParents)
	}
	// Edge should be present exactly once.
	if len(g.Edges) != 1 || g.Edges[0].From != "H-001" || g.Edges[0].To != "H-002" {
		t.Errorf("Edges = %+v", g.Edges)
	}
}

func TestBuildGraph_DeterministicOrdering(t *testing.T) {
	mermaid := []mermaidNode{{id: "H-003"}, {id: "H-001"}, {id: "H-002"}}
	edges := []HypothesisEdge{
		{From: "H-003", To: "H-001"},
		{From: "H-001", To: "H-002"},
	}
	g := BuildGraph(mermaid, edges, nil, nil)
	for i, want := range []string{"H-001", "H-002", "H-003"} {
		if g.Nodes[i].ID != want {
			t.Errorf("Nodes[%d].ID = %q, want %q", i, g.Nodes[i].ID, want)
		}
	}
	// Edges sorted by (From, To): H-001->H-002 before H-003->H-001.
	if g.Edges[0].From != "H-001" || g.Edges[1].From != "H-003" {
		t.Errorf("Edges not sorted: %+v", g.Edges)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// ComputeMetrics (pure)
// ──────────────────────────────────────────────────────────────────────────

func TestComputeMetrics(t *testing.T) {
	// Hand-built graph mirroring the R-001 testdata shape:
	//   H-001 (confirmed, root)
	//     -> H-002 (confirmed), H-003 (in-progress), H-006 (refuted), H-007 (cancelled)
	//        H-003 -> H-004 (open), H-005 (open)
	g := &HypothesisGraph{
		Nodes: []*HypothesisNode{
			{ID: "H-001", Status: StatusConfirmed},
			{ID: "H-002", Status: StatusConfirmed, Parents: []string{"H-001"}},
			{ID: "H-003", Status: StatusInProgress, Parents: []string{"H-001"}},
			{ID: "H-004", Status: StatusOpen, Parents: []string{"H-003"}},
			{ID: "H-005", Status: StatusOpen, Parents: []string{"H-003"}},
			{ID: "H-006", Status: StatusRefuted, Parents: []string{"H-001"}},
			{ID: "H-007", Status: StatusCancelled, Parents: []string{"H-001"}},
		},
		Edges: []HypothesisEdge{
			{From: "H-001", To: "H-002"},
			{From: "H-001", To: "H-003"},
			{From: "H-001", To: "H-006"},
			{From: "H-001", To: "H-007"},
			{From: "H-003", To: "H-004"},
			{From: "H-003", To: "H-005"},
		},
	}

	m := ComputeMetrics(g)
	if m.Total != 7 {
		t.Errorf("Total = %d, want 7", m.Total)
	}
	wantByStatus := map[HypothesisStatus]int{
		StatusOpen: 2, StatusInProgress: 1, StatusConfirmed: 2,
		StatusRefuted: 1, StatusCancelled: 1,
	}
	if !reflect.DeepEqual(m.ByStatus, wantByStatus) {
		t.Errorf("ByStatus = %+v, want %+v", m.ByStatus, wantByStatus)
	}
	if !floatEq(m.ConfirmationRate, 2.0/3.0) {
		t.Errorf("ConfirmationRate = %v, want %v", m.ConfirmationRate, 2.0/3.0)
	}
	if m.Depth != 2 {
		t.Errorf("Depth = %d, want 2", m.Depth)
	}
	if m.Breadth != 2 {
		t.Errorf("Breadth = %d, want 2 (two open nodes at level 2)", m.Breadth)
	}
	wantFront := []string{"H-003", "H-004", "H-005"}
	if !reflect.DeepEqual(m.ActiveFront, wantFront) {
		t.Errorf("ActiveFront = %+v, want %+v", m.ActiveFront, wantFront)
	}
}

func TestComputeMetrics_EmptyAndNoVerdicts(t *testing.T) {
	t.Run("nil graph", func(t *testing.T) {
		m := ComputeMetrics(nil)
		if m.Total != 0 || m.Depth != 0 || m.Breadth != 0 || m.ConfirmationRate != 0 {
			t.Errorf("nil graph metrics should be zero, got %+v", m)
		}
		if m.ByStatus == nil {
			t.Error("ByStatus map should be non-nil even when empty")
		}
	})
	t.Run("only open nodes (no verdicts)", func(t *testing.T) {
		g := &HypothesisGraph{Nodes: []*HypothesisNode{
			{ID: "H-001", Status: StatusOpen},
			{ID: "H-002", Status: StatusOpen, Parents: []string{"H-001"}},
		}, Edges: []HypothesisEdge{{From: "H-001", To: "H-002"}}}
		m := ComputeMetrics(g)
		if m.ConfirmationRate != 0 {
			t.Errorf("ConfirmationRate = %v, want 0 (no confirmed/refuted)", m.ConfirmationRate)
		}
		if m.Depth != 1 {
			t.Errorf("Depth = %d, want 1", m.Depth)
		}
		if m.Breadth != 1 {
			t.Errorf("Breadth = %d, want 1 (two open nodes but on different levels)", m.Breadth)
		}
	})
}

func TestComputeMetrics_CycleGuard(t *testing.T) {
	// A malformed graph with a cycle must not hang the metric computation.
	// H-001 -> H-002 -> H-001 (cycle). The cycle guard collapses the back-edge
	// to level 0, so ComputeMetrics always terminates (a hang would surface as
	// a test timeout).
	g := &HypothesisGraph{
		Nodes: []*HypothesisNode{
			{ID: "H-001", Status: StatusOpen, Parents: []string{"H-002"}},
			{ID: "H-002", Status: StatusOpen, Parents: []string{"H-001"}},
		},
		Edges: []HypothesisEdge{
			{From: "H-002", To: "H-001"},
			{From: "H-001", To: "H-002"},
		},
	}
	m := ComputeMetrics(g)
	if m.Total != 2 {
		t.Errorf("Total = %d, want 2", m.Total)
	}
	if m.Breadth != 1 {
		t.Errorf("Breadth = %d, want 1 (cycle guard splits the two nodes across levels)", m.Breadth)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Integration: ParseProject / ParseResearchRoot over the testdata tree
// ──────────────────────────────────────────────────────────────────────────

func testdataRoot(t *testing.T) string {
	t.Helper()
	return "testdata"
}

func TestParseProject_R001(t *testing.T) {
	p, err := ParseProject(filepath.Join("testdata", "R-001-web-exfil"))
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	if p.ID != "R-001" {
		t.Errorf("ID = %q, want R-001", p.ID)
	}
	if p.Brief.Title != "Web Exfil Detection" {
		t.Errorf("Brief.Title = %q", p.Brief.Title)
	}
	if p.HasReport {
		t.Error("HasReport should be false (report.md absent until synthesis)")
	}
	if p.PriorArtCount != 3 {
		t.Errorf("PriorArtCount = %d, want 3", p.PriorArtCount)
	}

	// Graph: 7 nodes, 6 edges.
	if len(p.Graph.Nodes) != 7 {
		t.Fatalf("Nodes = %d, want 7: %+v", len(p.Graph.Nodes), p.Graph.Nodes)
	}
	if len(p.Graph.Edges) != 6 {
		t.Errorf("Edges = %d, want 6: %+v", len(p.Graph.Edges), p.Graph.Edges)
	}

	// Card overrode the Mermaid "open" class for H-001 -> confirmed.
	h1 := p.Graph.Node("H-001")
	if h1 == nil || h1.Status != StatusConfirmed {
		t.Errorf("H-001 status = %v (card must override Mermaid 'open')", h1)
	}
	if h1.Result == "" {
		t.Error("H-001 Result should be populated from card")
	}

	// Partial card H-004 (no Parents declared) still gets parents from the edge.
	h4 := p.Graph.Node("H-004")
	if h4 == nil || len(h4.Parents) != 1 || h4.Parents[0] != "H-003" {
		t.Errorf("H-004 Parents = %+v, want [H-003] (derived from graph edge)", h4.Parents)
	}
	// Partial card H-005 (no Timebox) leaves Timebox empty.
	h5 := p.Graph.Node("H-005")
	if h5 != nil && h5.Timebox != "" {
		t.Errorf("H-005 Timebox = %q, want empty (partial card)", h5.Timebox)
	}

	// Metrics.
	m := p.Metrics
	if m.Total != 7 {
		t.Errorf("Total = %d, want 7", m.Total)
	}
	if !floatEq(m.ConfirmationRate, 2.0/3.0) {
		t.Errorf("ConfirmationRate = %v, want %v", m.ConfirmationRate, 2.0/3.0)
	}
	if m.Depth != 2 {
		t.Errorf("Depth = %d, want 2", m.Depth)
	}
	if m.Breadth != 2 {
		t.Errorf("Breadth = %d, want 2", m.Breadth)
	}
	wantFront := []string{"H-003", "H-004", "H-005"}
	if !reflect.DeepEqual(m.ActiveFront, wantFront) {
		t.Errorf("ActiveFront = %+v, want %+v", m.ActiveFront, wantFront)
	}

	// Log: the R-001 fixture carries a log.md with four entries.
	if len(p.Log) != 4 {
		t.Fatalf("Log = %d entries, want 4: %+v", len(p.Log), p.Log)
	}
	if p.Log[0].Kind != LogKindExperiment || p.Log[0].HypothesisID != "H-001" {
		t.Errorf("Log[0] = %+v, want experiment on H-001", p.Log[0])
	}
	if p.Log[1].Kind != LogKindDecision || p.Log[2].Kind != LogKindStatusChange || p.Log[3].Kind != LogKindNote {
		t.Errorf("Log kinds = %q,%q,%q, want decision,status_change,note",
			p.Log[1].Kind, p.Log[2].Kind, p.Log[3].Kind)
	}
}

func TestParseProject_R002_EmptyScaffold(t *testing.T) {
	p, err := ParseProject(filepath.Join("testdata", "R-002-empty"))
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	if p.ID != "R-002" {
		t.Errorf("ID = %q, want R-002", p.ID)
	}
	if len(p.Graph.Nodes) != 0 || len(p.Graph.Edges) != 0 {
		t.Errorf("empty graph expected, got %d nodes / %d edges", len(p.Graph.Nodes), len(p.Graph.Edges))
	}
	if p.Metrics.Total != 0 || p.Metrics.Depth != 0 || p.Metrics.Breadth != 0 {
		t.Errorf("empty project metrics should be zero, got %+v", p.Metrics)
	}
	if p.PriorArtCount != 0 {
		t.Errorf("PriorArtCount = %d, want 0 (placeholder only)", p.PriorArtCount)
	}
	if p.HasReport {
		t.Error("HasReport should be false for the empty scaffold")
	}
	// A project without a log.md still parses cleanly: Log is an empty
	// (non-nil) slice, preserving the partial-state contract.
	if p.Log == nil {
		t.Error("Log should be a non-nil empty slice for a project without log.md")
	}
	if len(p.Log) != 0 {
		t.Errorf("Log = %d entries, want 0", len(p.Log))
	}
}

func TestParseResearchRoot(t *testing.T) {
	root, err := ParseResearchRoot(testdataRoot(t))
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	if len(root.Index) != 2 {
		t.Errorf("Index = %d entries, want 2: %+v", len(root.Index), root.Index)
	}
	if len(root.Projects) != 2 {
		t.Fatalf("Projects = %d, want 2", len(root.Projects))
	}
	// Projects sorted by ID.
	if root.Projects[0].ID != "R-001" || root.Projects[1].ID != "R-002" {
		t.Errorf("project order = %s, %s", root.Projects[0].ID, root.Projects[1].ID)
	}
	// The active project is the latest index entry (R-002), precomputed once
	// at parse time so consumers share a single source of truth.
	if root.ActiveProjectID != "R-002" {
		t.Errorf("ActiveProjectID = %q, want R-002", root.ActiveProjectID)
	}
}

// TestPickActiveProject covers the exported selection helper directly: it
// returns the latest index entry when an index exists, the highest-numbered
// project directory as a fallback, and nil for a nil/empty root.
func TestPickActiveProject(t *testing.T) {
	t.Run("nil root returns nil", func(t *testing.T) {
		if got := PickActiveProject(nil); got != nil {
			t.Errorf("PickActiveProject(nil) = %+v, want nil", got)
		}
	})

	t.Run("empty root returns nil", func(t *testing.T) {
		if got := PickActiveProject(&ResearchRoot{Path: "/x"}); got != nil {
			t.Errorf("PickActiveProject(empty) = %+v, want nil", got)
		}
	})

	t.Run("latest index entry wins", func(t *testing.T) {
		root := &ResearchRoot{
			Index: []IndexEntry{{ID: "R-001"}, {ID: "R-002"}, {ID: "R-003"}},
			Projects: []*ResearchProject{
				{ID: "R-001"},
				{ID: "R-002"},
				{ID: "R-003"},
			},
		}
		got := PickActiveProject(root)
		if got == nil || got.ID != "R-003" {
			t.Errorf("PickActiveProject = %+v, want R-003", got)
		}
	})

	t.Run("fallback to highest-numbered dir when no index", func(t *testing.T) {
		root := &ResearchRoot{
			Projects: []*ResearchProject{
				{ID: "R-001"},
				{ID: "R-002"},
			},
		}
		got := PickActiveProject(root)
		if got == nil || got.ID != "R-002" {
			t.Errorf("PickActiveProject = %+v, want R-002 (last sorted)", got)
		}
	})
}

func TestParseProject_MissingDirIsError(t *testing.T) {
	if _, err := ParseProject(filepath.Join("testdata", "does-not-exist")); err == nil {
		t.Fatal("expected error for missing project dir, got nil")
	}
}

func TestParseResearchRoot_MissingDirIsError(t *testing.T) {
	if _, err := ParseResearchRoot(filepath.Join("testdata", "nope")); err == nil {
		t.Fatal("expected error for missing root dir, got nil")
	}
}

// TestParseResearchRoot_FlatSingleProject covers a research root that holds a
// single project's artifacts directly at its top level (the "flat" layout):
//
//	root/
//	├── brief.md
//	├── prior-art.md
//	└── hypotheses/
//	    ├── graph.md
//	    └── H-001.md
//
// This shape is non-conformant with the canonical nested layout
// (R-NNN-short-name/ wrapper), but it arises in practice (e.g. a dedicated
// single-project directory, or a root populated by an earlier workflow). The
// parser must surface it as a single project instead of rendering an empty
// panel.
func TestParseResearchRoot_FlatSingleProject(t *testing.T) {
	root := t.TempDir()

	writeFile := func(name, body string) {
		t.Helper()
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// A flat project: brief + prior-art + a one-hypothesis graph + one card,
	// all directly under the root. No index.md, no R-NNN wrapper dir.
	writeFile("brief.md", "# [R-007] Flat Project\n\n| **Status** | Active |\n")
	writeFile("prior-art.md", "## Entries\n\n| # | Title |\n|---|---|\n| 1 | One |\n| 2 | Two |\n")
	writeFile("hypotheses/graph.md", "# Graph\n\n```mermaid\n"+
		"graph TD\n"+
		"    H001[\"H-001: Root hypothesis\"]:::open\n"+
		"```\n")
	writeFile("hypotheses/H-001.md", "# H-001: Root hypothesis\n\n| **Status** | open |\n")

	parsed, err := ParseResearchRoot(root)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	if len(parsed.Projects) != 1 {
		t.Fatalf("Projects = %d, want 1 (flat project must be discovered): %+v",
			len(parsed.Projects), parsed.Projects)
	}
	p := parsed.Projects[0]
	if p.ID != "R-007" {
		t.Errorf("project ID = %q, want R-007 (from brief)", p.ID)
	}
	if p.Brief.Title != "Flat Project" {
		t.Errorf("Brief.Title = %q, want \"Flat Project\"", p.Brief.Title)
	}
	if p.PriorArtCount != 2 {
		t.Errorf("PriorArtCount = %d, want 2", p.PriorArtCount)
	}
	if len(p.Graph.Nodes) != 1 {
		t.Fatalf("Graph.Nodes = %d, want 1: %+v", len(p.Graph.Nodes), p.Graph.Nodes)
	}
	if p.Graph.Nodes[0].ID != "H-001" {
		t.Errorf("node ID = %q, want H-001", p.Graph.Nodes[0].ID)
	}
}

// TestParseResearchRoot_NestedUnaffected confirms the flat-layout fallback
// does not fire when the canonical nested layout is present: a root with an
// R-NNN subdirectory is parsed via the nested path only (no spurious second
// project from the root itself).
func TestParseResearchRoot_NestedUnaffected(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "R-001-x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Signature artifacts at the root level AND an R-NNN subdirectory: the
	// nested entry must win, and the root must not be double-counted.
	if err := os.WriteFile(filepath.Join(root, "brief.md"), []byte("# [R-099] Decoy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "R-001-x", "brief.md"), []byte("# [R-001] Nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	parsed, err := ParseResearchRoot(root)
	if err != nil {
		t.Fatalf("ParseResearchRoot: %v", err)
	}
	if len(parsed.Projects) != 1 {
		t.Fatalf("Projects = %d, want exactly 1 (nested only, no flat duplicate): %+v",
			len(parsed.Projects), parsed.Projects)
	}
	if parsed.Projects[0].ID != "R-001" {
		t.Errorf("project ID = %q, want R-001 (nested entry), not the R-099 decoy",
			parsed.Projects[0].ID)
	}
}

// ──────────────────────────────────────────────────────────────────────────
// Research log parsing (pure)
// ──────────────────────────────────────────────────────────────────────────

func TestParseLog_HappyPath(t *testing.T) {
	content := "# Research Log\n\n" +
		"## experiment 2025-04-02T10:15:00Z H-001\n" +
		"Recovered 97% of modules on the first pass.\n" +
		"\n" +
		"## decision 2025-04-03T09:00:00Z\n" +
		"Continue deepening the current front.\n" +
		"\n" +
		"## status_change 2025-04-04T11:30:00Z h-002\n" +
		"Transitioned H-002 from open to in-progress.\n" +
		"\n" +
		"## note 2025-04-05T08:00:00Z\n" +
		"Benchmark limited to 200 applications.\n"

	entries := ParseLog(content)
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(entries), entries)
	}

	// Ordinal IDs assigned in file order.
	if entries[0].ID != "1" || entries[1].ID != "2" || entries[2].ID != "3" || entries[3].ID != "4" {
		t.Errorf("IDs = %s,%s,%s,%s, want 1,2,3,4",
			entries[0].ID, entries[1].ID, entries[2].ID, entries[3].ID)
	}

	if entries[0].Kind != LogKindExperiment || entries[0].CreatedAt != "2025-04-02T10:15:00Z" {
		t.Errorf("entry0 kind/ts = %q / %q", entries[0].Kind, entries[0].CreatedAt)
	}
	if entries[0].HypothesisID != "H-001" {
		t.Errorf("entry0 hypothesis = %q, want H-001", entries[0].HypothesisID)
	}
	if entries[0].Message != "Recovered 97% of modules on the first pass." {
		t.Errorf("entry0 message = %q", entries[0].Message)
	}

	if entries[1].Kind != LogKindDecision || entries[1].HypothesisID != "" {
		t.Errorf("entry1 kind/hypothesis = %q / %q (project-scoped)", entries[1].Kind, entries[1].HypothesisID)
	}
	if entries[1].Message != "Continue deepening the current front." {
		t.Errorf("entry1 message = %q", entries[1].Message)
	}

	// Case-insensitive kind + lower-case hypothesis normalized to canonical form.
	if entries[2].Kind != LogKindStatusChange || entries[2].HypothesisID != "H-002" {
		t.Errorf("entry2 kind/hypothesis = %q / %q", entries[2].Kind, entries[2].HypothesisID)
	}

	if entries[3].Kind != LogKindNote || entries[3].Message != "Benchmark limited to 200 applications." {
		t.Errorf("entry3 = %+v", entries[3])
	}
}

func TestParseLog_MultiLineMessage(t *testing.T) {
	content := "## note 2025-04-05T08:00:00Z\n" +
		"First line.\n" +
		"Second line.\n" +
		"\n" +
		"Third paragraph after a blank line.\n"

	entries := ParseLog(content)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	want := "First line.\nSecond line.\n\nThird paragraph after a blank line."
	if entries[0].Message != want {
		t.Errorf("message = %q, want %q", entries[0].Message, want)
	}
}

func TestParseLog_EmptyAndPlaceholder(t *testing.T) {
	// Empty, whitespace-only, and placeholder-only content yield an empty
	// (non-nil) slice — the same partial-state contract as the other parsers.
	for _, c := range []string{"", "   \n\t\n", "# Research Log\n\n*No entries yet.*\n"} {
		entries := ParseLog(c)
		if entries == nil {
			t.Errorf("ParseLog(%q) = nil, want empty non-nil slice", c)
		}
		if len(entries) != 0 {
			t.Errorf("ParseLog(%q) = %d entries, want 0", c, len(entries))
		}
	}
}

func TestParseLog_MalformedKindAndTimestampIgnored(t *testing.T) {
	// A recognized kind followed by a non-timestamp token is prose, not an
	// entry; an unknown kind is skipped entirely. Neither yields an entry.
	content := "## bogus 2025-04-02T10:15:00Z H-001\nignored\n" +
		"## note about something\nthis is prose, not an entry\n"

	entries := ParseLog(content)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d: %+v", len(entries), entries)
	}
}

func TestParseLog_UnknownKindSkippedButValidEntriesKept(t *testing.T) {
	content := "## experiment 2025-04-02T10:15:00Z H-001\nReal entry.\n" +
		"## unknown 2025-04-03T09:00:00Z\nSkipped.\n" +
		"## note 2025-04-04T10:00:00Z\nAnother real entry.\n"

	entries := ParseLog(content)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (unknown kind skipped), got %d: %+v", len(entries), entries)
	}
	if entries[0].Kind != LogKindExperiment || entries[1].Kind != LogKindNote {
		t.Errorf("kinds = %q, %q", entries[0].Kind, entries[1].Kind)
	}
	// The unknown-kind heading is skipped without consuming an ordinal, but it
	// must not leak into the first entry's message either.
	if entries[0].Message != "Real entry." {
		t.Errorf("entry0 message = %q, want %q (unknown-kind heading must not leak in)", entries[0].Message, "Real entry.")
	}
	if entries[1].ID != "2" {
		t.Errorf("entry1 ID = %q, want 2 (ordinal still sequential)", entries[1].ID)
	}
}

func TestParseLog_BareOrdinalNotTimestamp(t *testing.T) {
	// A bare ordinal in the timestamp slot ("## note 5") must not be mistaken
	// for a timestamp: it is prose, not an entry.
	content := "## note 5\nA stray note-like heading.\n"
	entries := ParseLog(content)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d: %+v", len(entries), entries)
	}
}
