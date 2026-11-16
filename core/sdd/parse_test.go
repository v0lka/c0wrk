package sdd_test

import (
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/sdd"
)

const minimalDoc = `schema: sdd-framework/v1
id: demo
label: Demo framework
detect:
  markers:
    - demo/index.md
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: demo
    path: demo
concepts:
  - kind: index
    label: Index
    match:
      root: demo
      glob: index.md
    extract: [title, cross_refs]
  - kind: page
    label: Page
    match:
      root: demo
      glob: pages/*.md
    extract: [title, cross_refs, key_files]
checks:
  - kind: crossrefs
    finding: CROSS_REF_BROKEN
  - kind: required_files
    finding: PATH_MISSING
    from: key_files
`

// detectBlock is minimalDoc's detect block verbatim, so a test can swap the whole
// signal configuration — which now always carries a skill list — as one unit
// instead of restating it in every mutation target.
const detectBlock = "detect:\n  markers:\n    - demo/index.md\n  skills:\n    dirs: [.agents/skills]\n    any_of: [demo-init]\n"

func TestParse_MinimalDocument(t *testing.T) {
	fw, err := sdd.Parse("demo.yaml", []byte(minimalDoc))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if fw.ID != "demo" || fw.Schema != sdd.SchemaV1 {
		t.Fatalf("Parse() = %+v, want id demo and schema %q", fw, sdd.SchemaV1)
	}
	for _, c := range fw.Checks {
		if c.Severity != sdd.SeverityError {
			t.Errorf("check %q severity = %q, want the %q default", c.Finding, c.Severity, sdd.SeverityError)
		}
	}
	if fw.Priority != 0 {
		t.Errorf("priority = %d, want the zero default", fw.Priority)
	}
}

