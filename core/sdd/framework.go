// Package sdd models specification-driven-development frameworks as data.
//
// One YAML document describes a framework completely: how it is detected, the
// directories it owns, the document concepts those directories hold, the
// integrity checks that apply, the actions it offers and the views that render
// it. Built-in frameworks are the same kind of document — embedded YAML parsed
// by the same code — so there is no privileged "native" grammar.
//
// The parsing core is pure: Parse, ParseDefinitions and Merge consume bytes and
// perform no I/O. Parsing is strict — an unknown YAML key, an unknown schema, an
// unknown check kind, an extraction directive or a parameter belonging to
// another check kind is a load error, never a silently ignored field. Only the
// operator-file entry points (LoadDefinitions, Load) touch the filesystem, and
// they are fail-soft by design.
package sdd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaV1 is the only document schema this package accepts.
const SchemaV1 = "sdd-framework/v1"

// Check kinds — the closed vocabulary of integrity primitives. A check names
// exactly one kind and only that kind's parameters.
const (
	// CheckRequiredFiles asserts file existence: at each root (no `concept`),
	// in each matched concept instance directory (`concept`), or for every path
	// extracted from a document (`from`, e.g. `key_files`).
	CheckRequiredFiles = "required_files"
	// CheckNamePattern asserts each matched instance's base name (a directory
	// name for `dir` concepts) matches a regular expression, minus `allow`.
	CheckNamePattern = "name_pattern"
	// CheckCrossRefs asserts every relative markdown link (and anchor) extracted
	// from a concept resolves.
	CheckCrossRefs = "crossrefs"
	// CheckIndexCompleteness asserts the index concept names every instance of
	// each covered concept kind.
	CheckIndexCompleteness = "index_completeness"
	// CheckFrontmatterKeys asserts a document's frontmatter carries all keys.
	CheckFrontmatterKeys = "frontmatter_keys"
	// CheckContentRegex asserts a regular expression matches (and optionally
	// captures a value comparable to a filename-derived token).
	CheckContentRegex = "content_regex"
	// CheckUnique asserts an extracted field is unique across a concept's
	// instances, optionally inside a grouping field.
	CheckUnique = "unique"
	// CheckReferences asserts an extracted reference field resolves to an
	// instance of another concept, and, with `acyclic`, forms no cycle.
	CheckReferences = "references"
)

// CheckIn values select the text a `content_regex` check reads.
const (
	CheckInFirstLine    = "first_line"
	CheckInFirstHeading = "first_heading"
	CheckInBody         = "body"
	CheckInFrontmatter  = "frontmatter"
	CheckInProse        = "prose"
)

// CheckEquals values are the parse-derived tokens a `content_regex` capture may
// be compared against.
const (
	// CheckEqualsFilenameNumber compares the capture with the numeric token the
	// instance's own file name carries (the leading NNN of `NNN-slug.md`).
	CheckEqualsFilenameNumber = "filename_number"
)

// CheckFromFiles is the `required_files.from` token addressing a concept's own
// declared companion files rather than an extraction directive.
const CheckFromFiles = "files"

// Severities a check may carry; an omitted severity is SeverityError.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Extraction directives — what a concept pulls out of each instance document.
const (
	ExtractTitle        = "title"
	ExtractHeadings     = "headings"
	ExtractKeyFiles     = "key_files"
	ExtractCrossRefs    = "cross_refs"
	ExtractFrontmatter  = "frontmatter"
	ExtractNumber       = "number"
	ExtractStatus       = "status"
	ExtractSupersededBy = "superseded_by"
	ExtractPurpose      = "purpose"
	ExtractRequirements = "requirements"
	ExtractScenarios    = "scenarios"
	ExtractDeltas       = "deltas"
	ExtractTaskItems    = "task_items"
	ExtractPrinciples   = "principles"
	ExtractUserStories  = "user_stories"
	ExtractIndexTasks   = "index_tasks"
)

// Requirement tokens — what `concept.required` may assert about an instance.
const (
	RequireNonEmpty     = "non_empty"
	RequireTitle        = "title"
	RequireStatus       = "status"
	RequirePurpose      = "purpose"
	RequireRequirements = "requirements"
	RequireScenarios    = "scenarios"
	RequireTasks        = "tasks"
	RequirePrinciples   = "principles"
	RequireUserStories  = "user_stories"
)

// View section kinds.
const (
	SectionTree     = "tree"
	SectionFindings = "findings"
	SectionChecks   = "checks"
)

// Requirement tokens an action may carry in `requires`.
const (
	// RequireRoot means the framework's declared roots must be present.
	RequireRoot = "root"
	// RequireFindings means at least one check finding must exist.
	RequireFindings = "findings"
	// RequireConceptPrefix prefixes a `concept:<kind>` requirement.
	RequireConceptPrefix = "concept:"
	// RequireCompanionPrefix prefixes a `companion:<kind>` requirement: the
	// target directory already holds an instance of the kind (a feature with
	// its plan.md in place).
	RequireCompanionPrefix = "companion:"
	// RequireAbsentPrefix prefixes an `absent:<kind>` requirement: the target
	// directory holds no instance of the kind yet (the creation-step gate).
	RequireAbsentPrefix = "absent:"
)

