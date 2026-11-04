// Package specs parses a workspace's specification tree — the specs/
// directory whose layout and document formats specs/META.md defines — into
// the in-memory model behind the Specs sidebar panel: model.go — domain
// types (no I/O); parse.go — content parsing and the thin read-only
// filesystem orchestrator. The parser keys off the documented layout, not
// c0wrk-specific paths, and tolerates a missing, empty, or partial tree:
// zero-values are meaningful neutral states, never errors
// (specs/decisions/105-specs-sidebar-panel.md).
package specs

// Section names: the four documented section directories of a specs/ tree
// (META.md "File Organization"). The empty SpecFile.Section marks the root
// files INDEX.md and META.md, which belong to no section.
const (
	SectionArchitecture = "architecture"
	SectionDomains      = "domains"
	SectionContracts    = "contracts"
	SectionDecisions    = "decisions"
)

// FindingKind is one tier-1 static finding category (ADR-105 D5): computed
// by the parser on every parse, never persisted, always fresh.
type FindingKind string

const (
	// FindingPathMissing: a "## Key Files" entry that looks like a
	// workspace-root-relative path does not exist under the specs root's
	// parent directory.
	FindingPathMissing FindingKind = "PATH_MISSING"
	// FindingCrossRefBroken: a relative markdown link whose target file or
	// #anchor does not resolve.
	FindingCrossRefBroken FindingKind = "CROSS_REF_BROKEN"
	// FindingDuplicateADRNumber: two decisions/NNN-*.md files share the
	// leading NNN.
	FindingDuplicateADRNumber FindingKind = "DUPLICATE_ADR_NUMBER"
	// FindingIndexStale: a spec file under the four sections that the
	// INDEX.md directory listing does not name.
	FindingIndexStale FindingKind = "INDEX_STALE"
	// FindingRootFileMissing: a documented root file (INDEX.md, META.md)
	// is absent from a tree that carries at least one parsed file.
	FindingRootFileMissing FindingKind = "ROOT_FILE_MISSING"
	// FindingADRNameMalformed: a decisions/ file whose base name does not
	// match the documented NNN-slug.md shape, so the record is invisible
	// to the ADR model.
	FindingADRNameMalformed FindingKind = "ADR_NAME_MALFORMED"
	// FindingADRNumberMismatch: a decisions/NNN-slug.md file whose
	// "# ADR-NNN:" heading carries a different number than the file name.
	FindingADRNumberMismatch FindingKind = "ADR_NUMBER_MISMATCH"
	// FindingSupersededTargetMissing: a superseded ADR names a successor
	// NNN that no decisions/ file carries.
	FindingSupersededTargetMissing FindingKind = "SUPERSEDED_TARGET_MISSING"
	// FindingSupersededCycle: the supersession chain of an ADR loops back
	// to one of its own records.
	FindingSupersededCycle FindingKind = "SUPERSEDED_CYCLE"
)

// ADR Status classifications (ADRMeta.Status): the vocabulary a "## Status"
// first line is reduced to. Real status lines are free prose ("Accepted —
// amended by [ADR-052 ...]"), so the classification keys off the first word.
const (
	// StatusAccepted: the status line's first word is "accepted".
	StatusAccepted = "accepted"
	// StatusSuperseded: the status line's first word is "superseded"
	// (including "superseded in part by ...").
	StatusSuperseded = "superseded"
)

// SpecsRoot is the parsed model of one specs/ tree.
type SpecsRoot struct {
	// Path is the specs root path exactly as passed to ParseSpecsRoot.
	Path string `json:"path"`

	// Present reports whether the root existed and was a directory. A root
	// that is missing or not a directory parses to Present=false with
	// everything else empty — a neutral state, not an error (ADR-105 D2).
	Present bool `json:"present"`

	// Files holds every parsed spec file — the section trees plus the root
	// files INDEX.md and META.md (Section "") — sorted by path.
	Files []SpecFile `json:"files,omitempty"`

	// Index is the parsed INDEX.md; nil when INDEX.md is missing or
	// unreadable. A present-but-empty INDEX.md parses to a non-nil empty
	// index.
	Index *SpecsIndex `json:"index,omitempty"`

	// ADRs holds decisions/NNN-slug.md metadata, sorted by number (numeric,
	// file name as the tie-break so duplicates stay adjacent).
	ADRs []ADRMeta `json:"adrs,omitempty"`

	// Findings holds the deterministic tier-1 integrity findings (ADR-105
	// D5), sorted by kind, then file, then detail.
	Findings []Finding `json:"findings,omitempty"`
}

