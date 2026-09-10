package research

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// [16]b — escaping round-trips (pipes / newlines / quotes / emoji)
// ---------------------------------------------------------------------------

func TestEscapeCellRoundTrip(t *testing.T) {
	cases := []string{
		"plain",
		"pipe | inside",
		"two | pipes | here",
		"line one\nline two",
		"pipe |\nand newline",
		`escaped \| already`,
		"emoji 🔬🚀 and \"quotes\"",
		"trailing pipe |",
	}
	for _, raw := range cases {
		escaped := escapeCell(raw)
		if strings.ContainsAny(escaped, "\n\r") {
			t.Errorf("escapeCell(%q) still contains a newline: %q", raw, escaped)
		}
		row := "| **X** | " + escaped + " |"
		cells := splitCells(row)
		if len(cells) < 2 {
			t.Fatalf("splitCells(%q) = %v, want >= 2 cells", row, cells)
		}
		if got := cells[1]; got != raw {
			t.Errorf("round-trip mismatch: raw=%q got=%q (escaped=%q)", raw, got, escaped)
		}
		// write → parse → write: escaping the parsed value must reproduce the
		// identical written form (idempotence).
		if again := escapeCell(cells[1]); again != escaped {
			t.Errorf("re-escape mismatch: first=%q second=%q", escaped, again)
		}
	}
}

func TestEscapeMermaidLabelRoundTrip(t *testing.T) {
	cases := []string{
		"plain title",
		`say "hi" loudly`,
		"pipe | inside",
		`both "quote" | and pipe`,
		"line one\nline two",
		"emoji 🚀 mixed | with \"quotes\"",
	}
	for _, raw := range cases {
		escaped := escapeMermaidLabel(raw)
		if strings.ContainsAny(escaped, "\n\r\"") {
			t.Errorf("escapeMermaidLabel(%q) still contains newline/quote: %q", raw, escaped)
		}
		line := `    H001["H-001: ` + escaped + `"]:::open`
		// The escaped label MUST keep the line matching mermaidNodeLineRe —
		// the whole point of the writer/parser coordination.
		m := mermaidNodeLineRe.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("written mermaid line does not match mermaidNodeLineRe: %s", line)
		}
		// The label is "H-001: <title>"; mirror ParseMermaidGraph's prefix
		// strip before comparing.
		label := unescapeMermaidLabel(m[3])
		if idx := strings.Index(label, ":"); idx >= 0 {
			label = strings.TrimSpace(label[idx+1:])
		}
		if label != raw {
			t.Errorf("round-trip mismatch: raw=%q got=%q (escaped=%q)", raw, label, escaped)
		}
	}

	// A raw value that already contains a reserved marker sequence verbatim
	// ("#124;" here) has that sequence interpreted on parse — the same
	// ambiguity Markdown's own "\|" escape carries. The round-trip is still
	// stable: write → parse → write reproduces the identical written form.
	raw := "emoji 🚀 and #124; literal"
	write1 := escapeMermaidLabel(raw)
	parsed := unescapeMermaidLabel(write1)
	if write2 := escapeMermaidLabel(parsed); write2 != write1 {
		t.Errorf("reserved-sequence value not stable: write1=%q parsed=%q write2=%q", write1, parsed, write2)
	}
}

func TestSplitCells_EscapedPipesAndBR(t *testing.T) {
	cells := splitCells(`| a \| b | c<br>d |`)
	if len(cells) != 2 {
		t.Fatalf("cells = %#v, want 2", cells)
	}
	if cells[0] != "a | b" {
		t.Errorf("cells[0] = %q, want %q", cells[0], "a | b")
	}
	if cells[1] != "c\nd" {
		t.Errorf("cells[1] = %q, want %q", cells[1], "c\nd")
	}
	// A trailing escaped pipe is a value, not a row delimiter.
	cells = splitCells(`| **Timebox** | ends with \| |`)
	if len(cells) != 2 || cells[1] != `ends with |` {
		t.Errorf("cells = %#v, want [**Timebox** ends-with-pipe]", cells)
	}
	// Separator rows still parse as separators under the new splitter.
	if !isSeparatorRow(splitCells("|---|---|")) {
		t.Error("separator row no longer recognized")
	}
}