// ExtractDirectives lists every accepted `concept.extract` value.
var ExtractDirectives = []string{
	ExtractTitle, ExtractHeadings, ExtractKeyFiles, ExtractCrossRefs,
	ExtractFrontmatter, ExtractNumber, ExtractStatus, ExtractSupersededBy,
	ExtractPurpose, ExtractRequirements, ExtractScenarios, ExtractDeltas,
	ExtractTaskItems, ExtractPrinciples, ExtractUserStories, ExtractIndexTasks,
}

// ScalarFieldDirectives lists the extraction directives a per-document scalar
// consumer can actually read — the set fieldValue implements. The other
// directives extract lists (or nothing comparable): accepting them in a
// scalar position would load a check that silently compares nothing, so the
// validator rejects the combination instead of no-oping it.
var ScalarFieldDirectives = []string{
	ExtractNumber, ExtractTitle, ExtractPurpose, ExtractStatus, ExtractSupersededBy,
}

// ListFieldDirectives lists the extraction directives `required_files.from`
// can actually enumerate — the set valuesFor implements. A list position
// needs a directive that yields paths, not a scalar one.
var ListFieldDirectives = []string{
	ExtractKeyFiles, ExtractCrossRefs, ExtractHeadings, ExtractRequirements,
	ExtractScenarios, ExtractPrinciples, ExtractUserStories, ExtractDeltas,
}

// RequirementTokens lists every accepted `concept.required` value.
var RequirementTokens = []string{
	RequireNonEmpty, RequireTitle, RequireStatus, RequirePurpose,
	RequireRequirements, RequireScenarios, RequireTasks, RequirePrinciples,
	RequireUserStories,
}

// CheckKinds lists every accepted `check.kind` value.
var CheckKinds = []string{
	CheckRequiredFiles, CheckNamePattern, CheckCrossRefs, CheckIndexCompleteness,
	CheckFrontmatterKeys, CheckContentRegex, CheckUnique, CheckReferences,
}

// Framework is one declarative description of an SDD framework.
type Framework struct {
	Schema      string    `yaml:"schema"`                // SchemaV1
	ID          string    `yaml:"id"`                    // stable slug, unique across frameworks
	Label       string    `yaml:"label"`                 // human name
	Description string    `yaml:"description,omitempty"` // one-paragraph summary
	DocsURL     string    `yaml:"docs_url,omitempty"`    // canonical documentation URL
	Priority    int       `yaml:"priority,omitempty"`    // detection order, lower wins; 0 is the default
	Detect      Detect    `yaml:"detect"`                // how a workspace is recognised
	Roots       []Root    `yaml:"roots"`                 // directories the framework owns
	Concepts    []Concept `yaml:"concepts,omitempty"`    // document concepts inside those roots
	Checks      []Check   `yaml:"checks,omitempty"`      // integrity checks
	Actions     []Action  `yaml:"actions,omitempty"`     // lifecycle actions
	Views       Views     `yaml:"views,omitempty"`       // rendering sections
}

// Root is one directory a framework owns. Concepts address roots by ID rather
// than by path, so a framework can relocate a root without touching them.
type Root struct {
	ID       string `yaml:"id"`                 // slug referenced by Concept.Match.Root
	Path     string `yaml:"path"`               // workspace-relative directory, slash separated
	Label    string `yaml:"label,omitempty"`    // human name
	Required bool   `yaml:"required,omitempty"` // the root must exist for the framework to be present
}

// Detect is the framework's presence signal. Visibility is skill evidence: a
// definition is recognised only when its declared skill set is installed, so at
// least one of the two name lists is required. Markers and roots are the
// existence-only tree signal that orders the recognised frameworks for default
// selection; they never make a framework visible on their own.
type Detect struct {
	Markers []string `yaml:"markers,omitempty"` // workspace-relative probe files; any present matches
	Skills  Skills   `yaml:"skills,omitempty"`  // skill-directory probe
}

// Skills detects a framework through the agent skills its `init` installs.
type Skills struct {
	Dirs  []string `yaml:"dirs,omitempty"`   // workspace-relative skill directories to scan
	AllOf []string `yaml:"all_of,omitempty"` // every one of these skills must be present
	AnyOf []string `yaml:"any_of,omitempty"` // at least one of these skills must be present
}

