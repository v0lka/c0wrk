package research

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// This file implements the content parsing layer of the research package.
//
// The pure parsers (ParseBrief, ParseCard, ParseMermaidGraph, ParseCatalog,
// ParseIndex, ParsePriorArtCount) take already-read file contents and return
// structured data with no side effects, so they can be unit-tested with plain
// string fixtures. BuildGraph merges those sources into a single
// HypothesisGraph, and the thin orchestrators ParseProject / ParseResearchRoot
// are the only functions that touch the filesystem — and even they only ever
// read the given paths.

// ──────────────────────────────────────────────────────────────────────────
// ID / list helpers
// ──────────────────────────────────────────────────────────────────────────

// hidRe matches a hypothesis identifier in either spelling: "H001" (Mermaid
// node ID) or "H-001" (card / catalog). Case-insensitive.
var hidRe = regexp.MustCompile(`(?i)H-?(\d+)`)

// ridRe matches a research identifier, e.g. "R-001".
var ridRe = regexp.MustCompile(`(?i)R-?(\d+)`)

// NormalizeID canonicalizes a hypothesis identifier to its hyphenated,
// upper-case form ("H-001"). It accepts both "H001" and "H-001" spellings
// (case-insensitive) and returns "" for input that contains no identifier.
func NormalizeID(raw string) string {
	m := hidRe.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	return "H-" + m[1]
}

// normalizeResearchID canonicalizes a research identifier to "R-001".
func normalizeResearchID(raw string) string {
	m := ridRe.FindStringSubmatch(raw)
	if m == nil {
		return ""
	}
	return "R-" + m[1]
}

// parseParentList extracts the canonical parent IDs from a free-form string
// such as "H-001, H-002" or "—". It returns a sorted, de-duplicated slice
// (possibly empty). Non-identifier tokens (em-dashes, "none", etc.) are
// dropped.
func parseParentList(raw string) []string {
	matches := hidRe.FindAllStringSubmatch(raw, -1)
	if matches == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	parents := make([]string, 0, len(matches))
	for _, m := range matches {
		id := "H-" + m[1]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		parents = append(parents, id)
	}
	sort.Strings(parents)
	return parents
}

// splitCells splits a single Markdown table row (with leading/trailing pipes)
// into its trimmed cell values.
func splitCells(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	parts := strings.Split(row, "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	return cells
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

// ──────────────────────────────────────────────────────────────────────────
// Section / field extraction (shared by brief and card parsers)
// ──────────────────────────────────────────────────────────────────────────

// sectionHeadingLower returns the lower-cased text of a level-2 Markdown
// heading line ("## Name"), or "" if the line is not a level-2 heading. Only
// exactly two leading hashes followed by a space count, so deeper headings
// ("### ...") do not act as section boundaries.
func sectionHeadingLower(line string) string {
	trimmed := strings.TrimSpace(line)
	const marker = "## "
	if !strings.HasPrefix(trimmed, marker) {
		return ""
	}
	// Exclude deeper headings: "### X" does not start with "## " (3rd char differs).
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, marker)))
}

// extractSection returns the trimmed body of the first section whose heading
// contains the given (case-insensitive) name, or "" if absent. A section is a
// "## Name" block running until the next level-2 heading, a horizontal rule
// ("---"), or end of input.
//
// Implemented as a line scan rather than a single regex so that consecutive
// sections are all found (a regex terminator would consume the next heading's
// "## " marker and skip every other section).
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

// extractField extracts the value cell of a row in a two-column Markdown table
// whose first cell is **fieldName** (Markdown bold), e.g. "| **Status** | open |".
// Returns "" if the field is absent.
func extractField(content, fieldName string) string {
	target := "**" + fieldName + "**"
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 || strings.TrimSpace(cells[1]) != target {
			continue
		}
		return strings.TrimSpace(cells[2])
	}
	return ""
}

// ──────────────────────────────────────────────────────────────────────────
// Brief
// ──────────────────────────────────────────────────────────────────────────

// Brief holds the structured fields parsed from a research brief
// (R-NNN/brief.md). Unknown or absent fields are left empty; the parser never
// fails for partial briefs.
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

