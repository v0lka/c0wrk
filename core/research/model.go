// Package research parses the Iterative Engineering Research Methodology file
// catalog (index, briefs, hypothesis graph, cards) into the in-memory model
// behind the Research panel: model.go — domain types and pure derived logic
// (no I/O); parser.go — content parsing and the thin read-only filesystem
// orchestrators. Structural logic is pure and unit-testable without disk.
// Zero-values are meaningful: an empty graph is a valid partial state.
package research

import (
	"sort"
	"strings"
)

// HypothesisStatus is the lifecycle state of a hypothesis card (a string enum
// matching the methodology's vocabulary). Note the spelling split: cards and
// the catalog use "in-progress", the Mermaid CSS class "in_progress" —
// NormalizeStatus maps both to StatusInProgress.
type HypothesisStatus string

const (
	// StatusOpen: formulated, no experiment started yet.
	StatusOpen HypothesisStatus = "open"

	// StatusInProgress: an experiment is actively running.
	StatusInProgress HypothesisStatus = "in-progress"

	// StatusConfirmed: verification criterion met (terminal).
	StatusConfirmed HypothesisStatus = "confirmed"

	// StatusRefuted: falsified (terminal).
	StatusRefuted HypothesisStatus = "refuted"

	// StatusCancelled: abandoned without a verdict (terminal).
	StatusCancelled HypothesisStatus = "cancelled"
)

// IsTerminal: confirmed/refuted/cancelled. An unrecognized status is
// non-terminal (the parser cannot know it is finished).
func (s HypothesisStatus) IsTerminal() bool {
	switch s {
	case StatusConfirmed, StatusRefuted, StatusCancelled:
		return true
	default:
		return false
	}
}

// IsActive: open or in-progress — the research-status skill's "current front".
func (s HypothesisStatus) IsActive() bool {
	return s == StatusOpen || s == StatusInProgress
}

// NormalizeStatus canonicalizes a status token from any source: lower-cased,
// the Mermaid "in_progress" spelling mapped to "in-progress". An
// unrecognized value is returned verbatim so callers can still render it —
// it simply matches no IsTerminal/IsActive predicate.
func NormalizeStatus(raw string) HypothesisStatus {
	s := strings.ToLower(strings.TrimSpace(raw))
	// Mermaid CSS classes use underscores; everything else uses hyphens.
	if s == "in_progress" {
		s = "in-progress"
	}
	switch HypothesisStatus(s) {
	case StatusOpen, StatusInProgress, StatusConfirmed, StatusRefuted, StatusCancelled:
		return HypothesisStatus(s)
	default:
		return HypothesisStatus(s)
	}
}

// HypothesisNode is one hypothesis parsed from an H-NNN.md card and
// reconciled with its graph/catalog entries — the atomic unit of the DAG.
// Parents holds the canonical IDs of the hypotheses whose results led to
// this one (empty for roots); sorted and de-duplicated by the parser.
type HypothesisNode struct {
	// ID is the canonical identifier ("H-001").
	ID string `json:"id"`

	// Title is the short human-readable label.
	Title string `json:"title"`

	Status HypothesisStatus `json:"status"`

	Parents []string `json:"parents,omitempty"`

	// Timebox is the card's raw timebox value, preserved verbatim.
	Timebox string `json:"timebox,omitempty"`

	// Completed is the card's completion date (YYYY-MM-DD); empty while
	// open/in-progress.
	Completed string `json:"completed,omitempty"`

	// Result is the card's recorded finding (**Finding:** value).
	Result string `json:"result,omitempty"`

	// Statement is the card's "## Statement" body, preserved verbatim.
	Statement string `json:"statement,omitempty"`

	// VerificationCriterion is the card's "## Verification Criterion" body.
	VerificationCriterion string `json:"verification_criterion,omitempty"`

	// ExperimentNotes is the card's "## Experiment Notes" body.
	ExperimentNotes string `json:"experiment_notes,omitempty"`

	// Decision is the iteration decision (continue / pivot / kill / fork)
	// from the card's **Decision** row; empty while undecided.
	Decision string `json:"decision,omitempty"`
}

// HypothesisEdge is a directed parent→child edge: per the research-hypothesis
// skill, "the result of From led to the formulation of To".
type HypothesisEdge struct {
	// From is the parent hypothesis ID (canonical hyphenated form).
	From string `json:"from"`

	// To is the child hypothesis ID (canonical hyphenated form).
	To string `json:"to"`
}

// HypothesisGraph is the parsed DAG: nodes plus edges drawn in the diagram
// and/or inferred from card Parents. Nodes stay sorted; Parents stays
// consistent with the edge set. Valid in partial states — an empty graph is
// a freshly initialized research.
type HypothesisGraph struct {
	// Nodes is the set of hypothesis nodes, sorted by ID.
	Nodes []*HypothesisNode `json:"nodes"`

	// Edges is the set of parent→child edges, deterministically ordered.
	Edges []HypothesisEdge `json:"edges"`
}

