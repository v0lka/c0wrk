package sdd

// Regression tests for the parser/validator fixes reviewed against the
// c0wrk change set: bounded glob matching, CommonMark fences, document-text
// normalization, frontmatter/body separation, anchor namespaces, successor
// references, root validation and presence semantics. Each test names the
// behavior it pins.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/safeio"
)

// writeTree materializes files under a fresh workspace and returns its root.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
		}
		if err := safeio.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
	return dir
}

const crossrefProbeFW = `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/plan.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: plan
    match:
      root: docs
      glob: plan.md
    extract: [title, cross_refs]
checks:
  - kind: crossrefs
    finding: REF_BROKEN
    concept: plan
`

// findingDetails flattens a tree's findings to "KIND detail" lines.
func findingDetails(t *testing.T, tree *Tree) []string {
	t.Helper()
	out := make([]string, 0, len(tree.Findings))
	for _, f := range tree.Findings {
		out = append(out, f.Kind+" "+f.Detail)
	}
	return out
}

// TestParseTree_ViewSectionsStayScoped pins the per-section membership rule:
// two tree sections over disjoint concepts share the forest (and its common
// ancestor folders), but each section's Nodes and Members name only its own
// concepts' chains — a view may not leak the other section's documents
// through the shared ancestor.
func TestParseTree_ViewSectionsStayScoped(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: one
    match:
      root: docs
      glob: "pages/a.md"
    extract: [title]
  - kind: two
    match:
      root: docs
      glob: "pages/b.md"
    extract: [title]
views:
  sections:
    - id: one
      label: One
      kind: tree
      concepts: [one]
    - id: two
      label: Two
      kind: tree
      concepts: [two]
`)
	dir := writeTree(t, map[string]string{
		"docs/index.md":   "# Index\n",
		"docs/pages/a.md": "# A\n",
		"docs/pages/b.md": "# B\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]View, len(tree.Views))
	for _, view := range tree.Views {
		byID[view.ID] = view
	}
	one, ok := byID["one"]
	if !ok || !slices.Equal(one.Nodes, []string{"docs/pages"}) {
		t.Fatalf("section one nodes = %v (present %v), want the shared ancestor as its top", one.Nodes, ok)
	}
	if slices.Contains(one.Members, "docs/pages/b.md") {
		t.Errorf("section one members leak concept two: %v", one.Members)
	}
	if !slices.Contains(one.Members, "docs/pages/a.md") {
		t.Errorf("section one members lost their own document: %v", one.Members)
	}
	two := byID["two"]
	if slices.Contains(two.Members, "docs/pages/a.md") {
		t.Errorf("section two members leak concept one: %v", two.Members)
	}
	if !slices.Contains(two.Members, "docs/pages/b.md") {
		t.Errorf("section two members lost their own document: %v", two.Members)
	}
}

// TestParseTree_TopicsItemList pins the topic-entry predicate: every
// CommonMark list marker (dash, plus, ordered) is an entry, and the topic's
// own container heading ("User Stories", plural in any spelling) is never an
// entry — a heading-only section satisfies nothing.
func TestParseTree_TopicsItemList(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/spec.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: doc
    match:
      root: docs
      glob: "*.md"
    extract: [title, requirements, user_stories]
    required: [requirements, user_stories]
`)
	t.Run("plus and ordered markers are entries, headings are not", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"docs/spec.md": "# Spec\n\n## Requirements\n\n+ Requirement one\n\n## User Stories\n\n1. As a user I want X\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		doc, ok := tree.DocumentByPath("docs/spec.md")
		if !ok {
			t.Fatal("document missing")
		}
		if !slices.Equal(doc.Requirements, []string{"Requirement one"}) {
			t.Errorf("Requirements = %v, want the plus-marked entry", doc.Requirements)
		}
		if !slices.Equal(doc.UserStories, []string{"As a user I want X"}) {
			t.Errorf("UserStories = %v, want the ordered entry without the container heading", doc.UserStories)
		}
		if len(doc.Missing) != 0 {
			t.Errorf("Missing = %v, want none", doc.Missing)
		}
	})
	t.Run("a leaf entry keeps its own list to itself", func(t *testing.T) {
		// The Spec Kit template shape: the first heading carrying the topic word
		// is the ENTRY ("### User Story 1 …"), not the container, so the
		// acceptance scenarios below it are that entry's detail — they must not
		// read as further user stories.
		dir := writeTree(t, map[string]string{"docs/spec.md": "# Spec\n\n## User Scenarios & Testing\n\n### User Story 1 - Sign in with a password (Priority: P1)\n\nA returning user signs in.\n\n**Acceptance Scenarios**:\n\n1. **Given** a registered user, **When** they sign in, **Then** they land on the dashboard.\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		doc, ok := tree.DocumentByPath("docs/spec.md")
		if !ok {
			t.Fatal("document missing")
		}
		if !slices.Equal(doc.UserStories, []string{"User Story 1 - Sign in with a password (Priority: P1)"}) {
			t.Errorf("UserStories = %v, want exactly the leaf entry heading", doc.UserStories)
		}
	})
	t.Run("a heading-only section satisfies nothing", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"docs/spec.md": "# Spec\n\n## Requirements\n\n- Requirement one\n\n## User Stories\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		doc, ok := tree.DocumentByPath("docs/spec.md")
		if !ok {
			t.Fatal("document missing")
		}
		if len(doc.UserStories) != 0 {
			t.Errorf("UserStories = %v, want none (the bare container heading is not a story)", doc.UserStories)
		}
		if !slices.Equal(doc.Missing, []string{"user_stories"}) {
			t.Errorf("Missing = %v, want [user_stories]", doc.Missing)
		}
	})
}

// TestParseTree_SpecKitFixtureUserStories pins the leaf-entry rule on the
// upstream Spec Kit feature-spec template that ships as a fixture: the template
// puts each story under its own `### User Story N …` heading and lists the
// acceptance scenarios as an ordered list below it, so a correct model reports
// exactly one user story and no scenario line among the stories.
func TestParseTree_SpecKitFixtureUserStories(t *testing.T) {
	fw, err := Builtin("spec-kit")
	if err != nil {
		t.Fatalf("Builtin(spec-kit) error = %v", err)
	}
	tree, err := ParseTree(filepath.Join("testdata", "spec-kit"), fw)
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}
	doc, ok := tree.DocumentByPath("specs/001-login/spec.md")
	if !ok {
		t.Fatalf("fixture document missing (documents = %d)", len(tree.Documents))
	}
	if len(doc.UserStories) != 1 {
		t.Fatalf("UserStories = %v, want exactly the one story heading", doc.UserStories)
	}
	if !strings.HasPrefix(doc.UserStories[0], "User Story 1") {
		t.Errorf("UserStories[0] = %q, want the story heading", doc.UserStories[0])
	}
	for _, story := range doc.UserStories {
		if strings.Contains(story, "Given") {
			t.Errorf("UserStories contains an acceptance scenario: %q", story)
		}
	}
}

func mustParse(t *testing.T, doc string) Framework {
	t.Helper()
	fw, err := Parse("probe.yaml", []byte(doc))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	return fw
}

// TestParseTree_NestedFencesStayExamples pins the CommonMark fence state
// machine: an inner fence never ends the outer example, a mismatched
// character or a too-short run does not close a block, and the prose after a
// properly closed nested example is parsed again.
func TestParseTree_NestedFencesStayExamples(t *testing.T) {
	fw := mustParse(t, crossrefProbeFW)
	t.Run("nested four-tick example is not prose", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"docs/plan.md": "# Plan\n\n````markdown\n```markdown\n[example](./missing.md)\n```\n````\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 0 {
			t.Errorf("findings = %v, want none (the link lives inside an example)", findings)
		}
	})
	t.Run("odd inner fence count cannot flip the tail", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"docs/plan.md": "# Plan\n\n````text\n```\ninner\n````\n\n## Later\n\n[real](./nope.md)\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		findings := findingDetails(t, tree)
		if len(findings) != 1 || !strings.Contains(findings[0], "./nope.md") {
			t.Errorf("findings = %v, want exactly the real broken link after the block", findings)
		}
	})
	t.Run("tilde block closes only on tildes", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"docs/plan.md": "# Plan\n\n~~~text\n```\ninner\n~~~\n\n[real](./nope.md)\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		findings := findingDetails(t, tree)
		if len(findings) != 1 || !strings.Contains(findings[0], "./nope.md") {
			t.Errorf("findings = %v, want exactly the real broken link after the block", findings)
		}
	})
	t.Run("plain fence still hides its content", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"docs/plan.md": "# Plan\n\n```markdown\n[example](./missing.md)\n```\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 0 {
			t.Errorf("findings = %v, want none", findings)
		}
	})
}