// Concept is one document (or directory) kind inside a root.
type Concept struct {
	Kind     string   `yaml:"kind"`               // slug referenced by checks, actions and views
	Label    string   `yaml:"label,omitempty"`    // human name
	Match    Match    `yaml:"match"`              // which paths are instances
	Files    []string `yaml:"files,omitempty"`    // companion files, relative to each instance directory
	Extract  []string `yaml:"extract,omitempty"`  // extraction directives, from ExtractDirectives
	Required []string `yaml:"required,omitempty"` // per-instance assertions, from RequirementTokens
}

// Match selects a concept's instances inside one root.
//
// `*` matches within a single path segment and `**` matches zero or more
// segments, so `domains/**/*.md` covers both `domains/x.md` and
// `domains/frontend/x.md`. `exclude` names globs a matched candidate must NOT
// match to belong to the concept — how an actual-documents concept sheds its
// section's `_`-prefixed templates (a template concept with a more specific
// glob claims them instead). When several concepts match one path, the most
// specific glob wins (the deepest fixed prefix, then the longest pattern), which
// is how a container concept such as `changes/*` coexists with a nested one
// such as `changes/archive/*`.
type Match struct {
	Root    string   `yaml:"root"`              // Root.ID
	Glob    string   `yaml:"glob"`              // slash-separated pattern relative to the root
	Dir     bool     `yaml:"dir,omitempty"`     // instances are directories, not files
	Exclude []string `yaml:"exclude,omitempty"` // globs a matched path must not match
}

// Check is one integrity primitive invocation.
type Check struct {
	Kind     string   `yaml:"kind"`               // one of CheckKinds
	Finding  string   `yaml:"finding"`            // stable finding key, e.g. PATH_MISSING
	Message  string   `yaml:"message,omitempty"`  // operator-facing explanation
	Severity string   `yaml:"severity,omitempty"` // error (default) | warning
	Concept  string   `yaml:"concept,omitempty"`  // Concept.Kind the check applies to
	Files    []string `yaml:"files,omitempty"`    // required_files: paths asserted to exist
	From     string   `yaml:"from,omitempty"`     // required_files: extraction directive (or files) supplying the paths
	Pattern  string   `yaml:"pattern,omitempty"`  // name_pattern: RE2 over the instance base name
	Allow    []string `yaml:"allow,omitempty"`    // name_pattern: base names exempted from Pattern
	Keys     []string `yaml:"keys,omitempty"`     // frontmatter_keys: required frontmatter keys
	Regex    string   `yaml:"regex,omitempty"`    // content_regex: RE2 over the selected text
	In       string   `yaml:"in,omitempty"`       // content_regex: first_line | first_heading | body | prose | frontmatter
	Capture  string   `yaml:"capture,omitempty"`  // content_regex: named group holding the compared value
	Equals   string   `yaml:"equals,omitempty"`   // content_regex: filename_number
	Field    string   `yaml:"field,omitempty"`    // unique/references: extracted field
	Of       string   `yaml:"of,omitempty"`       // references: Concept.Kind the reference must resolve to
	Acyclic  bool     `yaml:"acyclic,omitempty"`  // references: the relation must form no cycle
	// CycleFinding is the finding token an acyclic references check reports a
	// loop under, distinct from `finding` (which reports an unresolved
	// reference). Optional; without it cycle findings carry `finding`.
	CycleFinding string `yaml:"cycle_finding,omitempty"`
	// Optional marks a content_regex whose non-match is not a finding: the
	// check asserts agreement between capture and equals only when the
	// pattern matched and captured something. It REQUIRES `equals` — optional
	// relaxes that comparison, so without it the check could never report
	// anything and the loader rejects the shape.
	Optional bool     `yaml:"optional,omitempty"`
	Covers   []string `yaml:"covers,omitempty"` // index_completeness: concept kinds the index must name
}

// Action is one lifecycle gesture a framework offers.
type Action struct {
	Kind     string   `yaml:"kind"`               // slug, unique within the framework
	Label    string   `yaml:"label"`              // button text
	Skill    string   `yaml:"skill,omitempty"`    // agent skill dispatched by the action
	Prompt   string   `yaml:"prompt,omitempty"`   // instruction carried into the chat
	Concept  string   `yaml:"concept,omitempty"`  // Concept.Kind the action targets
	Requires []string `yaml:"requires,omitempty"` // root | findings | concept:<kind> | companion:<kind> | absent:<kind> (absent: also needs Concept)
}

// Views declares how a framework's tree is presented.
type Views struct {
	Sections []Section `yaml:"sections,omitempty"`
}

// Section is one view section over a set of concepts.
type Section struct {
	ID       string   `yaml:"id"`                 // slug, unique within the framework
	Label    string   `yaml:"label"`              // heading
	Kind     string   `yaml:"kind"`               // tree | findings | checks
	Concepts []string `yaml:"concepts,omitempty"` // Concept.Kind members, in display order
}