// ParseBrief parses a research brief's Markdown content into a Brief. It is
// best-effort: missing fields are left empty. The ID is derived from the H1
// bracket; if the H1 is absent the ID is empty but the rest is still parsed.
func ParseBrief(content string) Brief {
	b := Brief{}
	if m := briefTitleRe.FindStringSubmatch(content); m != nil {
		b.ID = normalizeResearchID(m[1])
		b.Title = strings.TrimSpace(m[2])
	}
	// ID fallback: the canonical H1 is "# [R-NNN] Title", but some briefs use a
	// descriptive H1 ("# Research Brief: ...") and carry the ID only in the
	// Identifier table field. Fall back to that field when the H1 had no bracket.
	if b.ID == "" {
		b.ID = normalizeResearchID(extractField(content, "Identifier"))
	}
	// Title fallback: when the H1 is descriptive rather than the bracket form,
	// use the H1 text (with common "Research Brief:"/"Brief:" prefixes stripped)
	// as the title so the panel shows a meaningful label.
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
	if parents := parseParentList(extractField(content, "Parent(s)")); len(parents) > 0 {
		node.Parents = parents
	}
	node.Result = extractFinding(content)
	return node, nil
}

// extractFinding pulls the recorded finding out of a card's Result section.
// It prefers an explicit "**Finding:** value" line; failing that it returns
// the trimmed section body with placeholder/italic lines removed. Returns ""
// when there is no recorded result (template placeholder or absent section).
func extractFinding(content string) string {
	body := extractSection(content, "Result")
	if body == "" {
		return ""
	}
	if m := findingRe.FindStringSubmatch(body); m != nil {
		return dashToEmpty(m[1])
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

// ──────────────────────────────────────────────────────────────────────────
// Hypothesis graph sources: Mermaid diagram + catalog table
// ──────────────────────────────────────────────────────────────────────────

// mermaidBlockRe captures the body of a ```mermaid fenced code block.
var mermaidBlockRe = regexp.MustCompile("(?ms)```mermaid\\s*\\n(.*?)```")

// mermaidNodeRe matches a Mermaid node definition: H001["H-001: Title"]:::class.
// Group 1 = node ID (H001 form), group 2 = quoted label, group 3 = optional
// CSS class (status). The class marker is ":::" (two-or-more colons, matched
// greedily so the three-colon Mermaid syntax is captured).
var mermaidNodeRe = regexp.MustCompile(`\b(H\d+)\s*\["([^"]*)"\]\s*(?:::+([A-Za-z_]+))?`)

// mermaidEdgeRe matches a Mermaid edge: H001 --> H002.
var mermaidEdgeRe = regexp.MustCompile(`\b(H\d+)\s*-->\s*(H\d+)\b`)

// mermaidNode is an intermediate parse of one Mermaid node line.
type mermaidNode struct {
	id     string // canonical "H-001"
	title  string
	status HypothesisStatus
}

// ParseMermaidGraph extracts nodes and edges from the Mermaid diagram inside a
// graph.md file. It returns the node descriptors (keyed by canonical ID) and
// the directed edges (canonical from/to IDs). A missing or empty Mermaid block
// yields empty (non-nil) results — never an error, since an empty graph is a
// valid partial state.
func ParseMermaidGraph(content string) ([]mermaidNode, []HypothesisEdge) {
	block := ""
	if m := mermaidBlockRe.FindStringSubmatch(content); m != nil {
		block = m[1]
	}
	if block == "" {
		return nil, nil
	}

	// Strip Mermaid line comments (lines whose first non-space chars are "%%"),
	// so commented-out example nodes/edges in the graph template are not parsed
	// as real structure.
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
		label := m[2]
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

// ParseCatalog parses the Hypothesis Catalog table from a graph.md file. Rows
// without a parseable hypothesis ID (header, separator, placeholder) are
// skipped. The expected columns are: ID | Hypothesis | Status | Decision |
// Parent(s); extra/missing trailing columns are tolerated.
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

// BuildGraph reconciles the three structural sources of a hypothesis graph —
// the Mermaid diagram, the catalog table, and the parsed cards — into a single
// HypothesisGraph. Merge priority for per-node fields is card > catalog >
// Mermaid (the most authoritative source wins). Edges are the union of the
// Mermaid edges and every declared parent relationship, so the graph stays
// complete even when the diagram was not updated but a card lists its parents.
//
// All inputs are optional; passing all-empty inputs yields an empty (but
// valid) graph.
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
		if c.Result != "" {
			n.Result = c.Result
		}
		if len(c.Parents) > 0 {
			n.Parents = c.Parents
		}
	}

	// Edges: union of Mermaid edges and every node's declared parents, so the
	// edge set is consistent with the Parents fields even for partial graphs.
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

	// Reconcile each node's Parents with the final edge set so the two stay
	// consistent even when a partial card omitted its parents but the Mermaid
	// diagram drew the edge. Parents becomes the union of declared parents and
	// the From-IDs of all edges pointing at the node.
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
		sort.Strings(merged)
		n.Parents = merged
	}

	// Deterministic ordering.
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].From != g.Edges[j].From {
			return g.Edges[i].From < g.Edges[j].From
		}
		return g.Edges[i].To < g.Edges[j].To
	})
	return g
}