func TestParse_RejectsInvalidDocuments(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "unknown top-level key",
			doc:  mutate(minimalDoc, "roots:\n", "unknown_key: 1\nroots:\n"),
			want: "unknown_key",
		},
		{
			name: "unknown nested key",
			doc:  mutate(minimalDoc, "      glob: index.md\n", "      glob: index.md\n      depth: 2\n"),
			want: "depth",
		},
		{
			name: "unknown schema",
			doc:  mutate(minimalDoc, sdd.SchemaV1, "sdd-framework/v2"),
			want: `unknown schema "sdd-framework/v2"`,
		},
		{
			name: "missing schema",
			doc:  mutate(minimalDoc, "schema: "+sdd.SchemaV1+"\n", ""),
			want: "unknown schema",
		},
		{
			name: "empty document",
			doc:  "   \n",
			want: "document is empty",
		},
		{
			name: "two documents",
			doc:  minimalDoc + "---\n" + minimalDoc,
			want: "exactly one YAML document",
		},
		{
			name: "unknown check kind",
			doc:  mutate(minimalDoc, "kind: crossrefs", "kind: lint"),
			want: `unknown check kind "lint"`,
		},
		{
			name: "unknown extraction directive",
			doc:  mutate(minimalDoc, "extract: [title, cross_refs]\n", "extract: [title, frobnicate]\n"),
			want: `unknown extraction directive "frobnicate"`,
		},
		{
			name: "unknown requirement token",
			doc:  mutate(minimalDoc, "    extract: [title, cross_refs]\n", "    extract: [title, cross_refs]\n    required: [title, bogus]\n"),
			want: `unknown requirement "bogus"`,
		},
		{
			name: "unknown severity",
			doc:  mutate(minimalDoc, "    finding: CROSS_REF_BROKEN\n", "    finding: CROSS_REF_BROKEN\n    severity: fatal\n"),
			want: `must be "error" or "warning"`,
		},
		{
			name: "parameter of another kind",
			doc:  mutate(minimalDoc, "    finding: CROSS_REF_BROKEN\n", "    finding: CROSS_REF_BROKEN\n    pattern: '^x$'\n"),
			want: `pattern: is not a parameter of check kind "crossrefs"`,
		},
		{
			name: "finding required",
			doc:  mutate(minimalDoc, "  - kind: crossrefs\n    finding: CROSS_REF_BROKEN\n", "  - kind: crossrefs\n"),
			want: "finding: must be an upper-case token",
		},
		{
			name: "duplicate finding",
			doc:  mutate(minimalDoc, "  - kind: required_files\n", "  - kind: crossrefs\n    finding: CROSS_REF_BROKEN\n  - kind: required_files\n"),
			want: `duplicate finding "CROSS_REF_BROKEN"`,
		},
		{
			name: "concept required for name_pattern",
			doc:  mutate(minimalDoc, "  - kind: crossrefs\n", "  - kind: name_pattern\n    finding: NAME_MALFORMED\n    pattern: '^[a-z]+\\.md$'\n  - kind: crossrefs\n"),
			want: `concept: is required for check kind "name_pattern"`,
		},
		{
			name: "dangling concept reference",
			doc:  mutate(minimalDoc, "  - kind: crossrefs\n", "  - kind: crossrefs\n    concept: nope\n"),
			want: `unknown concept "nope"`,
		},
		{
			name: "dangling root reference",
			doc:  mutate(minimalDoc, "      root: demo\n      glob: index.md\n", "      root: nope\n      glob: index.md\n"),
			want: `unknown root "nope"`,
		},
		{
			name: "root path escapes the workspace",
			doc:  mutate(minimalDoc, "    path: demo\n", "    path: ../demo\n"),
			want: "clean workspace-relative path",
		},
		{
			name: "absolute glob",
			doc:  mutate(minimalDoc, "      glob: index.md\n", "      glob: /index.md\n"),
			want: "clean slash-separated pattern",
		},
		{
			name: "docs_url must be http(s)",
			doc:  mutate(minimalDoc, "label: Demo framework\n", "label: Demo framework\ndocs_url: ftp://example.com\n"),
			want: "absolute http(s) URL",
		},
		{
			name: "negative priority",
			doc:  mutate(minimalDoc, "label: Demo framework\n", "label: Demo framework\npriority: -1\n"),
			want: "must be >= 0",
		},
		{
			name: "detect signal required",
			doc:  mutate(minimalDoc, detectBlock, "detect: {}\n"),
			want: "at least one all_of or any_of skill is required",
		},
		{
			// Review 112: markers are an ordering signal, not a visibility one, so
			// a markers-only definition must not load — it could never be
			// recognised, and accepting it silently would be a trap.
			name: "markers alone are not a visibility signal",
			doc:  mutate(minimalDoc, detectBlock, "detect:\n  markers:\n    - demo/index.md\n"),
			want: "markers alone never make a framework visible",
		},
		{
			name: "skill list requires dirs",
			doc: mutate(minimalDoc, detectBlock,
				"detect:\n  skills:\n    any_of: [demo-init]\n"),
			want: "detect.skills.dirs: is required when all_of or any_of is set",
		},
		{
			name: "companion files require a directory concept",
			doc:  mutate(minimalDoc, "    extract: [title, cross_refs, key_files]\n", "    extract: [title, cross_refs, key_files]\n    files: [index.md]\n"),
			want: "companion files require match.dir",
		},
		{
			name: "duplicate concept kind",
			doc:  mutate(minimalDoc, "  - kind: page\n", "  - kind: index\n"),
			want: `duplicate concept kind "index"`,
		},
		{
			name: "duplicate root id",
			doc:  mutate(minimalDoc, "  - id: demo\n    path: demo\n", "  - id: demo\n    path: demo\n  - id: demo\n    path: other\n"),
			want: `duplicate root id "demo"`,
		},
		{
			name: "regex must compile",
			doc:  docWithCheck("  - kind: content_regex\n    finding: BAD_REGEX\n    concept: index\n    regex: '('\n"),
			want: "regex: must compile",
		},
		{
			name: "capture must name a group",
			doc:  docWithCheck("  - kind: content_regex\n    finding: BAD_CAPTURE\n    concept: index\n    regex: '^# ADR-([0-9]{3}):'\n    capture: number\n"),
			want: "capture: names no group in regex",
		},
		{
			name: "equals requires a capture",
			doc:  docWithCheck("  - kind: content_regex\n    finding: NO_CAPTURE\n    concept: index\n    regex: '^# ADR-'\n    equals: filename_number\n"),
			want: "capture: is required when equals is set",
		},
		{
			name: "unknown equals token",
			doc:  docWithCheck("  - kind: content_regex\n    finding: BAD_EQUALS\n    concept: index\n    regex: '^(?P<n>[0-9]+)'\n    capture: n\n    equals: filename_length\n"),
			want: `unknown comparison "filename_length"`,
		},
		{
			name: "index_completeness needs covers",
			doc:  docWithCheck("  - kind: index_completeness\n    finding: INDEX_STALE\n    concept: index\n"),
			want: `covers: is required for check kind "index_completeness"`,
		},
		{
			name: "frontmatter_keys needs keys",
			doc:  docWithCheck("  - kind: frontmatter_keys\n    finding: FRONTMATTER\n    concept: index\n"),
			want: `keys: is required for check kind "frontmatter_keys"`,
		},
		{
			name: "references needs a target concept",
			doc:  docWithCheck("  - kind: references\n    finding: REF\n    concept: index\n    field: superseded_by\n"),
			want: `of: is required for check kind "references"`,
		},
		{
			name: "unknown action requirement",
			doc:  mutate(minimalDoc, "checks:\n", "actions:\n  - kind: init\n    label: Init\n    requires: [nope]\nchecks:\n"),
			want: `unknown requirement "nope"`,
		},
		{
			name: "view section names an unknown concept",
			doc:  mutate(minimalDoc, "checks:\n", "views:\n  sections:\n    - id: tree\n      label: Tree\n      kind: tree\n      concepts: [nope]\nchecks:\n"),
			want: `unknown concept "nope"`,
		},
		{
			name: "unknown view section kind",
			doc:  mutate(minimalDoc, "checks:\n", "views:\n  sections:\n    - id: tree\n      label: Tree\n      kind: gallery\nchecks:\n"),
			want: "must be one of tree, findings, checks",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sdd.Parse("frameworks/demo.yaml", []byte(tc.doc))
			if err == nil {
				t.Fatalf("Parse() accepted an invalid document")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse() error = %q, want it to contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "frameworks/demo.yaml") {
				t.Errorf("Parse() error = %q, want the document label", err)
			}
		})
	}
}