// Error is one load failure naming the document and the field path inside it.
type Error struct {
	Document string // document label supplied to Parse (e.g. an embedded file name)
	Path     string // field path inside the document, e.g. "checks[2].pattern"; empty for a document-level fault
	Problem  string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return "sdd: " + e.Document + ": " + e.Problem
	}
	return "sdd: " + e.Document + ": " + e.Path + ": " + e.Problem
}

var (
	slugRE    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	findingRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// Parse decodes and validates one framework document. `document` labels error
// messages only (an embedded file name, a settings source); it is not read.
func Parse(document string, data []byte) (Framework, error) {
	if strings.TrimSpace(document) == "" {
		document = "document"
	}
	var fw Framework
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fw); err != nil {
		if errors.Is(err, io.EOF) {
			return Framework{}, &Error{Document: document, Problem: "document is empty"}
		}
		return Framework{}, &Error{Document: document, Problem: err.Error()}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Framework{}, &Error{Document: document, Problem: "document must hold exactly one YAML document"}
	}
	if err := fw.normalize(); err != nil {
		return Framework{}, err
	}
	if err := fw.validate(document); err != nil {
		return Framework{}, err
	}
	return fw, nil
}

// normalize applies defaults that a document may omit.
func (f *Framework) normalize() error {
	for i := range f.Checks {
		if f.Checks[i].Severity == "" {
			f.Checks[i].Severity = SeverityError
		}
	}
	return nil
}

func (f *Framework) validate(document string) error {
	var errs []error
	fail := func(field, format string, args ...any) {
		errs = append(errs, &Error{Document: document, Path: field, Problem: fmt.Sprintf(format, args...)})
	}

	if f.Schema != SchemaV1 {
		fail("schema", "unknown schema %q (want %q)", f.Schema, SchemaV1)
	}
	if !slugRE.MatchString(f.ID) {
		fail("id", "must match %s, got %q", slugRE, f.ID)
	}
	if strings.TrimSpace(f.Label) == "" {
		fail("label", "is required")
	}
	if f.DocsURL != "" && !isHTTPURL(f.DocsURL) {
		fail("docs_url", "must be an absolute http(s) URL, got %q", f.DocsURL)
	}
	if f.Priority < 0 {
		fail("priority", "must be >= 0, got %d", f.Priority)
	}

	roots := f.validateRoots(fail)
	f.validateDetect(fail)

	concepts, dirByKind := f.validateConcepts(roots, fail)
	f.validateChecks(concepts, dirByKind, fail)
	f.validateActions(concepts, fail)
	f.validateViews(concepts, fail)

	return errors.Join(errs...)
}

func (f *Framework) validateRoots(fail func(string, string, ...any)) map[string]bool {
	ids := make(map[string]bool, len(f.Roots))
	if len(f.Roots) == 0 {
		fail("roots", "at least one root is required")
	}
	type declared struct {
		id   string
		path string
	}
	declaredRoots := make([]declared, 0, len(f.Roots))
	for i, r := range f.Roots {
		field := fmt.Sprintf("roots[%d]", i)
		switch {
		case !slugRE.MatchString(r.ID):
			fail(field+".id", "must match %s, got %q", slugRE, r.ID)
		case ids[r.ID]:
			fail(field+".id", "duplicate root id %q", r.ID)
		default:
			ids[r.ID] = true
		}
		if !cleanRelative(r.Path) {
			fail(field+".path", "must be a clean workspace-relative path, got %q", r.Path)
		}
		// A file is addressed by its workspace path but owned through its
		// root, so two roots on nested or equal paths would walk one file
		// twice and show one document under two owners. The documented rule —
		// forest nodes use workspace paths as IDs — is enforced here.
		for _, prev := range declaredRoots {
			if rootPathsOverlap(r.Path, prev.path) {
				fail(field+".path", "overlaps root %q (path %q): every root must own its own subtree", prev.id, prev.path)
			}
		}
		declaredRoots = append(declaredRoots, declared{id: r.ID, path: r.Path})
	}
	return ids
}