// ──────────────────────────────────────────────────────────────────────────
// Index + prior art
// ──────────────────────────────────────────────────────────────────────────

// IndexEntry is one row of the research-root index (index.md): a research
// project's identifier, title, and relative link to its brief.
type IndexEntry struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Path  string `json:"path,omitempty"`
}

// indexLinkRe matches a Markdown link whose target is a brief.md file.
var indexLinkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)]*brief\.md)\)`)

// ParseIndex parses the research-root index into a list of entries. It prefers
// Markdown links to brief.md (extracting title and relative path) and derives
// the research ID from the link target or text. When no links are present it
// falls back to bare R-NNN tokens in the document. The result is
// de-duplicated and order-preserved.
func ParseIndex(content string) []IndexEntry {
	entries := make([]IndexEntry, 0)
	seen := make(map[string]struct{})

	for _, m := range indexLinkRe.FindAllStringSubmatch(content, -1) {
		text := strings.TrimSpace(m[1])
		path := strings.TrimSpace(m[2])
		id := normalizeResearchID(path)
		if id == "" {
			id = normalizeResearchID(text)
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
			id := normalizeResearchID(m[0])
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

// ParsePriorArtCount counts the cataloged entries in a prior-art.md file. It
// supports two layouts that appear in practice:
//
//  1. The canonical numbered table (| # | Source | Type | Annotation | Relevance |):
//     a row whose first cell is a number is one entry. This cleanly excludes
//     the header, separator, and the "*No entries yet*" placeholder.
//  2. A grouped-prose layout using "### N.M Name" subsection headings under
//     "## N. Category" sections — an alternative some researches prefer over
//     the table. Each such heading is one entry.
//
// The table layout is counted first; only when it yields zero entries is the
// heading layout counted, so a canonical catalog is never mis-counted by stray
// subsection headings. Returns 0 for an empty or placeholder-only catalog.
func ParsePriorArtCount(content string) int {
	count := parsePriorArtTableCount(content)
	if count > 0 {
		return count
	}
	return len(priorArtHeadingRe.FindAllString(content, -1))
}

// priorArtHeadingRe matches a numbered subsection heading like "### 1.2 Name" —
// the entry layout used by grouped-prose prior-art catalogs. It requires at
// least one non-space character after the number so "### 1.2" alone (a draft
// stub) is not double-counted, and it is anchored to the line start so prose
// mentioning "1.2" is ignored.
var priorArtHeadingRe = regexp.MustCompile(`(?m)^###\s+\d+\.\d+\s+\S`)

// parsePriorArtTableCount counts numbered rows in the canonical prior-art
// table (the "#" column). It is the primary counting strategy; the
// subsection-heading layout is a fallback handled by ParsePriorArtCount.
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

// ──────────────────────────────────────────────────────────────────────────
// Research log
// ──────────────────────────────────────────────────────────────────────────

