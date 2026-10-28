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

	"github.com/v0lka/sp4rk/safeio"
)

// This file implements the content-parsing layer of the research package:
// pure parsers over already-read file contents (unit-testable with plain
// string fixtures), BuildGraph merging them into one HypothesisGraph, and
// the thin orchestrators ParseProject/ParseResearchRoot — the only functions
// touching the filesystem, and they only ever read.

// -- ID / list helpers --

// hidRe matches a hypothesis identifier in either spelling: "H001" (Mermaid
// node ID) or "H-001" (card / catalog). Case-insensitive.
var hidRe = regexp.MustCompile(`(?i)H-?(\d+)`)

// ridRe matches a research identifier, e.g. "R-001".
var ridRe = regexp.MustCompile(`(?i)R-?(\d+)`)

// NormalizeID canonicalizes a hypothesis identifier to its zero-padded,
// hyphenated, upper-case form ("H-001"), accepting "H001"/"H-001"/"H1"
// (case-insensitive) and returning "" for input with no identifier. Padding
// to three digits keeps a hand-written spelling and the writer-generated one
// of the same number from coexisting as distinct graph nodes: "H-1" and
// "H-001" are one hypothesis.
func NormalizeID(raw string) string {
	m := hidRe.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return "H-" + m[1]
	}
	return fmt.Sprintf("H-%03d", n)
}

// NormalizeResearchID canonicalizes a research identifier to its hyphenated,
// upper-case form ("R-001") without zero-padding: the id is taken verbatim
// from the brief and compared numerically (compareResearchIDs), so "R-2" and
// "R-002" stay distinct spellings of distinct directories. Accepts
// "R001"/"R-001", case-insensitive; returns "" for input with no identifier.
func NormalizeResearchID(raw string) string {
	m := ridRe.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	return "R-" + m[1]
}

// researchIDNumber extracts the numeric part of a research ID ("R-010" → 10);
// ok=false when the ID carries no R-NNN number.
func researchIDNumber(id string) (int, bool) {
	m := ridRe.FindStringSubmatch(id)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// compareResearchIDs orders research IDs numerically (R-2 before R-10), with
// a lexicographic tie-break for equal numbers (duplicate R-NNN directories
// after a copy) and for IDs without a numeric part. The "highest-numbered"
// consumers (PickActiveProject fallback, ActiveProjectDir) rely on this:
// unpadded directory names must not flip the selection.
func compareResearchIDs(a, b string) int {
	na, aok := researchIDNumber(a)
	nb, bok := researchIDNumber(b)
	switch {
	case aok && bok && na != nb:
		return na - nb
	case aok && !bok:
		return -1 // numbered sorts before non-numbered
	case !aok && bok:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// hypothesisIDNumber extracts the numeric part of a hypothesis ID ("H-010" →
// 10); ok=false when the ID carries no H-NNN number.
func hypothesisIDNumber(id string) (int, bool) {
	m := hidRe.FindStringSubmatch(id)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// compareHypothesisIDs orders hypothesis IDs numerically (H-2 before H-10),
// lexicographic tie-break — mirroring compareResearchIDs. "Leading entry"
// consumers (RecommendNextStep's first ActiveFront ID) and graph display
// order rely on it: H-999 vs H-1000 (and any unpadded spelling surviving in
// a hand-edited file) must not flip the selection.
func compareHypothesisIDs(a, b string) int {
	na, aok := hypothesisIDNumber(a)
	nb, bok := hypothesisIDNumber(b)
	switch {
	case aok && bok && na != nb:
		return na - nb
	case aok && !bok:
		return -1 // numbered sorts before non-numbered
	case !aok && bok:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// parseParentList extracts canonical parent IDs from a free-form string
// ("H-001, H-002"), sorted and de-duplicated; non-identifier tokens are
// dropped.
func parseParentList(raw string) []string {
	matches := hidRe.FindAllStringSubmatch(raw, -1)
	if matches == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	parents := make([]string, 0, len(matches))
	for _, m := range matches {
		id := NormalizeID(m[0])
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		parents = append(parents, id)
	}
	sort.Slice(parents, func(i, j int) bool { return compareHypothesisIDs(parents[i], parents[j]) < 0 })
	return parents
}

// splitCells splits a Markdown table row into its trimmed cell values.
// Escaped pipes ("\|" — the writer's escapeCell output) are not split on, and
// each cell is unescaped back to its logical value, so writer → parser
// round-trips preserve pipes and multi-line values exactly.
func splitCells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	// Trim the trailing pipe only when it is unescaped: a value ending in an
	// escaped pipe ("... a \|") keeps its pipe.
	if strings.HasSuffix(row, "|") && !strings.HasSuffix(row, `\|`) {
		row = row[:len(row)-1]
	}
	parts := splitUnescapedPipes(row)
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(unescapeCell(p)))
	}
	return cells
}

// splitUnescapedPipes splits on "|" not preceded by a backslash; the
// unescape step later reduces the kept "\|" pair to a literal pipe.
func splitUnescapedPipes(s string) []string {
	parts := make([]string, 0, strings.Count(s, "|")+1)
	start := 0
	prev := rune(0)
	for i, r := range s {
		if r == '|' && prev != '\\' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
		prev = r
	}
	return append(parts, s[start:])
}

// restoreLiteralBR reverses escapeLiteralBR: the entity spelling shielding a
// literal "<br>" is restored to the marker text, and the doubly-escaped
// spelling to "&lt;br&gt;". It runs AFTER the "<br>" → "\n" unfold, and the
// single-shield replacement runs FIRST: "&amp;lt;br&gt;" does not contain
// "&lt;br&gt;", so the other order would let the first replacement's output
// be consumed by the second, over-restoring to "<br>".
func restoreLiteralBR(v string) string {
	if !strings.Contains(v, "&lt;br&gt;") && !strings.Contains(v, "&amp;lt;br&gt;") {
		return v
	}
	v = strings.ReplaceAll(v, "&lt;br&gt;", "<br>")
	return strings.ReplaceAll(v, "&amp;lt;br&gt;", "&lt;br&gt;")
}

// unfoldBR converts the writer's "<br>" fold marker back into a real newline
// and restores a shielded literal "<br>" (restoreLiteralBR), recovering the
// logical multi-line value written into any single-line context.
func unfoldBR(v string) string {
	if !strings.Contains(v, "<br>") && !strings.Contains(v, "&lt;br&gt;") && !strings.Contains(v, "&amp;lt;br&gt;") {
		return v
	}
	v = strings.ReplaceAll(v, "<br>", "\n")
	return restoreLiteralBR(v)
}

// unescapeCell reverses the writer's escapeCell — "\|" → pipe, "<br>" →
// newline, the shielded spelling restored — so an escaped value parses back
// to its original form.
func unescapeCell(v string) string {
	if !strings.Contains(v, `\|`) && !strings.Contains(v, "<br>") && !strings.Contains(v, "&lt;br&gt;") && !strings.Contains(v, "&amp;lt;br&gt;") {
		return v
	}
	return unfoldBR(strings.ReplaceAll(v, `\|`, "|"))
}

// isSeparatorRow reports whether a table row is a delimiter row like
// "|---|---|" (optionally with colons for alignment).
func isSeparatorRow(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if c == "" {
			return false
		}
		for _, r := range c {
			if r != '-' && r != ':' && r != ' ' {
				return false
			}
		}
	}
	return true
}

// dashToEmpty converts a placeholder cell value ("—" or "None" etc.) to the
// empty string, so downstream logic can treat "no value" uniformly.
func dashToEmpty(v string) string {
	v = strings.TrimSpace(v)
	switch v {
	case "", "—", "-", "–", "None", "none", "N/A", "n/a":
		return ""
	}
	return v
}

// -- Section / field extraction (shared by brief and card parsers) --

// sectionHeadingLower returns the lower-cased text of a level-2 heading line,
// or "" otherwise. Exactly two leading hashes: deeper headings ("### ...")
// never act as section boundaries.
func sectionHeadingLower(line string) string {
	trimmed := strings.TrimSpace(line)
	const marker = "## "
	if !strings.HasPrefix(trimmed, marker) {
		return ""
	}
	// Exclude deeper headings ("### X" does not start with "## ").
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, marker)))
}

