// Package research — writer layer.
//
// This file implements the mutation half of the research package: creating
// and updating hypothesis cards and the hypothesis graph (Mermaid diagram +
// catalog table) in place. It is the structured counterpart to the parser
// (parser.go), which only ever reads.
//
// The public entry points are UpdateHypothesis and CreateHypothesis. Both are
// "atomic-ish" in the sense that they (1) read everything first, (2) compute
// the new card and graph contents purely in memory, (3) validate the status
// transition (and any other invariants) before touching disk, and (4) write
// each file via a temp-file + rename so a single file is never left in a
// half-written state. A validation failure therefore leaves both files byte-
// for-byte unchanged.
package research

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/pathutil"
)

// HypothesisUpdate is the set of optional field updates for an existing
// hypothesis card. Pointer fields distinguish "leave unchanged" (nil) from
// "set to empty" (a non-nil pointer to ""). Only the five fields the UI mutates
// are represented; identifier, statement, verification criterion, created, and
// completed are not editable through this path.
type HypothesisUpdate struct {
	// Title is the short, human-readable label (also mirrored into the graph
	// node label and catalog row).
	Title *string

	// Status is the new lifecycle status (open / in-progress / confirmed /
	// refuted / cancelled). It is validated against the transition state
	// machine and normalized to the canonical hyphenated spelling.
	Status *string

	// Timebox is the raw timebox field (ISO 8601 duration or calendar dates).
	Timebox *string

	// Result is the recorded finding (the card's **Finding:** value).
	Result *string

	// Decision is the iteration decision (continue / pivot / kill / fork),
	// recorded by the research-decision skill.
	Decision *string
}

// NewHypothesis is the input for creating a fresh hypothesis card.
type NewHypothesis struct {
	Title                 string   // required short label
	Statement             string   // falsifiable assertion
	VerificationCriterion string   // what constitutes confirmation
	Timebox               string   // max time allocated (ISO 8601 / calendar)
	Parents               []string // parent hypothesis IDs (empty for roots)
	Status                HypothesisStatus
	Decision              string
	Created               string // ISO 8601 date; defaults to today when empty
}

// ---------------------------------------------------------------------------
// Status transition state machine
// ---------------------------------------------------------------------------

// transitions maps a current status to the set of statuses it may legally
// transition into, per the research-hypothesis skill:
//
//	open → in-progress → confirmed | refuted | cancelled
//	open → cancelled
//
// Terminal statuses (confirmed / refuted / cancelled) have no outgoing
// transitions. Backward transitions (in-progress → open, or any transition out
// of a terminal state) are rejected.
var transitions = map[HypothesisStatus][]HypothesisStatus{
	StatusOpen:       {StatusInProgress, StatusCancelled},
	StatusInProgress: {StatusConfirmed, StatusRefuted, StatusCancelled},
	StatusConfirmed:  {},
	StatusRefuted:    {},
	StatusCancelled:  {},
}

// isKnownStatus reports whether s is one of the five canonical statuses.
func isKnownStatus(s HypothesisStatus) bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusConfirmed, StatusRefuted, StatusCancelled:
		return true
	default:
		return false
	}
}

// ValidateTransition reports whether from→to is a legal lifecycle transition.
// Re-setting the same status is a no-op and is allowed. An empty or unknown
// current status (e.g. a card that predates status tracking) is treated as an
// initial set and accepts any known target status. An unknown target status is
// always rejected.
func ValidateTransition(from, to HypothesisStatus) error {
	to = NormalizeStatus(string(to))
	if !isKnownStatus(to) {
		return fmt.Errorf("unknown hypothesis status %q", string(to))
	}
	from = NormalizeStatus(string(from))
	if from == "" || !isKnownStatus(from) {
		return nil // initial set
	}
	if from == to {
		return nil // no-op
	}
	for _, a := range transitions[from] {
		if a == to {
			return nil
		}
	}
	return fmt.Errorf("illegal hypothesis status transition %q → %q", string(from), string(to))
}

// ---------------------------------------------------------------------------
// Value escaping (round-trip contract with parser.go)
// ---------------------------------------------------------------------------

// escapeCell renders a raw field value into a single-line Markdown table-cell
// value: newlines fold to "<br>" (a row must stay one line — the parser reads
// tables line by line) and literal pipes escape to "\|" (an unescaped pipe
// would split the cell). The parser's splitCells/unescapeCell reverses both,
// so written values parse back exactly.
func escapeCell(v string) string {
	if strings.ContainsAny(v, "|\n\r") {
		v = foldBR(v)
		v = strings.ReplaceAll(v, "|", `\|`)
	}
	return v
}

// foldBR replaces any newline flavor (and surrounding spaces) with the
// Markdown line-break marker "<br>", used wherever a value must be written
// onto a single line (table cells, the **Finding:** line, Mermaid labels).
func foldBR(v string) string {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	return strings.ReplaceAll(v, "\n", "<br>")
}

// foldTitle collapses a short-label value (a card / catalog / Mermaid title)
// to a single line by turning newlines into spaces. Titles are labels, not
// content: folding them identically at every write site (card H1, catalog
// cell, Mermaid label) keeps write → parse → write byte-stable instead of
// letting one site render "<br>" where the others render a space.
func foldTitle(v string) string {
	if !strings.ContainsAny(v, "\n\r") {
		return v
	}
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	fields := strings.Fields(v)
	return strings.Join(fields, " ")
}