func TestSetTableField_EscapesPipeAndNewline(t *testing.T) {
	card := "# H-001: T\n\n| Field | Value |\n|---|---|\n| **Status** | open |\n\n## Statement\n\nx\n"
	value := "pipes | and\nnewlines \"q\" 🚀"
	got := setTableField(card, "Decision", value)
	if !strings.Contains(got, `| **Decision** | pipes \| and<br>newlines "q" 🚀 |`) {
		t.Fatalf("value not escaped into the row:\n%s", got)
	}
	// The row must survive its own re-parse with the value intact.
	if gotField := extractField(got, "Decision"); gotField != value {
		t.Errorf("extractField = %q, want %q", gotField, value)
	}
	// An existing row's replacement is escaped too.
	got2 := setTableField(card, "Timebox", "a | b\nc")
	if !strings.Contains(got2, `| **Timebox** | a \| b<br>c |`) {
		t.Fatalf("existing-row replacement not escaped:\n%s", got2)
	}
}

// TestUpdateHypothesis_HostileValueRoundTrip is the acceptance round-trip:
// write hostile values (pipes, newlines, quotes, emoji), parse them back
// exactly, then write the parsed values again and require byte-identical
// files — the writer and the parser agree on one canonical spelling.
func TestUpdateHypothesis_HostileValueRoundTrip(t *testing.T) {
	root, dir := setupProjectDir(t)

	title := `Refined "bundle" parsing | v2 🚀`
	result := "Found pipes | and\nnewlines \"quoted\" 🔬"
	decision := "continue | pivot 🤔"
	status := "in-progress"

	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{
		Status:   &status,
		Title:    &title,
		Result:   &result,
		Decision: &decision,
	}); err != nil {
		t.Fatalf("UpdateHypothesis: %v", err)
	}

	card1 := readCard(t, dir, "H-001")
	graph1 := readGraph(t, dir)

	// Parsed values must be the originals, exactly.
	proj, err := ParseProject(dir)
	if err != nil {
		t.Fatalf("ParseProject: %v", err)
	}
	n := proj.Graph.Node("H-001")
	if n == nil {
		t.Fatal("H-001 missing")
	}
	if n.Title != title {
		t.Errorf("Title = %q, want %q", n.Title, title)
	}
	if n.Result != result {
		t.Errorf("Result = %q, want %q", n.Result, result)
	}
	if got := extractField(card1, "Decision"); got != decision {
		t.Errorf("card Decision = %q, want %q", got, decision)
	}

	// Card keeps raw pipes/quotes in the heading (fine for the parser).
	if !strings.Contains(card1, "# H-001: "+title) {
		t.Errorf("card H1 title mangled:\n%s", card1)
	}
	// Table cell and finding line carry the escape spellings.
	if !strings.Contains(card1, `| **Decision** | continue \| pivot 🤔 |`) {
		t.Errorf("card Decision row not pipe-escaped:\n%s", card1)
	}
	if !strings.Contains(card1, "**Finding:** Found pipes \\| and<br>newlines \"quoted\" 🔬") {
		t.Errorf("card Finding line not folded/escaped:\n%s", card1)
	}

	// Mermaid node line: escaped label, still matching the parser regex.
	mermaidLine := findLineContaining(graph1, `H001["H-001:`)
	if mermaidLine == "" {
		t.Fatalf("mermaid node line missing:\n%s", graph1)
	}
	if mermaidNodeLineRe.FindStringSubmatch(mermaidLine) == nil {
		t.Errorf("escaped mermaid line fails mermaidNodeLineRe: %s", mermaidLine)
	}
	if !strings.Contains(mermaidLine, "#quot;") || !strings.Contains(mermaidLine, "#124;") {
		t.Errorf("mermaid label not escaped: %s", mermaidLine)
	}
	mnodes, _ := ParseMermaidGraph(graph1)
	if len(mnodes) != 1 || mnodes[0].title != title {
		t.Errorf("ParseMermaidGraph title = %+v, want %q", mnodes, title)
	}

	// Catalog row parses back to the exact title and decision.
	row := findLineContaining(graph1, "[H-001](H-001.md)")
	cells := splitCells(row)
	if len(cells) < 4 || cells[1] != title {
		t.Errorf("catalog title cells = %#v, want title %q", cells, title)
	}
	if len(cells) < 4 || cells[3] != decision {
		t.Errorf("catalog decision cells = %#v, want %q", cells, decision)
	}

	// Second write with the parsed values: byte-identical card and graph.
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{
		Title:  &n.Title,
		Result: &n.Result,
	}); err != nil {
		t.Fatalf("second UpdateHypothesis: %v", err)
	}
	if card2 := readCard(t, dir, "H-001"); card2 != card1 {
		t.Errorf("card not byte-stable across write→parse→write:\n--- first ---\n%s\n--- second ---\n%s", card1, card2)
	}
	if graph2 := readGraph(t, dir); graph2 != graph1 {
		t.Errorf("graph not byte-stable across write→parse→write:\n--- first ---\n%s\n--- second ---\n%s", graph1, graph2)
	}
}