// TestParseTree_AnchorNamespace pins the renderer-shaped anchor judgments:
// setext headings, duplicate suffixes and line anchors resolve; empty
// fragments and heading-less targets are never judged; genuinely unknown
// anchors still report.
func TestParseTree_AnchorNamespace(t *testing.T) {
	fw := mustParse(t, crossrefProbeFW)
	doc := "# Plan\n\n" +
		"[setext](setext.md#usage)\n" +
		"[dup](dup.md#setup-1)\n" +
		"[line](guide.md#L10)\n" +
		"[range](guide.md#L10-L20)\n" +
		"[bare](#)\n" +
		"[source](../src/auth.ts#L42)\n" +
		"[encoded](../src/my%20file.ts)\n" +
		"[broken](guide.md#nope)\n"
	dir := writeTree(t, map[string]string{
		"docs/plan.md":   doc,
		"docs/setext.md": "Usage\n=====\n\nbody\n",
		"docs/dup.md":    "## Setup\n\n## Setup\n",
		"docs/guide.md":  "# Usage\n",
		"src/auth.ts":    "export const x = 1;\n",
		"src/my file.ts": "export const y = 2;\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	findings := findingDetails(t, tree)
	if len(findings) != 1 || !strings.Contains(findings[0], "guide.md#nope") {
		t.Errorf("findings = %v, want exactly the unknown guide.md#nope anchor", findings)
	}
}

// TestParseTree_SuccessorReferences pins the six-digit-capped successor token
// (a four-digit successor resolves) and the resolution-time year guard.
func TestParseTree_SuccessorReferences(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: refs
label: Refs
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: record
    match:
      root: docs
      glob: "*.md"
    extract: [title, superseded_by]
checks:
  - kind: references
    finding: TARGET_MISSING
    concept: record
    field: superseded_by
    of: record
    acyclic: true
`)
	t.Run("four-digit successor resolves", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/index.md":  "# Index\n",
			"docs/1000-a.md": "# A\n\n## Status\n\nSuperseded by [ADR-1024](./1024-b.md)\n",
			"docs/1024-b.md": "# B\n\n## Status\n\nAccepted\n",
		})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 0 {
			t.Errorf("findings = %v, want none (the successor exists)", findings)
		}
	})
	t.Run("a 7+-digit token truncates to its first six digits", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/index.md":  "# Index\n",
			"docs/0001-a.md": "# A\n\n## Status\n\nSuperseded by ADR-1234567\n",
		})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		// The unanchored plain form reads ADR-123456 out of the 7-digit run:
		// the truncation is the documented behavior, so the finding names the
		// TRUNCATED number (no record 123456 exists, and it is not a year).
		// The negative assertion is what pins the TRUNCATION: a parser that
		// captured the whole run would produce a finding containing the full
		// "1234567", which still contains the "123456" substring.
		findings := findingDetails(t, tree)
		if len(findings) != 1 || !strings.Contains(findings[0], "123456") {
			t.Errorf("findings = %v, want exactly the truncated unresolved superseded_by 123456", findings)
		}
		if strings.Contains(findings[0], "1234567") {
			t.Errorf("finding %q names the full 7-digit run, want the truncated 123456", findings[0])
		}
	})
	t.Run("unresolved calendar year is not a missing target", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/index.md":  "# Index\n",
			"docs/0001-a.md": "# A\n\n## Status\n\nSuperseded in [2024], rewritten later\n",
		})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 0 {
			t.Errorf("findings = %v, want none (a bracketed year names no record)", findings)
		}
	})
	t.Run("acyclic check still reports a missing target", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/index.md":  "# Index\n",
			"docs/0001-a.md": "# A\n\n## Status\n\nSuperseded by ADR-099\n",
		})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		findings := findingDetails(t, tree)
		if len(findings) != 1 || !strings.Contains(findings[0], "TARGET_MISSING") {
			t.Errorf("findings = %v, want the unresolved successor", findings)
		}
	})
	t.Run("cycle carries the declared cycle finding", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/index.md":  "# Index\n",
			"docs/0001-a.md": "# A\n\n## Status\n\nSuperseded by ADR-0002\n",
			"docs/0002-b.md": "# B\n\n## Status\n\nSuperseded by ADR-0001\n",
		})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		findings := findingDetails(t, tree)
		if len(findings) != 1 || !strings.Contains(findings[0], "cycle in superseded_by") {
			t.Errorf("findings = %v, want one cycle finding", findings)
		}
	})
}

// TestParseTree_ReferencePathIdentity pins reference resolution by path with
// and without the .md suffix: the identity index answers both spellings.
func TestParseTree_ReferencePathIdentity(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: refs
label: Refs
detect:
  markers: [docs/source.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: record
    match:
      root: docs
      glob: "records/*.md"
    extract: [title]
  - kind: source
    match:
      root: docs
      glob: source.md
    extract: [title]
checks:
  - kind: references
    finding: REF_UNRESOLVED
    concept: source
    field: title
    of: record
`)
	dir := writeTree(t, map[string]string{
		"docs/source.md":        "# records/001-a\n",
		"docs/records/001-a.md": "# records/001-a.md\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	// The source's title cites the record's path WITHOUT the extension; the
	// record exists under the extensioned spelling. Both identities resolve.
	if findings := findingDetails(t, tree); len(findings) != 0 {
		t.Errorf("findings = %v, want none (extensionless path identity resolves)", findings)
	}
}

// TestParseTree_FrontmatterIsMetadata pins the YAML/body separation: quoted
// keys, nested keys and block scalars behave as YAML, and the block never
// leaks headings or links into the prose.
func TestParseTree_FrontmatterIsMetadata(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/a.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: doc
    match:
      root: docs
      glob: "*.md"
    extract: [title, headings, cross_refs, frontmatter]
checks:
  - kind: frontmatter_keys
    finding: KEYS_MISSING
    concept: doc
    keys: [owner]
`)
	dir := writeTree(t, map[string]string{
		"docs/a.md": "---\n\"owner\": Alice\nmetadata:\n  owner: Bob\nnotes: |\n  # Example only\n  [sample](gone.md)\n---\n\n# Actual Title\n\n[real](./b.md)\n",
		"docs/b.md": "---\nowner: Team\n---\n\n# B\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	doc, ok := tree.DocumentByPath("docs/a.md")
	if !ok {
		t.Fatal("document missing")
	}
	if doc.Title != "Actual Title" {
		t.Errorf("Title = %q, want the body heading, not the YAML example", doc.Title)
	}
	if fm := doc.Frontmatter; fm["owner"] != "Alice" {
		t.Errorf("Frontmatter[owner] = %q, want Alice (quoted key parsed as YAML)", fm["owner"])
	}
	for _, detail := range findingDetails(t, tree) {
		if strings.Contains(detail, "gone.md") {
			t.Errorf("frontmatter block leaked a link into the prose: %v", detail)
		}
	}
	if findings := findingDetails(t, tree); len(findings) != 0 {
		t.Errorf("findings = %v, want none", findings)
	}
}

// TestParseTree_NormalizedDocumentText pins BOM/CRLF handling: a CRLF
// checkout satisfies $-anchored regex checks and a BOM does not hide
// frontmatter.
func TestParseTree_NormalizedDocumentText(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/a.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: doc
    match:
      root: docs
      glob: "*.md"
    extract: [frontmatter]
checks:
  - kind: content_regex
    finding: SECTION_MISSING
    concept: doc
    regex: '^## ADDED Requirements$'
  - kind: frontmatter_keys
    finding: KEYS_MISSING
    concept: doc
    keys: [owner]
`)
	dir := writeTree(t, map[string]string{})
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := "---\r\nowner: Core\r\n---\r\n\r\n## ADDED Requirements\r\n\r\n### Requirement: Sign in\r\n"
	b := "\xEF\xBB\xBF---\nowner: Alice\n---\n\n# B\n\n## ADDED Requirements\n"
	if err := os.WriteFile(filepath.Join(dir, "docs", "a.md"), []byte(a), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "b.md"), []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	if findings := findingDetails(t, tree); len(findings) != 0 {
		t.Errorf("findings = %v, want none (CRLF and BOM are normalized at read)", findings)
	}
}

// TestParseTree_RequiredFilesPinsFiles pins that required_files demands a
// regular file: a directory of the required name is missing.
func TestParseTree_RequiredFilesPinsFiles(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [specs/META.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: specs
    path: specs
    label: Specs
concepts:
  - kind: meta
    match:
      root: specs
      glob: META.md
    extract: [title]
checks:
  - kind: required_files
    finding: ROOT_FILE_MISSING
    files: [INDEX.md]
`)
	t.Run("directory standing in for the required file reports", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"specs/META.md": "# Meta\n", "specs/INDEX.md/keep": "x\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		findings := findingDetails(t, tree)
		if len(findings) != 1 || !strings.Contains(findings[0], "INDEX.md") {
			t.Errorf("findings = %v, want the directory-as-file reported missing", findings)
		}
	})
	t.Run("regular file passes", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"specs/META.md": "# Meta\n", "specs/INDEX.md": "# Index\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 0 {
			t.Errorf("findings = %v, want none", findings)
		}
	})
}

// TestParseTree_RequiredFilesConceptScoped pins the concept-scoped modes:
// extracted paths are checked only for the declared concept, and explicit
// files resolve next to every instance.
func TestParseTree_RequiredFilesConceptScoped(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: page
    match:
      root: docs
      glob: "pages/**/*.md"
    extract: [key_files]
  - kind: feature
    match:
      root: docs
      glob: "features/*"
      dir: true
checks:
  - kind: required_files
    finding: KEY_PATH_MISSING
    concept: page
    from: key_files
`)
	t.Run("from is scoped to the declared concept", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/index.md":       "# Index\n",
			"docs/pages/b/two.md": "# Two\n\n## Key Files\n\n- `docs/pages/b/gone.md`\n",
			// A document OUTSIDE the declared concept carries the same
			// broken key file and must NOT be checked. Extracted paths are
			// workspace-relative, so the missing file is spelled with the
			// root prefix.
			"docs/other/x.md": "# X\n\n## Key Files\n\n- `docs/gone-everywhere.md`\n",
		})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		findings := findingDetails(t, tree)
		if len(findings) != 1 || !strings.Contains(findings[0], "pages/b") {
			t.Errorf("findings = %v, want exactly the concept-scoped pages/b entry", findings)
		}
	})
}

