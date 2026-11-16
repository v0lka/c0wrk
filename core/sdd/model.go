package sdd

// The parsed model of one specification tree.
//
// parse.go reads a workspace through a Framework: it walks the framework's
// declared roots, classifies what it finds against the declared concepts,
// extracts the metadata each concept declares, renders the declared views and
// runs the declared checks. Nothing here is c0wrk-specific — the model is the
// same shape for c0wrk's own specs/ tree, for OpenSpec's openspec/ tree and
// for Spec Kit's specs/ + .specify/ pair, and an operator framework added
// through the definition files parses into exactly the same structures.
//
// This file holds the model only (no I/O); parse.go holds the reader.

// Node kinds. A tree view is a forest of these: intermediate directories of a
// root, concept instances that are files, and concept instances that are
// directories (a change folder, a capability folder, a feature folder).
const (
	// NodeFolder is an intermediate directory inside a root that no concept
	// declares — the grouping level a tree needs to nest instances.
	NodeFolder = "folder"
	// NodeDocument is a concept instance that is a file.
	NodeDocument = "document"
	// NodeDirectory is a concept instance that is a directory (a concept with
	// `match.dir: true`).
	NodeDirectory = "directory"
)

// Tree is the parsed model of one workspace through one framework.
type Tree struct {
	// Path is the workspace root exactly as passed to ParseTree.
	Path string

	// Framework is the framework's ID and Label, carried so a surface can name
	// what it rendered.
	Framework string
	Label     string

	// Present reports whether the framework counts as installed here: at least
	// one declared root existed as a directory AND every REQUIRED root did. A
	// workspace with none of the framework's roots parses to Present=false with
	// everything else empty — the neutral "framework not installed here" state,
	// never an error. An absent required root also yields Present=false, while
	// an absent optional root does not; whatever was readable is still carried,
	// so a partial tree stays diagnosable.
	Present bool

	// Roots reports each declared root's presence, in declaration order, so a
	// partial tree stays diagnosable.
	Roots []RootState

	// Documents holds every concept instance that is a file, sorted by path.
	// A file no concept matches is outside the model entirely.
	Documents []Document

	// Nodes is the flat forest the tree views render, sorted by path: folder
	// nodes plus concept instances. Views index into it by ID.
	Nodes []Node

	// Views holds one entry per declared view section, in declaration order.
	Views []View

	// Findings holds the declarations' check results, sorted by kind, then
	// file, then detail, deduplicated. It is empty when the framework declares
	// no checks or every check passed: a finding exists only because a
	// DECLARED check failed, the counted truncation summary below being the one
	// deliberate exception. The collection is bounded like Warnings — the
	// first maxParseFindings findings in emission order, then one counted
	// "further findings suppressed" summary that carries the truncated class's
	// token and severity.
	Findings []Finding

	// Counts aggregates the collections for badges and filters.
	Counts Counts

	// Warnings holds non-fatal read diagnostics (an escaping root, an
	// unreadable or oversized document, a spent budget). Warnings never become
	// findings — a finding exists only because a declared check failed, the
	// counted truncation summary aside — and the slice is capped (the first
	// maxParseWarnings lines, then one counted summary), because it is part of
	// the model and crosses the RPC boundary.
	Warnings []string
}

// RootState is one declared root's presence in the parsed workspace.
type RootState struct {
	// ID is the Root.ID concepts address the root by.
	ID string
	// Path is the root's workspace-relative path, slash separated.
	Path string
	// Label is the declared human name; "" when the document omits one.
	Label string
	// Required mirrors Root.Required.
	Required bool
	// Present reports whether the path existed as a directory.
	Present bool
}