// extractSection returns the trimmed body of the first section whose heading
// contains the name (case-insensitive), or "" if absent: a "## Name" block
// ending at the next level-2 heading, "---", or end of input. A line scan,
// not a regex — a regex terminator would consume the next heading's marker
// and skip every other section.
func extractSection(content, heading string) string {
	heading = strings.ToLower(strings.TrimSpace(heading))
	var (
		capturing bool
		body      strings.Builder
	)
	for _, line := range strings.Split(content, "\n") {
		if h := sectionHeadingLower(line); h != "" {
			if capturing {
				break // next section begins
			}
			if strings.Contains(h, heading) {
				capturing = true
			}
			continue
		}
		if capturing && strings.TrimSpace(line) == "---" {
			break // horizontal rule terminates the section
		}
		if capturing {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	return strings.TrimSpace(body.String())
}

// extractField returns the value cell of the "| **Field** | value |" row for
// fieldName ("" if absent), unescaping through splitCells so writer-escaped
// values come back in logical form.
func extractField(content, fieldName string) string {
	target := "**" + fieldName + "**"
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) < 2 || cells[0] != target {
			continue
		}
		return cells[1]
	}
	return ""
}

// -- Brief --

// Brief holds the structured fields parsed from R-NNN/brief.md; absent fields
// stay empty (the parser never fails on partial briefs).
type Brief struct {
	ID                string `json:"id"`
	Title             string `json:"title"`
	Status            string `json:"status,omitempty"`
	ProblemDomain     string `json:"problem_domain,omitempty"`
	Quarter           string `json:"quarter,omitempty"`
	Researchers       string `json:"researchers,omitempty"`
	RelatedResearches string `json:"related_researches,omitempty"`
	ResearchQuestion  string `json:"research_question,omitempty"`
	SuccessCriteria   string `json:"success_criteria,omitempty"`
}

// briefTitleRe matches the brief H1 "# [R-NNN] Title".
var briefTitleRe = regexp.MustCompile(`(?m)^#\s+\[(R-?\d+)\]\s*(.*)$`)