// ---------------------------------------------------------------------------
// [46]a — minimal field table when the card has none
// ---------------------------------------------------------------------------

func TestSetTableField_InsertsMinimalTableWhenAbsent(t *testing.T) {
	card := "# H-001: Only a heading\n\n## Statement\n\nFree-form body.\n"
	got := setTableField(card, "Status", "open")
	for _, want := range []string{
		"| Field | Value |",
		"|---|---|",
		"| **Status** | open |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("minimal table missing %q in:\n%s", want, got)
		}
	}
	// The update must not be dropped: the field parses back out.
	if field := extractField(got, "Status"); field != "open" {
		t.Errorf("extractField(Status) = %q, want open", field)
	}
	// The pre-existing body survives.
	if !strings.Contains(got, "Free-form body.") {
		t.Errorf("statement body lost:\n%s", got)
	}
	// The table lands between the H1 and the first section heading.
	tableIdx := strings.Index(got, "| **Status** | open |")
	stmtIdx := strings.Index(got, "## Statement")
	h1Idx := strings.Index(got, "# H-001: Only a heading")
	if h1Idx == -1 || tableIdx == -1 || stmtIdx == -1 || tableIdx <= h1Idx || stmtIdx <= tableIdx {
		t.Errorf("field table not placed between H1 and Statement:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// [68]a — setFinding folds newlines (single-line parser contract)
// ---------------------------------------------------------------------------

func TestSetFinding_FoldsNewlinesRoundTrip(t *testing.T) {
	card := "# H-001: T\n\n## Result\n\n**Finding:** —\n"
	multi := "first line\nsecond line"
	got := setFinding(card, multi)
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "**Finding:**") && strings.Contains(line, "\n") {
			t.Fatalf("finding line contains a newline:\n%s", got)
		}
	}
	if !strings.Contains(got, "**Finding:** first line<br>second line") {
		t.Fatalf("finding not folded to <br>:\n%s", got)
	}
	if back := extractFinding(got); back != multi {
		t.Errorf("extractFinding = %q, want %q", back, multi)
	}
	// Write the parsed value back: same bytes (idempotent fold).
	if again := setFinding(got, multi); again != got {
		t.Errorf("setFinding not idempotent:\n%s\n---\n%s", got, again)
	}
}

// ---------------------------------------------------------------------------
// [74]a — catalog row insertion after the separator when only a header exists
// ---------------------------------------------------------------------------

func TestAddCatalogRow_InsertsAfterSeparatorRow(t *testing.T) {
	content := "# Hypothesis Graph\n\n## Hypothesis Catalog\n\n| ID | Hypothesis | Status | Decision | Parent(s) |\n|---|---|---|---|---|\n\n---\n"
	got := addCatalogRow(content, "H-001", "First", StatusOpen, "", nil)

	row := findLineContaining(got, "[H-001](H-001.md)")
	if row == "" {
		t.Fatalf("catalog row not inserted:\n%s", got)
	}
	sepIdx := strings.Index(got, "|---|---|---|---|---|")
	rowIdx := strings.Index(got, "[H-001](H-001.md)")
	if sepIdx == -1 || rowIdx < sepIdx {
		t.Fatalf("row not after the separator row:\n%s", got)
	}
	// The inserted row parses as a catalog entry.
	rows := ParseCatalog(got)
	if len(rows) != 1 || rows[0].id != "H-001" || rows[0].title != "First" {
		t.Fatalf("ParseCatalog = %+v, want one H-001 row", rows)
	}
}

// ---------------------------------------------------------------------------
// [69]b — missing sections are inserted, existing content preserved
// ---------------------------------------------------------------------------