// rootPathsOverlap reports whether two cleaned root paths are equal or one is
// nested inside the other. The workspace root itself ("." — a clean relative
// path the loader accepts) owns every subtree, so it overlaps any sibling
// root: otherwise one file would be walked twice and shown under two owners.
func rootPathsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == "." || b == "." {
		return true
	}
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func (f *Framework) validateDetect(fail func(string, string, ...any)) {
	d := &f.Detect
	// A definition with no skill list can never be recognised (D8), so it could
	// never become visible or active: markers and roots only order frameworks
	// that already passed the skill test. Accepting it would be a load-time trap.
	if len(d.Skills.AllOf) == 0 && len(d.Skills.AnyOf) == 0 {
		fail("detect", "at least one all_of or any_of skill is required: markers alone never make a framework visible")
	}
	for i, m := range d.Markers {
		if !cleanRelative(m) {
			fail(fmt.Sprintf("detect.markers[%d]", i), "must be a clean workspace-relative path, got %q", m)
		}
	}
	for i, dir := range d.Skills.Dirs {
		if !cleanRelative(dir) {
			fail(fmt.Sprintf("detect.skills.dirs[%d]", i), "must be a clean workspace-relative path, got %q", dir)
		}
	}
	if len(d.Skills.Dirs) == 0 && (len(d.Skills.AllOf) > 0 || len(d.Skills.AnyOf) > 0) {
		fail("detect.skills.dirs", "is required when all_of or any_of is set")
	}
	// An ordered slice, not a map literal: both lists yield independent
	// diagnostics, and errors.Join preserves addition order — iterating a map
	// made the FIRST line of one defect's message flip between all_of and
	// any_of on every parse, so the panel's warnings[0] jumped at random.
	for _, group := range []struct {
		name string
		list []string
	}{
		{"detect.skills.all_of", d.Skills.AllOf},
		{"detect.skills.any_of", d.Skills.AnyOf},
	} {
		seen := map[string]bool{}
		for i, s := range group.list {
			switch {
			case !slugRE.MatchString(s):
				fail(fmt.Sprintf("%s[%d]", group.name, i), "must match %s, got %q", slugRE, s)
			case seen[s]:
				fail(fmt.Sprintf("%s[%d]", group.name, i), "duplicate skill %q", s)
			default:
				seen[s] = true
			}
		}
	}
}

func (f *Framework) validateConcepts(roots map[string]bool, fail func(string, string, ...any)) (kinds, dirByKind map[string]bool) {
	kinds = make(map[string]bool, len(f.Concepts))
	dirByKind = make(map[string]bool, len(f.Concepts))
	if len(f.Concepts) == 0 {
		fail("concepts", "at least one concept is required")
	}
	for i := range f.Concepts {
		c := &f.Concepts[i]
		field := fmt.Sprintf("concepts[%d]", i)
		switch {
		case !slugRE.MatchString(c.Kind):
			fail(field+".kind", "must match %s, got %q", slugRE, c.Kind)
		case kinds[c.Kind]:
			fail(field+".kind", "duplicate concept kind %q", c.Kind)
		default:
			kinds[c.Kind] = true
			dirByKind[c.Kind] = c.Match.Dir
		}
		if !roots[c.Match.Root] {
			fail(field+".match.root", "unknown root %q", c.Match.Root)
		}
		if !cleanPattern(c.Match.Glob) {
			fail(field+".match.glob", "must be a clean slash-separated pattern, got %q", c.Match.Glob)
		} else if err := validateGlobSyntax(c.Match.Glob); err != nil {
			fail(field+".match.glob", "%v", err)
		}
		for j, pattern := range c.Match.Exclude {
			excludeField := fmt.Sprintf("%s.match.exclude[%d]", field, j)
			if !cleanPattern(pattern) {
				fail(excludeField, "must be a clean slash-separated pattern, got %q", pattern)
			} else if err := validateGlobSyntax(pattern); err != nil {
				fail(excludeField, "%v", err)
			}
		}
		if len(c.Files) > 0 {
			if !c.Match.Dir {
				fail(field+".files", "companion files require match.dir")
			}
			validateRelativeList(c.Files, field+".files", "file", fail)
		}
		if c.Match.Dir {
			// Extraction and per-instance requirements are per-DOCUMENT work:
			// buildDocuments skips directory instances, so document() — the
			// only consumer of both — never sees them.
			if len(c.Extract) > 0 {
				fail(field+".extract", "runs on documents; a match.dir concept has none")
			}
			if len(c.Required) > 0 {
				fail(field+".required", "is evaluated per document; a match.dir concept has none")
			}
		}
		validateVocab(c.Extract, ExtractDirectives, field+".extract", "extraction directive", fail)
		validateVocab(c.Required, RequirementTokens, field+".required", "requirement", fail)
	}
	return kinds, dirByKind
}

// validateGlobSyntax checks every pattern segment parses as a path.Match
// pattern (`**` excepted — it is this DSL's own recursive-segment token).
// Without this a malformed segment compiled cleanly and matched nothing,
// silencing the concept and every check over it with no load diagnostic.
func validateGlobSyntax(pattern string) error {
	for i, seg := range strings.Split(pattern, "/") {
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, "x"); err != nil {
			return fmt.Errorf("segment %d (%q) is not a valid glob: %w", i, seg, err)
		}
	}
	return nil
}