// ParseBrief parses brief Markdown into a Brief, best-effort. The ID comes
// from the H1 bracket; the rest still parses when the H1 is absent.
func ParseBrief(content string) Brief {
	b := Brief{}
	if m := briefTitleRe.FindStringSubmatch(content); m != nil {
		b.ID = NormalizeResearchID(m[1])
		b.Title = strings.TrimSpace(m[2])
	}
	// ID fallback: descriptive H1s ("# Research Brief: ...") carry the ID only
	// in the Identifier table field.
	if b.ID == "" {
		b.ID = NormalizeResearchID(extractField(content, "Identifier"))
	}
	// Title fallback: strip common prefixes from a descriptive H1 so the panel
	// shows a meaningful label.
	if b.Title == "" {
		b.Title = briefTitleFromH1(content)
	}
	b.Status = extractField(content, "Status")
	b.ProblemDomain = extractField(content, "Problem domain")
	b.Quarter = extractField(content, "Quarter")
	b.Researchers = extractField(content, "Researcher(s)")
	b.RelatedResearches = extractField(content, "Related researches")
	b.ResearchQuestion = extractSection(content, "Research Question")
	b.SuccessCriteria = extractSection(content, "Success Criteria")
	return b
}

// briefH1Re matches a level-1 Markdown heading and captures its text.
var briefH1Re = regexp.MustCompile(`(?m)^#\s+(.+)$`)

// briefTitleFromH1 extracts a human-readable title from a descriptive brief H1
// such as "# Research Brief: Aurora — ..." by stripping well-known prefixes
// ("Research Brief:", "Brief:", "Research:", case-insensitive). When no prefix
// is recognized the raw H1 text is returned. It returns "" when there is no H1.
// This is a fallback used only when the canonical "# [R-NNN] Title" form is
// absent, so conformant briefs are unaffected.
func briefTitleFromH1(content string) string {
	m := briefH1Re.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	title := strings.TrimSpace(m[1])
	lower := strings.ToLower(title)
	for _, prefix := range []string{"research brief:", "brief:", "research:"} {
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(title[len(prefix):])
		}
	}
	return title
}

// ──────────────────────────────────────────────────────────────────────────
// Hypothesis card
// ──────────────────────────────────────────────────────────────────────────

// cardHeadingRe matches a card H1 like "# H-001: Short title".
var cardHeadingRe = regexp.MustCompile(`(?m)^#\s+(H-?\d+)(?::\s*(.+))?$`)

// findingRe captures the "**Finding:** value" line in a card Result section.
var findingRe = regexp.MustCompile(`(?m)\*\*Finding:\*\*\s*(.+)$`)

// ParseCard parses a single hypothesis card (H-NNN.md) into a HypothesisNode.
// It returns an error only when the card has no parseable identifier — the one
// piece of information without which a node is useless. All other fields are
// optional and left empty when absent (partial card).
func ParseCard(content string) (HypothesisNode, error) {
	node := HypothesisNode{}

	m := cardHeadingRe.FindStringSubmatch(content)
	if m == nil {
		// Fall back to the Identifier field if the heading is non-standard.
		if id := NormalizeID(extractField(content, "Identifier")); id != "" {
			node.ID = id
		} else {
			return node, errors.New("hypothesis card has no parseable identifier")
		}
	} else {
		node.ID = NormalizeID(m[1])
		if m[2] != "" {
			node.Title = strings.TrimSpace(m[2])
		}
	}

	if v := dashToEmpty(extractField(content, "Status")); v != "" {
		node.Status = NormalizeStatus(v)
	}
	node.Timebox = dashToEmpty(extractField(content, "Timebox"))
	node.Completed = dashToEmpty(extractField(content, "Completed"))
	if parents := parseParentList(extractField(content, "Parent(s)")); len(parents) > 0 {
		node.Parents = parents
	}
	node.Result = extractFinding(content)
	// Long-form card sections, parsed verbatim (the writer's setSection is
	// their round-trip counterpart). Absent sections — and sections whose
	// body is only template placeholder text — read as "".
	node.Statement = sectionBody(content, "Statement")
	node.VerificationCriterion = sectionBody(content, "Verification Criterion")
	node.ExperimentNotes = sectionBody(content, "Experiment Notes")
	node.Decision = dashToEmpty(extractField(content, "Decision"))
	return node, nil
}

// italicPlaceholderRe matches a single-line run of italics — the shape of the
// fresh-card template placeholders ("*Not yet started.*",
// "*Filled upon completion.*").
var italicPlaceholderRe = regexp.MustCompile(`^\*[^*]*\*$`)

// sectionBody extracts a long-form card section ("## Name") with the same
// placeholder normalization extractFinding applies to Result: a body that
// consists only of empty and italic-placeholder lines reads as "" (the
// fresh-card template), so template boilerplate never surfaces as editable
// content. A body carrying any real line is preserved verbatim.
func sectionBody(content, heading string) string {
	body := extractSection(content, heading)
	if body == "" {
		return ""
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || italicPlaceholderRe.MatchString(line) {
			continue
		}
		return body
	}
	return ""
}