// TestParseTree_RequiredFilesExtractedPathsAreExistenceOnly pins that a
// directory listed under "## Key Files" is NOT a finding. The regular-file
// rule belongs to literal `files:` and to concept companion files, which
// always name a file; extracted entries are the document's own claim about
// the workspace and real specs list package directories there. Requiring a
// regular file for them turns a correct tree into permanent PATH_MISSING
// findings — which is exactly what the shipped `vibespec` framework did.
func TestParseTree_RequiredFilesExtractedPathsAreExistenceOnly(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: page
    match:
      root: docs
      glob: "pages/*.md"
    extract: [key_files]
checks:
  - kind: required_files
    finding: KEY_PATH_MISSING
    from: key_files
`)
	dir := writeTree(t, map[string]string{
		"docs/index.md":          "# Index\n",
		"docs/pages/sub/keep.md": "# Keep\n",
		// The first entry is an existing DIRECTORY and must pass; the second
		// is absent and must still be reported.
		"docs/pages/two.md": "# Two\n\n## Key Files\n\n- `docs/pages/sub`\n- `docs/pages/gone.md`\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	findings := findingDetails(t, tree)
	if len(findings) != 1 || !strings.Contains(findings[0], "gone.md") {
		t.Errorf("findings = %v, want exactly the absent gone.md entry", findings)
	}
}

// TestParseTree_IndexCoversDirectories pins that a directory concept in
// covers is checked, not silently skipped.
func TestParseTree_IndexCoversDirectories(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: index
    match:
      root: docs
      glob: index.md
    extract: [title]
  - kind: bundle
    match:
      root: docs
      glob: "bundles/*"
      dir: true
checks:
  - kind: index_completeness
    finding: INDEX_STALE
    concept: index
    covers: [bundle]
`)
	t.Run("unlisted directory reports", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"docs/index.md": "# Index\n", "docs/bundles/a/keep": "x\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 1 {
			t.Errorf("findings = %v, want the unlisted bundle", findings)
		}
	})
	t.Run("listed directory passes", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"docs/index.md": "# Index\n\n[bundles/a](bundles/a)\n", "docs/bundles/a/keep": "x\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 0 {
			t.Errorf("findings = %v, want none", findings)
		}
	})
}

// TestParseTree_RequiredRootGatesPresence pins the Required semantics.
func TestParseTree_RequiredRootGatesPresence(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [extra/readme.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: main
    path: specs
    label: Main
    required: true
  - id: extra
    path: extra
    label: Extra
concepts:
  - kind: note
    match:
      root: extra
      glob: "*.md"
    extract: [title]
`)
	dir := writeTree(t, map[string]string{"extra/readme.md": "# Readme\n"})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Present {
		t.Errorf("Present = true, want false (the required root is missing)")
	}
	found := false
	for _, w := range tree.Warnings {
		if strings.Contains(w, "required root") && strings.Contains(w, "main") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want a warning naming the missing required root", tree.Warnings)
	}
}

// TestParseTree_RequiredRootOutsideWorkspaceGatesPresence pins that a REQUIRED
// root whose directory exists but does not resolve inside the workspace (a
// symlink pointing out of the tree) fails the presence gate exactly like a
// missing required root: Present is false, the root is named in Warnings and
// recorded as not present — even when other, optional roots exist and are
// walked.
func TestParseTree_RequiredRootOutsideWorkspaceGatesPresence(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [extra/readme.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: main
    path: specs
    label: Main
    required: true
  - id: extra
    path: extra
    label: Extra
concepts:
  - kind: note
    match:
      root: extra
      glob: "*.md"
    extract: [title]
`)
	dir := writeTree(t, map[string]string{"extra/readme.md": "# Readme\n"})
	outside := t.TempDir()
	// The required root's entry exists inside the workspace, so resolve and
	// Stat both succeed — but its physical target lives outside the tree, so
	// the root cannot be walked and must not count as present.
	if err := os.Symlink(outside, filepath.Join(dir, "specs")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Present {
		t.Errorf("Present = true, want false (the required root does not resolve inside the workspace)")
	}
	for _, r := range tree.Roots {
		if r.ID == "main" && r.Present {
			t.Errorf("root %q recorded Present = true, want false", r.ID)
		}
	}
	found := false
	for _, w := range tree.Warnings {
		if strings.Contains(w, "required root") && strings.Contains(w, "main") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want a warning naming the unusable required root", tree.Warnings)
	}
}

// TestParseTree_SymlinkedRootWalks pins that a workspace-internal symlinked
// root is walked through its target while instance paths stay in the
// declared namespace.
func TestParseTree_SymlinkedRootWalks(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: page
    match:
      root: docs
      glob: "**/*.md"
    extract: [title]
`)
	dir := writeTree(t, map[string]string{
		"real/index.md":    "# Index\n",
		"real/nested/a.md": "# A\n",
	})
	// The declared ROOT itself is a symlink to a workspace-internal
	// directory: containment accepts it, Stat follows it, but WalkDir would
	// see a non-directory entry and silently skip the whole root.
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "docs")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tree.DocumentByPath("docs/nested/a.md"); !ok {
		t.Errorf("documents = %v, want the symlinked root walked under its declared namespace", tree.Documents)
	}
}

// TestParseTree_ProseChecksIgnoreFencedExamples pins the prose input mode.
func TestParseTree_ProseChecksIgnoreFencedExamples(t *testing.T) {
	mk := func(in string) Framework {
		return mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/spec.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: spec
    match:
      root: docs
      glob: spec.md
    extract: [title]
checks:
  - kind: content_regex
    finding: REQUIREMENT_MISSING
    concept: spec
    in: `+in+`
    regex: '^### Requirement: '
`)
	}
	doc := "# Spec\n\n```markdown\n### Requirement: Example\n```\n\n## Body\n"
	t.Run("prose mode ignores the fenced example", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"docs/spec.md": doc})
		tree, err := ParseTree(dir, mk(CheckInProse))
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 1 {
			t.Errorf("findings = %v, want the requirement reported missing", findings)
		}
	})
	t.Run("body mode keeps raw semantics", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"docs/spec.md": doc})
		tree, err := ParseTree(dir, mk(CheckInBody))
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 0 {
			t.Errorf("findings = %v, want none (raw body still contains the line)", findings)
		}
	})
}