// logEntryHeadingRe matches a research-log entry heading of the form
//
//	## <kind> <created_at> [<hypothesis-id>]
//
// e.g. "## experiment 2025-04-02T10:15:00Z H-001". Group 1 is the kind token,
// group 2 is the raw timestamp, and group 3 (optional) is the hypothesis
// identifier in either "H-001" or "H001" spelling (case-insensitive). The
// message body is the block of lines following the heading until the next
// heading or end of file.
var logEntryHeadingRe = regexp.MustCompile(`(?i)^##\s+(\S+)\s+(\S+)(?:\s+(H-?\d+))?\s*$`)

// normalizeLogKind canonicalizes a log-kind token to its LogKind constant.
// It returns ok=false for an unrecognized kind, which the parser treats as
// "not an entry heading" (best-effort, never an error).
func normalizeLogKind(raw string) (LogKind, bool) {
	k := LogKind(strings.ToLower(strings.TrimSpace(raw)))
	switch k {
	case LogKindExperiment, LogKindDecision, LogKindStatusChange, LogKindNote:
		return k, true
	default:
		return "", false
	}
}

// timestampDateRe matches an ISO-8601 date prefix (YYYY-MM-DD) — the minimum
// component every log timestamp carries, whether the token is a bare date
// ("2025-04-02") or a full datetime ("2025-04-02T10:15:00Z").
var timestampDateRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

// looksLikeTimestamp is a light sanity check used to avoid treating a message
// line that happens to start with "## <word> <word>" as a new entry. A real
// timestamp token always carries an ISO-8601 date prefix (e.g. "2025-04-02" or
// "2025-04-02T10:15:00Z"), whereas a prose fragment or a bare ordinal
// ("## note 5") does not. Requiring a full date — rather than merely "contains
// a digit" — keeps such bare ordinals from being mistaken for a timestamp.
func looksLikeTimestamp(s string) bool {
	return timestampDateRe.MatchString(s)
}

// ParseLog parses a research-log file (log.md) into a chronological list of
// entries. It is best-effort and never returns an error: a missing, empty, or
// malformed file yields an empty (non-nil) slice, and unrecognized lines are
// simply ignored. Entries are returned in file order with a 1-based ordinal ID
// assigned by position (logs are append-only, so ordinals are stable).
//
// The expected format is a series of level-2 headings, one per entry:
//
//	## experiment 2025-04-02T10:15:00Z H-001
//	Recovered 97% of modules on the first pass.
//
//	## decision 2025-04-03T09:00:00Z
//	Continue deepening the current front.
//
// The heading is "<kind> <created_at> [<hypothesis-id>]"; everything following
// it (up to the next heading) is the message. kind is one of experiment,
// decision, status_change, or note (case-insensitive). created_at is preserved
// verbatim. An optional hypothesis ID ("H-001" / "H001") is normalized to the
// canonical hyphenated form; entries without one are project-scoped.
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
		// A level-2 heading line is reserved for entry headings; it is never
		// folded into the preceding entry's message. Lines that do not start
		// with "## " (including deeper "### " sub-headings inside a message)
		// are message text.
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
			// Invalid entry heading (unknown kind, non-timestamp token, or a
			// bare "## "): current stays nil, so the skipped entry's body is
			// ignored until the next valid heading.
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

// ──────────────────────────────────────────────────────────────────────────
// Filesystem orchestrators
// ──────────────────────────────────────────────────────────────────────────

// ResearchProject is the parsed view of a single R-NNN research project: its
// brief, the reconciled hypothesis graph, the computed metrics, and bookkeeping
// flags (whether the synthesis report exists, how many prior-art entries are
// cataloged). It is what the Research panel renders for one project.
type ResearchProject struct {
	ID            string             `json:"id"`
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
	// ActiveProjectID is the current active R-NNN (the latest index entry,
	// else the highest-numbered project directory). It is the single source of
	// truth shared by the orchestrator's research-awareness context and the
	// frontend Research panel, so both layers always agree on which project is
	// "active". Computed once at parse time (see PickActiveProject). Empty when
	// no project exists yet (a fresh research root).
	ActiveProjectID string `json:"active_project_id,omitempty"`
}

