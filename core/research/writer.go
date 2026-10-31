// Package research — writer layer: the mutation half of the research
// package, creating and updating hypothesis cards and the hypothesis graph
// in place (parser.go is the read-only half). Public entry points are
// UpdateHypothesis and CreateHypothesis: both read first, compute the new
// content purely in memory, validate before touching disk, and write each
// file via temp-file + rename — a validation failure leaves both files
// byte-for-byte unchanged.
package research

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
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
// "set to empty" (a non-nil pointer to "" / an empty Parents slice).
// Identifier, created, and completed are not editable through this path.
type HypothesisUpdate struct {
	// Title is the short, human-readable label (also mirrored into the graph
	// node label and catalog row).
	Title *string

	// Status is the new lifecycle status; it is validated against the
	// transition state machine and normalized to the canonical spelling.
	Status *string

	// Timebox is the raw timebox field (ISO 8601 duration or calendar dates).
	Timebox *string

	// Result is the recorded finding (the card's **Finding:** value).
	Result *string

	// Decision is the iteration decision recorded by the research-decision
	// skill (model.go documents the vocabulary).
	Decision *string

	// Statement replaces the card's "## Statement" section body.
	Statement *string

	// VerificationCriterion replaces the card's "## Verification Criterion"
	// section body.
	VerificationCriterion *string

	// ExperimentNotes replaces the card's "## Experiment Notes" section body.
	ExperimentNotes *string

	// Parents replaces the card's parent set, synchronized across the card's
	// **Parent(s)** row, the incoming Mermaid edges, and the catalog's
	// Parent(s) column (validated before any write — see
	// specs/domains/research.md).
	Parents *[]string
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

// -- Status transition state machine --

// transitions maps a current status to its legal successors, per the
// research-hypothesis skill: open → in-progress → confirmed | refuted |
// cancelled, open → cancelled. Terminal statuses have no outgoing
// transitions; everything else (backward jumps) is rejected.
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
// Re-setting the same status is a no-op; an empty/unknown current status (a
// card predating status tracking) is an initial set and accepts any known
// target; an unknown target is always rejected.
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

// -- Value escaping (round-trip contract with parser.go) --

// escapeCell renders a raw field value into a single-line Markdown table-cell
// value: newlines fold to "<br>" (a row must stay one line), literal pipes
// escape to "\|", and a literal "<br>" already in the value is shielded as
// "&lt;br&gt;" so it cannot masquerade as a folded newline on parse. The
// parser's splitCells/unescapeCell reverses all three, so written values
// parse back exactly (see escapeLiteralBR for the shielding).
func escapeCell(v string) string {
	if strings.ContainsAny(v, "|\n\r") || strings.Contains(v, "<br>") || strings.Contains(v, "&lt;br&gt;") {
		v = escapeLiteralBR(v)
		v = foldBR(v)
		v = strings.ReplaceAll(v, "|", `\|`)
	}
	return v
}

// escapeLiteralBR shields a literal "<br>" from the newline fold (foldBR
// renders newlines as "<br>"): it is rewritten to the entity spelling
// "&lt;br&gt;" on write and restored on parse (restoreLiteralBR). A value
// already containing the entity spelling is shielded one level deeper.
//
// The shield is single-level (like the Mermaid label escapes): a value
// literally containing the doubly-escaped spelling drifts once to
// "&lt;br&gt;" and is stable from then on — the same documented one-time
// drift unescapeMermaidLabel admits for "#quot;"/"#124;". Exactness at every
// depth would require escaping every "&", rewriting ordinary values ("R&D")
// for no practical gain.
func escapeLiteralBR(v string) string {
	if !strings.Contains(v, "<br>") && !strings.Contains(v, "&lt;br&gt;") {
		return v
	}
	// Escape a pre-existing entity spelling's ampersand FIRST, so the shield
	// below cannot claim it and restore maps it back to the entity text.
	v = strings.ReplaceAll(v, "&lt;br&gt;", "&amp;lt;br&gt;")
	return strings.ReplaceAll(v, "<br>", "&lt;br&gt;")
}

// foldBR replaces any newline flavor (and surrounding spaces) with the
// Markdown line-break marker "<br>", used wherever a value must be written
// onto a single line (table cells, the **Finding:** line, Mermaid labels).
func foldBR(v string) string {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	return strings.ReplaceAll(v, "\n", "<br>")
}

// foldTitle collapses a title value (card H1 / catalog cell / Mermaid label)
// to a single line by turning newlines into spaces. Titles are labels, not
// content: folding identically at every write site keeps write → parse →
// write byte-stable.
func foldTitle(v string) string {
	if !strings.ContainsAny(v, "\n\r") {
		return v
	}
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	fields := strings.Fields(v)
	return strings.Join(fields, " ")
}

// escapeMermaidLabel renders a raw title into a Mermaid quoted label:
// newlines fold to "<br>" (shielded via escapeLiteralBR), a pipe becomes
// "#124;" (edge-label syntax), a quote "#quot;". These are exactly the
// escapes that keep the line matching mermaidNodeLineRe ([^"]* label, single
// line); unescapeMermaidLabel (parser.go) reverses them.
func escapeMermaidLabel(v string) string {
	v = escapeLiteralBR(v)
	v = foldBR(v)
	if strings.Contains(v, "|") {
		v = strings.ReplaceAll(v, "|", "#124;")
	}
	if strings.Contains(v, `"`) {
		v = strings.ReplaceAll(v, `"`, "#quot;")
	}
	return v
}

// -- Card content rewriting (pure) --

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

// setTableField sets (or inserts) a "| **Field** | value |" row in the card's
// front-matter table: an existing row is replaced; a missing one is inserted
// after the last field row; a card with no field table at all gets a minimal
// one after the H1, so an update is never silently dropped. The value is
// escaped via escapeCell (single-line, pipe-safe); the parser reverses it.
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
	// top when there is no H1) so the update always lands somewhere parseable.
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

// setSection replaces the body of the first "## <heading>" section
// (case-insensitive Contains match, mirroring extractSection/sectionBody) or
// appends the section at the end of the card. The body is preserved verbatim:
// section content is free-form Markdown, unlike single-line table cells.
func setSection(content, heading, value string) string {
	body := strings.TrimRight(value, "\n")
	lines := strings.Split(content, "\n")
	target := strings.ToLower(strings.TrimSpace(heading))
	for i, line := range lines {
		if h := sectionHeadingLower(line); h == "" || !strings.Contains(h, target) {
			continue
		}
		// Section ends at the next level-2 heading or horizontal rule (extractSection semantics).
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if sectionHeadingLower(lines[j]) != "" || strings.TrimSpace(lines[j]) == "---" {
				end = j
				break
			}
		}
		replacement := make([]string, 0, len(lines))
		replacement = append(replacement, lines[:i+1]...)
		if body != "" {
			replacement = append(replacement, "", body)
		}
		// Keep exactly one blank line before the next heading/rule/end.
		replacement = append(replacement, "")
		replacement = append(replacement, lines[end:]...)
		return strings.TrimRight(strings.Join(replacement, "\n"), "\n")
	}
	// No such section: append one at the end of the card.
	out := strings.TrimRight(content, "\n")
	if out == "" {
		out = "# Research"
	}
	out += "\n\n## " + strings.TrimSpace(heading)
	if body != "" {
		out += "\n\n" + body
	}
	return out + "\n"
}