// TestParseTree_OptionalFirstHeading pins the ADR number semantics: numeric
// agreement, unpadded spellings, free-form headings and leading blanks.
func TestParseTree_OptionalFirstHeading(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: adr
    match:
      root: docs
      glob: "*.md"
    extract: [title]
checks:
  - kind: content_regex
    finding: NUMBER_MISMATCH
    concept: adr
    in: first_heading
    optional: true
    regex: '^ADR-(?P<number>\d+): \S'
    capture: number
    equals: filename_number
`)
	cases := []struct {
		name, content string
		wantFindings  int
	}{
		{"padded equal", "# ADR-043: One\n", 0},
		{"unpadded equal", "# ADR-43: One\n", 0},
		{"free-form heading", "# Decision document\n", 0},
		{"leading blank line", "\n# ADR-043: One\n", 0},
		{"numeric mismatch", "# ADR-030: One\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeTree(t, map[string]string{"docs/043-ok.md": tc.content})
			tree, err := ParseTree(dir, fw)
			if err != nil {
				t.Fatal(err)
			}
			if findings := findingDetails(t, tree); len(findings) != tc.wantFindings {
				t.Errorf("findings = %v, want %d", findings, tc.wantFindings)
			}
		})
	}
}

// TestParseTree_DeepGlobStaysBounded pins that a pathological glob chain
// completes: the `**` recursion is memoized, so a hostile checkout cannot
// turn a refresh into combinatorial backtracking.
func TestParseTree_DeepGlobStaysBounded(t *testing.T) {
	glob := strings.Repeat("**/", 16) + "missing.md"
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: ghost
    match:
      root: docs
      glob: "`+glob+`"
    extract: [title]
`)
	dir := writeTree(t, map[string]string{"docs/index.md": "# Index\n"})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Documents) != 0 {
		t.Errorf("documents = %d, want none (the glob names a missing file)", len(tree.Documents))
	}
}