func (f *Framework) validateChecks(concepts, dirByKind map[string]bool, fail func(string, string, ...any)) {
	findings := make(map[string]bool, len(f.Checks))
	for i := range f.Checks {
		c := &f.Checks[i]
		field := fmt.Sprintf("checks[%d]", i)
		if !slices.Contains(CheckKinds, c.Kind) {
			fail(field+".kind", "unknown check kind %q (want one of %s)", c.Kind, strings.Join(CheckKinds, ", "))
			continue
		}
		switch {
		case !findingRE.MatchString(c.Finding):
			fail(field+".finding", "must be an upper-case token matching %s, got %q", findingRE, c.Finding)
		case findings[c.Finding]:
			fail(field+".finding", "duplicate finding %q", c.Finding)
		default:
			findings[c.Finding] = true
		}
		if !slices.Contains([]string{SeverityError, SeverityWarning}, c.Severity) {
			fail(field+".severity", "must be %q or %q, got %q", SeverityError, SeverityWarning, c.Severity)
		}
		if c.Concept != "" && !concepts[c.Concept] {
			fail(field+".concept", "unknown concept %q", c.Concept)
		}
		validateCheckFields(c, field, fail)
		validateCheckKind(c, field, concepts, dirByKind, fail)
	}
}

// validateCheckFields rejects parameters the check's own kind does not declare,
// so a document cannot smuggle a meaningless field past the loader.
func validateCheckFields(c *Check, field string, fail func(string, string, ...any)) {
	allowed := map[string]bool{"kind": true, "finding": true, "message": true, "severity": true}
	for _, name := range checkKindFields[c.Kind] {
		allowed[name] = true
	}
	for _, name := range checkExplicitFields(c) {
		if !allowed[name] {
			fail(field+"."+name, "is not a parameter of check kind %q", c.Kind)
		}
	}
}

// checkKindFields lists the YAML keys each check kind accepts, in addition to
// the four every check carries (kind, finding, message, severity).
var checkKindFields = map[string][]string{
	CheckRequiredFiles:     {"concept", "files", "from"},
	CheckNamePattern:       {"concept", "pattern", "allow"},
	CheckCrossRefs:         {"concept"},
	CheckIndexCompleteness: {"concept", "covers"},
	CheckFrontmatterKeys:   {"concept", "keys"},
	CheckContentRegex:      {"concept", "regex", "in", "capture", "equals", "optional"},
	CheckUnique:            {"concept", "field"},
	CheckReferences:        {"concept", "field", "of", "acyclic", "cycle_finding"},
}

func checkExplicitFields(c *Check) []string {
	var names []string
	add := func(set bool, name string) {
		if set {
			names = append(names, name)
		}
	}
	add(c.Concept != "", "concept")
	add(len(c.Files) > 0, "files")
	add(c.From != "", "from")
	add(c.Pattern != "", "pattern")
	add(len(c.Allow) > 0, "allow")
	add(len(c.Keys) > 0, "keys")
	add(c.Regex != "", "regex")
	add(c.In != "", "in")
	add(c.Capture != "", "capture")
	add(c.Equals != "", "equals")
	add(c.Field != "", "field")
	add(c.Of != "", "of")
	add(c.Acyclic, "acyclic")
	add(c.CycleFinding != "", "cycle_finding")
	add(c.Optional, "optional")
	add(len(c.Covers) > 0, "covers")
	return names
}