// escapeMermaidLabel renders a raw title into a Mermaid quoted label
// ("H001[\"H-001: <label>\"]"): newlines fold to "<br>", a literal pipe
// becomes "#124;" (the pipe character is edge-label syntax in Mermaid), and a
// double quote becomes "#quot;". Those escapes are exactly what keeps the
// written line matching mermaidNodeLineRe — whose label group is [^"]* on a
// single line — so a quoted label containing pipes or quotes still parses
// back; unescapeMermaidLabel (parser.go) reverses them.
func escapeMermaidLabel(v string) string {
	v = foldBR(v)
	if strings.Contains(v, "|") {
		v = strings.ReplaceAll(v, "|", "#124;")
	}
	if strings.Contains(v, `"`) {
		v = strings.ReplaceAll(v, `"`, "#quot;")
	}
	return v
}

// ---------------------------------------------------------------------------
// Card content rewriting (pure)
// ---------------------------------------------------------------------------

// cardHeadingLineRe matches a card H1 like "# H-001: Short title".
var cardHeadingLineRe = regexp.MustCompile(`^#\s+(H-?\d+)(?::\s*(.*))?$`)

// setCardHeading replaces the H1 title of a card with the given canonical ID.
func setCardHeading(content, id, title string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		m := cardHeadingLineRe.FindStringSubmatch(line)
		if len(m) < 2 || NormalizeID(m[1]) != id {
			continue
		}
		lines[i] = fmt.Sprintf("# %s: %s", id, foldTitle(title))
		return strings.Join(lines, "\n")
	}
	return content
}

// setTableField sets (or inserts) a two-column field row "| **Field** | value |"
// in the card's front-matter table. It returns the updated content. When the
// field row does not exist it is inserted after the last existing field row,
// so updating e.g. "Decision" on a card that never recorded one still works.
// When the card carries no field table at all (no bolded field row anywhere),
// a minimal "| Field | Value |" table is inserted after the card H1 so the
// update can never be silently dropped. The value is escaped via escapeCell,
// keeping the row single-line and pipe-safe; the parser reverses the escape.
func setTableField(content, fieldName, value string) string {
	target := "**" + fieldName + "**"
	escaped := escapeCell(value)
	lines := strings.Split(content, "\n")

	// Replace an existing row.
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) >= 2 && cells[0] == target {
			lines[i] = "| " + target + " | " + escaped + " |"
			return strings.Join(lines, "\n")
		}
	}

	// Insert after the last field row (a row whose first cell is bolded).
	lastFieldRow := -1
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) >= 2 && strings.HasPrefix(cells[0], "**") && strings.HasSuffix(cells[0], "**") {
			lastFieldRow = i
		}
	}
	if lastFieldRow != -1 {
		newRow := "| " + target + " | " + escaped + " |"
		result := make([]string, 0, len(lines)+1)
		result = append(result, lines[:lastFieldRow+1]...)
		result = append(result, newRow)
		result = append(result, lines[lastFieldRow+1:]...)
		return strings.Join(result, "\n")
	}

	// No field table at all: insert a minimal one after the card H1 (or at the
	// top when the card has no H1), so the field update always lands somewhere
	// parseable instead of being silently dropped.
	table := []string{
		"| Field | Value |",
		"|---|---|",
		"| " + target + " | " + escaped + " |",
	}
	insertAt := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "# ") {
			insertAt = i
			break
		}
	}
	result := make([]string, 0, len(lines)+len(table)+2)
	if insertAt == -1 {
		// No H1: the table becomes the head of the file.
		result = append(result, table...)
		result = append(result, "")
		result = append(result, lines...)
		return strings.Join(result, "\n")
	}
	result = append(result, lines[:insertAt+1]...)
	result = append(result, "")
	result = append(result, table...)
	if insertAt+1 < len(lines) && strings.TrimSpace(lines[insertAt+1]) != "" {
		result = append(result, "")
	}
	result = append(result, lines[insertAt+1:]...)
	return strings.Join(result, "\n")
}

// setFinding sets the card's recorded finding (**Finding:** line). When no
// finding line exists yet it is inserted into the Result section (or appended
// as a fresh Result section when the card has none). The value is escaped via
// escapeCell — folded to a single line (newlines → "<br>") and pipe-safe —
// because the parser's findingRe reads exactly one line; extractFinding
// reverses the escape, so multi-line findings round-trip.
func setFinding(content, value string) string {
	folded := escapeCell(value)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(line, "**Finding:**") {
			lines[i] = "**Finding:** " + folded
			return strings.Join(lines, "\n")
		}
	}

	// No finding line: insert after the "## Result" heading if present.
	for i, line := range lines {
		if sectionHeadingLower(line) != "result" {
			continue
		}
		result := make([]string, 0, len(lines)+1)
		result = append(result, lines[:i+1]...)
		result = append(result, "", "**Finding:** "+folded)
		result = append(result, lines[i+1:]...)
		return strings.Join(result, "\n")
	}

	// No Result section at all: append one.
	return strings.TrimRight(content, "\n") + "\n\n## Result\n\n**Finding:** " + folded + "\n"
}

// rewriteCard applies the given updates to a card's Markdown content.
func rewriteCard(content, id string, upd HypothesisUpdate) string {
	c := content
	if upd.Title != nil {
		c = setCardHeading(c, id, *upd.Title)
	}
	if upd.Status != nil {
		c = setTableField(c, "Status", *upd.Status)
	}
	if upd.Timebox != nil {
		c = setTableField(c, "Timebox", *upd.Timebox)
	}
	if upd.Decision != nil {
		c = setTableField(c, "Decision", *upd.Decision)
	}
	if upd.Result != nil {
		c = setFinding(c, *upd.Result)
	}
	return c
}

// ---------------------------------------------------------------------------
// Graph content rewriting (pure)
// ---------------------------------------------------------------------------