// SpecFile is one parsed .md file of the tree. Files whose base name starts
// with "_" (templates, META.md naming rules) are excluded from the model
// entirely.
type SpecFile struct {
	// Path is the specs-root-relative, slash-separated path
	// ("domains/orchestration/conductor.md", "INDEX.md").
	Path string `json:"path"`

	// Section is the file's top-level section directory; "" for the root
	// files INDEX.md and META.md.
	Section string `json:"section,omitempty"`

	// Title is the first ATX heading outside code blocks, verbatim.
	Title string `json:"title,omitempty"`

	// KeyFiles holds the backticked entries of the "## Key Files" section,
	// verbatim, in document order. Module paths (github.com/...) and other
	// non-workspace shapes stay in the model; only the PATH_MISSING check
	// filters them.
	KeyFiles []string `json:"key_files,omitempty"`

	// CrossRefs holds every relative markdown link target in document order,
	// verbatim: a file path with an optional #anchor, or a pure #anchor
	// (intra-file). Absolute URLs are not cross-references and are excluded.
	CrossRefs []string `json:"cross_refs,omitempty"`
}

// SpecsIndex is the parsed INDEX.md.
type SpecsIndex struct {
	// Tasks is the "## Navigation by Task" table in document order. Rows
	// whose second column carries no relative link (the header row, the
	// separator row, external-only rows) are dropped.
	Tasks []IndexTask `json:"tasks,omitempty"`

	// ListedFiles is the "## Directory Listing" section's file set: every
	// relative .md link target, anchor-stripped and cleaned, deduplicated,
	// in document order — the baseline for the INDEX_STALE finding.
	ListedFiles []string `json:"listed_files,omitempty"`
}

// IndexTask is one row of the INDEX.md task navigation table.
type IndexTask struct {
	// Task is the first column's text, verbatim (trimmed).
	Task string `json:"task"`

	// Specs holds the second column's relative link targets in document
	// order, verbatim (anchors included).
	Specs []string `json:"specs,omitempty"`
}

// ADRMeta is one decisions/NNN-slug.md record. Number and slug come from
// the file name (the template's authority); title, status and supersession
// from the body.
type ADRMeta struct {
	// Number is the leading NNN of the file name, verbatim ("105").
	Number string `json:"number"`

	// Slug is the kebab-case tail of the file name ("specs-sidebar-panel").
	Slug string `json:"slug"`

	// File is the specs-root-relative path
	// ("decisions/105-specs-sidebar-panel.md").
	File string `json:"file"`

	// Title is the text after "# ADR-NNN: " (the whole heading when it does
	// not match the template shape).
	Title string `json:"title,omitempty"`

	// Status is the classification of the "## Status" section's first line
	// (StatusAccepted, StatusSuperseded, or the lower-cased first word).
	Status string `json:"status"`

	// SupersededBy is the successor's NNN ("055"), set only when Status is
	// superseded and the status line names a successor; "" otherwise.
	SupersededBy string `json:"superseded_by,omitempty"`
}

// Finding is one deterministic tier-1 integrity finding (ADR-105 D5). The
// categories deliberately under-approximate spec-check: semantic drift
// stays conversational, and every category here is decidable from the tree
// itself (paths, links, numbering, supersession graph, INDEX coverage).
type Finding struct {
	// Kind is the finding category.
	Kind FindingKind `json:"kind"`

	// File is the specs-root-relative path the finding is about (for
	// DUPLICATE_ADR_NUMBER: the first duplicate in sorted order).
	File string `json:"file"`

	// Detail is the human-readable message; identical input yields
	// identical detail.
	Detail string `json:"detail"`
}