// extractFinding pulls the recorded finding out of a card's Result section.
// It prefers an explicit "**Finding:** value" line; failing that it returns
// the trimmed section body with placeholder/italic lines removed. Returns ""
// when there is no recorded result (template placeholder or absent section).
// The explicit line is single-line by contract (findingRe), so the writer's
// escapes (folded newlines "<br>", escaped pipes "\|") are reversed here.
func extractFinding(content string) string {
	body := extractSection(content, "Result")
	if body == "" {
		return ""
	}
	if m := findingRe.FindStringSubmatch(body); m != nil {
		return dashToEmpty(unescapeCell(m[1]))
	}
	// Free-form result body: drop italic placeholders and empty lines.
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "*") {
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, "\n")
}

// -- Hypothesis graph sources: Mermaid diagram + catalog table --

// mermaidBlockRe captures the body of a ```mermaid fenced code block.
var mermaidBlockRe = regexp.MustCompile("(?ms)```mermaid\\s*\\n(.*?)```")

// mermaidNodeRe matches a Mermaid node definition H001["H-001: Title"]:::class
// (groups: node ID, quoted label, optional CSS class; the greedy ::: capture
// takes the three-colon Mermaid syntax).
var mermaidNodeRe = regexp.MustCompile(`\b(H\d+)\s*\["([^"]*)"\]\s*(?:::+([A-Za-z_]+))?`)

// mermaidEdgeRe matches a Mermaid edge: H001 --> H002.
var mermaidEdgeRe = regexp.MustCompile(`\b(H\d+)\s*-->\s*(H\d+)\b`)

// unescapeMermaidLabel reverses the writer's escapeMermaidLabel: "#quot;" →
// quote, "#124;" → pipe, "<br>" → newline, the shielded spelling restored.
// Real newlines, pipes, quotes, and literal "<br>" text survive the escape →
// unescape cycle exactly; a literal "#quot;"/"#124;" that pre-dates the write
// drifts once to its character and is stable from then on.
func unescapeMermaidLabel(v string) string {
	if !strings.Contains(v, "#quot;") && !strings.Contains(v, "#124;") && !strings.Contains(v, "<br>") && !strings.Contains(v, "&lt;br&gt;") && !strings.Contains(v, "&amp;lt;br&gt;") {
		return v
	}
	v = strings.ReplaceAll(v, "#quot;", `"`)
	v = strings.ReplaceAll(v, "#124;", "|")
	v = strings.ReplaceAll(v, "<br>", "\n")
	return restoreLiteralBR(v)
}

// mermaidNode is an intermediate parse of one Mermaid node line.
type mermaidNode struct {
	id     string // canonical "H-001"
	title  string
	status HypothesisStatus
}

// ParseMermaidGraph extracts nodes and edges from graph.md's Mermaid diagram
// (canonical IDs). A missing or empty block yields empty results — an empty
// graph is a valid partial state, never an error.
func ParseMermaidGraph(content string) ([]mermaidNode, []HypothesisEdge) {
	block := ""
	if m := mermaidBlockRe.FindStringSubmatch(content); m != nil {
		block = m[1]
	}
	if block == "" {
		return nil, nil
	}

	// Strip Mermaid "%%" comments so commented-out template nodes/edges are
	// not parsed as real structure.
	var kept strings.Builder
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "%%") {
			continue
		}
		kept.WriteString(line)
		kept.WriteByte('\n')
	}
	block = kept.String()

	// First pass: node definitions → labels and status classes.
	nodes := make(map[string]mermaidNode)
	nodeOrder := make([]string, 0)
	for _, m := range mermaidNodeRe.FindAllStringSubmatch(block, -1) {
		id := NormalizeID(m[1])
		if id == "" {
			continue
		}
		// Undo the writer's label escaping so the title is the logical value.
		label := unescapeMermaidLabel(m[2])
		title := label
		// Labels look like "H-001: Short title"; strip the leading ID.
		if idx := strings.Index(label, ":"); idx >= 0 {
			title = strings.TrimSpace(label[idx+1:])
		}
		var status HypothesisStatus
		if m[3] != "" {
			status = NormalizeStatus(m[3])
		}
		if _, ok := nodes[id]; !ok {
			nodeOrder = append(nodeOrder, id)
		}
		nodes[id] = mermaidNode{id: id, title: title, status: status}
	}

	out := make([]mermaidNode, 0, len(nodeOrder))
	for _, id := range nodeOrder {
		out = append(out, nodes[id])
	}

	// Second pass: edges.
	edges := make([]HypothesisEdge, 0)
	seen := make(map[string]struct{})
	for _, m := range mermaidEdgeRe.FindAllStringSubmatch(block, -1) {
		from := NormalizeID(m[1])
		to := NormalizeID(m[2])
		if from == "" || to == "" || from == to {
			continue
		}
		key := from + "|" + to
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		edges = append(edges, HypothesisEdge{From: from, To: to})
	}
	return out, edges
}

// catalogRow is an intermediate parse of one row of the graph.md catalog table.
type catalogRow struct {
	id      string
	title   string
	status  HypothesisStatus
	parents []string
}