func validateCheckKind(c *Check, field string, concepts, dirByKind map[string]bool, fail func(string, string, ...any)) {
	requireConcept := func() {
		if c.Concept == "" {
			fail(field+".concept", "is required for check kind %q", c.Kind)
		}
	}
	// A check over a concept runs against the concept's FILE instances; naming
	// a directory concept here declares a check that can never see an
	// instance — a valid-looking definition that silently checks nothing.
	// (required_files is the exception: its `from: files` mode is exactly the
	// per-directory companion check and demands a directory concept.)
	requireFileConcept := func() {
		if c.Concept != "" && dirByKind[c.Concept] {
			fail(field+".concept", "must name a file concept for check kind %q (concept %q matches directories)", c.Kind, c.Concept)
		}
	}
	switch c.Kind {
	case CheckRequiredFiles:
		switch {
		case c.From != "" && len(c.Files) > 0:
			fail(field+".files", "is mutually exclusive with from")
		case c.From == "" && len(c.Files) == 0:
			fail(field, "check kind %q requires files or from", c.Kind)
		case c.From != "" && c.From != CheckFromFiles && !slices.Contains(ListFieldDirectives, c.From):
			fail(field+".from", "must be one of %s for check kind %q, got %q", strings.Join(ListFieldDirectives, ", "), c.Kind, c.From)
		case c.From == CheckFromFiles && c.Concept == "":
			fail(field+".concept", "is required when from is %q (the concept's companion files)", CheckFromFiles)
		case c.From == CheckFromFiles && c.Concept != "" && !dirByKind[c.Concept]:
			fail(field+".concept", "must name a directory concept when from is %q (companion files live in instance directories)", CheckFromFiles)
		}
		// An extraction directive enumerates paths extracted from document
		// bodies: over a directory concept the check would see zero instances
		// and silently verify nothing (the shape the file checks reject).
		if c.From != "" && c.From != CheckFromFiles && c.Concept != "" {
			requireFileConcept()
		}
		validateRelativeList(c.Files, field+".files", "file", fail)

	case CheckNamePattern:
		requireConcept()
		if c.Pattern == "" {
			fail(field+".pattern", "is required for check kind %q", c.Kind)
		} else if _, err := regexp.Compile(c.Pattern); err != nil {
			fail(field+".pattern", "must compile: %v", err)
		}
		for i, name := range c.Allow {
			if !cleanRelative(name) || strings.Contains(name, "/") {
				fail(fmt.Sprintf("%s.allow[%d]", field, i), "must be a bare file name, got %q", name)
			}
		}

	case CheckCrossRefs:
		// concept is optional: absent means every concept extracting cross_refs.
		requireFileConcept()

	case CheckIndexCompleteness:
		if c.Concept == "" {
			fail(field+".concept", "is required for check kind %q (the index concept)", c.Kind)
		} else {
			requireFileConcept()
		}
		if len(c.Covers) == 0 {
			fail(field+".covers", "is required for check kind %q", c.Kind)
		}
		for i, kind := range c.Covers {
			if !concepts[kind] {
				fail(fmt.Sprintf("%s.covers[%d]", field, i), "unknown concept %q", kind)
			}
		}

	case CheckFrontmatterKeys:
		requireConcept()
		requireFileConcept()
		if len(c.Keys) == 0 {
			fail(field+".keys", "is required for check kind %q", c.Kind)
		}

	case CheckContentRegex:
		requireConcept()
		requireFileConcept()
		if c.Regex == "" {
			fail(field+".regex", "is required for check kind %q", c.Kind)
			break
		}
		re, err := regexp.Compile(c.Regex)
		if err != nil {
			fail(field+".regex", "must compile: %v", err)
			break
		}
		if c.In != "" && !slices.Contains([]string{CheckInFirstLine, CheckInFirstHeading, CheckInBody, CheckInFrontmatter, CheckInProse}, c.In) {
			fail(field+".in", "must be one of %s, %s, %s, %s, %s", CheckInFirstLine, CheckInFirstHeading, CheckInBody, CheckInFrontmatter, CheckInProse)
		}
		if c.Equals != "" {
			if c.Equals != CheckEqualsFilenameNumber {
				fail(field+".equals", "unknown comparison %q (want %q)", c.Equals, CheckEqualsFilenameNumber)
			}
			if c.Capture == "" {
				fail(field+".capture", "is required when equals is set")
			}
		}
		if c.Optional && c.Equals == "" {
			// `optional` only relaxes the filename-number comparison: without
			// `equals` there is nothing to relax, so the check reports nothing
			// on a match and stays silent on a miss — a valid-looking
			// definition that silently checks nothing.
			fail(field+".optional", "requires %q: optional only relaxes that comparison, so without it the check can never report anything", "equals")
		}
		if c.Capture != "" && re.SubexpIndex(c.Capture) < 0 {
			fail(field+".capture", "names no group in regex")
		}

	case CheckUnique:
		requireConcept()
		requireFileConcept()
		if c.Field == "" {
			fail(field+".field", "is required for check kind %q", c.Kind)
		} else if !slices.Contains(ScalarFieldDirectives, c.Field) {
			fail(field+".field", "must be one of %s for check kind %q, got %q", strings.Join(ScalarFieldDirectives, ", "), c.Kind, c.Field)
		}

	case CheckReferences:
		requireConcept()
		requireFileConcept()
		if c.Field == "" {
			fail(field+".field", "is required for check kind %q", c.Kind)
		} else if !slices.Contains(ScalarFieldDirectives, c.Field) {
			fail(field+".field", "must be one of %s for check kind %q, got %q", strings.Join(ScalarFieldDirectives, ", "), c.Kind, c.Field)
		}
		if c.Of == "" {
			fail(field+".of", "is required for check kind %q", c.Kind)
		} else if !concepts[c.Of] {
			fail(field+".of", "unknown concept %q", c.Of)
		}
		if !c.Acyclic && c.CycleFinding != "" {
			fail(field+".cycle_finding", "requires acyclic (it names the finding a loop is reported under)")
		}
		if c.CycleFinding != "" {
			switch {
			case !findingRE.MatchString(c.CycleFinding):
				fail(field+".cycle_finding", "must be an upper-case token matching %s, got %q", findingRE, c.CycleFinding)
			case c.CycleFinding == c.Finding:
				fail(field+".cycle_finding", "must differ from the check's own finding %q", c.Finding)
			}
		}
	}
}

