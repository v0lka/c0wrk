// Package research parses the Iterative Engineering Research Methodology file
// catalog (research root index, per-project briefs, the hypothesis graph, and
// individual hypothesis cards) into an in-memory model that powers the
// Research panel and the research-status overview.
//
// The package is split in two:
//
//   - model.go  — the domain types plus pure derived logic (status/ID
//     normalization, graph traversal, metric computation). No I/O.
//   - parser.go — content parsing (Markdown tables, Mermaid diagrams) and the
//     thin filesystem orchestrators (ParseProject / ParseResearchRoot). The
//     only side effect in the whole package is reading the given paths.
//
// All structural logic is exposed as pure functions over strings or already
// parsed structs so it can be unit-tested in isolation without touching disk.
//
// Zero-values are meaningful: an empty HypothesisGraph is a valid partial
// state (a freshly initialized research with no hypotheses yet), and Metrics
// computed from it are all zero.
package research

import (
	"sort"
	"strings"
)

// HypothesisStatus is the lifecycle state of a hypothesis card. It is a string
// enum so it round-trips cleanly through JSON and matches the methodology's
// status vocabulary (see the research-hypothesis skill).
//
// Note the spelling difference between sources: hypothesis cards and the
// catalog table use the hyphenated form "in-progress", while the Mermaid
// diagram's CSS class is the underscored "in_progress". NormalizeStatus maps
// both spellings to the canonical StatusInProgress constant.
type HypothesisStatus string

const (
	// StatusOpen: the hypothesis has been formulated but no experiment has
	// started yet.
	StatusOpen HypothesisStatus = "open"

	// StatusInProgress: an experiment is actively running against the
	// hypothesis.
	StatusInProgress HypothesisStatus = "in-progress"

	// StatusConfirmed: the verification criterion was met (terminal).
	StatusConfirmed HypothesisStatus = "confirmed"

	// StatusRefuted: the hypothesis was falsified (terminal).
	StatusRefuted HypothesisStatus = "refuted"

	// StatusCancelled: the hypothesis was abandoned without a verdict
	// (terminal).
	StatusCancelled HypothesisStatus = "cancelled"
)

// IsTerminal reports whether the status is terminal — i.e. the hypothesis
// will no longer transition. Confirmed, refuted, and cancelled are terminal;
// open and in-progress are active. An unrecognized status is treated as
// non-terminal (the parser does not know it is finished).
func (s HypothesisStatus) IsTerminal() bool {
	switch s {
	case StatusConfirmed, StatusRefuted, StatusCancelled:
		return true
	default:
		return false
	}
}

// IsActive reports whether the hypothesis is on the active front line — open
// or in-progress. This is the set the research-status skill calls the
// "current front".
func (s HypothesisStatus) IsActive() bool {
	return s == StatusOpen || s == StatusInProgress
}

// NormalizeStatus canonicalizes a status token drawn from any source (card
// table, catalog column, or Mermaid CSS class). It lowercases the input,
// maps the Mermaid "in_progress" spelling to "in-progress", and validates it
// against the known vocabulary. An unrecognized value is returned verbatim
// (lower-cased) so callers can still render it; it is simply not one of the
// five constants above.
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
		// Unknown: return as-is (untyped) so it is not lost, but it will not
		// match any IsTerminal/IsActive predicate.
		return HypothesisStatus(s)
	}
}

// HypothesisNode is a single hypothesis parsed from an H-NNN.md card and
// reconciled with its graph/catalog entries. It is the atomic unit of the
// hypothesis DAG.
//
// Parents holds the canonical hyphenated IDs ("H-001") of the hypotheses
// whose results led to this one; it is empty for root hypotheses. The slice
// is sorted and de-duplicated by the parser.
type HypothesisNode struct {
	// ID is the canonical hypothesis identifier, e.g. "H-001" (hyphenated,
	// zero-padded to three digits per the methodology).
	ID string `json:"id"`

	// Title is the short, human-readable label used in graph nodes and
	// catalog entries.
	Title string `json:"title"`

	// Status is the normalized lifecycle status of the hypothesis.
	Status HypothesisStatus `json:"status"`

	// Parents lists the parent hypothesis IDs (canonical hyphenated form).
	Parents []string `json:"parents,omitempty"`

	// Timebox is the raw timebox field from the card (ISO 8601 duration or
	// calendar dates), preserved verbatim for rendering.
	Timebox string `json:"timebox,omitempty"`

	// Result is the recorded finding from the card's Result section (the
	// **Finding:** value). Empty until the experiment completes.
	Result string `json:"result,omitempty"`
}

// HypothesisEdge is a directed parent→child relationship in the hypothesis
// DAG. The semantics, per the research-hypothesis skill, are "the result of
// From led to the formulation of To".
type HypothesisEdge struct {
	// From is the parent hypothesis ID (canonical hyphenated form).
	From string `json:"from"`

	// To is the child hypothesis ID (canonical hyphenated form).
	To string `json:"to"`
}