func TestEnsureGraphSkeleton_InsertsMissingSectionsPreservingContent(t *testing.T) {
	// Catalog present, Mermaid missing: the diagram section is inserted and
	// the catalog rows + a free-form note survive.
	catalogOnly := "# Hypothesis Graph\n\nSome free-form note that must survive.\n\n## Hypothesis Catalog\n\n| ID | Hypothesis | Status | Decision | Parent(s) |\n|---|---|---|---|---|\n| [H-001](H-001.md) | One | open | — | — |\n"
	got := ensureGraphSkeleton(catalogOnly)
	if !strings.Contains(got, "```mermaid") {
		t.Errorf("mermaid section not inserted:\n%s", got)
	}
	if !strings.Contains(got, "Some free-form note that must survive.") {
		t.Errorf("free-form note lost:\n%s", got)
	}
	if !strings.Contains(got, "[H-001](H-001.md) | One | open") {
		t.Errorf("existing catalog row lost:\n%s", got)
	}
	// The diagram is inserted above the catalog heading.
	if strings.Index(got, "```mermaid") > strings.Index(got, "## Hypothesis Catalog") {
		t.Errorf("mermaid section inserted below the catalog:\n%s", got)
	}

	// Mermaid present, catalog missing: catalog section inserted below the fence.
	mermaidOnly := "# Hypothesis Graph\n\n## Diagram\n\n```mermaid\ngraph TD\n    H001[\"H-001: One\"]:::open\n```\n\nKeep me too.\n"
	got2 := ensureGraphSkeleton(mermaidOnly)
	if !strings.Contains(got2, "## Hypothesis Catalog") {
		t.Errorf("catalog section not inserted:\n%s", got2)
	}
	if !strings.Contains(got2, "| *No hypotheses yet.* |") {
		t.Errorf("catalog placeholder missing:\n%s", got2)
	}
	if !strings.Contains(got2, "Keep me too.") {
		t.Errorf("trailing content lost:\n%s", got2)
	}
	if strings.Index(got2, "## Hypothesis Catalog") < strings.Index(got2, "```") {
		t.Errorf("catalog section inserted above the diagram:\n%s", got2)
	}
	// The inserted skeleton parts are usable: a node and a row land in them.
	withRow := addCatalogRow(got2, "H-002", "Two", StatusOpen, "", nil)
	if !strings.Contains(withRow, "[H-002](H-002.md)") {
		t.Errorf("row not added into the inserted catalog:\n%s", withRow)
	}
	withNode := addMermaidNodeAndEdges(got2, "H-002", "Two", StatusOpen, nil)
	if !strings.Contains(withNode, `H002["H-002: Two"]`) {
		t.Errorf("node not added into the inserted diagram:\n%s", withNode)
	}

	// Blank content still yields the full skeleton.
	skeleton := ensureGraphSkeleton("   \n\n")
	if !strings.Contains(skeleton, "```mermaid") || !strings.Contains(skeleton, "Hypothesis Catalog") {
		t.Errorf("blank content did not get a full skeleton:\n%s", skeleton)
	}

	// Complete content is returned unchanged.
	complete := mermaidOnly + "\n## Hypothesis Catalog\n\n| ID | Hypothesis | Status | Decision | Parent(s) |\n|---|---|---|---|---|\n"
	if got3 := ensureGraphSkeleton(complete); got3 != complete {
		t.Errorf("complete content modified:\n%s", got3)
	}
}

// ---------------------------------------------------------------------------
// [72]a — log.md append via temp+rename
// ---------------------------------------------------------------------------