func (f *Framework) validateActions(concepts map[string]bool, fail func(string, string, ...any)) {
	kinds := make(map[string]bool, len(f.Actions))
	for i := range f.Actions {
		a := &f.Actions[i]
		field := fmt.Sprintf("actions[%d]", i)
		switch {
		case !slugRE.MatchString(a.Kind):
			fail(field+".kind", "must match %s, got %q", slugRE, a.Kind)
		case kinds[a.Kind]:
			fail(field+".kind", "duplicate action kind %q", a.Kind)
		default:
			kinds[a.Kind] = true
		}
		if strings.TrimSpace(a.Label) == "" {
			fail(field+".label", "is required")
		}
		if a.Skill != "" && !slugRE.MatchString(a.Skill) {
			fail(field+".skill", "must match %s, got %q", slugRE, a.Skill)
		}
		if a.Concept != "" && !concepts[a.Concept] {
			fail(field+".concept", "unknown concept %q", a.Concept)
		}
		seen := map[string]bool{}
		for j, req := range a.Requires {
			reqField := fmt.Sprintf("%s.requires[%d]", field, j)
			switch {
			case !validActionRequirement(req, concepts):
				fail(reqField, "unknown requirement %q (want %q, %q, %q<kind>, %q<kind> or %q<kind>)",
					req, RequireRoot, RequireFindings, RequireConceptPrefix, RequireCompanionPrefix, RequireAbsentPrefix)
			case seen[req]:
				fail(reqField, "duplicate requirement %q", req)
			case strings.HasPrefix(req, RequireAbsentPrefix) && a.Concept == "":
				// The executor compares the <kind> count against the action's
				// holder concept and fails closed without one, so this gate
				// could load and then never open.
				fail(reqField, "%q needs `concept` on the action: the holder whose instances the %q count is compared with",
					req, strings.TrimPrefix(req, RequireAbsentPrefix))
			default:
				seen[req] = true
			}
		}
	}
}

func (f *Framework) validateViews(concepts map[string]bool, fail func(string, string, ...any)) {
	ids := make(map[string]bool, len(f.Views.Sections))
	nonTreeSeen := false
	for i := range f.Views.Sections {
		s := &f.Views.Sections[i]
		field := fmt.Sprintf("views.sections[%d]", i)
		switch {
		case !slugRE.MatchString(s.ID):
			fail(field+".id", "must match %s, got %q", slugRE, s.ID)
		case ids[s.ID]:
			fail(field+".id", "duplicate section id %q", s.ID)
		default:
			ids[s.ID] = true
		}
		if strings.TrimSpace(s.Label) == "" {
			fail(field+".label", "is required")
		}
		if !slices.Contains([]string{SectionTree, SectionFindings, SectionChecks}, s.Kind) {
			fail(field+".kind", "must be one of %s, %s, %s", SectionTree, SectionFindings, SectionChecks)
		}
		// The findings/checks sections render the one shared findings list;
		// a second non-tree section would print the same findings twice with
		// two headings and no way to tell which check produced which card.
		// One framework, one non-tree section — until checks carry per-check
		// attribution, a second one has nothing of its own to show.
		if s.Kind != SectionTree {
			if nonTreeSeen {
				fail(field+".kind", "a framework declares at most one non-tree section (kind %q duplicates an earlier findings/checks section)", s.Kind)
			}
			nonTreeSeen = true
		}
		for j, kind := range s.Concepts {
			if !concepts[kind] {
				fail(fmt.Sprintf("%s.concepts[%d]", field, j), "unknown concept %q", kind)
			}
		}
	}
}

func validActionRequirement(req string, concepts map[string]bool) bool {
	if req == RequireRoot || req == RequireFindings {
		return true
	}
	for _, prefix := range []string{RequireConceptPrefix, RequireCompanionPrefix, RequireAbsentPrefix} {
		if kind, ok := strings.CutPrefix(req, prefix); ok {
			return concepts[kind]
		}
	}
	return false
}

func validateVocab(values, allowed []string, field, what string, fail func(string, string, ...any)) {
	seen := map[string]bool{}
	for i, v := range values {
		switch {
		case !slices.Contains(allowed, v):
			fail(fmt.Sprintf("%s[%d]", field, i), "unknown %s %q (want one of %s)", what, v, strings.Join(allowed, ", "))
		case seen[v]:
			fail(fmt.Sprintf("%s[%d]", field, i), "duplicate %s %q", what, v)
		default:
			seen[v] = true
		}
	}
}

func validateRelativeList(values []string, field, what string, fail func(string, string, ...any)) {
	for i, v := range values {
		if !cleanRelative(v) {
			fail(fmt.Sprintf("%s[%d]", field, i), "must be a clean relative %s path, got %q", what, v)
		}
	}
}

// cleanRelative reports whether p is a non-empty, slash-separated, cleaned
// relative path — the only shape the DSL accepts for framework-owned paths.
// A leading "~" is rejected outright (documented contract): one layer would
// expand it to the home directory and another would treat it as a literal
// segment, so the same declaration would mean different things to detection
// and to the tree reader.
func cleanRelative(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.Contains(p, `\`) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return path.Clean(p) == p
}

// cleanPattern is cleanRelative for globs: wildcards are literal to the check.
func cleanPattern(p string) bool {
	if !cleanRelative(p) {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" {
			return false
		}
	}
	return true
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