// HypothesisGraph is the parsed DAG of hypotheses: the node set plus the
// explicit edges drawn in the Mermaid diagram (and/or inferred from card
// Parents fields). Nodes are kept sorted by ID for deterministic output;
// each node's Parents field is kept consistent with the edge set.
//
// A HypothesisGraph is valid in partial states — an empty graph (no nodes,
// no edges) represents a freshly initialized research that has not yet
// formulated its first hypothesis.
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

// Roots returns the root hypotheses — nodes with no parents. A node is a root
// when it has no Parents entries and no incoming edge. The result is sorted by
// ID. For an empty graph the result is nil.
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
	sort.Slice(roots, func(i, j int) bool { return roots[i].ID < roots[j].ID })
	return roots
}

// levels computes, for every node, the length of the longest path from any
// root to that node (a root has level 0). It is the foundation for both graph
// depth (max level) and graph breadth (max width per level).
//
// The methodology guarantees a DAG, but the parser defends against malformed
// input (e.g. a cycle introduced by a hand-edited graph) with a recursion
// stack: a node currently being resolved is treated as level 0 for that
// branch, breaking the cycle rather than recursing forever.
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

		// Seed from explicit edges, then fold in any Parents declared on the
		// node itself (a card may list a parent that has no drawn edge).
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

// Metrics are the computed progress indicators for a research project, as
// defined by the research-status skill (Step 3). All fields are meaningful
// for partial states: a research with no hypotheses yields all-zero metrics.
type Metrics struct {
	// Total is the total number of hypotheses in the graph.
	Total int `json:"total"`

	// ByStatus counts hypotheses grouped by lifecycle status. The map is
	// non-nil even when empty.
	ByStatus map[HypothesisStatus]int `json:"by_status"`

	// ConfirmationRate is confirmed / (confirmed + refuted), in [0, 1]. It
	// excludes open, in-progress, and cancelled hypotheses. It is 0 when no
	// hypothesis has reached a confirmed/refuted verdict.
	ConfirmationRate float64 `json:"confirmation_rate"`

	// Depth is the longest root→leaf path measured in edges. A lone root has
	// depth 0; an empty graph has depth 0.
	Depth int `json:"depth"`

	// Breadth is the maximum number of non-terminal (active) nodes sharing a
	// single depth level — i.e. the widest active layer of parallel
	// investigation. It is 0 when no active nodes exist.
	Breadth int `json:"breadth"`

	// ActiveFront is the sorted list of IDs of hypotheses that are open or
	// in-progress — the current leading edge of the research.
	ActiveFront []string `json:"active_front,omitempty"`
}

// ComputeMetrics derives the progress metrics from a parsed hypothesis graph.
// It is a pure function over the graph (no I/O) and is safe to call on a
// partial/empty graph. The returned ByStatus map is always non-nil.
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
	sort.Strings(activeFront)
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

// LogKind is the category of a research-log entry. It is a string enum so it
// round-trips cleanly through JSON and matches the research log's kind
// vocabulary (see ParseLog). Each entry in a project's log.md is tagged with
// exactly one kind.
type LogKind string

const (
	// LogKindExperiment records the outcome of an experiment run against a
	// hypothesis.
	LogKindExperiment LogKind = "experiment"

	// LogKindDecision records an iteration decision (continue / pivot / kill /
	// fork) made after reviewing results.
	LogKindDecision LogKind = "decision"

	// LogKindStatusChange records a hypothesis status transition.
	LogKindStatusChange LogKind = "status_change"

	// LogKindNote records a free-form observation or note.
	LogKindNote LogKind = "note"
)

// ResearchLogEntry is a single timestamped entry parsed from a project's
// log.md file. It is the atomic unit of the research-history timeline.
//
// ID is the stable, 1-based ordinal assigned by ParseLog in file order (logs
// are append-only, so the ordinal is stable for a given file). HypothesisID is
// the canonical identifier ("H-001") of the hypothesis the entry pertains to;
// it is empty for entries that are not tied to a specific hypothesis (e.g.
// project-level decisions or notes). CreatedAt is the raw timestamp token
// (ISO 8601) preserved verbatim, mirroring how cards preserve their Timebox.
type ResearchLogEntry struct {
	// ID is the 1-based ordinal of the entry in the log (assigned by ParseLog).
	ID string `json:"id"`

	// Kind is the entry's category: experiment, decision, status_change, or
	// note.
	Kind LogKind `json:"kind"`

	// HypothesisID is the canonical hypothesis identifier ("H-001") this entry
	// refers to, or empty when the entry is project-scoped.
	HypothesisID string `json:"hypothesis_id,omitempty"`

	// Message is the free-form body of the entry (may span multiple lines).
	Message string `json:"message"`

	// CreatedAt is the raw timestamp token from the log entry heading.
	CreatedAt string `json:"created_at"`
}