// setFinding sets the card's recorded finding (**Finding:** line), inserting
// it into the Result section (or a fresh one) when no line exists yet. The
// value is escaped via escapeCell because findingRe reads exactly one line;
// extractFinding reverses it, so multi-line findings round-trip.
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

// parentsCell renders a parent list for a card/table cell: "—" when empty
// (mirroring buildCardContent), else the canonical ids joined by ", ".
func parentsCell(parents []string) string {
	if len(parents) == 0 {
		return "—"
	}
	return strings.Join(parents, ", ")
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
	if upd.Parents != nil {
		c = setTableField(c, "Parent(s)", parentsCell(*upd.Parents))
	}
	if upd.Statement != nil {
		c = setSection(c, "Statement", *upd.Statement)
	}
	if upd.VerificationCriterion != nil {
		c = setSection(c, "Verification Criterion", *upd.VerificationCriterion)
	}
	if upd.ExperimentNotes != nil {
		c = setSection(c, "Experiment Notes", *upd.ExperimentNotes)
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

// mermaidNodeLineRe matches a whole Mermaid node-definition line: leading
// whitespace, node token (H001 form), quoted label, optional CSS class.
var mermaidNodeLineRe = regexp.MustCompile(`^(\s*)(H\d+)\s*\["([^"]*)"\]\s*(?:::+([A-Za-z_]+))?`)

// updateMermaidNode updates the label and/or CSS class of one node (by
// canonical ID). The existing label is unescaped before its title is reused
// and re-escaped on write, keeping the line matching mermaidNodeLineRe while
// round-tripping quotes, pipes, and newlines.
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

// updateCatalogParents sets the Parent(s) column of one row in the Hypothesis
// Catalog table (matching by canonical ID), padding the row with empty cells
// when the catalog predates the five-column layout.
func updateCatalogParents(content, id string, parents []string) string {
	val := parentsCell(parents)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) < 3 || isSeparatorRow(cells) || NormalizeID(cells[0]) != id {
			continue
		}
		for len(cells) < 5 {
			cells = append(cells, "")
		}
		cells[4] = val
		lines[i] = joinCells(cells)
	}
	return strings.Join(lines, "\n")
}