func TestAppendLogEntry_CreatesAppendsAndParses(t *testing.T) {
	root, dir := setupProjectDir(t)

	// First append creates the file with the "# Research Log" preamble.
	if err := AppendLogEntry(root, dir, ResearchLogEntry{
		Kind:         LogKindStatusChange,
		HypothesisID: "H001", // normalized to H-001
		CreatedAt:    "2025-04-06T12:00:00Z",
		Message:      "Moved H-001 from open to in-progress.",
	}); err != nil {
		t.Fatalf("AppendLogEntry: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "log.md"))
	if err != nil {
		t.Fatalf("log.md not created: %v", err)
	}
	content := string(raw)
	if !strings.HasPrefix(content, "# Research Log\n") {
		t.Errorf("missing preamble:\n%s", content)
	}
	if !strings.Contains(content, "## status_change 2025-04-06T12:00:00Z H-001") {
		t.Errorf("entry heading malformed:\n%s", content)
	}

	// Second append preserves the first entry.
	if err := AppendLogEntry(root, dir, ResearchLogEntry{
		Kind:         LogKindDecision,
		HypothesisID: "H-001",
		Message:      "Decision: continue.", // CreatedAt defaults to now
	}); err != nil {
		t.Fatalf("second AppendLogEntry: %v", err)
	}
	content2 := readLog(t, dir)
	if !strings.Contains(content2, "Moved H-001 from open to in-progress.") {
		t.Errorf("first entry lost on append:\n%s", content2)
	}

	entries := ParseLog(content2)
	if len(entries) != 2 {
		t.Fatalf("ParseLog = %d entries, want 2: %+v", len(entries), entries)
	}
	first := entries[0]
	if first.Kind != LogKindStatusChange || first.HypothesisID != "H-001" || first.CreatedAt != "2025-04-06T12:00:00Z" {
		t.Errorf("first entry = %+v", first)
	}
	if first.Message != "Moved H-001 from open to in-progress." {
		t.Errorf("first message = %q", first.Message)
	}
	second := entries[1]
	if second.Kind != LogKindDecision || !looksLikeTimestamp(second.CreatedAt) {
		t.Errorf("second entry = %+v", second)
	}
	if second.ID != "2" {
		t.Errorf("second ordinal = %q, want 2", second.ID)
	}
}

func TestAppendLogEntry_MessageHeadingsDemoted(t *testing.T) {
	// A message line starting with "## " would terminate the entry at parse
	// time; the writer demotes it to "### " so the body survives.
	msg := "summary line\n## not a new entry\nbody continues"
	got, err := appendLogEntryContent("", LogKindNote, "", "2025-04-06T12:00:00Z", msg)
	if err != nil {
		t.Fatalf("appendLogEntryContent: %v", err)
	}
	if strings.Contains(got, "\n## not a new entry") {
		t.Fatalf("level-2 heading not demoted:\n%s", got)
	}
	entries := ParseLog(got)
	if len(entries) != 1 {
		t.Fatalf("ParseLog = %+v, want a single entry", entries)
	}
	if !strings.Contains(entries[0].Message, "### not a new entry") || !strings.Contains(entries[0].Message, "body continues") {
		t.Errorf("message body truncated: %q", entries[0].Message)
	}
}

func TestAppendLogEntry_RejectsBadKindAndTimestamp(t *testing.T) {
	root, dir := setupProjectDir(t)
	if err := AppendLogEntry(root, dir, ResearchLogEntry{Kind: LogKind("bogus"), CreatedAt: "2025-04-06T12:00:00Z"}); err == nil {
		t.Error("expected error for unknown kind")
	}
	if err := AppendLogEntry(root, dir, ResearchLogEntry{Kind: LogKindNote, CreatedAt: "yesterday"}); err == nil {
		t.Error("expected error for non-ISO-8601 timestamp")
	}
	if _, err := os.Stat(filepath.Join(dir, "log.md")); !os.IsNotExist(err) {
		t.Error("rejected entries must not create log.md")
	}
}

func TestAppendLogEntry_OutsideRootRejected(t *testing.T) {
	root, _ := setupProjectDir(t)
	outside := t.TempDir()
	if err := AppendLogEntry(root, outside, ResearchLogEntry{Kind: LogKindNote, Message: "x"}); err == nil {
		t.Fatal("expected containment error for a project dir outside the research root")
	}
	if _, err := os.Stat(filepath.Join(outside, "log.md")); !os.IsNotExist(err) {
		t.Error("log.md written outside the research root")
	}
}

func TestUpdateHypothesis_AppendsStatusChangeAndDecisionToLog(t *testing.T) {
	root, dir := setupProjectDir(t)

	status := "in-progress"
	decision := "continue"
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{Status: &status, Decision: &decision}); err != nil {
		t.Fatalf("UpdateHypothesis: %v", err)
	}
	entries := ParseLog(readLog(t, dir))
	if len(entries) != 2 {
		t.Fatalf("log entries = %+v, want 2 (status_change + decision)", entries)
	}
	if entries[0].Kind != LogKindStatusChange || entries[0].HypothesisID != "H-001" {
		t.Errorf("entry[0] = %+v, want status_change for H-001", entries[0])
	}
	if !strings.Contains(entries[0].Message, "open") || !strings.Contains(entries[0].Message, "in-progress") {
		t.Errorf("status_change message = %q", entries[0].Message)
	}
	if entries[1].Kind != LogKindDecision || entries[1].HypothesisID != "H-001" {
		t.Errorf("entry[1] = %+v, want decision for H-001", entries[1])
	}

	// Re-setting the SAME status is a no-op transition: no duplicate
	// status_change entry. The decision does append again (it is a new,
	// explicit recording).
	before := readLog(t, dir)
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{Status: &status, Decision: &decision}); err != nil {
		t.Fatalf("second UpdateHypothesis: %v", err)
	}
	entries = ParseLog(readLog(t, dir))
	if len(entries) != 3 {
		t.Fatalf("log entries = %d, want 3 (no-op status must not log): %+v", len(entries), entries)
	}
	if !strings.Contains(readLog(t, dir), strings.TrimSpace(before)) {
		t.Error("append rewrote existing log content")
	}
}