// mermaidClass maps a canonical status to the Mermaid CSS class spelling
// (the diagram uses "in_progress", not "in-progress").
func mermaidClass(s HypothesisStatus) string {
	if s == StatusInProgress {
		return "in_progress"
	}
	return string(s)
}

// mermaidNodeLineRe matches a whole Mermaid node-definition line, capturing
// leading whitespace, the node token (H001 form), the quoted label, and the
// optional CSS class.
var mermaidNodeLineRe = regexp.MustCompile(`^(\s*)(H\d+)\s*\["([^"]*)"\]\s*(?:::+([A-Za-z_]+))?`)

// updateMermaidNode updates the label and/or CSS class of one node in the
// Mermaid diagram (matching by canonical ID). The existing label is unescaped
// (unescapeMermaidLabel) before its title is reused, and any written label is
// re-escaped (escapeMermaidLabel) — the pair keeps lines matching
// mermaidNodeLineRe (single-line, [^"]* label) while round-tripping titles
// that contain quotes, pipes, or newlines.
func updateMermaidNode(content, id string, title, status *string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		m := mermaidNodeLineRe.FindStringSubmatch(line)
		if len(m) < 3 || NormalizeID(m[2]) != id {
			continue
		}
		label := unescapeMermaidLabel(m[3])
		curTitle := label
		if idx := strings.Index(label, ":"); idx >= 0 {
			curTitle = strings.TrimSpace(label[idx+1:])
		}
		class := m[4]
		if title != nil {
			curTitle = *title
		}
		if status != nil {
			class = mermaidClass(NormalizeStatus(*status))
		}
		suffix := ""
		if class != "" {
			suffix = ":::" + class
		}
		lines[i] = m[1] + m[2] + `["` + id + ": " + escapeMermaidLabel(foldTitle(curTitle)) + `"]` + suffix
		return strings.Join(lines, "\n")
	}
	return content
}

// updateCatalogRow updates the title / status / decision columns of one row in
// the Hypothesis Catalog table (matching by canonical ID in the first column).
func updateCatalogRow(content, id string, title, status, decision *string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) < 3 || isSeparatorRow(cells) {
			continue
		}
		if NormalizeID(cells[0]) != id {
			continue
		}
		if title != nil && len(cells) > 1 {
			cells[1] = *title
		}
		if status != nil && len(cells) > 2 {
			cells[2] = string(NormalizeStatus(*status))
		}
		if decision != nil && len(cells) > 3 {
			cells[3] = *decision
		}
		lines[i] = joinCells(cells)
	}
	return strings.Join(lines, "\n")
}

// rewriteGraphForUpdate applies the graph-relevant parts of an update (title /
// status → Mermaid node + catalog; decision → catalog only).
func rewriteGraphForUpdate(content, id string, upd HypothesisUpdate) string {
	c := content
	if upd.Title != nil || upd.Status != nil {
		c = updateMermaidNode(c, id, upd.Title, upd.Status)
	}
	if upd.Title != nil || upd.Status != nil || upd.Decision != nil {
		c = updateCatalogRow(c, id, upd.Title, upd.Status, upd.Decision)
	}
	return c
}

// buildCatalogRow renders one catalog table row.
func buildCatalogRow(id, title string, status HypothesisStatus, decision string, parents []string) string {
	link := fmt.Sprintf("[%s](%s.md)", id, id)
	st := string(status)
	dec := decision
	if dec == "" {
		dec = "—"
	}
	par := strings.Join(parents, ", ")
	if par == "" {
		par = "—"
	}
	return joinCells([]string{link, foldTitle(title), st, dec, par})
}

// joinCells re-joins trimmed cells into a pipe-delimited Markdown row with
// surrounding spaces, matching the canonical catalog/table formatting. Every
// cell is escaped (escapeCell): cells here are logical values (as parsed by
// splitCells or freshly supplied), and the escape keeps a cell containing a
// pipe or newline from corrupting the row — splitCells reverses it on parse.
func joinCells(cells []string) string {
	escaped := make([]string, len(cells))
	for i, c := range cells {
		escaped[i] = escapeCell(c)
	}
	return "| " + strings.Join(escaped, " | ") + " |"
}

// addCatalogRow inserts a new catalog row: it replaces the "*No hypotheses
// yet*" placeholder row when present, otherwise appends after the last data row.
func addCatalogRow(content, id, title string, status HypothesisStatus, decision string, parents []string) string {
	newRow := buildCatalogRow(id, title, status, decision, parents)
	lines := strings.Split(content, "\n")

	// Replace the placeholder row when it exists.
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) == 0 || isSeparatorRow(cells) || NormalizeID(cells[0]) != "" {
			continue
		}
		if strings.Contains(strings.ToLower(cells[0]), "no hypotheses") {
			lines[i] = newRow
			return strings.Join(lines, "\n")
		}
	}

	// Otherwise append after the last data row.
	lastDataRow := -1
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) == 0 || isSeparatorRow(cells) || NormalizeID(cells[0]) == "" {
			continue
		}
		lastDataRow = i
	}
	if lastDataRow == -1 {
		// No data rows and no placeholder: a table that carries only the
		// header + separator rows. Insert directly after that separator so
		// the first catalog row lands inside the (empty) table instead of
		// being silently dropped.
		if sepIdx := catalogSeparatorRow(lines); sepIdx != -1 {
			result := make([]string, 0, len(lines)+1)
			result = append(result, lines[:sepIdx+1]...)
			result = append(result, newRow)
			result = append(result, lines[sepIdx+1:]...)
			return strings.Join(result, "\n")
		}
		return content
	}
	result := make([]string, 0, len(lines)+1)
	result = append(result, lines[:lastDataRow+1]...)
	result = append(result, newRow)
	result = append(result, lines[lastDataRow+1:]...)
	return strings.Join(result, "\n")
}