// syncMermaidIncomingEdges synchronizes one node's incoming edges with the
// given parent set: stale edges dropped, missing added. A node lacking a
// diagram definition gets one from nodeMeta, so a new edge never references
// an undefined Mermaid token.
func syncMermaidIncomingEdges(content, id string, parents []string, nodeMeta map[string]*HypothesisNode) string {
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

	parentSet := make(map[string]struct{}, len(parents))
	for _, p := range parents {
		parentSet[p] = struct{}{}
	}
	childTok := strings.ReplaceAll(id, "-", "")

	// Drop stale incoming edges (kept ones seed the existing-parents set).
	kept := make(map[string]struct{}, len(parents))
	dropped := 0
	body := make([]string, 0, end-start)
	body = append(body, lines[:start+1]...)
	for i := start + 1; i < end; i++ {
		if m := mermaidEdgeRe.FindStringSubmatch(strings.TrimSpace(lines[i])); len(m) >= 3 && NormalizeID(m[2]) == id {
			if _, ok := parentSet[NormalizeID(m[1])]; !ok {
				dropped++
				continue
			}
			kept[NormalizeID(m[1])] = struct{}{}
		}
		body = append(body, lines[i])
	}

	// Which nodes already have a definition line in the diagram?
	defined := make(map[string]bool)
	for _, l := range body[start+1:] {
		if m := mermaidNodeLineRe.FindStringSubmatch(l); len(m) >= 3 {
			defined[NormalizeID(m[2])] = true
		}
	}

	// Additions: missing node definitions (target first, then parents) and
	// the missing edges, placed before the first non-comment edge line (or
	// the closing fence) — addMermaidNodeAndEdges' placement rule.
	additions := make([]string, 0, 2*len(parents)+1)
	if !defined[id] {
		additions = append(additions, mermaidNodeLine(id, nodeMeta[id]))
	}
	for _, p := range parents {
		if !defined[p] {
			additions = append(additions, mermaidNodeLine(p, nodeMeta[p]))
		}
	}
	for _, p := range parents {
		if _, ok := kept[p]; !ok {
			additions = append(additions, fmt.Sprintf("    %s --> %s", strings.ReplaceAll(p, "-", ""), childTok))
		}
	}
	// Nothing to change: return the original untouched so a no-op parents
	// update never rewrites the diagram's formatting.
	if len(additions) == 0 && dropped == 0 {
		return content
	}

	insertAt := len(body) // just before the closing fence
	for i := start + 1; i < len(body); i++ {
		t := strings.TrimSpace(body[i])
		if strings.HasPrefix(t, "%%") {
			continue
		}
		if mermaidEdgeRe.MatchString(t) {
			insertAt = i
			break
		}
	}

	result := make([]string, 0, len(lines)+len(additions))
	result = append(result, body[:insertAt]...)
	result = append(result, additions...)
	result = append(result, body[insertAt:]...)
	result = append(result, lines[end:]...)
	return strings.Join(result, "\n")
}

