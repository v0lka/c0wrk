package specs

import (
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// fixtureRepo is the synthetic workspace rooted at testdata/repo: its
// specs/ tree exercises every finding category, every exemption rule and
// every model field.
const fixtureRepo = "testdata/repo"

func TestParseSpecsRootMissingRoot(t *testing.T) {
	root, err := ParseSpecsRoot(filepath.Join(t.TempDir(), "no-such-specs"))
	if err != nil {
		t.Fatalf("missing specs root is an error: %v", err)
	}
	if root.Present {
		t.Error("Present = true for a missing root")
	}
	if root.Files != nil || root.Index != nil || root.ADRs != nil || root.Findings != nil {
		t.Errorf("missing root parsed to a non-empty model: %+v", root)
	}
}

func TestParseSpecsRootNotADirectory(t *testing.T) {
	root, err := ParseSpecsRoot(filepath.Join(fixtureRepo, "main.go"))
	if err != nil {
		t.Fatalf("a file in place of the specs root is an error: %v", err)
	}
	if root.Present {
		t.Error("Present = true for a non-directory root")
	}
}

func TestParseSpecsRootEmptyDir(t *testing.T) {
	root, err := ParseSpecsRoot(t.TempDir())
	if err != nil {
		t.Fatalf("empty specs root is an error: %v", err)
	}
	if !root.Present {
		t.Error("Present = false for an existing empty directory")
	}
	if root.Files != nil || root.Index != nil || root.ADRs != nil || root.Findings != nil {
		t.Errorf("empty root parsed to a non-empty model: %+v", root)
	}
}

func TestParseSpecsRootPartialTree(t *testing.T) {
	dir := t.TempDir()
	decisions := filepath.Join(dir, "decisions")
	if err := os.MkdirAll(decisions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decisions, "042-late.md"), []byte("# ADR-042: Late\n\n## Status\n\nAccepted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := ParseSpecsRoot(dir)
	if err != nil {
		t.Fatalf("partial tree is an error: %v", err)
	}
	if !root.Present || root.Index != nil {
		t.Errorf("partial tree: Present=%v Index=%v, want true/nil", root.Present, root.Index)
	}
	if len(root.ADRs) != 1 || root.ADRs[0].Number != "042" || root.ADRs[0].Title != "Late" {
		t.Errorf("partial tree ADRs = %+v, want one 042 Late", root.ADRs)
	}
	// The tree carries a file, so the missing documented root files are
	// integrity findings now (ROOT_FILE_MISSING); without INDEX.md there is
	// still no listing to be INDEX_STALE against.
	want := []Finding{
		{Kind: FindingRootFileMissing, File: "INDEX.md", Detail: "required root file is missing: INDEX.md"},
		{Kind: FindingRootFileMissing, File: "META.md", Detail: "required root file is missing: META.md"},
	}
	if !reflect.DeepEqual(root.Findings, want) {
		t.Errorf("partial tree findings = %+v\nwant %+v", root.Findings, want)
	}
}

func TestParseSpecsRootDuplicateADRUnpadded(t *testing.T) {
	// "002" and "2" are the same ADR number in unpadded spelling — the
	// most likely hand-created collision — and must group into one
	// DUPLICATE_ADR_NUMBER finding with a canonical (unpadded) number.
	dir := t.TempDir()
	decisions := filepath.Join(dir, "decisions")
	if err := os.MkdirAll(decisions, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"002-one.md", "2-two.md"} {
		if err := os.WriteFile(filepath.Join(decisions, name), []byte("# ADR: "+name+"\n\n## Status\n\nAccepted\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Both root files present, and the INDEX listing names both decisions
	// files, so the case stays focused on the duplicate kind (an INDEX
	// without a listing would INDEX_STALE them; no INDEX at all would
	// ROOT_FILE_MISSING).
	if err := os.WriteFile(filepath.Join(dir, "INDEX.md"), []byte("# Index\n\n## Directory Listing\n\n- [one](decisions/002-one.md)\n- [two](decisions/2-two.md)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "META.md"), []byte("# Meta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root, err := ParseSpecsRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Finding{
		{
			Kind:   FindingDuplicateADRNumber,
			File:   "decisions/002-one.md",
			Detail: "duplicate ADR number 2: decisions/002-one.md, decisions/2-two.md",
		},
	}
	if !reflect.DeepEqual(root.Findings, want) {
		t.Errorf("Findings = %+v\nwant %+v", root.Findings, want)
	}
}

// seedTree writes both root files plus the extra files (specs-root-relative
// path → content) under a fresh temp specs root, builds INDEX.md's
// Directory Listing from every extra file so the baseline stays clean of
// INDEX_STALE, and parses the tree. The per-kind tests seed exactly one
// anomaly each and assert the exact findings list against this clean
// baseline. Returns the parse and the root path (for on-disk mutations).
func seedTree(t *testing.T, extra map[string]string) (root *SpecsRoot, dir string) {
	t.Helper()
	dir = t.TempDir()
	listing := make([]string, 0, len(extra))
	for rel := range extra {
		listing = append(listing, "- ["+path.Base(rel)+"]("+rel+")\n")
	}
	sort.Strings(listing)
	all := make(map[string]string, len(extra)+2)
	for rel, content := range extra {
		all[rel] = content
	}
	all["INDEX.md"] = "# Index\n\n## Directory Listing\n\n" + strings.Join(listing, "")
	all["META.md"] = "# Meta\n\nFormats.\n"
	for rel, content := range all {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root, err := ParseSpecsRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root, dir
}

func TestParseSpecsRootRootFileMissing(t *testing.T) {
	// A tree that carries files but lost META.md flags exactly that root
	// file; the present INDEX.md stays clean.
	_, dir := seedTree(t, map[string]string{
		"domains/app.md": "# App\n\nBody.\n",
	})
	if err := os.Remove(filepath.Join(dir, "META.md")); err != nil {
		t.Fatal(err)
	}
	root, err := ParseSpecsRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Finding{
		{Kind: FindingRootFileMissing, File: "META.md", Detail: "required root file is missing: META.md"},
	}
	if !reflect.DeepEqual(root.Findings, want) {
		t.Errorf("Findings = %+v\nwant %+v", root.Findings, want)
	}

	// A fully EMPTY tree stays clean — that is the not-initialized-yet
	// state the panel's empty state covers.
	empty, err := ParseSpecsRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Findings) != 0 {
		t.Errorf("empty tree findings = %+v, want none", empty.Findings)
	}
}

func TestParseSpecsRootADRNameMalformed(t *testing.T) {
	root, _ := seedTree(t, map[string]string{
		"decisions/003-ok.md":    "# ADR-003: Ok\n\n## Status\n\nAccepted\n",
		"decisions/notes.md":     "# Notes\n\nNot an ADR file name.\n",
		"decisions/sub/extra.md": "# Extra\n\nBuried, still malformed.\n",
	})
	want := []Finding{
		{Kind: FindingADRNameMalformed, File: "decisions/notes.md", Detail: "ADR file name is not NNN-slug.md: notes.md"},
		{Kind: FindingADRNameMalformed, File: "decisions/sub/extra.md", Detail: "ADR file name is not NNN-slug.md: extra.md"},
	}
	if !reflect.DeepEqual(root.Findings, want) {
		t.Errorf("Findings = %+v\nwant %+v", root.Findings, want)
	}
	// The well-formed sibling stays a parsed ADR and raises no finding.
	if len(root.ADRs) != 1 || root.ADRs[0].Number != "003" {
		t.Errorf("ADRs = %+v, want exactly 003", root.ADRs)
	}
}

func TestParseSpecsRootADRNumberMismatch(t *testing.T) {
	root, _ := seedTree(t, map[string]string{
		"decisions/042-late.md": "# ADR-041: Late\n\n## Status\n\nAccepted\n",
		"decisions/043-ok.md":   "# ADR-43: Unpadded but numerically equal\n\n## Status\n\nAccepted\n",
		"decisions/044-free.md": "# Free-form heading\n\n## Status\n\nAccepted\n",
	})
	want := []Finding{
		{Kind: FindingADRNumberMismatch, File: "decisions/042-late.md", Detail: "heading number 041 does not match file name number 042"},
	}
	if !reflect.DeepEqual(root.Findings, want) {
		t.Errorf("Findings = %+v\nwant %+v", root.Findings, want)
	}
}

func TestParseSpecsRootSupersededTargetMissing(t *testing.T) {
	root, _ := seedTree(t, map[string]string{
		// Plain-form reference (no markdown link): the missing successor
		// must surface as SUPERSEDED_TARGET_MISSING alone, without the
		// dangling link also firing CROSS_REF_BROKEN.
		"decisions/010-old.md": "# ADR-010: Old\n\n## Status\n\nSuperseded by ADR-011\n",
		// An unpadded reference resolving to a padded file name is fine.
		"decisions/012-fine.md":  "# ADR-012: Fine\n\n## Status\n\nSuperseded by [84](084-later.md)\n",
		"decisions/084-later.md": "# ADR-084: Later\n\n## Status\n\nAccepted\n",
	})
	want := []Finding{
		{Kind: FindingSupersededTargetMissing, File: "decisions/010-old.md", Detail: "superseded by missing ADR 011: no decisions/ file carries that number"},
	}
	if !reflect.DeepEqual(root.Findings, want) {
		t.Errorf("Findings = %+v\nwant %+v", root.Findings, want)
	}
}

func TestParseSpecsRootSupersededCycle(t *testing.T) {
	// A two-record loop reports ONCE, on the lowest-path member, with the
	// walk order showing the closure; the record leading INTO the cycle
	// (000-entry) is not part of it and raises nothing.
	root, _ := seedTree(t, map[string]string{
		"decisions/000-entry.md": "# ADR-000: Entry\n\n## Status\n\nSuperseded by ADR-001\n",
		"decisions/001-a.md":     "# ADR-001: A\n\n## Status\n\nSuperseded by ADR-002\n",
		"decisions/002-b.md":     "# ADR-002: B\n\n## Status\n\nSuperseded by ADR-001\n",
	})
	want := []Finding{
		{
			Kind:   FindingSupersededCycle,
			File:   "decisions/001-a.md",
			Detail: "supersession cycle: decisions/001-a.md -> decisions/002-b.md -> decisions/001-a.md",
		},
	}
	if !reflect.DeepEqual(root.Findings, want) {
		t.Errorf("Findings = %+v\nwant %+v", root.Findings, want)
	}
}

func TestParseSpecsRootSupersededSelfLoop(t *testing.T) {
	// An ADR superseded by its own number is a one-record cycle.
	root, _ := seedTree(t, map[string]string{
		"decisions/005-x.md": "# ADR-005: X\n\n## Status\n\nSuperseded by ADR-005\n",
	})
	want := []Finding{
		{
			Kind:   FindingSupersededCycle,
			File:   "decisions/005-x.md",
			Detail: "supersession cycle: decisions/005-x.md -> decisions/005-x.md",
		},
	}
	if !reflect.DeepEqual(root.Findings, want) {
		t.Errorf("Findings = %+v\nwant %+v", root.Findings, want)
	}
}

func TestParseSpecsRootFixture(t *testing.T) {
	root, err := ParseSpecsRoot(filepath.Join(fixtureRepo, "specs"))
	if err != nil {
		t.Fatal(err)
	}
	if !root.Present {
		t.Fatal("fixture root not present")
	}

	wantPaths := []string{
		"INDEX.md",
		"META.md",
		"architecture/layers.md",
		"decisions/001-duplicate.md",
		"decisions/001-one.md",
		"decisions/002-two.md",
		"domains/app.md",
		"domains/unlisted.md",
	}
	gotPaths := make([]string, len(root.Files))
	for i, f := range root.Files {
		gotPaths[i] = f.Path
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Errorf("Files paths = %v, want %v", gotPaths, wantPaths)
	}

	fileByPath := make(map[string]SpecFile, len(root.Files))
	for _, f := range root.Files {
		fileByPath[f.Path] = f
	}
	if got := fileByPath["architecture/layers.md"].Section; got != SectionArchitecture {
		t.Errorf("layers.md Section = %q, want %q", got, SectionArchitecture)
	}
	if got := fileByPath["INDEX.md"].Section; got != "" {
		t.Errorf("INDEX.md Section = %q, want empty", got)
	}
	if got := fileByPath["INDEX.md"].Title; got != "Fixture Specs Index" {
		t.Errorf("INDEX.md Title = %q", got)
	}

	// Key Files: verbatim entries, fence bullets excluded, exemptions kept
	// in the model.
	wantKeyFiles := []string{
		"main.go",
		"github.com/example/sdk/layer.go",
		"~/notes.md",
		"<generated>/x.go",
	}
	if got := fileByPath["architecture/layers.md"].KeyFiles; !reflect.DeepEqual(got, wantKeyFiles) {
		t.Errorf("layers.md KeyFiles = %v, want %v", got, wantKeyFiles)
	}

	// Cross refs: relative targets only, in document order; the inline
	// format example in the INDEX intro must not appear.
	wantRefs := []string{
		"../domains/app.md",
		"../decisions/002-two.md#decision",
		"../../README.md",
	}
	if got := fileByPath["architecture/layers.md"].CrossRefs; !reflect.DeepEqual(got, wantRefs) {
		t.Errorf("layers.md CrossRefs = %v, want %v", got, wantRefs)
	}
	wantIndexRefs := []string{
		"architecture/layers.md",
		"domains/app.md",
		"domains/missing.md",
		"architecture/layers.md",
		"domains/app.md",
		"decisions/001-one.md",
		"decisions/001-duplicate.md",
		"decisions/002-two.md",
	}
	if got := fileByPath["INDEX.md"].CrossRefs; !reflect.DeepEqual(got, wantIndexRefs) {
		t.Errorf("INDEX.md CrossRefs = %v, want %v", got, wantIndexRefs)
	}

	// INDEX model: header/separator/external-only rows dropped.
	if len(root.Index.Tasks) != 2 {
		t.Fatalf("Index.Tasks = %+v, want 2 rows", root.Index.Tasks)
	}
	if root.Index.Tasks[1].Task != "Application domain, cross-refs" {
		t.Errorf("Tasks[1].Task = %q", root.Index.Tasks[1].Task)
	}
	wantSpecs := []string{"domains/app.md", "domains/missing.md"}
	if got := root.Index.Tasks[1].Specs; !reflect.DeepEqual(got, wantSpecs) {
		t.Errorf("Tasks[1].Specs = %v, want %v", got, wantSpecs)
	}
	wantListed := []string{
		"architecture/layers.md",
		"domains/app.md",
		"decisions/001-one.md",
		"decisions/001-duplicate.md",
		"decisions/002-two.md",
	}
	if got := root.Index.ListedFiles; !reflect.DeepEqual(got, wantListed) {
		t.Errorf("Index.ListedFiles = %v, want %v", got, wantListed)
	}

	// ADRs: sorted numerically, template excluded, metadata parsed.
	if len(root.ADRs) != 3 {
		t.Fatalf("ADRs = %+v, want 3", root.ADRs)
	}
	wantADRFiles := []string{
		"decisions/001-duplicate.md",
		"decisions/001-one.md",
		"decisions/002-two.md",
	}
	for i, want := range wantADRFiles {
		if root.ADRs[i].File != want {
			t.Errorf("ADRs[%d].File = %q, want %q", i, root.ADRs[i].File, want)
		}
	}
	if a := root.ADRs[0]; a.Number != "001" || a.Slug != "duplicate" || a.Title != "Duplicate Number" ||
		a.Status != StatusAccepted || a.SupersededBy != "" {
		t.Errorf("ADR 001-duplicate = %+v", a)
	}
	if a := root.ADRs[2]; a.Number != "002" || a.Slug != "two" || a.Title != "Two" ||
		a.Status != StatusSuperseded || a.SupersededBy != "001" {
		t.Errorf("ADR 002 = %+v", a)
	}

	// Findings: exactly the six seeded ones, in deterministic order.
	wantFindings := []Finding{
		{Kind: FindingCrossRefBroken, File: "INDEX.md", Detail: "link does not resolve: domains/missing.md"},
		{Kind: FindingCrossRefBroken, File: "domains/app.md", Detail: "anchor does not resolve: ../architecture/layers.md#no-such-heading"},
		{Kind: FindingCrossRefBroken, File: "domains/app.md", Detail: "link does not resolve: ../nope.md"},
		{Kind: FindingDuplicateADRNumber, File: "decisions/001-duplicate.md", Detail: "duplicate ADR number 1: decisions/001-duplicate.md, decisions/001-one.md"},
		{Kind: FindingIndexStale, File: "domains/unlisted.md", Detail: "not listed in INDEX.md"},
		{Kind: FindingPathMissing, File: "domains/app.md", Detail: "key file does not exist: specs/gone.md"},
	}
	if !reflect.DeepEqual(root.Findings, wantFindings) {
		t.Errorf("Findings = %+v\nwant %+v", root.Findings, wantFindings)
	}
}

func TestParseSpecsRootDeterministic(t *testing.T) {
	first, err := ParseSpecsRoot(filepath.Join(fixtureRepo, "specs"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := ParseSpecsRoot(filepath.Join(fixtureRepo, "specs"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Error("two parses of the same tree differ")
	}
}

func TestParseADRStatusVariants(t *testing.T) {
	tests := []struct {
		name         string
		statusLine   string
		wantStatus   string
		wantSupersed string
	}{
		{"plain accepted", "Accepted", StatusAccepted, ""},
		{"accepted with amendment prose", "Accepted — amended by [ADR-052](./052-x.md)", StatusAccepted, ""},
		{"accepted partially supersedes another", "Accepted → Partially supersedes [ADR-044 (scope note)](x.md)", StatusAccepted, ""},
		{"superseded bracketed ADR", "**Superseded by [ADR-055](./055-symlink-gate-literal-paths-only.md)** — prose", StatusSuperseded, "055"},
		{"superseded bare number", "Superseded by [084](decisions/084-x.md)", StatusSuperseded, "084"},
		{"superseded in part", "Superseded in part by [ADR-051](x.md), then re-decided by [ADR-056](y.md)", StatusSuperseded, "051"},
		{"superseded without successor", "Superseded (no successor ADR — reversed by code drift)", StatusSuperseded, ""},
		{"superseded with a year in prose", "Superseded — recorded 2026-10-22 in place", StatusSuperseded, ""},
		{"other vocabulary", "Proposed", "proposed", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := "# ADR-038: Fixture\n\n## Status\n\n" + tt.statusLine + "\n\n## Decision\n\nBody.\n"
			_, heading, status, supersededBy := ParseADR(content)
			if heading != "038" {
				t.Errorf("heading number = %q, want %q", heading, "038")
			}
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if supersededBy != tt.wantSupersed {
				t.Errorf("supersededBy = %q, want %q", supersededBy, tt.wantSupersed)
			}
		})
	}
}

func TestParseADRTitle(t *testing.T) {
	title, heading, _, _ := ParseADR("# ADR-105: Specs sidebar panel\n\n## Status\n\nAccepted\n")
	if title != "Specs sidebar panel" {
		t.Errorf("title = %q, want %q", title, "Specs sidebar panel")
	}
	if heading != "105" {
		t.Errorf("heading number = %q, want %q", heading, "105")
	}
	// A heading that does not match the template shape stays verbatim.
	title, heading, _, _ = ParseADR("# Not a template heading\n\n## Status\n\nAccepted\n")
	if title != "Not a template heading" {
		t.Errorf("title = %q, want verbatim heading", title)
	}
	if heading != "" {
		t.Errorf("heading number = %q, want empty for a non-template heading", heading)
	}
	// A template-shaped heading inside a code fence is not the title.
	title, heading, _, _ = ParseADR("```\n# ADR-NNN: Fenced\n```\n\n# Real Title\n")
	if title != "Real Title" {
		t.Errorf("title = %q, want %q", title, "Real Title")
	}
	if heading != "" {
		t.Errorf("heading number = %q, want empty for a fenced heading", heading)
	}
}

func TestSlugifyHeading(t *testing.T) {
	tests := []struct{ heading, want string }{
		{"Key Files", "key-files"},
		{"Mention gating (`manual` mode)", "mention-gating-manual-mode"},
		{"Session Roots: Workspace, Temp Directory, and Auxiliary Directories", "session-roots-workspace-temp-directory-and-auxiliary-directories"},
		{"Notes — Extra", "notes--extra"},
		{"D1 — Assignment form only", "d1--assignment-form-only"},
		{"What NOT to do", "what-not-to-do"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := SlugifyHeading(tt.heading); got != tt.want {
			t.Errorf("SlugifyHeading(%q) = %q, want %q", tt.heading, got, tt.want)
		}
	}
}

func TestParseKeyFilesRules(t *testing.T) {
	content := "# T\n\n## Key Files\n\n- `a.go` - first\n* `b.go` - star bullet\n  - `c.go` - indented\nplain prose `d.go` without a bullet\n- no backticks here\n\n## Next\n\n- `e.go` - later section\n"
	want := []string{"a.go", "b.go", "c.go"}
	if got := ParseKeyFiles(content); !reflect.DeepEqual(got, want) {
		t.Errorf("ParseKeyFiles = %v, want %v", got, want)
	}
	if got := ParseKeyFiles("# T\n\nNo section here.\n"); got != nil {
		t.Errorf("ParseKeyFiles without a section = %v, want nil", got)
	}
	// A fenced template's Key Files section must not be picked up.
	fenced := "# META\n\n```markdown\n## Key Files\n\n- `template/entry.go` - example\n```\n"
	if got := ParseKeyFiles(fenced); got != nil {
		t.Errorf("ParseKeyFiles found a fenced section: %v", got)
	}
}

func TestParseIndexTableRules(t *testing.T) {
	content := "# Index\n\n## Navigation by Task\n\n| If your task involves... | Read these specs |\n| --- | --- |\n| Only external | [upstream](https://example.com) |\n| Real row | [a.md](a.md) and [b.md](b.md#section) |\n| Empty second cell | |\n\n## After\n\n| Late | [late.md](late.md) |\n"
	idx := ParseIndex(content)
	if len(idx.Tasks) != 1 {
		t.Fatalf("Tasks = %+v, want the single real row", idx.Tasks)
	}
	wantSpecs := []string{"a.md", "b.md#section"}
	if got := idx.Tasks[0].Specs; !reflect.DeepEqual(got, wantSpecs) {
		t.Errorf("Specs = %v, want %v", got, wantSpecs)
	}
	if idx.ListedFiles != nil {
		t.Errorf("ListedFiles = %v, want nil without a Directory Listing section", idx.ListedFiles)
	}
}

// -- self-parse of c0wrk's own specs/ tree --

// TestSelfParseC0wrkSpecs parses the repository's real specs/ tree and
// enforces the acceptance criterion "no false positives": every reported
// finding is re-verified against the filesystem with independent means, and
// categories that must be clean on this tree (duplicate ADR numbers) are
// asserted empty. True drift the parser finds is allowed — it must simply
// be true.
func TestSelfParseC0wrkSpecs(t *testing.T) {
	const repoRoot = "../.."
	root, err := ParseSpecsRoot(filepath.Join(repoRoot, "specs"))
	if err != nil {
		t.Fatal(err)
	}
	if !root.Present {
		t.Fatal("repository specs/ tree not present")
	}

	// Structural expectations.
	if root.Index == nil {
		t.Fatal("INDEX.md not parsed")
	}
	if len(root.Index.Tasks) < 30 {
		t.Errorf("Index.Tasks = %d rows, want >= 30", len(root.Index.Tasks))
	}
	if len(root.Index.ListedFiles) < 100 {
		t.Errorf("Index.ListedFiles = %d entries, want >= 100", len(root.Index.ListedFiles))
	}
	sectionCounts := make(map[string]int)
	for _, f := range root.Files {
		sectionCounts[f.Section]++
	}
	for _, section := range sectionDirs {
		if sectionCounts[section] == 0 {
			t.Errorf("section %s has no parsed files", section)
		}
	}
	if len(root.ADRs) < 100 {
		t.Errorf("ADRs = %d, want >= 100", len(root.ADRs))
	}
	adrByNumber := make(map[string]ADRMeta, len(root.ADRs))
	for _, a := range root.ADRs {
		if _, dup := adrByNumber[a.Number]; dup {
			t.Errorf("ADR number %s carried by more than one file", a.Number)
		}
		adrByNumber[a.Number] = a
	}
	if a := adrByNumber["105"]; a.Title != "Specs sidebar panel" || a.Slug != "specs-sidebar-panel" || a.Status != StatusAccepted {
		t.Errorf("ADR-105 = %+v", a)
	}
	if a := adrByNumber["038"]; a.Status != StatusSuperseded || a.SupersededBy != "055" {
		t.Errorf("ADR-038 = %+v, want superseded by 055", a)
	}

	indexRaw, err := os.ReadFile(filepath.Join(repoRoot, "specs", "INDEX.md"))
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range root.Findings {
		t.Logf("self-parse finding: %s %s: %s", f.Kind, f.File, f.Detail)
		switch f.Kind {
		case FindingDuplicateADRNumber:
			t.Errorf("false positive DUPLICATE_ADR_NUMBER: numbers verified unique above: %s", f.Detail)
		case FindingIndexStale:
			// The file must appear as a link target nowhere in INDEX.md
			// (listing or task table) for the finding to be true.
			if strings.Contains(string(indexRaw), "("+f.File+")") || strings.Contains(string(indexRaw), "("+f.File+"#") {
				t.Errorf("false positive INDEX_STALE: %s IS linked in INDEX.md", f.File)
			}
		case FindingPathMissing:
			entry := strings.TrimPrefix(f.Detail, "key file does not exist: ")
			if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(entry))); err == nil {
				t.Errorf("false positive PATH_MISSING: %s exists", entry)
			}
		case FindingCrossRefBroken:
			verifySelfParseCrossRef(t, repoRoot, f)
		}
	}
}

// verifySelfParseCrossRef re-checks one CROSS_REF_BROKEN against the real
// tree: the target path must really be missing, or — for an anchor finding —
// the target's headings must really not carry the anchor under a crude
// lower-case/hyphen transform that shares no code with the slugger.
func verifySelfParseCrossRef(t *testing.T, repoRoot string, f Finding) {
	t.Helper()
	ref := f.Detail
	if after, ok := strings.CutPrefix(ref, "link does not resolve: "); ok {
		ref = after
	} else if after, ok := strings.CutPrefix(ref, "anchor does not resolve: "); ok {
		ref = after
	}
	p, anchor := splitRef(ref)
	target := f.File
	if p != "" {
		target = path.Clean(path.Join(path.Dir(f.File), p))
	}
	abs := filepath.Join(repoRoot, "specs", filepath.FromSlash(target))
	if _, err := os.Stat(abs); err != nil {
		return // the link target is genuinely missing — true positive
	}
	if anchor == "" {
		t.Errorf("false positive CROSS_REF_BROKEN: target of %q exists: %s", ref, target)
		return
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return // unreadable target: the parser treats anchors there as resolved
	}
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.ToLower(strings.TrimSpace(line))
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		crude := strings.ReplaceAll(strings.TrimLeft(trimmed, "# "), " ", "-")
		if strings.Contains(crude, anchor) {
			t.Errorf("false positive CROSS_REF_BROKEN anchor %q: heading %q carries it", ref, trimmed)
			return
		}
	}
}