// ParseCatalog parses the Hypothesis Catalog table from graph.md, skipping
// rows without a parseable ID (header, separator, placeholder). Expected
// columns: ID | Hypothesis | Status | Decision | Parent(s); trailing
// extras tolerated.
func ParseCatalog(content string) []catalogRow {
	rows := make([]catalogRow, 0)
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) < 3 || isSeparatorRow(cells) {
			continue
		}
		id := NormalizeID(cells[0])
		if id == "" {
			continue // header or "*No hypotheses yet*" placeholder
		}
		row := catalogRow{id: id}
		if len(cells) > 1 {
			row.title = dashToEmpty(cells[1])
		}
		if len(cells) > 2 {
			if v := dashToEmpty(cells[2]); v != "" {
				row.status = NormalizeStatus(v)
			}
		}
		if len(cells) > 4 {
			row.parents = parseParentList(cells[4])
		}
		rows = append(rows, row)
	}
	return rows
}

// BuildGraph reconciles the three structural sources — Mermaid diagram,
// catalog table, parsed cards — into one HypothesisGraph. Per-node field
// priority is card > catalog > Mermaid; edges are the union of drawn edges
// and declared parents, so the graph stays complete even when the diagram
// lagged the cards. All inputs optional; all-empty yields a valid empty graph.
func BuildGraph(mermaidNodes []mermaidNode, mermaidEdges []HypothesisEdge, catalog []catalogRow, cards []HypothesisNode) HypothesisGraph {
	g := HypothesisGraph{}
	index := make(map[string]*HypothesisNode)

	get := func(id string) *HypothesisNode {
		if id == "" {
			return nil
		}
		if n, ok := index[id]; ok {
			return n
		}
		n := &HypothesisNode{ID: id}
		index[id] = n
		g.Nodes = append(g.Nodes, n)
		return n
	}

	// 1) Mermaid (lowest priority): seed nodes with label-derived titles and
	//    class-derived statuses.
	for _, mn := range mermaidNodes {
		n := get(mn.id)
		if n.Title == "" && mn.title != "" {
			n.Title = mn.title
		}
		if n.Status == "" && mn.status != "" {
			n.Status = mn.status
		}
	}

	// 2) Catalog (middle priority): fill gaps from the table.
	for _, c := range catalog {
		n := get(c.id)
		if n.Title == "" && c.title != "" {
			n.Title = c.title
		}
		if n.Status == "" && c.status != "" {
			n.Status = c.status
		}
		if len(n.Parents) == 0 && len(c.parents) > 0 {
			n.Parents = c.parents
		}
	}

	// 3) Cards (highest priority): authoritative for every field they carry.
	for _, c := range cards {
		n := get(c.ID)
		if c.Title != "" {
			n.Title = c.Title
		}
		if c.Status != "" {
			n.Status = c.Status
		}
		if c.Timebox != "" {
			n.Timebox = c.Timebox
		}
		if c.Completed != "" {
			n.Completed = c.Completed
		}
		if c.Result != "" {
			n.Result = c.Result
		}
		if len(c.Parents) > 0 {
			n.Parents = c.Parents
		}
		if c.Statement != "" {
			n.Statement = c.Statement
		}
		if c.VerificationCriterion != "" {
			n.VerificationCriterion = c.VerificationCriterion
		}
		if c.ExperimentNotes != "" {
			n.ExperimentNotes = c.ExperimentNotes
		}
		if c.Decision != "" {
			n.Decision = c.Decision
		}
	}

	// Edges: union of drawn edges and declared parents.
	edgeSet := make(map[string]struct{})
	addEdge := func(from, to string) {
		if from == "" || to == "" || from == to {
			return
		}
		key := from + "|" + to
		if _, ok := edgeSet[key]; ok {
			return
		}
		edgeSet[key] = struct{}{}
		g.Edges = append(g.Edges, HypothesisEdge{From: from, To: to})
	}
	for _, e := range mermaidEdges {
		addEdge(e.From, e.To)
	}
	for _, n := range g.Nodes {
		for _, p := range n.Parents {
			addEdge(p, n.ID)
		}
	}

	// Reconcile each node's Parents with the final edge set (partial cards +
	// drawn edges): Parents becomes the union of declared parents and the
	// From-IDs of all incoming edges.
	parentsFromEdges := make(map[string]map[string]struct{})
	for _, e := range g.Edges {
		set, ok := parentsFromEdges[e.To]
		if !ok {
			set = make(map[string]struct{})
			parentsFromEdges[e.To] = set
		}
		set[e.From] = struct{}{}
	}
	for _, n := range g.Nodes {
		seen := make(map[string]struct{}, len(n.Parents)+1)
		merged := make([]string, 0, len(n.Parents)+1)
		add := func(id string) {
			if id == "" {
				return
			}
			if _, ok := seen[id]; ok {
				return
			}
			seen[id] = struct{}{}
			merged = append(merged, id)
		}
		for _, p := range n.Parents {
			add(p)
		}
		for p := range parentsFromEdges[n.ID] {
			add(p)
		}
		sort.Slice(merged, func(i, j int) bool { return compareHypothesisIDs(merged[i], merged[j]) < 0 })
		n.Parents = merged
	}

	// Deterministic numeric ordering (compareHypothesisIDs).
	sort.Slice(g.Nodes, func(i, j int) bool { return compareHypothesisIDs(g.Nodes[i].ID, g.Nodes[j].ID) < 0 })
	sort.Slice(g.Edges, func(i, j int) bool {
		if c := compareHypothesisIDs(g.Edges[i].From, g.Edges[j].From); c != 0 {
			return c < 0
		}
		return compareHypothesisIDs(g.Edges[i].To, g.Edges[j].To) < 0
	})
	return g
}

