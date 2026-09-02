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
// Card content rewriting (pure)
// ---------------------------------------------------------------------------

// cardHeadingLineRe matches a card H1 like "# H-001: Short title".
var cardHeadingLineRe = regexp.MustCompile(`^#\s+(H-?\d+)(?::\s*(.*))?$`)

// setCardHeading replaces the H1 title of a card with the given canonical ID.
func setCardHeading(content, id, title string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		m := cardHeadingLineRe.FindStringSubmatch(line)
		if m == nil || NormalizeID(m[1]) != id {
			continue
		}
		lines[i] = fmt.Sprintf("# %s: %s", id, title)
		return strings.Join(lines, "\n")
	}
	return content
}

// setTableField sets (or inserts) a two-column field row "| **Field** | value |"
// in the card's front-matter table. It returns the updated content. When the
// field row does not exist it is inserted after the last existing field row,
// so updating e.g. "Decision" on a card that never recorded one still works.
func setTableField(content, fieldName, value string) string {
	target := "**" + fieldName + "**"
	lines := strings.Split(content, "\n")

	// Replace an existing row.
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cells := splitCells(line)
		if len(cells) >= 2 && cells[0] == target {
			lines[i] = "| " + target + " | " + value + " |"
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
	if lastFieldRow == -1 {
		return content
	}
	newRow := "| " + target + " | " + value + " |"
	result := make([]string, 0, len(lines)+1)
	result = append(result, lines[:lastFieldRow+1]...)
	result = append(result, newRow)
	result = append(result, lines[lastFieldRow+1:]...)
	return strings.Join(result, "\n")
}

// setFinding sets the card's recorded finding (**Finding:** line). When no
// finding line exists yet it is inserted into the Result section (or appended
// as a fresh Result section when the card has none).
func setFinding(content, value string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(line, "**Finding:**") {
			lines[i] = "**Finding:** " + value
			return strings.Join(lines, "\n")
		}
	}

	// No finding line: insert after the "## Result" heading if present.
	for i, line := range lines {
		if sectionHeadingLower(line) == "result" {
			result := make([]string, 0, len(lines)+1)
			result = append(result, lines[:i+1]...)
			result = append(result, "", "**Finding:** "+value)
			result = append(result, lines[i+1:]...)
			return strings.Join(result, "\n")
		}
	}

	// No Result section at all: append one.
	return strings.TrimRight(content, "\n") + "\n\n## Result\n\n**Finding:** " + value + "\n"
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
// Mermaid diagram (matching by canonical ID).
func updateMermaidNode(content, id string, title, status *string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		m := mermaidNodeLineRe.FindStringSubmatch(line)
		if m == nil || NormalizeID(m[2]) != id {
			continue
		}
		label := m[3]
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
		lines[i] = m[1] + m[2] + `["` + id + ": " + curTitle + `"]` + suffix
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
	return joinCells([]string{link, title, st, dec, par})
}

// joinCells re-joins trimmed cells into a pipe-delimited Markdown row with
// surrounding spaces, matching the canonical catalog/table formatting.
func joinCells(cells []string) string {
	return "| " + strings.Join(cells, " | ") + " |"
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
		return content
	}
	result := make([]string, 0, len(lines)+1)
	result = append(result, lines[:lastDataRow+1]...)
	result = append(result, newRow)
	result = append(result, lines[lastDataRow+1:]...)
	return strings.Join(result, "\n")
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
	nodeLine := fmt.Sprintf("    %s[\"%s: %s\"]:::%s", nodeToken, id, title, mermaidClass(status))
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
	fmt.Fprintf(&b, "# %s: %s\n\n", id, title)
	b.WriteString("| Field | Value |\n")
	b.WriteString("|---|---|\n")
	fmt.Fprintf(&b, "| **Identifier** | %s |\n", id)
	fmt.Fprintf(&b, "| **Status** | %s |\n", string(status))
	fmt.Fprintf(&b, "| **Timebox** | %s |\n", timeboxVal)
	fmt.Fprintf(&b, "| **Parent(s)** | %s |\n", parentsVal)
	fmt.Fprintf(&b, "| **Created** | %s |\n", created)
	b.WriteString("| **Completed** | — |\n")
	fmt.Fprintf(&b, "| **Decision** | %s |\n", decisionVal)
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
	max := 0

	if paths, err := filepath.Glob(filepath.Join(hypDir, "H-*.md")); err == nil {
		for _, p := range paths {
			if n := parseIDNum(filepath.Base(p)); n > max {
				max = n
			}
		}
	}
	if content, ok := readFile(filepath.Join(hypDir, "graph.md")); ok {
		for _, c := range ParseCatalog(content) {
			if n := parseIDNum(c.id); n > max {
				max = n
			}
		}
		mnodes, _ := ParseMermaidGraph(content)
		for _, mn := range mnodes {
			if n := parseIDNum(mn.id); n > max {
				max = n
			}
		}
	}
	return max
}