// catalogSeparatorRow locates the separator row of the Hypothesis Catalog
// table: the "|---|" line directly following the catalog header row (first
// cell "ID" plus a "Status" column). It returns the index of that separator
// line, or -1 when no such header + separator pair exists — in that case the
// caller has no table to insert into and leaves the content unchanged.
func catalogSeparatorRow(lines []string) int {
	for i := 0; i+1 < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "|") {
			continue
		}
		header := splitCells(lines[i])
		if !isCatalogHeaderRow(header) {
			continue
		}
		if sep := splitCells(lines[i+1]); isSeparatorRow(sep) {
			return i + 1
		}
	}
	return -1
}

// isCatalogHeaderRow reports whether the cells form the catalog table's header
// row: a leading "ID" column and a "Status" column (case-insensitive), as in
// "| ID | Hypothesis | Status | Decision | Parent(s) |".
func isCatalogHeaderRow(cells []string) bool {
	if len(cells) < 3 || !strings.EqualFold(cells[0], "id") {
		return false
	}
	for _, c := range cells[1:] {
		if strings.EqualFold(c, "status") {
			return true
		}
	}
	return false
}

// addMermaidNodeAndEdges inserts a new node (with its parent edges) into the
// Mermaid diagram. The node + edges are placed before the first non-comment
// edge line, or just before the closing fence when the diagram has no edges.
func addMermaidNodeAndEdges(content, id, title string, status HypothesisStatus, parents []string) string {
	lines := strings.Split(content, "\n")

	start, end := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "```mermaid" {
			start = i
		}
		if start != -1 && t == "```" && i > start {
			end = i
			break
		}
	}
	if start == -1 || end == -1 {
		return content
	}

	nodeToken := strings.ReplaceAll(id, "-", "")
	nodeLine := fmt.Sprintf("    %s[\"%s: %s\"]:::%s", nodeToken, id, escapeMermaidLabel(foldTitle(title)), mermaidClass(status))
	edgeLines := make([]string, 0, len(parents))
	for _, p := range parents {
		edgeLines = append(edgeLines, fmt.Sprintf("    %s --> %s", strings.ReplaceAll(p, "-", ""), nodeToken))
	}

	insertAt := end
	for i := start + 1; i < end; i++ {
		t := strings.TrimSpace(lines[i])
		if strings.HasPrefix(t, "%%") {
			continue
		}
		if mermaidEdgeRe.MatchString(t) {
			insertAt = i
			break
		}
	}

	block := make([]string, 0, len(edgeLines)+1)
	block = append(block, nodeLine)
	block = append(block, edgeLines...)

	result := make([]string, 0, len(lines)+len(block))
	result = append(result, lines[:insertAt]...)
	result = append(result, block...)
	result = append(result, lines[insertAt:]...)
	return strings.Join(result, "\n")
}