// -- Index + prior art --

// IndexEntry is one row of the research-root index (index.md): a research
// project's identifier, title, and relative link to its brief.
type IndexEntry struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Path  string `json:"path,omitempty"`
}

// indexLinkRe matches a Markdown link whose target is a brief.md file.
var indexLinkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)]*brief\.md)\)`)

// ParseIndex parses the research-root index into de-duplicated, order-
// preserved entries: brief.md links preferred (title + path; ID from target
// or text), bare R-NNN tokens as fallback.
func ParseIndex(content string) []IndexEntry {
	entries := make([]IndexEntry, 0)
	seen := make(map[string]struct{})

	for _, m := range indexLinkRe.FindAllStringSubmatch(content, -1) {
		text := strings.TrimSpace(m[1])
		path := strings.TrimSpace(m[2])
		id := NormalizeResearchID(path)
		if id == "" {
			id = NormalizeResearchID(text)
		}
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		entries = append(entries, IndexEntry{ID: id, Title: text, Path: path})
	}

	// Fallback: bare R-NNN tokens (e.g. a plain list with no links).
	if len(entries) == 0 {
		for _, m := range ridRe.FindAllStringSubmatch(content, -1) {
			id := NormalizeResearchID(m[0])
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			entries = append(entries, IndexEntry{ID: id})
		}
	}
	return entries
}

// ParsePriorArtCount counts the cataloged entries in prior-art.md across the
// two layouts seen in practice: the canonical numbered table (a numeric first
// cell = one entry) and grouped prose ("### N.M Name" headings under
// "## N. Category"). The table is counted first; only a zero count falls back
// to headings, so a canonical catalog is never mis-counted by stray
// subsection headings. 0 for an empty or placeholder-only catalog.
func ParsePriorArtCount(content string) int {
	count := parsePriorArtTableCount(content)
	if count > 0 {
		return count
	}
	return len(priorArtHeadingRe.FindAllString(content, -1))
}

// priorArtHeadingRe matches "### N.M Name" (grouped-prose layout): a non-space
// character is required after the number (a "### 1.2" stub is not an entry)
// and the match is line-anchored (prose mentioning "1.2" is ignored).
var priorArtHeadingRe = regexp.MustCompile(`(?m)^###\s+\d+\.\d+\s+\S`)

// parsePriorArtTableCount counts numbered rows in the canonical prior-art
// table; ParsePriorArtCount owns the heading-layout fallback.
func parsePriorArtTableCount(content string) int {
	count := 0
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) == 0 || isSeparatorRow(cells) {
			continue
		}
		if isNumeric(cells[0]) {
			count++
		}
	}
	return count
}

// isNumeric reports whether s consists solely of ASCII digits.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// -- Research log --

// logEntryHeadingRe matches a research-log entry heading
// "## <kind> <created_at> [<hypothesis-id>]" (groups: kind, raw timestamp,
// optional H-NNN). The trailing (?:\s+.*)? tolerates free-form annotation —
// the log is model-authored free text following a skill template, and a
// strict end-anchor would silently drop the whole entry AND its body. The
// annotation itself is ignored.
var logEntryHeadingRe = regexp.MustCompile(`(?i)^##\s+(\S+)\s+(\S+)(?:\s+(H-?\d+))?(?:\s+.*)?$`)

// normalizeLogKind canonicalizes a log-kind token; ok=false reads as "not an
// entry heading" (best-effort, never an error).
func normalizeLogKind(raw string) (LogKind, bool) {
	k := LogKind(strings.ToLower(strings.TrimSpace(raw)))
	switch k {
	case LogKindExperiment, LogKindDecision, LogKindStatusChange, LogKindNote:
		return k, true
	default:
		return "", false
	}
}

// timestampDateRe matches an ISO-8601 date prefix (YYYY-MM-DD) — bare dates
// and full datetimes both carry it.
var timestampDateRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

// looksLikeTimestamp keeps a "## <word> <word>" message line from being taken
// for an entry: a bare ordinal ("## note 5") has no ISO-8601 date prefix, so
// requiring a full date — not merely a digit — rejects it.
func looksLikeTimestamp(s string) bool {
	return timestampDateRe.MatchString(s)
}