// PickActiveProject selects the current active R-NNN from a parsed research
// root: the latest index entry (chronological append order) when an index
// exists, otherwise the highest-numbered project directory. Returns nil when
// no project exists. It is the canonical selection rule shared by the
// orchestrator (research-awareness context) and the frontend (Research panel),
// so both never disagree on which project is active.
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
	// Fallback: ParseResearchRoot appends projects in directory-read order
	// (sorted by name), so the last is the highest-numbered R-NNN.
	if len(root.Projects) > 0 {
		return root.Projects[len(root.Projects)-1]
	}
	return nil
}

// readFile reads a file's contents, returning ("", false) when the file is
// missing rather than an error — missing optional artifacts are a normal
// partial state, not a failure.
func readFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false
		}
		return "", false
	}
	return string(data), true
}

// ParseProject parses a single research project directory (R-NNN-...). It is
// fully best-effort: every artifact is optional, so a freshly initialized
// project (no graph, no cards, no report) parses cleanly into a project with
// an empty graph and zero metrics. Only a missing or unreadable directory
// itself is an error.
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
		id = normalizeResearchID(filepath.Base(projectPath))
	}

	return &ResearchProject{
		ID:            id,
		Brief:         brief,
		Graph:         graph,
		Metrics:       ComputeMetrics(&graph),
		PriorArtCount: priorArtCount,
		HasReport:     hasReport,
		Log:           logEntries,
	}, nil
}

// loadCards reads every H-*.md card under dir, parsing each and skipping any
// that fail to parse (a malformed card must not break the whole graph). The
// returned slice is sorted by canonical ID. Missing directory → nil.
func loadCards(dir string) []HypothesisNode {
	paths, err := filepath.Glob(filepath.Join(dir, "H-*.md"))
	if err != nil || len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
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
		if normalizeResearchID(e.Name()) == "" {
			continue // not an R-NNN project directory
		}
		project, perr := ParseProject(filepath.Join(rootPath, e.Name()))
		if perr != nil {
			continue
		}
		root.Projects = append(root.Projects, project)
	}

	// Flat single-project fallback. The canonical methodology layout wraps
	// every project in an R-NNN-short-name/ subdirectory (handled above), but a
	// research root may also hold a single project flat at its top level —
	// brief.md, prior-art.md and hypotheses/ sitting directly under the root,
	// with no R-NNN wrapper. This shape arises when a root was populated by an
	// earlier or non-conformant workflow, or is used as a dedicated
	// single-project directory. When no R-NNN subdirectories were found but the
	// root itself carries the signature project artifacts, treat the root as
	// one project so the panel renders it instead of an empty state. Nested
	// roots are unaffected: they always have at least one R-NNN entry, so this
	// branch never runs for them.
	if len(root.Projects) == 0 && isFlatProjectRoot(rootPath) {
		if project, perr := ParseProject(rootPath); perr == nil {
			root.Projects = append(root.Projects, project)
		}
	}

	sort.Slice(root.Projects, func(i, j int) bool { return root.Projects[i].ID < root.Projects[j].ID })

	// Compute the active project once at parse time so every consumer
	// (orchestrator research context, frontend panel) reads the same value.
	if active := PickActiveProject(root); active != nil {
		root.ActiveProjectID = active.ID
	}
	return root, nil
}

// isFlatProjectRoot reports whether a directory holds a single research
// project's artifacts directly at its top level (the flat single-project
// layout), as opposed to the canonical nested layout where projects live in
// R-NNN-short-name/ subdirectories. The signature artifacts are brief.md
// and/or a hypotheses/ container; the presence of either is sufficient to
// recognize the shape. It never returns true for a canonical nested root
// (whose top level holds only index.md and R-NNN-* directories).
func isFlatProjectRoot(dir string) bool {
	if _, ok := readFile(filepath.Join(dir, "brief.md")); ok {
		return true
	}
	if info, err := os.Stat(filepath.Join(dir, "hypotheses")); err == nil && info.IsDir() {
		return true
	}
	return false
}