// buildCardContent renders a complete hypothesis card from scratch.
func buildCardContent(id, title, statement, criterion, timebox, created string, parents []string, status HypothesisStatus, decision string) string {
	if created == "" {
		created = time.Now().Format("2006-01-02")
	}
	timeboxVal := timebox
	if strings.TrimSpace(timeboxVal) == "" {
		timeboxVal = "—"
	}
	parentsVal := "—"
	if len(parents) > 0 {
		parentsVal = strings.Join(parents, ", ")
	}
	decisionVal := decision
	if decisionVal == "" {
		decisionVal = "—"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", id, foldTitle(title))
	b.WriteString("| Field | Value |\n")
	b.WriteString("|---|---|\n")
	fmt.Fprintf(&b, "| **Identifier** | %s |\n", id)
	fmt.Fprintf(&b, "| **Status** | %s |\n", string(status))
	fmt.Fprintf(&b, "| **Timebox** | %s |\n", escapeCell(timeboxVal))
	fmt.Fprintf(&b, "| **Parent(s)** | %s |\n", parentsVal)
	fmt.Fprintf(&b, "| **Created** | %s |\n", created)
	if status.IsTerminal() {
		// A card created directly in a terminal state completed today.
		fmt.Fprintf(&b, "| **Completed** | %s |\n", time.Now().Format("2006-01-02"))
	} else {
		b.WriteString("| **Completed** | — |\n")
	}
	fmt.Fprintf(&b, "| **Decision** | %s |\n", escapeCell(decisionVal))
	b.WriteString("\n## Statement\n\n")
	b.WriteString(statement)
	b.WriteString("\n\n## Verification Criterion\n\n")
	b.WriteString(criterion)
	b.WriteString("\n\n## Experiment Notes\n\n*Not yet started.*\n\n## Result\n\n*Filled upon completion.*\n\n**Finding:** —\n\n**Prototype / Proof:** —\n\n**Artifacts:**\n\n- *(links to repositories, branches, directories, logs, screenshots)*\n\n---\n\n[Back to Hypothesis Graph](graph.md) | [Back to Brief](../brief.md)\n")
	return b.String()
}

// normalizeParents canonicalizes, de-duplicates, and sorts a list of parent
// IDs. Non-identifier tokens are dropped.
func normalizeParents(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		id := NormalizeID(r)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// parseIDNum extracts the numeric part of a canonical ID ("H-007" → 7).
func parseIDNum(id string) int {
	m := hidRe.FindStringSubmatch(id)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// maxHypothesisNumber returns the highest H-NNN number seen across the card
// files and the graph.md (catalog + Mermaid) of a project.
func maxHypothesisNumber(projectDir string) int {
	hypDir := filepath.Join(projectDir, "hypotheses")
	maxNum := 0

	if paths, err := filepath.Glob(filepath.Join(hypDir, "H-*.md")); err == nil {
		for _, p := range paths {
			if n := parseIDNum(filepath.Base(p)); n > maxNum {
				maxNum = n
			}
		}
	}
	if content, ok := readFile(filepath.Join(hypDir, "graph.md")); ok {
		for _, c := range ParseCatalog(content) {
			if n := parseIDNum(c.id); n > maxNum {
				maxNum = n
			}
		}
		mnodes, _ := ParseMermaidGraph(content)
		for _, mn := range mnodes {
			if n := parseIDNum(mn.id); n > maxNum {
				maxNum = n
			}
		}
	}
	return maxNum
}

// ---------------------------------------------------------------------------
// Filesystem orchestration
// ---------------------------------------------------------------------------

// ActiveProjectDir returns the absolute path of the active R-NNN project
// directory under a research root, using the same selection rule as
// PickActiveProject (latest index entry, else highest-numbered directory).
// The directory comes straight from the parse (ResearchProject.Dir) — the
// very directory the parser read — so a brief/dir ID divergence (a typo in
// the brief's "# [R-NNN]" header, a renamed/copied project directory, or two
// directories normalizing to the same R-NNN) cannot redirect mutations to a
// different project than the one the panel renders. The legacy name-matching
// path remains only as a fallback for hand-constructed models whose Dir was
// never set. It handles both the canonical nested layout (R-NNN-short-name/)
// and the flat single-project layout. It returns an error when no project
// exists yet.
func ActiveProjectDir(researchRoot string) (string, error) {
	root, err := ParseResearchRoot(researchRoot)
	if err != nil {
		return "", err
	}
	active := PickActiveProject(root)
	if active == nil {
		return "", errors.New("no active research project (no R-NNN directory yet)")
	}

	// Primary path: the directory the parser actually read this project from.
	if active.Dir != "" {
		return active.Dir, nil
	}

	// Fallback for parsed-before-Dir models: resolve by directory-name ID.
	entries, err := os.ReadDir(researchRoot)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if NormalizeResearchID(e.Name()) == active.ID {
			return filepath.Join(researchRoot, e.Name()), nil
		}
	}
	// Flat single-project layout: the root itself is the project.
	if isFlatProjectRoot(researchRoot) {
		return researchRoot, nil
	}
	return "", fmt.Errorf("active research project directory for %q not found", active.ID)
}

// ProjectDir returns the absolute path of the research project whose parsed ID
// is rid under researchRoot. It resolves through the same parse the panel
// renders (brief-preferred IDs, ResearchProject.Dir), so the returned
// directory is the one the parser actually read. When rid is the ACTIVE
// research project (PickActiveProject — the single selection rule the
// orchestrator and the frontend panel share), the active project's Dir wins:
// duplicate directories can normalize to the same R-NNN (a copy under another
// name), and without this preference the first sorted match could silently
// diverge from the duplicate the panel renders and mutations would write to a
// different project's copy. The first-match loop remains only as the fallback
// for non-active IDs. It doubles as the ownership
// check for mutation callers that carry a caller-expected R-NNN: an id that
// does not live under this root — e.g. a research project belonging to
// ANOTHER project's research root — does not resolve here and is rejected
// before any file is touched.
func ProjectDir(researchRoot, rid string) (string, error) {
	rid = NormalizeResearchID(rid)
	if rid == "" {
		return "", errors.New("invalid research project id (want R-NNN)")
	}

	root, err := ParseResearchRoot(researchRoot)
	if err != nil {
		return "", err
	}
	// Prefer the active project's Dir — the same directory the panel
	// renders and ActiveProjectDir resolves — so a caller-mutated card in
	// the visible project never lands in a different duplicate of the
	// same R-NNN.
	if active := PickActiveProject(root); active != nil && active.ID == rid && active.Dir != "" {
		return active.Dir, nil
	}
	for _, p := range root.Projects {
		if p.ID == rid && p.Dir != "" {
			return p.Dir, nil
		}
	}
	return "", fmt.Errorf("research project %q not found under %q", rid, researchRoot)
}

// UpdateHypothesis applies an update to an existing hypothesis card and its
// graph entries. It is atomic-ish: validation (including the status-transition
// check) happens before any write, and each file is written via temp+rename.
// researchRoot is the containment root every written target is validated
// against (see writeFilesAtomic); projectDir is the active R-NNN directory
// (research.ActiveProjectDir).
//
// Side effects beyond the card + graph: a transition into a terminal status
// (confirmed / refuted / cancelled) stamps the card's Completed field with
// today's date when it is still empty, and real status changes plus recorded
// decisions are appended to log.md as status_change / decision entries — in
// the same atomic write as the card and graph.
func UpdateHypothesis(researchRoot, projectDir, id string, upd HypothesisUpdate) error {
	id = NormalizeID(id)
	if id == "" {
		return errors.New("invalid hypothesis id")
	}

	hypDir := filepath.Join(projectDir, "hypotheses")
	cardPath := filepath.Join(hypDir, id+".md")
	graphPath := filepath.Join(hypDir, "graph.md")

	cardContent, ok := readFile(cardPath)
	if !ok {
		return fmt.Errorf("hypothesis card %s not found", id)
	}
	graphContent, ok := readFile(graphPath)
	if !ok {
		return fmt.Errorf("hypothesis graph not found for %s", id)
	}

	// Parse the current card to recover the current status for transition
	// validation. A card that fails to parse cannot be safely mutated.
	node, err := ParseCard(cardContent)
	if err != nil {
		return fmt.Errorf("failed to parse card %s: %w", id, err)
	}

	// Normalize and validate the status before any mutation.
	var newStatus HypothesisStatus
	if upd.Status != nil {
		newStatus = NormalizeStatus(*upd.Status)
		if err := ValidateTransition(node.Status, newStatus); err != nil {
			return err
		}
		s := string(newStatus)
		upd.Status = &s
	}
	// Titles are single-line labels: fold once here so the card H1, catalog
	// cell, and Mermaid label all render the identical folded spelling.
	if upd.Title != nil {
		t := foldTitle(*upd.Title)
		upd.Title = &t
	}

	newCard := rewriteCard(cardContent, id, upd)
	newGraph := rewriteGraphForUpdate(graphContent, id, upd)

	// A real transition into a terminal status completes the hypothesis today
	// (unless a completion date is already recorded — never overwrite it).
	statusChanged := upd.Status != nil && newStatus != node.Status
	if statusChanged && newStatus.IsTerminal() && dashToEmpty(extractField(cardContent, "Completed")) == "" {
		newCard = setTableField(newCard, "Completed", time.Now().Format("2006-01-02"))
	}

	files := map[string][]byte{
		cardPath:  []byte(newCard),
		graphPath: []byte(newGraph),
	}

	// Research log: one status_change entry per real transition, one decision
	// entry per recorded decision — appended to log.md within the same atomic
	// write, so card, graph, and log never disagree.
	decisionLogged := upd.Decision != nil && strings.TrimSpace(*upd.Decision) != ""
	if statusChanged || decisionLogged {
		logPath := filepath.Join(projectDir, "log.md")
		logContent, _ := readFile(logPath)
		now := time.Now().UTC().Format(time.RFC3339)
		if statusChanged {
			msg := fmt.Sprintf("Moved %s from %s to %s.", id, node.Status, newStatus)
			if node.Status == "" {
				msg = fmt.Sprintf("Set %s to %s.", id, newStatus)
			}
			if logContent, err = appendLogEntryContent(logContent, LogKindStatusChange, id, now, msg); err != nil {
				return fmt.Errorf("failed to stage research log entry: %w", err)
			}
		}
		if decisionLogged {
			msg := fmt.Sprintf("Decision: %s.", strings.TrimSpace(*upd.Decision))
			if logContent, err = appendLogEntryContent(logContent, LogKindDecision, id, now, msg); err != nil {
				return fmt.Errorf("failed to stage research log entry: %w", err)
			}
		}
		files[logPath] = []byte(logContent)
	}

	if err := writeFilesAtomic(researchRoot, files); err != nil {
		return fmt.Errorf("failed to write hypothesis update: %w", err)
	}
	return nil
}

// CreateHypothesis creates a new hypothesis card (assigning the next H-NNN id)
// and updates the graph (Mermaid node + edges + catalog row). It returns the
// new canonical ID. researchRoot is the containment root every written target
// is validated against (see writeFilesAtomic); projectDir is the active R-NNN
// directory (research.ActiveProjectDir).
func CreateHypothesis(researchRoot, projectDir string, nh NewHypothesis) (string, error) {
	title := strings.TrimSpace(nh.Title)
	if title == "" {
		return "", errors.New("new hypothesis requires a title")
	}
	// Titles are single-line labels everywhere they are written (card H1,
	// catalog cell, Mermaid label); fold once up front.
	title = foldTitle(title)

	status := nh.Status
	if status == "" {
		status = StatusOpen
	}
	status = NormalizeStatus(string(status))
	if !isKnownStatus(status) {
		return "", fmt.Errorf("unknown hypothesis status %q", string(status))
	}

	parents := normalizeParents(nh.Parents)

	hypDir := filepath.Join(projectDir, "hypotheses")
	if err := os.MkdirAll(hypDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create hypotheses dir: %w", err)
	}

	// Validate parents reference hypotheses that already exist.
	if len(parents) > 0 {
		known := make(map[string]struct{})
		if content, ok := readFile(filepath.Join(hypDir, "graph.md")); ok {
			for _, c := range ParseCatalog(content) {
				known[c.id] = struct{}{}
			}
			mnodes, _ := ParseMermaidGraph(content)
			for _, mn := range mnodes {
				known[mn.id] = struct{}{}
			}
		}
		for _, card := range loadCards(hypDir) {
			known[card.ID] = struct{}{}
		}
		for _, p := range parents {
			if _, ok := known[p]; !ok {
				return "", fmt.Errorf("parent hypothesis %s not found", p)
			}
		}
	}

	id := fmt.Sprintf("H-%03d", maxHypothesisNumber(projectDir)+1)

	cardContent := buildCardContent(id, title, nh.Statement, nh.VerificationCriterion, nh.Timebox, nh.Created, parents, status, nh.Decision)

	graphContent, _ := readFile(filepath.Join(hypDir, "graph.md"))
	graphContent = ensureGraphSkeleton(graphContent)
	newGraph := addMermaidNodeAndEdges(graphContent, id, title, status, parents)
	newGraph = addCatalogRow(newGraph, id, title, status, nh.Decision, parents)

	if err := writeFilesAtomic(researchRoot, map[string][]byte{
		filepath.Join(hypDir, id+".md"):   []byte(cardContent),
		filepath.Join(hypDir, "graph.md"): []byte(newGraph),
	}); err != nil {
		return "", fmt.Errorf("failed to write new hypothesis: %w", err)
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// Research log (log.md) writing
// ---------------------------------------------------------------------------

// AppendLogEntry appends one entry (experiment / decision / status_change /
// note) to the research log at <projectDir>/log.md, creating the file with the
// standard "# Research Log" preamble when it does not exist. Existing entries
// are never rewritten — the log is append-only. The write goes through
// writeFilesAtomic (temp file + rename, containment-checked against
// researchRoot), like every other research mutation. An empty CreatedAt
// defaults to the current UTC time in RFC 3339 (the YYYY-MM-DDTHH:MM:SSZ form
// the skills prescribe); an unknown Kind or a non-ISO-8601 timestamp is an
// error and leaves the log untouched.
func AppendLogEntry(researchRoot, projectDir string, entry ResearchLogEntry) error {
	logPath := filepath.Join(projectDir, "log.md")
	current, _ := readFile(logPath)
	next, err := appendLogEntryContent(current, entry.Kind, entry.HypothesisID, entry.CreatedAt, entry.Message)
	if err != nil {
		return err
	}
	if err := writeFilesAtomic(researchRoot, map[string][]byte{logPath: []byte(next)}); err != nil {
		return fmt.Errorf("failed to append research log entry: %w", err)
	}
	return nil
}

// appendLogEntryContent is the pure half of AppendLogEntry: it renders one
// entry onto a log.md's current content and returns the new content. The
// heading follows ParseLog's contract ("## <kind> <created_at> [<H-NNN>]");
// message lines that would themselves parse as entry headings ("## ...") are
// demoted to "### ..." subheadings so the entry's body is never truncated.
func appendLogEntryContent(content string, kind LogKind, hypothesisID, createdAt, message string) (string, error) {
	k, ok := normalizeLogKind(string(kind))
	if !ok {
		return "", fmt.Errorf("unknown research log kind %q", string(kind))
	}
	createdAt = strings.TrimSpace(createdAt)
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}
	if !looksLikeTimestamp(createdAt) {
		return "", fmt.Errorf("research log timestamp %q carries no ISO 8601 date (want a YYYY-MM-DD prefix)", createdAt)
	}

	heading := "## " + string(k) + " " + createdAt
	if hypID := NormalizeID(hypothesisID); hypID != "" {
		heading += " " + hypID
	}

	var b strings.Builder
	if base := strings.TrimRight(content, "\n"); base == "" {
		b.WriteString("# Research Log\n\n")
	} else {
		b.WriteString(base)
		b.WriteString("\n\n")
	}
	b.WriteString(heading)
	b.WriteString("\n\n")
	b.WriteString(demoteLevelTwoHeadings(strings.TrimRight(message, "\n")))
	b.WriteString("\n")
	return b.String(), nil
}

// demoteLevelTwoHeadings rewrites message lines starting with "## " as "### "
// subheadings. ParseLog reserves level-2 headings for entry headings: an
// undemoted "## ..." line inside a message would terminate the entry and
// silently drop the remainder of the body. Level-3 headings parse as message
// text, and demoting is stable across write → parse → write.
func demoteLevelTwoHeadings(message string) string {
	if !strings.Contains(message, "## ") {
		return message
	}
	lines := strings.Split(message, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "## ") {
			lines[i] = "### " + strings.TrimSpace(strings.TrimPrefix(t, "## "))
		}
	}
	return strings.Join(lines, "\n")
}

// ensureGraphSkeleton guarantees graphContent carries both structures the
// mutation helpers operate on — the Mermaid diagram fence and a Hypothesis
// Catalog table. Content that already has both is returned unchanged. When a
// structure is missing, ONLY that section is inserted (the Mermaid section
// before the catalog heading, the catalog section after the diagram fence),
// so free-form notes, headings, and any other surviving data in a malformed
// or partially written graph.md are preserved rather than wiped by a fresh
// skeleton. A blank file gets the full skeleton (nothing to preserve).
func ensureGraphSkeleton(graphContent string) string {
	hasMermaid := strings.Contains(graphContent, "```mermaid")
	hasCatalog := strings.Contains(graphContent, "Hypothesis Catalog")
	switch {
	case hasMermaid && hasCatalog:
		return graphContent
	case !hasMermaid && !hasCatalog:
		if strings.TrimSpace(graphContent) == "" {
			return graphSkeleton()
		}
		// Neither structure, but the file carries content: insert the diagram
		// first, then hang the catalog off the freshly inserted fence.
		return insertCatalogSection(insertMermaidSection(graphContent))
	case !hasMermaid:
		return insertMermaidSection(graphContent)
	default:
		return insertCatalogSection(graphContent)
	}
}

// mermaidSection renders the standalone Diagram section (heading + fenced
// Mermaid block with the status classDefs and no nodes) inserted by
// ensureGraphSkeleton when a graph.md lacks a diagram.
func mermaidSection() string {
	return "## Diagram\n\n" + "```mermaid\n" + `graph TD
    classDef confirmed fill:#4CAF50,color:#fff
    classDef refuted fill:#F44336,color:#fff
    classDef in_progress fill:#FF9800,color:#fff
    classDef open fill:#2196F3,color:#fff
    classDef cancelled fill:#9E9E9E,color:#fff

    %% Add hypothesis nodes here as they are created.
` + "```\n"
}

// catalogSection renders the standalone Hypothesis Catalog section (heading +
// header row + separator + placeholder) inserted by ensureGraphSkeleton when a
// graph.md lacks a catalog table.
func catalogSection() string {
	return "## Hypothesis Catalog\n\n| ID | Hypothesis | Status | Decision | Parent(s) |\n|---|---|---|---|---|\n| *No hypotheses yet.* | | | | |\n"
}

// insertMermaidSection inserts the Diagram section before the Hypothesis
// Catalog heading when present (the diagram belongs above the catalog), else
// appends it at the end with a blank-line separation.
func insertMermaidSection(content string) string {
	section := mermaidSection()
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if !strings.Contains(sectionHeadingLower(line), "hypothesis catalog") {
			continue
		}
		result := make([]string, 0, len(lines)+strings.Count(section, "\n")+2)
		result = append(result, lines[:i]...)
		result = append(result, "")
		result = append(result, strings.Split(strings.TrimRight(section, "\n"), "\n")...)
		result = append(result, "")
		result = append(result, lines[i:]...)
		return strings.Join(result, "\n")
	}
	return strings.TrimRight(content, "\n") + "\n\n" + section
}

// insertCatalogSection inserts the Hypothesis Catalog section directly after
// the Mermaid block's closing fence when a diagram is present (the catalog
// belongs below it), else appends it at the end with a blank-line separation.
func insertCatalogSection(content string) string {
	section := catalogSection()
	lines := strings.Split(content, "\n")
	inFence := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if t == "```mermaid" {
			inFence = true
			continue
		}
		if inFence && t == "```" {
			result := make([]string, 0, len(lines)+strings.Count(section, "\n")+2)
			result = append(result, lines[:i+1]...)
			result = append(result, "")
			result = append(result, strings.Split(strings.TrimRight(section, "\n"), "\n")...)
			result = append(result, lines[i+1:]...)
			return strings.Join(result, "\n")
		}
	}
	return strings.TrimRight(content, "\n") + "\n\n" + section
}