// ParseLog parses log.md into a chronological list of entries, best-effort
// and never an error: a missing/empty/malformed file yields an empty slice
// and unrecognized lines are ignored. Entries carry a stable 1-based ordinal
// (file order; the log is append-only). Expected format — level-2 headings,
// one per entry:
//
//	## experiment 2025-04-02T10:15:00Z H-001
//	Recovered 97% of modules on the first pass.
//
// kind ∈ {experiment, decision, status_change, note} (case-insensitive),
// created_at verbatim, the optional hypothesis ID normalized; entries without
// one are project-scoped; everything up to the next heading is the message.
func ParseLog(content string) []ResearchLogEntry {
	entries := make([]ResearchLogEntry, 0)
	if strings.TrimSpace(content) == "" {
		return entries
	}

	var (
		current *ResearchLogEntry
		message strings.Builder
	)

	// flush appends the in-progress entry (if any) to the result and resets the
	// message accumulator for the next entry.
	flush := func() {
		if current == nil {
			return
		}
		current.Message = strings.TrimSpace(message.String())
		entries = append(entries, *current)
		current = nil
		message.Reset()
	}

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		// Level-2 headings are reserved for entry headings; deeper "### " lines
		// inside a message stay message text.
		if strings.HasPrefix(trimmed, "## ") {
			// Any heading terminates the current entry, valid or not.
			flush()
			if m := logEntryHeadingRe.FindStringSubmatch(trimmed); m != nil {
				kind, ok := normalizeLogKind(m[1])
				if ok && looksLikeTimestamp(m[2]) {
					current = &ResearchLogEntry{
						ID:        strconv.Itoa(len(entries) + 1),
						Kind:      kind,
						CreatedAt: strings.TrimSpace(m[2]),
					}
					if m[3] != "" {
						current.HypothesisID = NormalizeID(m[3])
					}
				}
			}
			// Invalid heading: current stays nil, so the body is ignored until
			// the next valid heading.
			continue
		}
		if current != nil {
			message.WriteString(line)
			message.WriteByte('\n')
		}
	}
	flush()
	return entries
}

// -- Filesystem orchestrators --

// ResearchProject is the parsed view of one R-NNN project — brief, reconciled
// graph, metrics, bookkeeping flags — as the Research panel renders it.
type ResearchProject struct {
	ID string `json:"id"`
	// Dir is the absolute directory the project was parsed from, threaded
	// through by ParseProject/ParseResearchRoot so mutation entry points
	// resolve the SAME directory the parser read — a brief/dir ID mismatch
	// or duplicate R-NNN names must not redirect mutations to a sibling
	// project. Empty for never-parsed, hand-constructed models.
	Dir           string             `json:"dir,omitempty"`
	Brief         Brief              `json:"brief"`
	Graph         HypothesisGraph    `json:"graph"`
	Metrics       Metrics            `json:"metrics"`
	PriorArtCount int                `json:"prior_art_count"`
	HasReport     bool               `json:"has_report"`
	Log           []ResearchLogEntry `json:"log"`
}

// ResearchRoot is the parsed view of a {research-root} directory: the index of
// research projects plus each parsed project. It powers the Research panel's
// project list and per-project status.
type ResearchRoot struct {
	Path     string             `json:"path"`
	Index    []IndexEntry       `json:"index"`
	Projects []*ResearchProject `json:"projects"`
	// ActiveProjectID is the active R-NNN (latest index entry, else
	// highest-numbered directory) — the single source of truth shared by the
	// orchestrator context and the frontend panel, computed once at parse
	// time (PickActiveProject). Empty on a fresh research root.
	ActiveProjectID string `json:"active_project_id,omitempty"`
}

// PickActiveProject selects the active R-NNN: the latest index entry when an
// index exists, else the highest-numbered project directory — the canonical
// selection rule shared by the orchestrator and the frontend panel
// (specs/domains/research.md). Returns nil when no project exists.
func PickActiveProject(root *ResearchRoot) *ResearchProject {
	if root == nil {
		return nil
	}
	// Prefer the index (chronological order): the last entry is the newest.
	if len(root.Index) > 0 {
		lastID := root.Index[len(root.Index)-1].ID
		for _, p := range root.Projects {
			if p.ID == lastID {
				return p
			}
		}
	}
	// Fallback: projects are sorted numerically (compareResearchIDs), so the
	// last is the highest-numbered R-NNN even for unpadded names.
	if len(root.Projects) > 0 {
		return root.Projects[len(root.Projects)-1]
	}
	return nil
}

// maxArtifactBytes bounds a single RESEARCH artifact read: the root is
// workspace content feeding read-only status RPCs, and safeio.ReadFile is an
// unbounded io.ReadAll, so an oversized (hostile or accidental) artifact must
// not be read whole. 8 MiB is far above any real artifact; over-cap files are
// treated as unreadable (skipped by the tolerant reader, refused by the
// strict one).
const maxArtifactBytes = 8 << 20

// oversizedArtifact reports whether path exists and is larger than the
// per-artifact read cap. Stat errors — including NotExist — are not
// "oversized": the caller's read handles them.
func oversizedArtifact(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > maxArtifactBytes
}

// readFile reads a file's contents, returning ("", false) when missing —
// missing optional artifacts are a normal partial state. Rendering (parser)
// paths use this tolerant form; mutation paths must use readFileStrict so an
// unreadable-but-present file is never mistaken for a missing one. Over-cap
// artifacts are likewise "unreadable".
func readFile(path string) (string, bool) {
	if oversizedArtifact(path) {
		return "", false
	}
	data, err := safeio.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		return "", false
	}
	return string(data), true
}