// Document is one concept instance that is a file, with the metadata its
// concept declared extracted from it. Only declared directives are populated:
// a concept that does not extract headings keeps Headings nil even when the
// file is full of them.
type Document struct {
	// Path is the workspace-relative, slash-separated path — the path a file
	// viewer opens.
	Path string
	// Root is the Root.ID the document was found under.
	Root string
	// Concept is the Concept.Kind that matched the document.
	Concept string
	// Label is that concept's declared human name; "" when it has none.
	Label string
	// Section is the document's top-level directory inside its root
	// ("architecture" for architecture/layers.md); "" for a document sitting
	// directly in the root.
	Section string
	// Name is the base name (the file name for a document, the directory name
	// for a directory instance).
	Name string
	// Number is the leading numeric token of Name ("106" for
	// 106-specs-sidebar-panel.md, "001" for a 001-feature directory); "" when
	// the name carries none.
	Number string
	// Slug is Name without its extension and without the leading Number token
	// ("specs-sidebar-panel").
	Slug string

	// Title is the first ATX heading of the document, verbatim, when `title`
	// is extracted.
	Title string
	// DisplayTitle is Title with the document's own identity prefix removed
	// — "# ADR-001: One" in 001-one.md becomes "One", because the number
	// already travels beside the record as its identifier. It equals Title
	// when the heading carries no such prefix, and it is what a record card
	// shows.
	DisplayTitle string
	// Headings holds every ATX heading outside fenced code blocks, in document
	// order, when `headings` is extracted.
	Headings []string
	// KeyFiles holds the backticked entries of the "## Key Files" section, in
	// document order, when `key_files` is extracted.
	KeyFiles []string
	// CrossRefs holds every relative markdown link target in document order,
	// verbatim (a path with an optional #anchor, or a pure #anchor), when
	// `cross_refs` is extracted.
	CrossRefs []string
	// Frontmatter holds the document's leading YAML frontmatter as flat string
	// values, when `frontmatter` is extracted. A document without frontmatter
	// keeps a nil map.
	Frontmatter map[string]string

	// Status is the reduced classification of the "## Status" section's first
	// line — its first word lower-cased ("accepted", "superseded") — when
	// `status` is extracted.
	Status string
	// SupersededBy is the successor token a superseded status line names
	// ("001" for "[ADR-001](./001-one.md)"), when `superseded_by` is
	// extracted. "" when the status is not a supersession or names nobody.
	SupersededBy string

	// Purpose is the body of the first section whose heading carries the word
	// "purpose", when `purpose` is extracted.
	Purpose string
	// Requirements, Scenarios, Deltas, Principles and UserStories hold the
	// entries of the corresponding sections/headings, in document order, when
	// the same-named directive is extracted. An entry is a heading carrying the
	// topic word that is not the section's CONTAINER heading ("## User
	// Stories"), or a list item directly under that container: a leaf entry
	// heading ("### User Story 1 …") is itself the entry, so its own list is
	// that entry's detail and never a sibling entry.
	Requirements []string
	Scenarios    []string
	Deltas       []string
	Principles   []string
	UserStories  []string
	// TaskItems holds the checkbox items ("- [ ] text", "- [x] text") in
	// document order, when `task_items` is extracted.
	TaskItems []TaskItem
	// Tasks holds the navigation table of the document's first task-headed
	// section, when `index_tasks` is extracted.
	Tasks []IndexTask

	// Missing holds the concept's declared `required` tokens the document does
	// not satisfy, in declaration order. It is the per-document completeness
	// signal; findings stay the checks' output.
	Missing []string
}

// TaskItem is one checkbox line of a task-list document.
type TaskItem struct {
	// Text is the item text after the checkbox, trimmed.
	Text string
	// Done reports a ticked box ("- [x]" / "- [X]").
	Done bool
}

// IndexTask is one row of a navigation table — the task-to-documents index a
// framework's index document carries.
type IndexTask struct {
	// Task is the row's first cell, verbatim (trimmed).
	Task string
	// Specs holds the row's second cell's relative link targets in document
	// order, verbatim (anchors included).
	Specs []string
}