// graphSkeleton renders a minimal, well-formed graph.md with an empty Mermaid
// diagram (class definitions only, no nodes) and an empty Hypothesis Catalog
// table, matching the seeded research-init graph template.
func graphSkeleton() string {
	return `# Hypothesis Graph

## Diagram

` + "```mermaid\n" + `graph TD
    classDef confirmed fill:#4CAF50,color:#fff
    classDef refuted fill:#F44336,color:#fff
    classDef in_progress fill:#FF9800,color:#fff
    classDef open fill:#2196F3,color:#fff
    classDef cancelled fill:#9E9E9E,color:#fff

    %% Add hypothesis nodes here as they are created.
` + "```\n" + `
## Hypothesis Catalog

| ID | Hypothesis | Status | Decision | Parent(s) |
|---|---|---|---|---|
| *No hypotheses yet.* | | | | |

---
`
}

// writeFilesAtomic writes a group of files "atomic-ish": every file is first
// staged as a temp file in its own directory (0600 → chmod 0644), and only
// after all files are fully staged are they renamed into place. Staging before
// the first rename means a staging failure (disk full, permissions) leaves
// every target untouched; only the rename phase — near-instant and per-file
// atomic — can produce a partial state, which no two-file scheme can avoid
// without a filesystem transaction.
//
// researchRoot is the containment root (SECURITY.md: RESEARCH artifacts stay
// strictly inside the project workspace). Every target is symlink-resolved
// and checked against the resolved root BEFORE any staging: a symlinked
// intermediate directory under the root (e.g.
// .research/R-001-x/hypotheses -> /Users/x/Documents) would otherwise be
// followed by os.CreateTemp/os.Rename and redirect the write outside the
// workspace — the same escape the write_file tool rejects with
// ReasonCodeSymlinkEscape. Staging and renaming then use the RESOLVED target,
// so the location that was validated is exactly the location written and a
// directory swap between check and write cannot re-introduce a different
// symlink chain.
func writeFilesAtomic(researchRoot string, files map[string][]byte) error {
	type stagedFile struct {
		target string
		tmp    string
	}
	staged := make([]stagedFile, 0, len(files))
	defer func() {
		for _, s := range staged {
			_ = os.Remove(s.tmp)
		}
	}()

	// Deterministic staging order (sorted keys) so card-vs-graph write order is
	// stable regardless of map iteration.
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// Resolve and containment-check every target before touching the disk:
	// a rejection here leaves all files untouched.
	resolved := make(map[string]string, len(paths))
	for _, p := range paths {
		rp, err := resolveTargetWithinRoot(researchRoot, p)
		if err != nil {
			return err
		}
		resolved[p] = rp
	}

	for _, p := range paths {
		rp := resolved[p]
		tmp, err := os.CreateTemp(filepath.Dir(rp), ".hyp-*.tmp")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		if _, err := tmp.Write(files[p]); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Chmod(tmpName, 0o644); err != nil {
			return err
		}
		staged = append(staged, stagedFile{target: rp, tmp: tmpName})
	}

	for _, s := range staged {
		if err := os.Rename(s.tmp, s.target); err != nil {
			return err
		}
	}
	return nil
}