// readFileStrict reads a file's contents for a mutation path. Like readFile,
// a missing file is ("", nil) — the writer may create it. Every other read
// error is returned so the mutation aborts: treating "cannot read" as "does
// not exist" would let the atomic rename silently replace unreadable prior
// content (log.md truncated to one entry, index.md rewritten as a skeleton).
// RESEARCH artifacts are persisted agent memory (SECURITY.md, ASI06) — this
// fails closed.
func readFileStrict(path string) (string, error) {
	if oversizedArtifact(path) {
		return "", fmt.Errorf("reading %s: artifact exceeds the %d byte read cap", path, maxArtifactBytes)
	}
	data, err := safeio.ReadFile(path)
	switch {
	case err == nil:
		return string(data), nil
	case errors.Is(err, os.ErrNotExist):
		return "", nil
	default:
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
}

// ParseProject parses one R-NNN project directory, fully best-effort: every
// artifact is optional (a fresh project parses with an empty graph and zero
// metrics); only a missing/unreadable directory is an error.
func ParseProject(projectPath string) (*ResearchProject, error) {
	info, err := os.Stat(projectPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("research project path is not a directory: " + projectPath)
	}

	briefContent, _ := readFile(filepath.Join(projectPath, "brief.md"))
	brief := ParseBrief(briefContent)

	graphContent, _ := readFile(filepath.Join(projectPath, "hypotheses", "graph.md"))
	mermaidNodes, mermaidEdges := ParseMermaidGraph(graphContent)
	catalog := ParseCatalog(graphContent)
	cards := loadCards(filepath.Join(projectPath, "hypotheses"))
	graph := BuildGraph(mermaidNodes, mermaidEdges, catalog, cards)

	priorArtContent, _ := readFile(filepath.Join(projectPath, "prior-art.md"))
	priorArtCount := ParsePriorArtCount(priorArtContent)

	logContent, _ := readFile(filepath.Join(projectPath, "log.md"))
	logEntries := ParseLog(logContent)

	_, hasReport := readFile(filepath.Join(projectPath, "report.md"))

	id := brief.ID
	if id == "" {
		id = NormalizeResearchID(filepath.Base(projectPath))
	}

	return &ResearchProject{
		ID:            id,
		Dir:           projectPath,
		Brief:         brief,
		Graph:         graph,
		Metrics:       ComputeMetrics(&graph),
		PriorArtCount: priorArtCount,
		HasReport:     hasReport,
		Log:           logEntries,
	}, nil
}

// listHypothesisCards returns every H-*.md path under dir, sorted by base
// name (nil when dir is missing/unreadable). os.ReadDir, not filepath.Glob:
// glob metacharacters in the directory PATH itself (a workspace under
// "[proj]") must match literally, or every card would silently vanish.
func listHypothesisCards(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := e.Name()
		if strings.HasPrefix(base, "H-") && strings.HasSuffix(base, ".md") {
			names = append(names, base)
		}
	}
	sort.Strings(names)
	paths := make([]string, len(names))
	for i, name := range names {
		paths[i] = filepath.Join(dir, name)
	}
	return paths
}

// loadCards reads and parses every card, skipping failures (a malformed card
// must not break the whole graph); file-name order — numeric-order callers
// re-sort via compareHypothesisIDs.
func loadCards(dir string) []HypothesisNode {
	paths := listHypothesisCards(dir)
	if len(paths) == 0 {
		return nil
	}
	cards := make([]HypothesisNode, 0, len(paths))
	for _, p := range paths {
		content, ok := readFile(p)
		if !ok {
			continue
		}
		node, perr := ParseCard(content)
		if perr != nil {
			continue
		}
		cards = append(cards, node)
	}
	return cards
}

// ParseResearchRoot parses a research-root directory: its index plus every
// R-NNN-* project subdirectory. The index is optional (a root may contain
// projects before index.md is written); missing projects or a missing index
// are not errors. Only an unreadable root path is an error.
func ParseResearchRoot(rootPath string) (*ResearchRoot, error) {
	info, err := os.Stat(rootPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("research root path is not a directory: " + rootPath)
	}

	root := &ResearchRoot{Path: rootPath}

	if indexContent, ok := readFile(filepath.Join(rootPath, "index.md")); ok {
		root.Index = ParseIndex(indexContent)
	}

	entries, err := os.ReadDir(rootPath)
	if err != nil {
		return nil, err
	}
	// Nested layout: descend into every R-NNN-* project subdirectory.
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if NormalizeResearchID(e.Name()) == "" {
			continue // not an R-NNN project directory
		}
		project, perr := ParseProject(filepath.Join(rootPath, e.Name()))
		if perr != nil {
			continue
		}
		root.Projects = append(root.Projects, project)
	}

	// Numeric R-NNN order with the parsed directory as tie-break, so
	// duplicate IDs keep a deterministic order and PickActiveProject's
	// "last = active" resolves to the directory ActiveProjectDir returns.
	sort.Slice(root.Projects, func(i, j int) bool {
		if c := compareResearchIDs(root.Projects[i].ID, root.Projects[j].ID); c != 0 {
			return c < 0
		}
		return root.Projects[i].Dir < root.Projects[j].Dir
	})

	// Computed once at parse time so every consumer reads the same value.
	if active := PickActiveProject(root); active != nil {
		root.ActiveProjectID = active.ID
	}
	return root, nil
}