// Finding is one failed declared check.
type Finding struct {
	// Kind is the check's declared `finding` token (e.g. PATH_MISSING, or any
	// token an operator framework declares) — an open string, because the
	// vocabulary belongs to the framework document, not to this package.
	Kind string
	// File is the subject of the finding: the workspace-relative path of the
	// document (or directory) the check looked at, or the path a root-level
	// `required_files` check found missing.
	File string
	// Detail is the concrete observation, computed from the tree; identical
	// input yields identical detail.
	Detail string
	// Message is the check's declared operator-facing explanation; "" when the
	// document declares none.
	Message string
	// Severity is the check's declared severity (SeverityError when omitted).
	Severity string
}

// Node is one entry of the rendered tree: an intermediate folder, a document,
// or a directory concept instance.
type Node struct {
	// ID is the node's identity — its workspace-relative path.
	ID string
	// Kind is NodeFolder, NodeDocument or NodeDirectory.
	Kind string
	// Path is the workspace-relative, slash-separated path the node stands for.
	Path string
	// Label is the display name of the INSTANCE: a document's parsed title
	// (falling back to its file name), or a directory's name. The concept's own
	// label is deliberately not used here — eight ADRs must read as eight
	// different rows — and stays available on the model through Document.Label.
	Label string
	// Concept is the Concept.Kind for an instance; "" for a folder.
	Concept string
	// Name is the base name (the file name for a document, the directory name
	// otherwise).
	Name string
	// Parent is the parent node's ID; "" at the top of a root.
	Parent string
	// Children holds the child node IDs, sorted by path.
	Children []string
	// Missing holds the companion files a directory instance declares but does
	// not carry (Concept.Files); nil for every other node.
	Missing []string
}

// View is one declared view section.
type View struct {
	// ID and Label are the section's declared identity and heading.
	ID    string
	Label string
	// Kind is the declared section kind: SectionTree, SectionFindings or
	// SectionChecks.
	Kind string
	// Nodes holds the top-level node IDs of a tree section, in declaration
	// order; nil for the findings and checks sections, which render model
	// collections rather than nodes.
	Nodes []string
	// Members holds EVERY node ID this section owns — its concepts'
	// instances plus the ancestor folders they hang under — so a consumer
	// can render the section's own subtree instead of the shared forest:
	// two sections sharing an ancestor folder stop bleeding each other's
	// concepts through it. nil for the findings and checks sections.
	Members []string
}

// Counts aggregates a tree for badges and filters.
type Counts struct {
	// Documents and Findings are the sizes of the corresponding collections.
	// Findings therefore counts RETAINED findings: once the retention cap is
	// reached it stays at the cap plus the one counted summary, and the rest of
	// the model says so instead of the count growing without bound.
	Documents int
	Findings  int
	// ByConcept counts instances per Concept.Kind — directory instances
	// included, so a change folder is counted once.
	ByConcept map[string]int
	// FindingsByKind counts RETAINED findings per declared finding token; the
	// dropped ones are named by the truncation summary's Detail, not here.
	FindingsByKind map[string]int
}

// DocumentByPath returns the document at a workspace-relative path.
func (t *Tree) DocumentByPath(path string) (Document, bool) {
	for _, doc := range t.Documents {
		if doc.Path == path {
			return doc, true
		}
	}
	return Document{}, false
}

// DocumentsOfConcept returns the documents of one concept kind, in path order.
func (t *Tree) DocumentsOfConcept(kind string) []Document {
	var out []Document
	for _, doc := range t.Documents {
		if doc.Concept == kind {
			out = append(out, doc)
		}
	}
	return out
}

// NodeByID returns the tree node with the given ID.
func (t *Tree) NodeByID(id string) (Node, bool) {
	for _, node := range t.Nodes {
		if node.ID == id {
			return node, true
		}
	}
	return Node{}, false
}

// FindingsOf adds every finding of one declared token, in model order.
func (t *Tree) FindingsOf(kind string) []Finding {
	var out []Finding
	for _, f := range t.Findings {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}