// ---------------------------------------------------------------------------
// Filesystem orchestration
// ---------------------------------------------------------------------------

// ActiveProjectDir returns the absolute path of the active R-NNN project
// directory under a research root, using the same selection rule as
// PickActiveProject (latest index entry, else highest-numbered directory).
// It handles both the canonical nested layout (R-NNN-short-name/) and the flat
// single-project layout. It returns an error when no project exists yet.
func ActiveProjectDir(researchRoot string) (string, error) {
	root, err := ParseResearchRoot(researchRoot)
	if err != nil {
		return "", err
	}
	active := PickActiveProject(root)
	if active == nil {
		return "", errors.New("no active research project (no R-NNN directory yet)")
	}

	entries, err := os.ReadDir(researchRoot)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if normalizeResearchID(e.Name()) == active.ID {
			return filepath.Join(researchRoot, e.Name()), nil
		}
	}
	// Flat single-project layout: the root itself is the project.
	if isFlatProjectRoot(researchRoot) {
		return researchRoot, nil
	}
	return "", fmt.Errorf("active research project directory for %q not found", active.ID)
}

// UpdateHypothesis applies an update to an existing hypothesis card and its
// graph entries. It is atomic-ish: validation (including the status-transition
// check) happens before any write, and each file is written via temp+rename.
func UpdateHypothesis(projectDir, id string, upd HypothesisUpdate) error {
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
	if upd.Status != nil {
		ns := NormalizeStatus(*upd.Status)
		if err := ValidateTransition(node.Status, ns); err != nil {
			return err
		}
		s := string(ns)
		upd.Status = &s
	}

	newCard := rewriteCard(cardContent, id, upd)
	newGraph := rewriteGraphForUpdate(graphContent, id, upd)

	if err := writeFilesAtomic(map[string][]byte{
		cardPath:  []byte(newCard),
		graphPath: []byte(newGraph),
	}); err != nil {
		return fmt.Errorf("failed to write hypothesis update: %w", err)
	}
	return nil
}

// CreateHypothesis creates a new hypothesis card (assigning the next H-NNN id)
// and updates the graph (Mermaid node + edges + catalog row). It returns the
// new canonical ID.
func CreateHypothesis(projectDir string, nh NewHypothesis) (string, error) {
	title := strings.TrimSpace(nh.Title)
	if title == "" {
		return "", errors.New("new hypothesis requires a title")
	}

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

	if err := writeFilesAtomic(map[string][]byte{
		filepath.Join(hypDir, id+".md"):   []byte(cardContent),
		filepath.Join(hypDir, "graph.md"): []byte(newGraph),
	}); err != nil {
		return "", fmt.Errorf("failed to write new hypothesis: %w", err)
	}
	return id, nil
}

// ensureGraphSkeleton returns graphContent when it already contains both the
// Mermaid diagram fence and a Hypothesis Catalog table (the two structures the
// mutation helpers operate on), and otherwise a fresh skeleton containing both.
// This guarantees CreateHypothesis never writes an empty or malformed graph.md
// when the file is missing or corrupted: the node/edge and catalog-row helpers
// are no-ops when their target structure is absent, so a missing graph would
// otherwise be written back out empty.
func ensureGraphSkeleton(graphContent string) string {
	if strings.Contains(graphContent, "```mermaid") && strings.Contains(graphContent, "Hypothesis Catalog") {
		return graphContent
	}
	return graphSkeleton()
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
func writeFilesAtomic(files map[string][]byte) error {
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

	for _, p := range paths {
		tmp, err := os.CreateTemp(filepath.Dir(p), ".hyp-*.tmp")
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
		staged = append(staged, stagedFile{target: p, tmp: tmpName})
	}

	for _, s := range staged {
		if err := os.Rename(s.tmp, s.target); err != nil {
			return err
		}
	}
	return nil
}