// mermaidNodeLine renders one node-definition line, falling back to the bare
// id / open status when no reconciled metadata exists (a catalog-only node).
func mermaidNodeLine(id string, n *HypothesisNode) string {
	title, status := id, StatusOpen
	if n != nil {
		title, status = n.Title, n.Status
	}
	return fmt.Sprintf("    %s[\"%s: %s\"]:::%s", strings.ReplaceAll(id, "-", ""), id, escapeMermaidLabel(foldTitle(title)), mermaidClass(status))
}

// rewriteGraphForUpdate applies the graph-relevant parts of an update (title/
// status → Mermaid node + catalog; decision → catalog only; parents →
// incoming edges + catalog column). nodeMeta carries the reconciled node
// metadata used when a definition must be added; consulted only when Parents
// is set.
func rewriteGraphForUpdate(content, id string, upd HypothesisUpdate, nodeMeta map[string]*HypothesisNode) string {
	c := content
	if upd.Title != nil || upd.Status != nil {
		c = updateMermaidNode(c, id, upd.Title, upd.Status)
	}
	if upd.Parents != nil {
		c = syncMermaidIncomingEdges(c, id, *upd.Parents, nodeMeta)
	}
	if upd.Title != nil || upd.Status != nil || upd.Decision != nil {
		c = updateCatalogRow(c, id, upd.Title, upd.Status, upd.Decision)
	}
	if upd.Parents != nil {
		c = updateCatalogParents(c, id, *upd.Parents)
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

// joinCells joins trimmed cells into a pipe-delimited Markdown row, escaping
// every logical cell (escapeCell) so a pipe or newline inside a value cannot
// corrupt the row; splitCells reverses it on parse.
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
		// Header + separator only: insert after the separator so the first
		// catalog row lands inside the (empty) table instead of being dropped.
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

// catalogSeparatorRow locates the separator row following the catalog header
// (first cell "ID" plus a Status column), or -1 when no such pair exists —
// the caller then has no table to insert into.
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

// isCatalogHeaderRow reports a catalog header row: leading "ID" column plus a
// "Status" column (case-insensitive).
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

// addMermaidNodeAndEdges inserts a new node with its parent edges before the
// first non-comment edge line, or just before the closing fence when none.
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

// normalizeParents canonicalizes, de-duplicates, and sorts parent IDs
// (numeric order); non-identifier tokens are dropped.
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
	sort.Slice(out, func(i, j int) bool { return compareHypothesisIDs(out[i], out[j]) < 0 })
	return out
}

// childAdjacency derives the parent → children adjacency from BOTH explicit
// edges and declared node parents — the same union semantics as BuildGraph,
// so cycle checks agree with the rendered graph.
func childAdjacency(g *HypothesisGraph) map[string][]string {
	m := make(map[string][]string, len(g.Nodes))
	add := func(parent, child string) {
		m[parent] = append(m[parent], child)
	}
	for _, e := range g.Edges {
		add(e.From, e.To)
	}
	for _, n := range g.Nodes {
		for _, p := range n.Parents {
			add(p, n.ID)
		}
	}
	return m
}

// reachesChild reports whether `to` is reachable from `from` following the
// given child adjacency (iterative DFS; a malformed cyclic graph terminates
// via the seen set).
func reachesChild(children map[string][]string, from, to string) bool {
	if from == to {
		return true
	}
	seen := map[string]bool{from: true}
	stack := []string{from}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range children[cur] {
			if c == to {
				return true
			}
			if !seen[c] {
				seen[c] = true
				stack = append(stack, c)
			}
		}
	}
	return false
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

	for _, p := range listHypothesisCards(hypDir) {
		if n := parseIDNum(filepath.Base(p)); n > maxNum {
			maxNum = n
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

// -- Filesystem orchestration --

// ActiveProjectDir returns the absolute path of the active R-NNN project
// directory under a research root, using the same selection rule as
// PickActiveProject. It prefers ResearchProject.Dir — the very directory the
// parser read — so a brief/dir ID divergence or duplicate R-NNN directories
// cannot redirect mutations away from the project the panel renders; the
// legacy name-matching loop remains only as a fallback for models parsed
// before Dir existed. Errors when no project exists yet.
func ActiveProjectDir(researchRoot string) (string, error) {
	root, err := ParseResearchRoot(researchRoot)
	if err != nil {
		return "", err
	}
	active := PickActiveProject(root)
	if active == nil {
		return "", errors.New("no active research project (no R-NNN directory yet)")
	}

	if active.Dir != "" {
		return active.Dir, nil
	}

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
	return "", fmt.Errorf("active research project directory for %q not found", active.ID)
}

// ProjectDir returns the absolute path of the research project whose parsed
// ID is rid under researchRoot, resolved through the same parse the panel
// renders. For the ACTIVE research project the parsed Dir wins: duplicate
// directories can normalize to the same R-NNN, and without this preference
// the first sorted match could silently diverge from the duplicate the panel
// renders. The first-match loop remains as the fallback for non-active IDs.
// ProjectDir doubles as the ownership check for mutation callers carrying a
// caller-expected R-NNN: an id that does not live under this root is
// rejected before any file is touched.
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

// cardPathForID resolves the on-disk card file for a canonical (padded)
// hypothesis ID: the canonical <id>.md spelling when it exists, else the
// first H-*.md file whose filename normalizes to the same ID (a hand-named
// "H-1.md" for H-001), so cards created outside the writer keep working. The
// canonical path is returned when nothing matches, so a "not found" error
// names the canonical spelling.
func cardPathForID(hypDir, id string) string {
	canonical := filepath.Join(hypDir, id+".md")
	if _, err := os.Stat(canonical); err == nil {
		return canonical
	}
	for _, p := range listHypothesisCards(hypDir) {
		if NormalizeID(strings.TrimSuffix(filepath.Base(p), ".md")) == id {
			return p
		}
	}
	return canonical
}

// UpdateHypothesis applies an update to an existing hypothesis card and its
// graph entries; validation precedes every write and a failure leaves all
// files byte-for-byte unchanged (specs/domains/research.md, UpdateHypothesis
// invariants). researchRoot is the containment root every written target is
// validated against; projectDir is the active R-NNN directory
// (ActiveProjectDir).
//
// Deltas beyond the fields themselves: a real transition into a terminal
// status stamps Completed with today's date when still empty, and real
// transitions plus recorded decisions append status_change/decision entries
// to log.md — all in the same atomic write as card and graph.
func UpdateHypothesis(researchRoot, projectDir, id string, upd HypothesisUpdate) error {
	id = NormalizeID(id)
	if id == "" {
		return errors.New("invalid hypothesis id")
	}

	hypDir := filepath.Join(projectDir, "hypotheses")
	// A hand-named unpadded card file ("H-1.md") still parses to the canonical
	// ID; resolve the actual file rather than assuming the canonical spelling.
	cardPath := cardPathForID(hypDir, id)
	graphPath := filepath.Join(hypDir, "graph.md")

	// Fail closed on unreadable files: treating a present-but-unreadable
	// card or graph as missing would let the atomic rename below destroy
	// its prior content (see readFileStrict).
	cardContent, err := readFileStrict(cardPath)
	if err != nil {
		return fmt.Errorf("failed to read hypothesis card: %w", err)
	}
	if cardContent == "" {
		return fmt.Errorf("hypothesis card %s not found", id)
	}
	graphContent, err := readFileStrict(graphPath)
	if err != nil {
		return fmt.Errorf("failed to read hypothesis graph: %w", err)
	}
	if graphContent == "" {
		return fmt.Errorf("hypothesis graph not found for %s", id)
	}

	// Recover the current status for transition validation; an unparseable
	// card cannot be safely mutated.
	node, err := ParseCard(cardContent)
	if err != nil {
		return fmt.Errorf("failed to parse card %s: %w", id, err)
	}

	var newStatus HypothesisStatus
	if upd.Status != nil {
		newStatus = NormalizeStatus(*upd.Status)
		if err := ValidateTransition(node.Status, newStatus); err != nil {
			return err
		}
		s := string(newStatus)
		upd.Status = &s
	}
	// Titles are single-line labels: fold once so every written form matches.
	if upd.Title != nil {
		t := foldTitle(*upd.Title)
		upd.Title = &t
	}

	// Parents: normalize, then validate existence / self-reference / cycles
	// against the RECONCILED graph before any mutation — an invalid set
	// leaves every file byte-for-byte unchanged. The reconciled graph also
	// feeds nodeMeta for definitions syncMermaidIncomingEdges may add.
	var nodeMeta map[string]*HypothesisNode
	if upd.Parents != nil {
		parents := normalizeParents(*upd.Parents)
		for _, p := range parents {
			if p == id {
				return fmt.Errorf("hypothesis %s cannot be its own parent", id)
			}
		}
		mnodes, medges := ParseMermaidGraph(graphContent)
		g := BuildGraph(mnodes, medges, ParseCatalog(graphContent), loadCards(hypDir))
		for _, p := range parents {
			if g.Node(p) == nil {
				return fmt.Errorf("parent hypothesis %s not found", p)
			}
		}
		children := childAdjacency(&g)
		for _, p := range parents {
			// The new edge p → id cycles iff a path id ⇝ p already exists.
			if reachesChild(children, id, p) {
				return fmt.Errorf("making %s a parent of %s would create a cycle", p, id)
			}
		}
		nodeMeta = make(map[string]*HypothesisNode, len(g.Nodes))
		for _, n := range g.Nodes {
			nodeMeta[n.ID] = n
		}
		upd.Parents = &parents
	}

	newCard := rewriteCard(cardContent, id, upd)
	newGraph := rewriteGraphForUpdate(graphContent, id, upd, nodeMeta)

	// A real transition into a terminal status completes today (never
	// overwriting a recorded completion date).
	statusChanged := upd.Status != nil && newStatus != node.Status
	if statusChanged && newStatus.IsTerminal() && dashToEmpty(extractField(cardContent, "Completed")) == "" {
		newCard = setTableField(newCard, "Completed", time.Now().Format("2006-01-02"))
	}

	files := map[string][]byte{
		cardPath:  []byte(newCard),
		graphPath: []byte(newGraph),
	}

	// Research log: one status_change entry per real transition, one decision
	// entry per recorded decision — same atomic write, so card, graph, and
	// log never disagree.
	decisionLogged := upd.Decision != nil && strings.TrimSpace(*upd.Decision) != ""
	if statusChanged || decisionLogged {
		logPath := filepath.Join(projectDir, "log.md")
		// Fail closed on an unreadable log: proceeding with "" would replace
		// the whole research history with a log containing only this entry.
		logContent, readErr := readFileStrict(logPath)
		if readErr != nil {
			return fmt.Errorf("failed to read research log: %w", readErr)
		}
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

// CreateHypothesis creates a new hypothesis card (next H-NNN id) and updates
// the graph (Mermaid node + edges + catalog row) in one atomic write,
// returning the new canonical ID; concurrent callers serialize upstream
// (specs/domains/research.md). researchRoot is the containment root for
// every written target; projectDir is the active R-NNN directory
// (ActiveProjectDir).
func CreateHypothesis(researchRoot, projectDir string, nh NewHypothesis) (string, error) {
	title := strings.TrimSpace(nh.Title)
	if title == "" {
		return "", errors.New("new hypothesis requires a title")
	}
	// Titles are single-line labels everywhere they are written; fold once up front.
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
	// Contain the directory creation: a bare MkdirAll would run before the
	// writeFilesAtomic gate and could create real directories outside the
	// research root for a mis-wired projectDir. Resolving through the same
	// check first also normalizes symlinks, so the created tree is the tree
	// the writes validate against.
	resolvedHypDir, err := resolveTargetWithinRoot(researchRoot, hypDir)
	if err != nil {
		return "", err
	}
	hypDir = resolvedHypDir
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

	// Fail closed on an unreadable graph: proceeding with "" would let the
	// atomic write below replace its prior content with a fresh skeleton.
	graphContent, readErr := readFileStrict(filepath.Join(hypDir, "graph.md"))
	if readErr != nil {
		return "", fmt.Errorf("failed to read hypothesis graph: %w", readErr)
	}
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

// -- Research log (log.md) writing --

// AppendLogEntry appends one entry (experiment / decision / status_change /
// note) to <projectDir>/log.md, creating the file with the standard preamble
// when absent. The log is append-only; the write goes through writeFilesAtomic
// like every research mutation. An empty CreatedAt defaults to now (UTC RFC
// 3339); an unknown Kind or a non-ISO-8601 timestamp is an error and leaves
// the log untouched.
func AppendLogEntry(researchRoot, projectDir string, entry ResearchLogEntry) error {
	logPath := filepath.Join(projectDir, "log.md")
	// Fail closed on an unreadable log: proceeding with "" would replace the
	// whole research history with a log containing only the new entry.
	current, err := readFileStrict(logPath)
	if err != nil {
		return fmt.Errorf("failed to read research log: %w", err)
	}
	next, err := appendLogEntryContent(current, entry.Kind, entry.HypothesisID, entry.CreatedAt, entry.Message)
	if err != nil {
		return err
	}
	if err := writeFilesAtomic(researchRoot, map[string][]byte{logPath: []byte(next)}); err != nil {
		return fmt.Errorf("failed to append research log entry: %w", err)
	}
	return nil
}

// appendLogEntryContent is the pure half of AppendLogEntry: renders one entry
// onto the current content, heading per ParseLog's contract; message lines
// that would themselves parse as entry headings are demoted
// (demoteLevelTwoHeadings) so the entry body is never truncated.
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

// demoteLevelTwoHeadings rewrites "## " lines as "### ": ParseLog reserves
// level-2 headings for entry headings, so an undemoted line would terminate
// the entry and silently drop the rest of the body. Demoting is stable
// across write → parse → write.
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

// ensureGraphSkeleton guarantees the Mermaid diagram fence and the Hypothesis
// Catalog table both exist, inserting ONLY a missing section (the diagram
// before the catalog heading, the catalog after the fence) so free-form
// content in a malformed graph.md survives; a blank file gets the full
// skeleton (nothing to preserve).
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

// mermaidSection renders the standalone Diagram section inserted by
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

// catalogSection renders the standalone Hypothesis Catalog section inserted
// by ensureGraphSkeleton when a graph.md lacks a catalog table.
func catalogSection() string {
	return "## Hypothesis Catalog\n\n| ID | Hypothesis | Status | Decision | Parent(s) |\n|---|---|---|---|---|\n| *No hypotheses yet.* | | | | |\n"
}

// insertMermaidSection inserts the Diagram section before the Hypothesis
// Catalog heading (the diagram belongs above the catalog), else appends it.
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
// the Mermaid closing fence when a diagram is present, else appends it.
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

// graphSkeleton renders a minimal, well-formed graph.md matching the seeded
// research-init graph template (empty diagram classDefs + empty catalog).
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

// writeFilesAtomic stages every file as a same-directory temp (0600 → 0644),
// moves existing originals aside to dot-prefixed same-directory backups, and
// commits by rename — a staging or commit failure rolls the whole set back to
// the byte-for-byte original state; only a hard kill mid-phase can strand a
// set, and even then the original bytes survive in the adjacent backup. Full
// protocol: specs/domains/research.md (all-or-nothing mutation writes).
//
// Existing targets must be regular files: os.Rename would silently replace a
// symlinked target instead of writing through it and cannot atomically
// replace a directory. researchRoot is the containment root (SECURITY.md:
// RESEARCH artifacts stay strictly inside the workspace) — every target is
// symlink-resolved and checked before staging, and staging/renaming then use
// the RESOLVED target, so a directory swap between check and write cannot
// re-introduce a different symlink chain.
// stagedFile tracks one file of a writeFilesAtomic set: the resolved target,
// the staged temp, and (once the original is moved aside) the backup parking
// it — "" while untouched or brand-new. existed records pre-write presence.
type stagedFile struct {
	target  string
	tmp     string
	backup  string
	existed bool
}

func writeFilesAtomic(researchRoot string, files map[string][]byte) error {
	staged := make([]stagedFile, 0, len(files))
	defer func() {
		// Best-effort cleanup of anything still parked at a temp name: only
		// staging leftovers and rolled-back new contents remain there.
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

	// Pre-flight: every existing target must be a regular file (see doc).
	// Lstat, not Stat: a symlinked final component is itself the hazard.
	for i := range staged {
		info, err := os.Lstat(staged[i].target)
		switch {
		case err == nil && info.Mode().IsRegular():
			staged[i].existed = true
		case err == nil:
			return fmt.Errorf("research write target %q is not a regular file", staged[i].target)
		case errors.Is(err, fs.ErrNotExist):
			// New file: nothing to move aside, nothing to roll back.
		default:
			return fmt.Errorf("research write target %q not statable: %w", staged[i].target, err)
		}
	}

	// Phase 1 — move every original aside to a same-directory backup; a
	// failure rolls the already-moved originals back, leaving every target
	// untouched.
	for i := range staged {
		if !staged[i].existed {
			continue
		}
		backup, err := backupTargetPath(staged[i].target)
		if err == nil {
			err = os.Rename(staged[i].target, backup)
		}
		if err != nil {
			return fmt.Errorf("moving %q aside for an atomic write: %w", staged[i].target,
				errors.Join(err, rollbackStaged(staged[:i])))
		}
		staged[i].backup = backup
	}

	// Phase 2 — commit: rename every staged temp onto its now-free target; a
	// failure rolls the whole set back. The FULL set is passed, not just the
	// committed prefix: phase 1 already moved every original aside, so entries
	// after the failing index still need their backups renamed back
	// (rollbackStaged tolerates never-committed entries).
	for i := range staged {
		if err := os.Rename(staged[i].tmp, staged[i].target); err != nil {
			return fmt.Errorf("committing %q: %w", staged[i].target, errors.Join(err, rollbackStaged(staged)))
		}
	}

	// Success — drop the backups (best-effort: the mutation is committed, a
	// stuck backup must not fail it; a leftover keeps the original bytes).
	for i := range staged {
		if staged[i].backup != "" {
			_ = os.Remove(staged[i].backup)
		}
	}
	return nil
}

// rollbackStaged restores every entry of a partially committed write set: a
// committed entry gets its new contents moved back to the temp name and its
// original renamed back; an uncommitted one just needs its original (if any)
// renamed back; brand-new files lose their partial creations. Returns the
// joined errors of every failed restore — a non-nil result means some file
// may be left missing or replaced, which the caller MUST surface.
func rollbackStaged(staged []stagedFile) error {
	var restoreErrs []error
	for i := range staged {
		s := &staged[i]
		switch s.backup {
		case "":
			// Brand-new file: if the temp got as far as the target, remove it.
			if err := os.Remove(s.target); err != nil && !errors.Is(err, fs.ErrNotExist) {
				restoreErrs = append(restoreErrs, fmt.Errorf("removing rolled-back new file %q: %w", s.target, err))
			}
		default:
			// Original parked at the backup: whatever sits at the target makes
			// way for it, and the displaced bytes move to the temp name so a
			// later failure still leaves them inspectable.
			if err := os.Rename(s.target, s.tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
				restoreErrs = append(restoreErrs, fmt.Errorf("displacing rolled-back contents of %q: %w", s.target, err))
				continue
			}
			if err := os.Rename(s.backup, s.target); err != nil {
				restoreErrs = append(restoreErrs, fmt.Errorf("restoring original %q from backup: %w", s.target, err))
			}
		}
	}
	return errors.Join(restoreErrs...)
}

// backupTargetPath returns a fresh same-directory parking path
// (.<base>.bak-<random>): the dot prefix hides it from listing/globbing
// paths, the .bak suffix from *.md matches.
func backupTargetPath(target string) (string, error) {
	dir, base := filepath.Dir(target), filepath.Base(target)
	var randBytes [4]byte
	for range 3 {
		if _, err := rand.Read(randBytes[:]); err != nil {
			return "", fmt.Errorf("generating backup name for %q: %w", target, err)
		}
		candidate := filepath.Join(dir, fmt.Sprintf(".%s.bak-%s", base, hex.EncodeToString(randBytes[:])))
		if _, err := os.Lstat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("finding a free backup name for %q", target)
}

// resolveTargetWithinRoot symlink-resolves the root and the target directory
// (the file itself may not exist yet) and re-checks containment immediately
// before staging and renaming, returning the resolved path the caller may
// safely write. Fails closed, so a symlinked intermediate directory can never
// stage a temp file outside the workspace.
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