// TestParseTree_CompanionAndInstanceFiles pins the two instance-scoped
// required_files modes: explicit files resolve NEXT TO EVERY INSTANCE of the
// declared concept (never against the workspace root), and a companion file
// that is a directory does not satisfy the declaration.
func TestParseTree_CompanionAndInstanceFiles(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/pages/a.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: page
    match:
      root: docs
      glob: "pages/**/*.md"
    extract: [title]
checks:
  - kind: required_files
    finding: INDEX_MISSING
    concept: page
    files: [index.md]
`)
	t.Run("explicit files resolve beside each instance", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/pages/a.md":     "# A\n",
			"docs/pages/index.md": "# Index\n",
			"docs/pages/sub/b.md": "# B\n",
			"docs/index.md":       "# Root index\n",
		})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		findings := findingDetails(t, tree)
		if len(findings) != 1 {
			t.Fatalf("findings = %v, want exactly one (the root-level index.md satisfies nothing)", findings)
		}
		if tree.Findings[0].File != "docs/pages/sub/index.md" {
			t.Errorf("finding file = %q, want docs/pages/sub/index.md (resolved beside the sub instance)", tree.Findings[0].File)
		}
	})
	t.Run("a directory of the companion name is missing", func(t *testing.T) {
		dir := writeTree(t, map[string]string{"docs/pages/a.md": "# A\n", "docs/pages/index.md/keep": "x\n"})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		findings := findingDetails(t, tree)
		if len(findings) != 1 || !strings.Contains(findings[0], "index.md") {
			t.Errorf("findings = %v, want the directory-as-companion reported missing", findings)
		}
	})
}

// TestParseTree_CompanionDeclarationNeedsFiles pins the from: files mode: a
// directory concept's declared companions are asserted per instance, and a
// directory standing in for the companion reports.
func TestParseTree_CompanionDeclarationNeedsFiles(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/features/x]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: feature
    match:
      root: docs
      glob: "features/*"
      dir: true
    files: [spec.md]
checks:
  - kind: required_files
    finding: SPEC_MISSING
    concept: feature
    from: files
`)
	dir := writeTree(t, map[string]string{
		"docs/features/x/spec.md/keep": "x\n", // a directory standing in for spec.md
		"docs/features/y/keep":         "x\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	findings := findingDetails(t, tree)
	if len(findings) != 2 {
		t.Fatalf("findings = %v, want one per feature instance", findings)
	}
	files := []string{tree.Findings[0].File, tree.Findings[1].File}
	slices.Sort(files)
	if !slices.Equal(files, []string{"docs/features/x", "docs/features/y"}) {
		t.Errorf("finding files = %v, want the companion reported missing on both feature instances", files)
	}
}

// TestParseTree_IndexIgnoresFrontmatter pins that the index-completeness scan
// reads the index document's BODY: a path mentioned only inside the
// frontmatter is metadata, not a listing, so it covers nothing.
func TestParseTree_IndexIgnoresFrontmatter(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: index
    match:
      root: docs
      glob: index.md
    extract: [title]
  - kind: page
    match:
      root: docs
      glob: "pages/*.md"
    extract: [title]
checks:
  - kind: index_completeness
    finding: INDEX_STALE
    concept: index
    covers: [page]
`)
	dir := writeTree(t, map[string]string{
		"docs/index.md":   "---\nlisted:\n  - pages/b.md\n---\n\n# Index\n\n[pages/a.md](pages/a.md)\n",
		"docs/pages/a.md": "# A\n",
		"docs/pages/b.md": "# B\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	findings := findingDetails(t, tree)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one (the frontmatter mention covers nothing)", findings)
	}
	if tree.Findings[0].File != "docs/pages/b.md" {
		t.Errorf("finding file = %q, want docs/pages/b.md (unlisted in the body; the frontmatter mention covers nothing)", tree.Findings[0].File)
	}
}

// TestParseTree_NodeLabelComesFromTheBody pins the label-side consequence of
// the frontmatter/body split: a YAML block scalar's example heading never
// becomes a row label.
func TestParseTree_NodeLabelComesFromTheBody(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/a.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: doc
    match:
      root: docs
      glob: "*.md"
    extract: [title, frontmatter]
`)
	dir := writeTree(t, map[string]string{
		"docs/a.md": "---\nnotes: |\n  # Example Only\n---\n\n# Actual Title\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	node, ok := tree.NodeByID("docs/a.md")
	if !ok {
		t.Fatal("node missing")
	}
	if node.Label != "Actual Title" {
		t.Errorf("node label = %q, want the body title (the frontmatter example is not prose)", node.Label)
	}
}

// reviewedCheckBlock is the base document's check block, which the shape
// subtests below replace with the shape under test.
const reviewedCheckBlock = `  - kind: frontmatter_keys
    finding: KEYS_MISSING
    concept: page
    keys: [owner]`

// TestValidate_RejectsReviewedShapes pins the loader diagnostics the review
// called for: overlapping roots, home paths, malformed globs and checks over
// directory concepts.
func TestValidate_RejectsReviewedShapes(t *testing.T) {
	base := func(mutate func(string) string) string {
		return mutate(`schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
  - id: adr
    path: docs/adr
    label: ADRs
concepts:
  - kind: page
    match:
      root: docs
      glob: "**/*.md"
    extract: [title]
  - kind: bundle
    match:
      root: docs
      glob: "bundles/*"
      dir: true
checks:
  - kind: frontmatter_keys
    finding: KEYS_MISSING
    concept: page
    keys: [owner]
`)
	}
	t.Run("overlapping roots", func(t *testing.T) {
		_, err := Parse("probe.yaml", []byte(base(func(s string) string { return s })))
		if err == nil || !strings.Contains(err.Error(), "overlaps root") {
			t.Fatalf("Parse() error = %v, want the overlap named", err)
		}
	})
	t.Run("dot root owns every subtree", func(t *testing.T) {
		doc := base(func(s string) string {
			s = strings.Replace(s, "path: docs/adr", "path: specs", 1)
			return strings.Replace(s, "    path: docs\n", "    path: .\n", 1)
		})
		_, err := Parse("probe.yaml", []byte(doc))
		if err == nil || !strings.Contains(err.Error(), "overlaps root") {
			t.Fatalf("Parse() error = %v, want the workspace-root root named as overlapping", err)
		}
	})
	t.Run("home path in root", func(t *testing.T) {
		_, err := Parse("probe.yaml", []byte(base(func(s string) string {
			return strings.Replace(s, "path: docs/adr", "path: \"~/docs\"", 1)
		})))
		if err == nil || !strings.Contains(err.Error(), "clean workspace-relative") {
			t.Fatalf("Parse() error = %v, want ~ rejected", err)
		}
	})
	t.Run("malformed glob", func(t *testing.T) {
		_, err := Parse("probe.yaml", []byte(base(func(s string) string {
			return strings.Replace(s, `"**/*.md"`, "'['", 1)
		})))
		if err == nil || !strings.Contains(err.Error(), "not a valid glob") {
			t.Fatalf("Parse() error = %v, want the glob syntax named", err)
		}
	})
	t.Run("optional without equals", func(t *testing.T) {
		// `optional` only relaxes the filename-number comparison, so without
		// `equals` the check can never report anything — a valid-looking
		// definition that silently checks nothing.
		doc := base(func(s string) string {
			return strings.Replace(s, reviewedCheckBlock, `  - kind: content_regex
    finding: TAG_MISSING
    concept: page
    regex: "MUST"
    optional: true`, 1)
		})
		_, err := Parse("probe.yaml", []byte(doc))
		if err == nil || !strings.Contains(err.Error(), "optional") {
			t.Fatalf("Parse() error = %v, want optional-without-equals rejected", err)
		}
	})
	t.Run("optional with equals loads", func(t *testing.T) {
		doc := base(func(s string) string {
			s = strings.Replace(s, "path: docs/adr", "path: registry", 1)
			return strings.Replace(s, reviewedCheckBlock, `  - kind: content_regex
    finding: TAG_MISSING
    concept: page
    regex: "ADR-(?P<num>\\d+)"
    capture: num
    equals: filename_number
    optional: true`, 1)
		})
		if _, err := Parse("probe.yaml", []byte(doc)); err != nil {
			t.Fatalf("Parse() error = %v, want the relaxed comparison accepted", err)
		}
	})
	t.Run("check over a directory concept", func(t *testing.T) {
		_, err := Parse("probe.yaml", []byte(base(func(s string) string {
			return strings.Replace(s, "concept: page", "concept: bundle", 1)
		})))
		if err == nil || !strings.Contains(err.Error(), "file concept") {
			t.Fatalf("Parse() error = %v, want the concept shape named", err)
		}
	})
	t.Run("second non-tree section", func(t *testing.T) {
		_, err := Parse("probe.yaml", []byte(base(func(s string) string {
			return s + "\nviews:\n  sections:\n    - id: findings\n      label: F\n      kind: findings\n    - id: checks\n      label: C\n      kind: checks\n"
		})))
		if err == nil || !strings.Contains(err.Error(), "at most one non-tree section") {
			t.Fatalf("Parse() error = %v, want the duplicate section rejected", err)
		}
	})
	t.Run("cycle_finding without acyclic", func(t *testing.T) {
		doc := strings.Replace(base(func(s string) string { return s }),
			"  - kind: frontmatter_keys",
			"  - kind: references\n    finding: T_MISSING\n    cycle_finding: T_CYCLE\n    concept: page\n    field: title\n    of: page\n  - kind: frontmatter_keys", 1)
		_, err := Parse("probe.yaml", []byte(doc))
		if err == nil || !strings.Contains(err.Error(), "requires acyclic") {
			t.Fatalf("Parse() error = %v, want cycle_finding tied to acyclic", err)
		}
	})
	t.Run("unique over a list directive", func(t *testing.T) {
		doc := strings.Replace(base(func(s string) string { return s }),
			"  - kind: frontmatter_keys\n    finding: KEYS_MISSING\n    concept: page\n    keys: [owner]",
			"  - kind: unique\n    finding: DUP\n    concept: page\n    field: headings", 1)
		_, err := Parse("probe.yaml", []byte(doc))
		if err == nil || !strings.Contains(err.Error(), "must be one of") {
			t.Fatalf("Parse() error = %v, want the scalar directive set named", err)
		}
	})
	t.Run("references over a list directive", func(t *testing.T) {
		doc := strings.Replace(base(func(s string) string { return s }),
			"  - kind: frontmatter_keys\n    finding: KEYS_MISSING\n    concept: page\n    keys: [owner]",
			"  - kind: references\n    finding: T_MISSING\n    concept: page\n    field: key_files\n    of: page", 1)
		_, err := Parse("probe.yaml", []byte(doc))
		if err == nil || !strings.Contains(err.Error(), "must be one of") {
			t.Fatalf("Parse() error = %v, want the scalar directive set named", err)
		}
	})
	t.Run("required_files over a scalar from", func(t *testing.T) {
		doc := strings.Replace(base(func(s string) string { return s }),
			"  - kind: frontmatter_keys\n    finding: KEYS_MISSING\n    concept: page\n    keys: [owner]",
			"  - kind: required_files\n    finding: F_MISSING\n    from: title", 1)
		_, err := Parse("probe.yaml", []byte(doc))
		if err == nil || !strings.Contains(err.Error(), "must be one of") {
			t.Fatalf("Parse() error = %v, want the list directive set named", err)
		}
	})
	t.Run("required_files from over a directory concept", func(t *testing.T) {
		doc := strings.Replace(base(func(s string) string { return s }),
			"  - kind: frontmatter_keys\n    finding: KEYS_MISSING\n    concept: page\n    keys: [owner]",
			"  - kind: required_files\n    finding: F_MISSING\n    concept: bundle\n    from: key_files", 1)
		_, err := Parse("probe.yaml", []byte(doc))
		if err == nil || !strings.Contains(err.Error(), "file concept") {
			t.Fatalf("Parse() error = %v, want the directory concept named (the extraction would see zero instances)", err)
		}
	})
	t.Run("lone dot root stays valid", func(t *testing.T) {
		doc := `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [readme.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: all
    path: .
    label: Everything
concepts:
  - kind: page
    match:
      root: all
      glob: "*.md"
    extract: [title]
`
		if _, err := Parse("probe.yaml", []byte(doc)); err != nil {
			t.Fatalf("Parse() error = %v, want a lone workspace-root root to load", err)
		}
	})
	t.Run("scalar and list directives load", func(t *testing.T) {
		doc := strings.Replace(base(func(s string) string { return s }),
			"  - id: adr\n    path: docs/adr\n    label: ADRs\n", "", 1)
		doc = strings.Replace(doc,
			"  - kind: frontmatter_keys\n    finding: KEYS_MISSING\n    concept: page\n    keys: [owner]",
			"  - kind: unique\n    finding: DUP\n    concept: page\n    field: title\n  - kind: required_files\n    finding: F_MISSING\n    from: key_files", 1)
		if _, err := Parse("probe.yaml", []byte(doc)); err != nil {
			t.Fatalf("Parse() error = %v, want the supported combinations to load", err)
		}
	})
}

// TestValidate_DeterministicDiagnostics pins that one defect always yields
// one error string (the diagnostics order is declaration order, not map
// iteration order).
func TestValidate_DeterministicDiagnostics(t *testing.T) {
	doc := `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  skills:
    dirs: [.agents/skills]
    all_of: ["BAD SKILL", "BAD SKILL2"]
    any_of: ["ALSO BAD", "ALSO BAD2"]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: page
    match:
      root: docs
      glob: "*.md"
`
	first := ""
	for i := 0; i < 40; i++ {
		_, err := Parse("probe.yaml", []byte(doc))
		if err == nil {
			t.Fatal("Parse() error = nil, want the all_of diagnostic first")
		}
		if first == "" {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("Parse() error flipped at run %d:\n%s\n%s", i, first, err.Error())
		}
	}
	if !strings.Contains(first, "detect.skills.all_of[0]") {
		t.Errorf("first diagnostic = %q, want the all_of group first (declaration order)", first)
	}
}

// TestParseTree_ImplicitView pins the no-views fallback: the framework's
// documents are rendered and labeled by their own identity.
func TestParseTree_ImplicitView(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/001-one.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: record
    match:
      root: docs
      glob: "*.md"
    extract: [title]
`)
	dir := writeTree(t, map[string]string{
		"docs/001-one.md": "# ADR-001: One\n",
		"docs/002-two.md": "# ADR-002: Two\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Views) != 1 || tree.Views[0].Kind != SectionTree {
		t.Fatalf("views = %+v, want one implicit tree section", tree.Views)
	}
	if len(tree.Nodes) == 0 {
		t.Fatal("nodes = 0, want the implicit view to render the documents")
	}
	labels := make(map[string]string, len(tree.Nodes))
	for _, node := range tree.Nodes {
		labels[node.Path] = node.Label
	}
	if labels["docs/001-one.md"] != "One" || labels["docs/002-two.md"] != "Two" {
		t.Errorf("node labels = %v, want per-document display titles", labels)
	}
}

// TestParseTree_TemplatesStayTemplates pins the exclude semantics: the
// `_`-prefixed templates of every section stay out of the actual-documents
// concepts (views, INDEX coverage, content checks) while a nested template is
// still recognized under its template concept.
func TestParseTree_TemplatesStayTemplates(t *testing.T) {
	fw, err := Builtin("vibespec")
	if err != nil {
		t.Fatal(err)
	}
	dir := writeTree(t, map[string]string{
		"specs/META.md":                       "# Meta\n",
		"specs/INDEX.md":                      "# Index\n\n[ADR](decisions/001-one.md)\n[domain](domains/app.md)\n[arch](architecture/layers.md)\n[contract](contracts/api.md)\n",
		"specs/architecture/_template.md":     "# Template\n\n[example](./gone.md)\n",
		"specs/architecture/nested/layers.md": "# Layers\n",
		"specs/domains/app.md":                "# App\n",
		"specs/contracts/_template.md":        "# Template\n",
		"specs/contracts/api.md":              "# API\n",
		"specs/decisions/_template.md":        "# Template\n",
		"specs/decisions/nested/_x.md":        "# X\n",
		"specs/decisions/001-one.md":          "# ADR-001: One\n\n## Status\n\nAccepted\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range tree.Findings {
		if strings.Contains(f.Detail, "_template") || strings.Contains(f.Detail, "_x.md") {
			t.Errorf("a template entered the checks: %s %s", f.Kind, f.Detail)
		}
	}
	if doc, ok := tree.DocumentByPath("specs/architecture/_template.md"); ok && doc.Concept != "template" {
		t.Errorf("architecture template concept = %q, want it outside the actual-docs concepts", doc.Concept)
	}
	if _, ok := tree.DocumentByPath("specs/decisions/nested/_x.md"); !ok {
		t.Errorf("nested decision template missing from the model: %v", tree.Documents)
	}
	if _, ok := tree.DocumentByPath("specs/architecture/nested/layers.md"); !ok {
		t.Errorf("nested architecture document missing: %v", tree.Documents)
	}
	for _, f := range tree.Findings {
		if f.Kind == "CROSS_REF_BROKEN" && f.File == "specs/architecture/_template.md" {
			t.Errorf("template's example link was judged: %s", f.Detail)
		}
	}
}

// TestParseTree_ParseBudget pins the whole-tree budget: past the aggregate
// limit the walk stops reading (documents are excluded WHOLE — never parsed
// half-content), the skip is named in Warnings once, and the already-read
// documents still parse and check normally.
func TestParseTree_ParseBudget(t *testing.T) {
	previousBytes, previousDocs := maxParseTotalBytes, maxParseTotalDocuments
	maxParseTotalBytes, maxParseTotalDocuments = 1<<20, 2
	t.Cleanup(func() { maxParseTotalBytes, maxParseTotalDocuments = previousBytes, previousDocs })

	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/0001-a.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: adr
    match:
      root: docs
      glob: "*.md"
    extract: [title, cross_refs]
checks:
  - kind: crossrefs
    finding: REF_BROKEN
    concept: adr
`)
	body := "# A\n\n" + strings.Repeat("filler ", 1<<14) + "\n[live](./0001-a.md)\n"
	dir := writeTree(t, map[string]string{
		"docs/0001-a.md": body,
		"docs/0002-b.md": body,
		"docs/0003-c.md": body,
		"docs/0004-d.md": body,
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Documents) != 2 {
		t.Errorf("documents = %d, want the budgeted 2", len(tree.Documents))
	}
	warned := false
	for _, w := range tree.Warnings {
		if strings.Contains(w, "parse budget exhausted") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("Warnings = %v, want the budget-exhaustion notice", tree.Warnings)
	}
	exhaustions := 0
	for _, w := range tree.Warnings {
		if strings.Contains(w, "parse budget exhausted") {
			exhaustions++
		}
	}
	if exhaustions != 1 {
		t.Errorf("budget warning repeated %d times, want exactly one", exhaustions)
	}
}

// TestParseTree_AnchorTargetsRespectParseBudget pins the cross-reference half
// of the parse budget: anchor judging reads link targets that never passed
// through the walk, so without budget accounting one document full of
// anchored links to distinct large files re-read half the checkout on every
// refresh, outside the cap. Past the budget the target's namespace is simply
// unknown — checkAnchor under-approximates (no finding), never a false one:
// big-a.md carries the "## Want" heading, so its missing #nope anchor WOULD
// be reported if the read happened.
func TestParseTree_AnchorTargetsRespectParseBudget(t *testing.T) {
	previousBytes, previousDocs := maxParseTotalBytes, maxParseTotalDocuments
	maxParseTotalBytes, maxParseTotalDocuments = 1<<20, 3
	t.Cleanup(func() { maxParseTotalBytes, maxParseTotalDocuments = previousBytes, previousDocs })

	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/subject.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: adr
    match:
      root: docs
      glob: "*.md"
    extract: [title, cross_refs]
checks:
  - kind: crossrefs
    finding: REF_BROKEN
    concept: adr
`)
	// Three matched documents = the document budget exactly: the walk spends
	// it (all three stay instances), and the anchor pass must not read past
	// it — both targets carry ~450 KiB, so the byte budget alone would not
	// fire first.
	filler := strings.Repeat("filler ", 1<<16)
	dir := writeTree(t, map[string]string{
		"docs/big-a.md":   "# A\n\n" + filler + "\n## Want\n",
		"docs/big-b.md":   "# B\n\n" + filler,
		"docs/subject.md": "# Subject\n\n[bad](./big-a.md#nope) and [silent](./big-b.md#whatever)\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Documents) != 3 {
		t.Fatalf("documents = %d, want all three (the walk itself fits the budget)", len(tree.Documents))
	}
	if len(tree.Findings) != 0 {
		t.Errorf("findings = %v, want none (anchor targets past the budget under-approximate)", tree.Findings)
	}
	var budgetWarning string
	exhaustions := 0
	for _, w := range tree.Warnings {
		if strings.Contains(w, "parse budget exhausted") {
			budgetWarning = w
			exhaustions++
		}
	}
	// The budget first crossed HERE — in the anchor pass, after the walk had
	// already read every matched document. The warning must therefore be the
	// site-neutral policy phrase, not the walk-eventive claim that matched
	// documents were skipped (they were not).
	if budgetWarning == "" {
		t.Fatalf("Warnings = %v, want the budget-exhaustion notice", tree.Warnings)
	}
	if !strings.Contains(budgetWarning, "excluded whole") || !strings.Contains(budgetWarning, "cross-reference targets beyond it resolve unjudged") {
		t.Errorf("budget warning = %q, want the site-neutral clauses for both excluded documents and unjudged anchors", budgetWarning)
	}
	if exhaustions != 1 {
		t.Errorf("budget warning count = %d, want exactly one (the anchor pass crossed it)", exhaustions)
	}
}

// TestParseTree_ATXClosingSequence pins the CommonMark closing-sequence rule
// in atxHeading: a whitespace-preceded trailing # run is not heading text
// ("## Heading ##" renders "Heading"), while a # glued to the last word
// ("foo#") or inside the text ("foo # bar") is content. The healed text is
// what anchors resolve against and what Title/DisplayTitle carry.
func TestParseTree_ATXClosingSequence(t *testing.T) {
	fw := mustParse(t, crossrefProbeFW)
	t.Run("closing sequence does not break the anchor it hides", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/plan.md":   "# Plan\n\n[ref](target.md#heading)\n",
			"docs/target.md": "# Target\n\n## Heading ##\n",
		})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 0 {
			t.Errorf("findings = %v, want none (## Heading ## anchors as heading)", findings)
		}
	})
	t.Run("a # glued to the last word is content slugging to foo", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/plan.md": "# foo#\n\n[self](plan.md#foo)\n",
		})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 0 {
			t.Errorf("findings = %v, want none (foo# stays content and slugs to foo)", findings)
		}
		doc, ok := tree.DocumentByPath("docs/plan.md")
		if !ok {
			t.Fatal("document missing")
		}
		if doc.Title != "foo#" {
			t.Errorf("Title = %q, want the unstripped foo#", doc.Title)
		}
	})
	t.Run("an interior # stays in the text and its slug", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/plan.md": "# foo # bar\n\n[self](plan.md#foo--bar)\n",
		})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		if findings := findingDetails(t, tree); len(findings) != 0 {
			t.Errorf("findings = %v, want none (foo # bar keeps its interior # and slugs to foo--bar)", findings)
		}
		doc, ok := tree.DocumentByPath("docs/plan.md")
		if !ok {
			t.Fatal("document missing")
		}
		if doc.Title != "foo # bar" {
			t.Errorf("Title = %q, want the unchanged foo # bar", doc.Title)
		}
	})
	t.Run("Title and DisplayTitle read the healed heading text", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/plan.md": "## Heading ##\n\nbody\n",
		})
		tree, err := ParseTree(dir, fw)
		if err != nil {
			t.Fatal(err)
		}
		doc, ok := tree.DocumentByPath("docs/plan.md")
		if !ok {
			t.Fatal("document missing")
		}
		if doc.Title != "Heading" || doc.DisplayTitle != "Heading" {
			t.Errorf("Title = %q, DisplayTitle = %q, want Heading for both", doc.Title, doc.DisplayTitle)
		}
	})
}

// TestParseTree_CrossReferencesSkipNonTreeTargets pins №104: a site-root-relative
// destination ("/docs/other.md") resolves from the site root, and a CommonMark
// angle-bracket destination ("<./other.md>") carries delimiters that are not
// part of the path. Neither may be appended to the document's directory and
// reported broken, while a genuinely missing relative target keeps being judged.
func TestParseTree_CrossReferencesSkipNonTreeTargets(t *testing.T) {
	t.Run("legitimate destinations are not reported", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/plan.md":  "# Plan\n\n[site](/docs/other.md)\n[angle](<./other.md>)\n[plain](./other.md)\n",
			"docs/other.md": "# Other\n",
		})
		tree, err := ParseTree(dir, mustParse(t, crossrefProbeFW))
		if err != nil {
			t.Fatal(err)
		}
		if got := findingDetails(t, tree); len(got) != 0 {
			t.Fatalf("findings = %v, want none for existing site-root/angle/plain targets", got)
		}
	})
	t.Run("a missing angle-bracket target stays judged", func(t *testing.T) {
		dir := writeTree(t, map[string]string{
			"docs/plan.md": "# Plan\n\n[angle](<./missing.md>)\n",
		})
		tree, err := ParseTree(dir, mustParse(t, crossrefProbeFW))
		if err != nil {
			t.Fatal(err)
		}
		got := findingDetails(t, tree)
		if len(got) != 1 || !strings.HasPrefix(got[0], "REF_BROKEN link does not resolve:") || !strings.Contains(got[0], "missing.md") {
			t.Fatalf("findings = %v, want exactly one broken-link finding for the missing target", got)
		}
	})
}

// TestValidate_ActionRequirements pins №105 and №106: `absent:<kind>` needs the
// action's holder `concept` (the executor fails closed without one, so a
// concept-less gate could only ever be inert), and the unknown-token diagnostic
// teaches the full five-token vocabulary.
func TestValidate_ActionRequirements(t *testing.T) {
	doc := func(requires, concept string) string {
		conceptLine := ""
		if concept != "" {
			conceptLine = "\n    concept: " + concept
		}
		return `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: page
    match:
      root: docs
      glob: "**/*.md"
  - kind: bundle
    match:
      root: docs
      glob: "bundles/*"
      dir: true
actions:
  - kind: add
    label: Add
    requires: [` + requires + `]` + conceptLine + `
`
	}
	t.Run("absent without concept is rejected", func(t *testing.T) {
		_, err := Parse("probe.yaml", []byte(doc("absent:page", "")))
		if err == nil || !strings.Contains(err.Error(), "needs `concept`") {
			t.Fatalf("Parse() error = %v, want the absent: holder rule named", err)
		}
	})
	t.Run("absent with concept loads", func(t *testing.T) {
		if _, err := Parse("probe.yaml", []byte(doc("absent:page", "bundle"))); err != nil {
			t.Fatalf("Parse() error = %v, want the declared holder accepted", err)
		}
	})
	t.Run("unknown token lists the whole vocabulary", func(t *testing.T) {
		_, err := Parse("probe.yaml", []byte(doc("present:page", "")))
		if err == nil {
			t.Fatal("Parse() error = nil, want the unknown token rejected")
		}
		for _, token := range []string{RequireRoot, RequireFindings, RequireConceptPrefix, RequireCompanionPrefix, RequireAbsentPrefix} {
			if !strings.Contains(err.Error(), token) {
				t.Fatalf("Parse() error = %v, want the vocabulary to name %q", err, token)
			}
		}
	})
}

// TestValidate_RejectsPerDocumentDirectivesOnDirectoryConcept pins №108: a
// match.dir concept never becomes a document, so extraction and per-instance
// requirements declared on it could only ever be inert; companion files — the
// directory-only directive — keep loading.
func TestValidate_RejectsPerDocumentDirectivesOnDirectoryConcept(t *testing.T) {
	doc := func(extra string) string {
		return `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/index.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: bundle
    match:
      root: docs
      glob: "bundles/*"
      dir: true` + extra + `
`
	}
	t.Run("extract", func(t *testing.T) {
		_, err := Parse("probe.yaml", []byte(doc("\n    extract: [title]")))
		if err == nil || !strings.Contains(err.Error(), "match.dir concept has none") {
			t.Fatalf("Parse() error = %v, want extract rejected on a directory concept", err)
		}
	})
	t.Run("required", func(t *testing.T) {
		_, err := Parse("probe.yaml", []byte(doc("\n    required: [title]")))
		if err == nil || !strings.Contains(err.Error(), "match.dir concept has none") {
			t.Fatalf("Parse() error = %v, want required rejected on a directory concept", err)
		}
	})
	t.Run("companion files still load", func(t *testing.T) {
		if _, err := Parse("probe.yaml", []byte(doc("\n    files: [index.md]"))); err != nil {
			t.Fatalf("Parse() error = %v, want companion files accepted", err)
		}
	})
}

// Review 109: a leading dot is a path segment, not punctuation. Both token
// vocabularies go through one helper, so a `./`-mention still satisfies index
// completeness while `.archive/b.md` is not rewritten into `archive/b.md`.
func TestTrimTokenMarkup_KeepsLeadingDot(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"./pages/a.md", "./pages/a.md"},
		{".archive/a.md", ".archive/a.md"},
		{"pages/a.md,", "pages/a.md"},
		{"pages/a.md.", "pages/a.md"},
		{"(pages/a.md)", "pages/a.md"},
		{`"pages/a.md"`, "pages/a.md"},
		{"`pages/a.md`", "pages/a.md"},
	} {
		if got := trimTokenMarkup(tc.in); got != tc.want {
			t.Errorf("trimTokenMarkup(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseTree_IndexMentionsKeepLeadingDot(t *testing.T) {
	const doc = `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/INDEX.md]
  skills:
    dirs: [.agents/skills]
    any_of: [probe-init]
roots:
  - id: docs
    path: docs
concepts:
  - kind: index
    label: Index
    match:
      root: docs
      glob: INDEX.md
    extract: [title, cross_refs]
  - kind: page-a
    label: Page A
    match:
      root: docs
      glob: pages/*.md
    extract: [title]
  - kind: page-b
    label: Page B
    match:
      root: docs
      glob: archive/*.md
    extract: [title]
checks:
  - kind: index_completeness
    finding: INDEX_STALE
    concept: index
    covers: [page-a, page-b]
`
	fw, err := Parse("probe.yaml", []byte(doc))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	listed := writeTree(t, map[string]string{
		"docs/INDEX.md":   "# Index\n\n- [A](./pages/a.md)\n",
		"docs/pages/a.md": "# A\n",
	})
	tree, err := ParseTree(listed, fw)
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}
	if len(tree.Findings) != 0 {
		t.Errorf("findings = %v, want none: a ./ mention does list the document", tree.Findings)
	}

	dotDir := writeTree(t, map[string]string{
		"docs/INDEX.md":     "# Index\n\n- [B](.archive/b.md)\n",
		"docs/archive/b.md": "# B\n",
	})
	tree, err = ParseTree(dotDir, fw)
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}
	found := false
	for _, f := range tree.Findings {
		if f.Kind == "INDEX_STALE" && strings.Contains(f.File, "docs/archive/b.md") {
			found = true
		}
	}
	if !found {
		t.Errorf("findings = %v, want INDEX_STALE for docs/archive/b.md: .archive/b.md names a different path", tree.Findings)
	}
}

// TestParseTree_WalkSkipsVCSAndDependencyDirs pins the walk-side pruning: a
// lone `path: "."` root is legitimate (the whole workspace as one subtree), so
// a broad concept glob would otherwise classify a VCS object store and npm
// READMEs as specification documents. Pruning happens before classification,
// so neither the model nor the entry budget sees those dirs.
func TestParseTree_WalkSkipsVCSAndDependencyDirs(t *testing.T) {
	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [specs/a.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: all
    path: .
    label: All
concepts:
  - kind: doc
    match:
      root: all
      glob: "**/*.md"
    extract: [title]
`)
	dir := writeTree(t, map[string]string{
		"specs/a.md":                 "# A\n",
		".git/objects/aa/bb.md":      "# Git object\n",
		"node_modules/pkg/readme.md": "# Dependency readme\n",
		"docs/nested/b.md":           "# B\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}
	paths := make([]string, 0, len(tree.Documents))
	for _, doc := range tree.Documents {
		paths = append(paths, doc.Path)
	}
	for _, path := range paths {
		if strings.HasPrefix(path, ".git/") || strings.HasPrefix(path, "node_modules/") {
			t.Errorf("walk classified %q as a specification document: %v", path, paths)
		}
	}
	for _, want := range []string{"specs/a.md", "docs/nested/b.md"} {
		if !slices.Contains(paths, want) {
			t.Errorf("documents = %v, want %q present", paths, want)
		}
	}
}

// TestParseTree_WalkEntryBudget pins the work budget: entries the walk visits
// but never reads — a non-matching sibling, a directory instance, an unreadable
// file — are charged, so the retained-content budget is not the only bound. The
// visit order is lexical, so the kept set is deterministic.
func TestParseTree_WalkEntryBudget(t *testing.T) {
	previous := maxParseEntries
	maxParseEntries = 3
	t.Cleanup(func() { maxParseEntries = previous })

	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/0001-a.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: adr
    match:
      root: docs
      glob: "*.md"
    extract: [title]
`)
	dir := writeTree(t, map[string]string{
		"docs/0001-a.md": "# A\n",
		"docs/0002-b.md": "# B\n",
		"docs/0003-c.md": "# C\n",
		"docs/0004-d.md": "# D\n",
	})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}
	if len(tree.Documents) != maxParseEntries {
		t.Errorf("documents = %d, want the walk budget's %d: %v", len(tree.Documents), maxParseEntries, tree.Documents)
	}
	if !strings.Contains(strings.Join(tree.Warnings, "\n"), "walk budget exhausted") {
		t.Errorf("warnings = %v, want the walk-budget notice", tree.Warnings)
	}
}