// TestParse_ReportsEveryFault proves the loader aggregates faults instead of
// stopping at the first one, so a broken document is repaired in one pass.
func TestParse_ReportsEveryFault(t *testing.T) {
	doc := mutate(minimalDoc, "label: Demo framework\n", "label: Demo framework\npriority: -2\ndocs_url: nope\n")
	_, err := sdd.Parse("demo.yaml", []byte(doc))
	if err == nil {
		t.Fatal("Parse() accepted a document with two faults")
	}
	for _, want := range []string{"priority: must be >= 0", "docs_url: must be an absolute http(s) URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Parse() error = %q, want it to contain %q", err, want)
		}
	}
}

// TestParse_AcceptsEveryDeclaredPrimitive covers the primitives the built-ins
// do not exercise (frontmatter_keys, unique, references, index_completeness),
// so the vocabulary is pinned by a passing parse rather than by declaration.
func TestParse_AcceptsEveryDeclaredPrimitive(t *testing.T) {
	doc := `schema: sdd-framework/v1
id: kitchen-sink
label: Kitchen sink
docs_url: https://example.com/spec
priority: 7
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    all_of: [sink-init]
    any_of: [sink-check, sink-update]
roots:
  - id: docs
    path: docs
    label: Docs
    required: true
concepts:
  - kind: index
    match:
      root: docs
      glob: index.md
    extract: [title, cross_refs]
    required: [title, non_empty]
  - kind: record
    match:
      root: docs
      glob: records/*.md
    extract: [title, number, status, superseded_by, frontmatter]
    required: [title, status]
  - kind: bundle
    match:
      root: docs
      glob: bundles/*
      dir: true
    files: [index.md]
checks:
  - kind: required_files
    finding: ROOT_FILE_MISSING
    files: [index.md]
  - kind: required_files
    finding: BUNDLE_FILE_MISSING
    concept: bundle
    from: files
  - kind: name_pattern
    finding: RECORD_NAME_MALFORMED
    concept: record
    pattern: '^[0-9]{3}-[a-z-]+\.md$'
    allow: [_template.md]
  - kind: crossrefs
    finding: CROSS_REF_BROKEN
    concept: index
  - kind: index_completeness
    finding: INDEX_STALE
    concept: index
    covers: [record]
  - kind: frontmatter_keys
    finding: FRONTMATTER_INCOMPLETE
    concept: record
    keys: [status, owner]
  - kind: content_regex
    finding: NUMBER_MISMATCH
    concept: record
    in: first_line
    regex: '^# (?P<number>[0-9]{3}): \S'
    capture: number
    equals: filename_number
  - kind: unique
    finding: DUPLICATE_NUMBER
    concept: record
    field: number
  - kind: references
    finding: TARGET_MISSING
    cycle_finding: CYCLE
    concept: record
    field: superseded_by
    of: record
    acyclic: true
actions:
  - kind: init
    label: Init
    skill: sink-init
    prompt: Initialize.
  - kind: update
    label: Update
    skill: sink-update
    requires: [root, findings, concept:record]
views:
  sections:
    - id: tree
      label: Tree
      kind: tree
      concepts: [index, record, bundle]
    - id: findings
      label: Findings
      kind: findings
`
	fw, err := sdd.Parse("kitchen-sink.yaml", []byte(doc))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(fw.Checks) != 9 || len(fw.Concepts) != 3 || len(fw.Actions) != 2 || len(fw.Views.Sections) != 2 {
		t.Fatalf("Parse() dropped declarations: %d checks, %d concepts, %d actions, %d sections",
			len(fw.Checks), len(fw.Concepts), len(fw.Actions), len(fw.Views.Sections))
	}
	if fw.Priority != 7 || fw.DocsURL != "https://example.com/spec" {
		t.Errorf("Parse() lost priority/docs_url: %d / %q", fw.Priority, fw.DocsURL)
	}
	if !fw.Concepts[2].Match.Dir || len(fw.Concepts[2].Files) != 1 {
		t.Errorf("Parse() lost the directory concept's companion files: %+v", fw.Concepts[2])
	}
	if fw.Checks[6].In != sdd.CheckInFirstLine || fw.Checks[6].Equals != sdd.CheckEqualsFilenameNumber {
		t.Errorf("Parse() lost the content_regex comparison: %+v", fw.Checks[6])
	}
	if !fw.Checks[8].Acyclic || fw.Checks[8].Finding != "TARGET_MISSING" || fw.Checks[8].CycleFinding != "CYCLE" {
		t.Errorf("Parse() lost the acyclic references check with its cycle finding: %+v", fw.Checks[8])
	}
}

func TestParse_DefaultsDocumentLabel(t *testing.T) {
	_, err := sdd.Parse("", []byte("schema: nope\nid: x\nlabel: X\n"))
	if err == nil || !strings.Contains(err.Error(), "sdd: document: schema:") {
		t.Fatalf("Parse() error = %v, want the placeholder document label", err)
	}
}

func mutate(doc, old, replacement string) string {
	if !strings.Contains(doc, old) {
		panic("mutation target not found: " + old)
	}
	return strings.Replace(doc, old, replacement, 1)
}

func docWithCheck(check string) string {
	return mutate(minimalDoc, "checks:\n", "checks:\n"+check)
}