// Node returns the hypothesis node with the given canonical ID, or nil if no
// such node exists. Lookup is by exact ID match.
func (g *HypothesisGraph) Node(id string) *HypothesisNode {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// Roots returns parent-less nodes (no Parents, no incoming edge), sorted;
// nil for an empty graph.
func (g *HypothesisGraph) Roots() []*HypothesisNode {
	incoming := make(map[string]struct{}, len(g.Nodes))
	for _, e := range g.Edges {
		incoming[e.To] = struct{}{}
	}
	hasParent := func(n *HypothesisNode) bool {
		if _, ok := incoming[n.ID]; ok {
			return true
		}
		return len(n.Parents) > 0
	}
	var roots []*HypothesisNode
	for _, n := range g.Nodes {
		if !hasParent(n) {
			roots = append(roots, n)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return compareHypothesisIDs(roots[i].ID, roots[j].ID) < 0 })
	return roots
}

// levels computes each node's longest path from any root (root = 0) — the
// basis for graph depth and breadth. The methodology guarantees a DAG, but a
// cycle in malformed input is broken by treating an in-resolution node as
// level 0 rather than recursing forever.
func (g *HypothesisGraph) levels() map[string]int {
	// child → parents, derived from explicit edges.
	parentsOf := make(map[string][]string, len(g.Nodes))
	for _, e := range g.Edges {
		parentsOf[e.To] = append(parentsOf[e.To], e.From)
	}

	level := make(map[string]int, len(g.Nodes))
	inProgress := make(map[string]bool, len(g.Nodes))

	var resolve func(id string) int
	resolve = func(id string) int {
		if v, ok := level[id]; ok {
			return v
		}
		if inProgress[id] {
			// Cycle guard: collapse the back-edge to level 0.
			return 0
		}
		inProgress[id] = true
		defer delete(inProgress, id)

		// Explicit edges first, then Parents declared on the node itself.
		ps := parentsOf[id]
		if n := g.Node(id); n != nil {
			ps = append(ps, n.Parents...)
		}

		maxParent := -1
		for _, p := range ps {
			if l := resolve(p); l > maxParent {
				maxParent = l
			}
		}
		lvl := maxParent + 1 // roots (no parents) → 0
		level[id] = lvl
		return lvl
	}

	for _, n := range g.Nodes {
		resolve(n.ID)
	}
	return level
}

// Metrics are the computed progress indicators defined by the research-status
// skill (specs/domains/research.md). All fields are meaningful for partial
// states: no hypotheses → all zero.
type Metrics struct {
	Total int `json:"total"`

	// ByStatus counts by lifecycle status (non-nil even when empty).
	ByStatus map[HypothesisStatus]int `json:"by_status"`

	// ConfirmationRate is confirmed / (confirmed + refuted); 0 before any
	// verdict (open/in-progress/cancelled excluded).
	ConfirmationRate float64 `json:"confirmation_rate"`

	// Depth is the longest root→leaf path in edges (0 for a lone root).
	Depth int `json:"depth"`

	// Breadth is the widest depth level of non-terminal nodes; 0 when none.
	Breadth int `json:"breadth"`

	// ActiveFront is the sorted set of open/in-progress hypothesis IDs.
	ActiveFront []string `json:"active_front,omitempty"`
}

// ComputeMetrics derives the metrics from a graph (pure; safe on empty); the
// ByStatus map is always non-nil.
func ComputeMetrics(g *HypothesisGraph) Metrics {
	m := Metrics{ByStatus: make(map[HypothesisStatus]int)}
	if g == nil || len(g.Nodes) == 0 {
		return m
	}

	m.Total = len(g.Nodes)
	var confirmed, refuted int
	activeFront := make([]string, 0)
	for _, n := range g.Nodes {
		m.ByStatus[n.Status]++
		switch n.Status {
		case StatusConfirmed:
			confirmed++
		case StatusRefuted:
			refuted++
		}
		if n.Status.IsActive() {
			activeFront = append(activeFront, n.ID)
		}
	}
	// Numeric order: lexicographic would put H-1000 before H-999, flipping
	// the leading entry RecommendNextStep targets.
	sort.Slice(activeFront, func(i, j int) bool { return compareHypothesisIDs(activeFront[i], activeFront[j]) < 0 })
	m.ActiveFront = activeFront

	decided := confirmed + refuted
	if decided > 0 {
		m.ConfirmationRate = float64(confirmed) / float64(decided)
	}

	levels := g.levels()
	depth := 0
	widthPerLevel := make(map[int]int)
	for id, lvl := range levels {
		if lvl > depth {
			depth = lvl
		}
		if n := g.Node(id); n != nil && !n.Status.IsTerminal() {
			widthPerLevel[lvl]++
		}
	}
	m.Depth = depth
	breadth := 0
	for _, w := range widthPerLevel {
		if w > breadth {
			breadth = w
		}
	}
	m.Breadth = breadth
	return m
}

// LogKind is a research-log entry's category (ParseLog's kind vocabulary);
// each log.md entry carries exactly one.
type LogKind string

const (
	// LogKindExperiment: outcome of an experiment run.
	LogKindExperiment LogKind = "experiment"

	// LogKindDecision: iteration decision after reviewing results.
	LogKindDecision LogKind = "decision"

	// LogKindStatusChange: hypothesis status transition.
	LogKindStatusChange LogKind = "status_change"

	// LogKindNote: free-form observation.
	LogKindNote LogKind = "note"
)

// ResearchLogEntry is one timestamped log.md entry. ID is the stable 1-based
// ordinal assigned by ParseLog in file order (append-only, so stable per
// file); HypothesisID is empty for project-scoped entries; CreatedAt is the
// raw ISO 8601 token preserved verbatim.
type ResearchLogEntry struct {
	ID string `json:"id"`

	Kind LogKind `json:"kind"`

	HypothesisID string `json:"hypothesis_id,omitempty"`

	Message string `json:"message"`

	CreatedAt string `json:"created_at"`
}