// TestParser_WarningsAreBounded pins the diagnostics cap: a tree with thousands
// of unreadable entries must not turn Warnings itself into the unbounded
// artefact, because that array is part of the model, crosses the RPC boundary on
// every refresh and is joined into one title attribute. The remainder collapses
// into a single counted line.
func TestParser_WarningsAreBounded(t *testing.T) {
	p := &parser{
		framework: Framework{},
		tree:      &Tree{},
		byConcept: map[string][]*instance{},
	}
	for i := 0; i < 100; i++ {
		p.warnf("file %d cannot be read, skipped", i)
	}
	p.finish()

	if want := maxParseWarnings + 1; len(p.tree.Warnings) != want {
		t.Fatalf("len(Warnings) = %d, want %d (the cap plus one summary line)", len(p.tree.Warnings), want)
	}
	last := p.tree.Warnings[len(p.tree.Warnings)-1]
	if want := "80 further diagnostics suppressed"; !strings.Contains(last, want) {
		t.Errorf("last warning = %q, want it to name %q", last, want)
	}
	if !strings.Contains(p.tree.Warnings[0], "file 0 cannot be read") {
		t.Errorf("first warning = %q, want the first diagnostic kept in order", p.tree.Warnings[0])
	}
}

// TestParser_FindingsAreBounded pins the findings half of the bounded-model
// contract: a finding is part of the model, crosses the RPC boundary on every
// refresh and is rendered as a list, so a reference-dense clone must not be
// able to grow the collection linearly with its link count. The first
// maxParseFindings findings are kept in emission order, the remainder collapses
// into one counted summary carrying the truncated class's token and severity.
func TestParser_FindingsAreBounded(t *testing.T) {
	previous := maxParseFindings
	maxParseFindings = 3
	t.Cleanup(func() { maxParseFindings = previous })

	p := &parser{framework: Framework{}, tree: &Tree{}, byConcept: map[string][]*instance{}}
	check := &Check{Finding: "REF_BROKEN", Severity: SeverityWarning, Message: "declared explanation"}
	for i := 0; i < 100; i++ {
		p.addFindingKind(check, "specs/a.md", fmt.Sprintf("link %d does not resolve", i), "REF_BROKEN")
	}
	p.finish()

	if want := maxParseFindings + 1; len(p.tree.Findings) != want {
		t.Fatalf("len(Findings) = %d, want %d (the cap plus one summary)", len(p.tree.Findings), want)
	}
	if got := p.tree.Findings[0].Detail; got != "link 0 does not resolve" {
		t.Errorf("first finding = %q, want the first emission kept in order", got)
	}
	summary := p.tree.Findings[len(p.tree.Findings)-1]
	if want := "97 further findings suppressed"; !strings.Contains(summary.Detail, want) {
		t.Errorf("summary detail = %q, want it to name %q", summary.Detail, want)
	}
	if summary.Kind != "REF_BROKEN" || summary.Severity != SeverityWarning {
		t.Errorf("summary = %+v, want the truncated class's kind and severity", summary)
	}
	if want := maxParseFindings + 1; p.tree.Counts.Findings != want {
		t.Errorf("counts.findings = %d, want the bounded count %d", p.tree.Counts.Findings, want)
	}
}