// resolveTargetWithinRoot symlink-resolves the containment root and the
// target (EvalSymlinks on the root and on the target's directory — the file
// itself may not exist yet) and re-checks pathutil.IsWithinPath immediately
// before the caller stages (CreateTemp) and renames. It returns the resolved
// target path — the on-disk location the caller may safely write. It fails
// closed: an unresolvable root/directory or any containment mismatch is an
// error, so a symlinked intermediate directory can never stage a temp file
// outside the workspace.
func resolveTargetWithinRoot(researchRoot, target string) (string, error) {
	if researchRoot == "" {
		return "", errors.New("research write rejected: empty research root")
	}
	resolvedRoot, err := filepath.EvalSymlinks(researchRoot)
	if err != nil {
		return "", fmt.Errorf("research root %q not resolvable: %w", researchRoot, err)
	}
	dir, base := filepath.Dir(target), filepath.Base(target)
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("research write target directory %q not resolvable: %w", dir, err)
	}
	resolvedTarget := filepath.Join(resolvedDir, base)

	contained, withinErr := pathutil.IsWithinPath(resolvedRoot, resolvedTarget)
	if withinErr != nil {
		return "", fmt.Errorf("research write containment check failed for %q: %w", target, withinErr)
	}
	if !contained {
		return "", fmt.Errorf("research write target %q resolves to %q, outside the research root %q (symlinked intermediate directory?)", target, resolvedTarget, researchRoot)
	}
	return resolvedTarget, nil
}