// ---------------------------------------------------------------------------
// [73]a — Completed = today on terminal transition + field parsing
// ---------------------------------------------------------------------------

func TestUpdateHypothesis_TerminalTransitionSetsCompleted(t *testing.T) {
	root, dir := setupProjectDir(t)
	today := time.Now().Format("2006-01-02")

	inProgress := "in-progress"
	confirmed := "confirmed"
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{Status: &inProgress}); err != nil {
		t.Fatalf("open→in-progress: %v", err)
	}
	if got := extractField(readCard(t, dir, "H-001"), "Completed"); got != "—" && got != "" {
		t.Errorf("non-terminal transition stamped Completed = %q", got)
	}
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{Status: &confirmed}); err != nil {
		t.Fatalf("in-progress→confirmed: %v", err)
	}

	card := readCard(t, dir, "H-001")
	if got := extractField(card, "Completed"); got != today {
		t.Errorf("Completed = %q, want %q\n%s", got, today, card)
	}
	node, err := ParseCard(card)
	if err != nil {
		t.Fatalf("ParseCard: %v", err)
	}
	if node.Completed != today {
		t.Errorf("HypothesisNode.Completed = %q, want %q", node.Completed, today)
	}

	// A pre-existing completion date is never overwritten, and re-setting the
	// same terminal status neither logs nor re-stamps.
	manual := "| **Completed** | 2024-01-01 |"
	if err := os.WriteFile(filepath.Join(dir, "hypotheses", "H-001.md"), []byte(strings.Replace(card, "| **Completed** | "+today+" |", manual, 1)), 0o644); err != nil {
		t.Fatalf("rewrite card: %v", err)
	}
	logBefore := ParseLog(readLog(t, dir))
	if err := UpdateHypothesis(root, dir, "H-001", HypothesisUpdate{Status: &confirmed}); err != nil {
		t.Fatalf("re-set terminal status: %v", err)
	}
	if got := extractField(readCard(t, dir, "H-001"), "Completed"); got != "2024-01-01" {
		t.Errorf("Completed overwritten: %q, want 2024-01-01", got)
	}
	if got := len(ParseLog(readLog(t, dir))); got != len(logBefore) {
		t.Errorf("no-op terminal re-set logged an entry (%d → %d)", len(logBefore), got)
	}
}

func TestParseCard_CompletedField(t *testing.T) {
	card := "# H-001: T\n\n| Field | Value |\n|---|---|\n| **Status** | confirmed |\n| **Completed** | 2025-04-07 |\n"
	node, err := ParseCard(card)
	if err != nil {
		t.Fatalf("ParseCard: %v", err)
	}
	if node.Completed != "2025-04-07" {
		t.Errorf("Completed = %q, want 2025-04-07", node.Completed)
	}
	// The placeholder dash parses as empty.
	open := "# H-002: T\n\n| Field | Value |\n|---|---|\n| **Completed** | — |\n"
	node2, err := ParseCard(open)
	if err != nil {
		t.Fatalf("ParseCard(open): %v", err)
	}
	if node2.Completed != "" {
		t.Errorf("Completed = %q, want empty", node2.Completed)
	}
}

func TestCreateHypothesis_TerminalStatusStampsCompleted(t *testing.T) {
	root, dir := setupProjectDir(t)
	today := time.Now().Format("2006-01-02")
	id, err := CreateHypothesis(root, dir, NewHypothesis{Title: "Doomed", Status: StatusCancelled})
	if err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}
	if got := extractField(readCard(t, dir, id), "Completed"); got != today {
		t.Errorf("Completed = %q, want %q", got, today)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func readCard(t *testing.T, dir, id string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "hypotheses", id+".md"))
	if err != nil {
		t.Fatalf("read card: %v", err)
	}
	return string(raw)
}

func readGraph(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "hypotheses", "graph.md"))
	if err != nil {
		t.Fatalf("read graph: %v", err)
	}
	return string(raw)
}

func readLog(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "log.md"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return string(raw)
}

func findLineContaining(content, needle string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