// TestParseTree_CrossRefResolutionBudget pins the resolution half: a stat per
// reference is work spent BEFORE a finding is recorded, so the findings cap
// alone leaves a dense document free to stat the whole checkout on every
// refresh. Past maxParseCrossRefs the remaining links are simply not judged,
// and the skip is named exactly once.
func TestParseTree_CrossRefResolutionBudget(t *testing.T) {
	previous := maxParseCrossRefs
	maxParseCrossRefs = 3
	t.Cleanup(func() { maxParseCrossRefs = previous })

	fw := mustParse(t, `schema: sdd-framework/v1
id: probe
label: Probe
detect:
  markers: [docs/subject.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
    label: Docs
concepts:
  - kind: adr
    match:
      root: docs
      glob: subject.md
    extract: [title, cross_refs]
checks:
  - kind: crossrefs
    finding: REF_BROKEN
    concept: adr
`)
	var body strings.Builder
	body.WriteString("# Subject\n\n")
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&body, "[link %d](./missing-%d.md)\n", i, i)
	}
	dir := writeTree(t, map[string]string{"docs/subject.md": body.String()})
	tree, err := ParseTree(dir, fw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Findings) > maxParseCrossRefs {
		t.Errorf("findings = %d, want at most the %d-reference budget", len(tree.Findings), maxParseCrossRefs)
	}
	if len(tree.Findings) == 0 {
		t.Fatal("no findings: the budget must not silently disable the check")
	}
	notices := 0
	for _, w := range tree.Warnings {
		if strings.Contains(w, "cross-reference budget exhausted") {
			notices++
		}
	}
	if notices != 1 {
		t.Errorf("budget notice appeared %d times, want exactly one (Warnings = %v)", notices, tree.Warnings)
	}
}
