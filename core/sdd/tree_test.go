package sdd_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/sdd"
	"github.com/v0lka/c0wrk/core/specs"
)

// The fixtures are three synthetic WORKSPACES, each carrying one framework's
// tree: testdata/sdd (a specs/ tree), testdata/openspec (an openspec/ tree) and
// testdata/spec-kit (a specs/ + .specify/ pair). A fixture is a workspace, not
// a tree, because a framework's roots are workspace-relative.

func builtinFramework(t *testing.T, id string) sdd.Framework {
	t.Helper()
	fw, err := sdd.Builtin(id)
	if err != nil {
		t.Fatalf("Builtin(%q) error = %v", id, err)
	}
	return fw
}

func parseTree(t *testing.T, root, frameworkID string) *sdd.Tree {
	t.Helper()
	tree, err := sdd.ParseTree(root, builtinFramework(t, frameworkID))
	if err != nil {
		t.Fatalf("ParseTree(%q, %q) error = %v", root, frameworkID, err)
	}
	return tree
}

// treeViewDocuments returns the documents a panel would render: every document
// node reachable from a declared tree view, in path order. A concept the views
// leave out (the `template` concept, whose files the panel must not list as
// specifications) stays out of this set while remaining in Tree.Documents.
func treeViewDocuments(t *testing.T, tree *sdd.Tree) []sdd.Document {
	t.Helper()
	seen := make(map[string]bool, len(tree.Nodes))
	var docs []sdd.Document
	var walk func(id string)
	walk = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		node, ok := tree.NodeByID(id)
		if !ok {
			t.Fatalf("view references unknown node %q", id)
		}
		if node.Kind == sdd.NodeDocument {
			doc, ok := tree.DocumentByPath(node.Path)
			if !ok {
				t.Fatalf("document node %q has no document", node.Path)
			}
			docs = append(docs, doc)
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	for _, view := range tree.Views {
		for _, id := range view.Nodes {
			walk(id)
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	return docs
}

// nodeIDsOfConcept returns the IDs of the nodes built from one concept kind, in
// path order.
func nodeIDsOfConcept(tree *sdd.Tree, kind string) []string {
	var ids []string
	for _, node := range tree.Nodes {
		if node.Concept == kind {
			ids = append(ids, node.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// findingPairs projects the findings onto their (kind, file) identity, sorted —
// the part of a finding that the model's migration parity is stated in. The
// detail text is the parser's own wording, so it is compared separately.
func findingPairs(findings []sdd.Finding) [][2]string {
	pairs := make([][2]string, 0, len(findings))
	for _, finding := range findings {
		pairs = append(pairs, [2]string{finding.Kind, finding.File})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	return pairs
}

// TestParseTree_ParityWithSpecsParser is AC-1: the sdd framework document,
// parsed by the generic reader over the same fixture tree, reproduces the
// documents, the navigation table, the ADR metadata and the findings the
// hand-written core/specs parser produced — including all nine finding kinds.
//
// This is the migration's parity proof, so it imports the parser being
// replaced on purpose: when core/specs is retired, this ONE test goes with it
// and the remaining assertions (kinds, details, counts) stand on their own.
func TestParseTree_ParityWithSpecsParser(t *testing.T) {
	tree := parseTree(t, filepath.Join("testdata", "sdd"), "sdd")
	if !tree.Present {
		t.Fatal("fixture tree not present")
	}
	legacy, err := specs.ParseSpecsRoot(filepath.Join("testdata", "sdd", "specs"))
	if err != nil {
		t.Fatalf("ParseSpecsRoot() error = %v", err)
	}
	if !legacy.Present {
		t.Fatal("legacy parser sees no tree")
	}

	// documents ↔ files: path, section, title, key files, cross references.
	type fileView struct {
		Path, Section, Title string
		KeyFiles, CrossRefs  []string
	}
	gotFiles := make([]fileView, 0, len(tree.Documents))
	for _, doc := range treeViewDocuments(t, tree) {
		gotFiles = append(gotFiles, fileView{
			Path:      strings.TrimPrefix(doc.Path, "specs/"),
			Section:   doc.Section,
			Title:     doc.Title,
			KeyFiles:  doc.KeyFiles,
			CrossRefs: doc.CrossRefs,
		})
	}
	wantFiles := make([]fileView, 0, len(legacy.Files))
	for _, file := range legacy.Files {
		wantFiles = append(wantFiles, fileView{
			Path:      file.Path,
			Section:   file.Section,
			Title:     file.Title,
			KeyFiles:  file.KeyFiles,
			CrossRefs: file.CrossRefs,
		})
	}
	if !reflect.DeepEqual(gotFiles, wantFiles) {
		t.Errorf("documents = %#v\nwant %#v", gotFiles, wantFiles)
	}

	// index_tasks ↔ INDEX.md navigation table.
	type taskView struct {
		Task  string
		Specs []string
	}
	indexDocs := tree.DocumentsOfConcept("index")
	if len(indexDocs) != 1 {
		t.Fatalf("index concept documents = %d, want 1", len(indexDocs))
	}
	gotTasks := make([]taskView, 0, len(indexDocs[0].Tasks))
	for _, task := range indexDocs[0].Tasks {
		gotTasks = append(gotTasks, taskView{Task: task.Task, Specs: task.Specs})
	}
	wantTasks := make([]taskView, 0, len(legacy.Index.Tasks))
	for _, task := range legacy.Index.Tasks {
		wantTasks = append(wantTasks, taskView{Task: task.Task, Specs: task.Specs})
	}
	if !reflect.DeepEqual(gotTasks, wantTasks) {
		t.Errorf("index tasks = %#v\nwant %#v", gotTasks, wantTasks)
	}

	// adrs ↔ decision documents that carry a number.
	type adrView struct {
		Number, Slug, File, Title, Status, SupersededBy string
	}
	var gotADRs []adrView
	for _, doc := range tree.DocumentsOfConcept("decision") {
		if doc.Number == "" {
			continue // a misnamed file is not an ADR record, exactly as before
		}
		gotADRs = append(gotADRs, adrView{
			Number:       doc.Number,
			Slug:         doc.Slug,
			File:         strings.TrimPrefix(doc.Path, "specs/"),
			Title:        doc.DisplayTitle,
			Status:       doc.Status,
			SupersededBy: doc.SupersededBy,
		})
	}
	sortADRs := func(list []adrView) {
		sort.SliceStable(list, func(i, j int) bool {
			ni, iErr := strconv.Atoi(list[i].Number)
			nj, jErr := strconv.Atoi(list[j].Number)
			if iErr == nil && jErr == nil && ni != nj {
				return ni < nj
			}
			if list[i].Number != list[j].Number {
				return list[i].Number < list[j].Number
			}
			return list[i].File < list[j].File
		})
	}
	sortADRs(gotADRs)
	wantADRs := make([]adrView, 0, len(legacy.ADRs))
	for _, adr := range legacy.ADRs {
		wantADRs = append(wantADRs, adrView{
			Number:       adr.Number,
			Slug:         adr.Slug,
			File:         adr.File,
			Title:        adr.Title,
			Status:       adr.Status,
			SupersededBy: adr.SupersededBy,
		})
	}
	sortADRs(wantADRs)
	if !reflect.DeepEqual(gotADRs, wantADRs) {
		t.Errorf("adrs = %#v\nwant %#v", gotADRs, wantADRs)
	}

	// findings ↔ findings, on their (kind, file) identity.
	// The generic model names files workspace-relative ("specs/INDEX.md"), the
	// legacy parser tree-relative ("INDEX.md"), so the projection trims the
	// single root prefix this framework declares.
	gotFindings := make([][2]string, 0, len(tree.Findings))
	for _, finding := range tree.Findings {
		gotFindings = append(gotFindings, [2]string{finding.Kind, strings.TrimPrefix(finding.File, "specs/")})
	}
	wantFindings := make([][2]string, 0, len(legacy.Findings))
	for _, finding := range legacy.Findings {
		wantFindings = append(wantFindings, [2]string{string(finding.Kind), finding.File})
	}
	sortPairs := func(pairs [][2]string) {
		sort.Slice(pairs, func(i, j int) bool {
			if pairs[i][0] != pairs[j][0] {
				return pairs[i][0] < pairs[j][0]
			}
			return pairs[i][1] < pairs[j][1]
		})
	}
	sortPairs(gotFindings)
	sortPairs(wantFindings)
	if !reflect.DeepEqual(gotFindings, wantFindings) {
		t.Errorf("findings = %v\nwant %v", gotFindings, wantFindings)
	}

	// All nine finding kinds are reproduced by the declared checks.
	wantKinds := []string{
		"ADR_NAME_MALFORMED", "ADR_NUMBER_MISMATCH", "CROSS_REF_BROKEN",
		"DUPLICATE_ADR_NUMBER", "INDEX_STALE", "PATH_MISSING",
		"ROOT_FILE_MISSING", "SUPERSEDED_CYCLE", "SUPERSEDED_TARGET_MISSING",
	}
	gotKinds := make([]string, 0, len(tree.Counts.FindingsByKind))
	for kind := range tree.Counts.FindingsByKind {
		gotKinds = append(gotKinds, kind)
	}
	sort.Strings(gotKinds)
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Errorf("finding kinds = %v, want %v", gotKinds, wantKinds)
	}
	if len(tree.Findings) != len(legacy.Findings) {
		t.Errorf("finding count = %d, want %d", len(tree.Findings), len(legacy.Findings))
	}

	// The finding details stay specific to their instance, even though the
	// wording is this package's own.
	for _, want := range []struct{ kind, file, detail string }{
		{"PATH_MISSING", "specs/architecture/layers.md", "specs/gone.md"},
		{"INDEX_STALE", "specs/domains/unlisted.md", "INDEX.md"},
		{"ADR_NUMBER_MISMATCH", "specs/decisions/003-mismatch.md", "030"},
		{"SUPERSEDED_TARGET_MISSING", "specs/decisions/006-dangling.md", "099"},
		{"ROOT_FILE_MISSING", "specs/META.md", "META.md"},
	} {
		assertFindingDetail(t, tree, want.kind, want.file, want.detail)
	}
}

// assertFindingDetail requires one finding of the given identity whose detail
// carries a token.
func assertFindingDetail(t *testing.T, tree *sdd.Tree, kind, file, token string) {
	t.Helper()
	for _, finding := range tree.FindingsOf(kind) {
		if finding.File == file && strings.Contains(finding.Detail, token) {
			return
		}
	}
	t.Errorf("no %s finding on %q mentioning %q (findings: %+v)", kind, file, token, tree.FindingsOf(kind))
}

// TestParseTree_OpenSpecTree is AC-2: the openspec document over its fixture
// yields capability and change nodes and exactly the findings its declared
// checks describe.
func TestParseTree_OpenSpecTree(t *testing.T) {
	tree := parseTree(t, filepath.Join("testdata", "openspec"), "openspec")
	if !tree.Present {
		t.Fatal("openspec tree not present")
	}

	wantCapabilities := []string{"openspec/specs/auth", "openspec/specs/billing"}
	if got := nodeIDsOfConcept(tree, "capability"); !reflect.DeepEqual(got, wantCapabilities) {
		t.Errorf("capability nodes = %v, want %v", got, wantCapabilities)
	}
	wantChanges := []string{
		"openspec/changes/add-mfa",
		"openspec/changes/legacy-change",
		"openspec/changes/no-delta-heading",
	}
	if got := nodeIDsOfConcept(tree, "change"); !reflect.DeepEqual(got, wantChanges) {
		t.Errorf("change nodes = %v, want %v", got, wantChanges)
	}
	for _, id := range append(wantCapabilities, wantChanges...) {
		node, ok := tree.NodeByID(id)
		if !ok {
			t.Fatalf("node %q missing", id)
		}
		if node.Kind != sdd.NodeDirectory {
			t.Errorf("node %q kind = %q, want %q", id, node.Kind, sdd.NodeDirectory)
		}
	}
	if node, ok := tree.NodeByID("openspec/changes/archive"); !ok || node.Concept != "archive" {
		t.Errorf("archive node = %+v (present=%v), want concept archive", node, ok)
	}
	if node, ok := tree.NodeByID("openspec/changes/archive/2025-01-old"); !ok || node.Concept != "archived-change" {
		t.Errorf("archived change node = %+v (present=%v), want concept archived-change", node, ok)
	}
	// Archived changes keep their artifacts: the folders must not render
	// empty, and the artifacts stay inside every document check.
	for path, wantConcept := range map[string]string{
		"openspec/changes/archive/2025-01-old/proposal.md": "archived-proposal",
		"openspec/changes/archive/2025-01-old/tasks.md":    "archived-tasks",
	} {
		doc, ok := tree.DocumentByPath(path)
		if !ok {
			t.Errorf("archived artifact %s is not a document (the archived folder renders empty)", path)
			continue
		}
		if doc.Concept != wantConcept {
			t.Errorf("archived artifact %s concept = %q, want %q", path, doc.Concept, wantConcept)
		}
	}

	// A directory instance's missing companion file is reported on the node.
	if node, ok := tree.NodeByID("openspec/changes/legacy-change"); !ok || !reflect.DeepEqual(node.Missing, []string{"proposal.md"}) {
		t.Errorf("legacy-change node = %+v (present=%v), want missing [proposal.md]", node, ok)
	}

	// Declared extraction: the delta and spec metadata.
	delta, ok := tree.DocumentByPath("openspec/changes/add-mfa/specs/auth/spec.md")
	if !ok {
		t.Fatal("delta document missing")
	}
	if !reflect.DeepEqual(delta.Deltas, []string{"ADDED"}) {
		t.Errorf("delta Deltas = %v, want [ADDED]", delta.Deltas)
	}
	if len(delta.Requirements) != 1 {
		t.Errorf("delta Requirements = %v, want one requirement", delta.Requirements)
	}
	specDoc, ok := tree.DocumentByPath("openspec/specs/auth/spec.md")
	if !ok {
		t.Fatal("capability spec document missing")
	}
	if len(specDoc.Requirements) != 2 || len(specDoc.Scenarios) == 0 {
		t.Errorf("capability spec = requirements %v, scenarios %v", specDoc.Requirements, specDoc.Scenarios)
	}

	want := [][2]string{
		{"CHANGE_PROPOSAL_MISSING", "openspec/changes/legacy-change"},
		{"CROSS_REF_BROKEN", "openspec/changes/add-mfa/proposal.md"},
		{"DELTA_SECTION_MISSING", "openspec/changes/no-delta-heading/specs/auth/spec.md"},
		{"REQUIREMENT_MISSING", "openspec/specs/billing/spec.md"},
	}
	if got := findingPairs(tree.Findings); !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
	assertFindingsDeclared(t, tree, builtinFramework(t, "openspec"))
}

// TestParseTree_SpecKitTree is AC-3: the spec-kit document over its fixture
// yields feature nodes and the .specify constitution.
func TestParseTree_SpecKitTree(t *testing.T) {
	tree := parseTree(t, filepath.Join("testdata", "spec-kit"), "spec-kit")
	if !tree.Present {
		t.Fatal("spec-kit tree not present")
	}
	if len(tree.Roots) != 2 || !tree.Roots[0].Present || !tree.Roots[1].Present {
		t.Fatalf("roots = %+v, want both specs and .specify present", tree.Roots)
	}

	wantFeatures := []string{"specs/001-login", "specs/002-billing", "specs/Bad Name"}
	if got := nodeIDsOfConcept(tree, "feature"); !reflect.DeepEqual(got, wantFeatures) {
		t.Errorf("feature nodes = %v, want %v", got, wantFeatures)
	}
	for _, id := range wantFeatures {
		node, ok := tree.NodeByID(id)
		if !ok || node.Kind != sdd.NodeDirectory {
			t.Errorf("feature node %q = %+v (present=%v), want a directory node", id, node, ok)
		}
	}
	if node, ok := tree.NodeByID("specs/002-billing"); !ok || !reflect.DeepEqual(node.Missing, []string{"spec.md"}) {
		t.Errorf("002-billing node = %+v (present=%v), want missing [spec.md]", node, ok)
	}
	// Contract and checklist artifacts are documents too: the directories
	// must not render as empty containers.
	for path, wantConcept := range map[string]string{
		"specs/001-login/contracts/api.md": "contract-file",
		"specs/001-login/checklists/ux.md": "checklist-file",
	} {
		doc, ok := tree.DocumentByPath(path)
		if !ok {
			t.Errorf("artifact %s is not a document (its container renders empty)", path)
			continue
		}
		if doc.Concept != wantConcept {
			t.Errorf("artifact %s concept = %q, want %q", path, doc.Concept, wantConcept)
		}
	}

	constitution, ok := tree.DocumentByPath(".specify/memory/constitution.md")
	if !ok {
		t.Fatal("constitution document missing")
	}
	if constitution.Concept != "constitution" || constitution.Title != "Fixture Constitution" {
		t.Errorf("constitution = %+v", constitution)
	}
	if len(constitution.Principles) != 3 {
		t.Errorf("constitution Principles = %v, want 3", constitution.Principles)
	}
	if len(constitution.Missing) != 0 {
		t.Errorf("constitution Missing = %v, want none", constitution.Missing)
	}

	// Declared extraction of a feature specification.
	specDoc, ok := tree.DocumentByPath("specs/001-login/spec.md")
	if !ok {
		t.Fatal("feature specification missing")
	}
	// The story heading is the entry; the ordered acceptance scenario under it
	// is that entry's detail, not a second story (a list belongs to the
	// section's CONTAINER heading, and here the story heading is a leaf).
	if len(specDoc.UserStories) != 1 || !strings.HasPrefix(specDoc.UserStories[0], "User Story 1") {
		t.Errorf("UserStories = %v, want exactly the story heading", specDoc.UserStories)
	}
	if len(specDoc.Requirements) != 2 {
		t.Errorf("Requirements = %v, want 2", specDoc.Requirements)
	}
	tasks, ok := tree.DocumentByPath("specs/001-login/tasks.md")
	if !ok {
		t.Fatal("task document missing")
	}
	wantTasks := []sdd.TaskItem{{Text: "T001 Create the sign-in route", Done: true}, {Text: "T002 Wire the session store"}, {Text: "T003 Add the acceptance test"}}
	if !reflect.DeepEqual(tasks.TaskItems, wantTasks) {
		t.Errorf("TaskItems = %+v, want %+v", tasks.TaskItems, wantTasks)
	}

	// Views: project state, features, findings.
	viewIDs := make([]string, 0, len(tree.Views))
	for _, view := range tree.Views {
		viewIDs = append(viewIDs, view.ID)
	}
	if want := []string{"project", "features", "findings"}; !reflect.DeepEqual(viewIDs, want) {
		t.Errorf("view ids = %v, want %v", viewIDs, want)
	}

	want := [][2]string{
		{"CROSS_REF_BROKEN", "specs/001-login/plan.md"},
		{"FEATURE_NAME_MALFORMED", "specs/Bad Name"},
		{"FEATURE_SPEC_MISSING", "specs/002-billing"},
		{"TASK_ID_MISSING", "specs/002-billing/tasks.md"},
		{"USER_STORY_MISSING", "specs/Bad Name/spec.md"},
	}
	if got := findingPairs(tree.Findings); !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
	assertFindingsDeclared(t, tree, builtinFramework(t, "spec-kit"))
}

// assertFindingsDeclared proves every finding carries a kind the framework's
// own check list declares — the reader invents no categories.
func assertFindingsDeclared(t *testing.T, tree *sdd.Tree, fw sdd.Framework) {
	t.Helper()
	declared := make(map[string]bool, len(fw.Checks))
	for _, check := range fw.Checks {
		declared[check.Finding] = true
	}
	for _, finding := range tree.Findings {
		if !declared[finding.Kind] {
			t.Errorf("finding %q on %q is not a declared check", finding.Kind, finding.File)
		}
		if finding.Severity == "" {
			t.Errorf("finding %q carries no severity", finding.Kind)
		}
	}
}

// TestParseTree_AbsentAndPartialTrees is AC-4's tolerance half: a missing tree,
// a root that is a file, an empty present root and a partial tree all parse
// without an error.
func TestParseTree_AbsentAndPartialTrees(t *testing.T) {
	fw := builtinFramework(t, "sdd")

	t.Run("absent workspace", func(t *testing.T) {
		tree, err := sdd.ParseTree(filepath.Join(t.TempDir(), "no-such-workspace"), fw)
		if err != nil {
			t.Fatalf("ParseTree() error = %v, want the neutral state", err)
		}
		if tree.Present {
			t.Error("Present = true, want false")
		}
		if len(tree.Documents) != 0 || len(tree.Nodes) != 0 || len(tree.Findings) != 0 {
			t.Errorf("absent tree carries content: %d docs, %d nodes, %d findings",
				len(tree.Documents), len(tree.Nodes), len(tree.Findings))
		}
		if len(tree.Roots) != len(fw.Roots) || tree.Roots[0].Present {
			t.Errorf("Roots = %+v, want the declared roots all absent", tree.Roots)
		}
		// Views still render — an empty section is a legal view.
		if len(tree.Views) != len(fw.Views.Sections) {
			t.Errorf("Views = %d, want %d", len(tree.Views), len(fw.Views.Sections))
		}
	})

	t.Run("root is a file", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.WriteFile(filepath.Join(workspace, "specs"), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		tree, err := sdd.ParseTree(workspace, fw)
		if err != nil {
			t.Fatalf("ParseTree() error = %v", err)
		}
		if tree.Present {
			t.Error("Present = true, want false for a root that is not a directory")
		}
	})

	t.Run("empty present root", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.MkdirAll(filepath.Join(workspace, "specs"), 0o755); err != nil {
			t.Fatal(err)
		}
		tree, err := sdd.ParseTree(workspace, fw)
		if err != nil {
			t.Fatalf("ParseTree() error = %v", err)
		}
		if !tree.Present {
			t.Fatal("Present = false, want true for an existing root")
		}
		if len(tree.Documents) != 0 {
			t.Errorf("Documents = %d, want 0", len(tree.Documents))
		}
		// The declared root files are still asserted: an empty present tree is
		// initialized-but-empty, not absent.
		if got := len(tree.FindingsOf("ROOT_FILE_MISSING")); got != 2 {
			t.Errorf("ROOT_FILE_MISSING findings = %d, want 2 (INDEX.md and META.md)", got)
		}
	})

	t.Run("partial tree", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.MkdirAll(filepath.Join(workspace, "specs", "architecture"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "specs", "INDEX.md"), []byte("# Index\n\n## Navigation by Task\n\n| Task | Specs |\n| --- | --- |\n| Layers | [layers.md](architecture/layers.md) |\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "specs", "architecture", "layers.md"), []byte("# Layers\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		tree, err := sdd.ParseTree(workspace, fw)
		if err != nil {
			t.Fatalf("ParseTree() error = %v", err)
		}
		if !tree.Present {
			t.Fatal("Present = false, want true")
		}
		if len(tree.Documents) != 2 {
			t.Fatalf("Documents = %d, want 2", len(tree.Documents))
		}
		if len(tree.FindingsOf("ROOT_FILE_MISSING")) != 1 {
			t.Errorf("ROOT_FILE_MISSING = %v, want only META.md", tree.FindingsOf("ROOT_FILE_MISSING"))
		}
		if len(tree.FindingsOf("CROSS_REF_BROKEN")) != 0 {
			t.Errorf("CROSS_REF_BROKEN = %v, want none", tree.FindingsOf("CROSS_REF_BROKEN"))
		}
	})
}

// TestParseTree_ReadOnly is AC-4's other half: a parse never writes. The whole
// fixture workspace is fingerprinted before and after.
func TestParseTree_ReadOnly(t *testing.T) {
	for _, fixture := range []string{"sdd", "openspec", "spec-kit"} {
		t.Run(fixture, func(t *testing.T) {
			root := filepath.Join("testdata", fixture)
			before := fingerprint(t, root)
			parseTree(t, root, frameworkOf(fixture))
			after := fingerprint(t, root)
			if !reflect.DeepEqual(before, after) {
				t.Errorf("%s changed during a parse:\nbefore %v\nafter  %v", fixture, before, after)
			}
		})
	}
}

// frameworkOf maps a fixture directory to its framework ID.
func frameworkOf(fixture string) string {
	if fixture == "spec-kit" {
		return "spec-kit"
	}
	return fixture
}

// fingerprint records every entry under root with its size and content, so a
// created, removed or rewritten file shows up as a difference.
func fingerprint(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if entry.IsDir() {
			entries = append(entries, "dir "+filepath.ToSlash(rel))
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		entries = append(entries, "file "+filepath.ToSlash(rel)+" "+string(data))
		return nil
	})
	if err != nil {
		t.Fatalf("fingerprint(%q) error = %v", root, err)
	}
	sort.Strings(entries)
	return entries
}

// TestParseTree_EscapesWorkspace proves the containment gate: a document
// reachable only through a symlink that leaves the workspace is excluded (and
// reported as a warning) instead of read.
func TestParseTree_EscapesWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("# Outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "specs", "decisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "specs", "INDEX.md"), []byte("# Index\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "specs", "decisions", "001-escape.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	tree, err := sdd.ParseTree(workspace, builtinFramework(t, "sdd"))
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}
	if _, ok := tree.DocumentByPath("specs/decisions/001-escape.md"); ok {
		t.Error("a document behind an escaping symlink entered the model")
	}
	if len(tree.Warnings) == 0 {
		t.Error("Warnings is empty, want a containment warning")
	}
	if !strings.Contains(strings.Join(tree.Warnings, "\n"), "escapes the workspace") {
		t.Errorf("Warnings = %v, want a containment warning", tree.Warnings)
	}
}

// TestParseTree_OnlyDeclaredChecksAndConcepts proves the reader invents
// nothing: a framework declaring no checks reports no findings even over a tree
// full of broken links, and a file no concept matches never enters the model.
func TestParseTree_OnlyDeclaredChecksAndConcepts(t *testing.T) {
	fw, err := sdd.Parse("minimal.yaml", []byte(`schema: sdd-framework/v1
id: minimal
label: Minimal
detect:
  markers: [docs/INDEX.md]
  skills:
    dirs: [.agents/skills]
    any_of: [demo-init]
roots:
  - id: docs
    path: docs
concepts:
  - kind: page
    match:
      root: docs
      glob: '*.md'
    extract: [title]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "docs", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"INDEX.md":        "# Index\n\nSee [gone](./gone.md).\n",
		"notes.txt":       "not a document concept\n",
		"nested/deep.md":  "# Deep\n",
		"nested/other.md": "# Other\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace, "docs", name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tree, err := sdd.ParseTree(workspace, fw)
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}
	if !tree.Present {
		t.Fatal("Present = false, want true")
	}
	if got, want := len(tree.Documents), 1; got != want {
		t.Errorf("Documents = %d, want %d (only *.md at the root matches)", got, want)
	}
	if len(tree.Findings) != 0 {
		t.Errorf("Findings = %+v, want none: the framework declares no checks", tree.Findings)
	}
}

// TestParseTree_Deterministic pins the ordering guarantees a panel relies on.
func TestParseTree_Deterministic(t *testing.T) {
	for _, fixture := range []string{"sdd", "openspec", "spec-kit"} {
		first := parseTree(t, filepath.Join("testdata", fixture), frameworkOf(fixture))
		second := parseTree(t, filepath.Join("testdata", fixture), frameworkOf(fixture))
		if !reflect.DeepEqual(first, second) {
			t.Errorf("%s: two parses of the same tree differ", fixture)
		}
		for i := 1; i < len(first.Documents); i++ {
			if first.Documents[i-1].Path >= first.Documents[i].Path {
				t.Errorf("%s: documents are not path-ordered at %d", fixture, i)
				break
			}
		}
		for i := 1; i < len(first.Nodes); i++ {
			if first.Nodes[i-1].Path > first.Nodes[i].Path {
				t.Errorf("%s: nodes are not path-ordered at %d", fixture, i)
				break
			}
		}
	}
}

// TestParseTree_Counts pins the aggregate block the panel's badges read.
func TestParseTree_Counts(t *testing.T) {
	tree := parseTree(t, filepath.Join("testdata", "sdd"), "sdd")
	if tree.Counts.Documents != len(tree.Documents) {
		t.Errorf("Counts.Documents = %d, want %d", tree.Counts.Documents, len(tree.Documents))
	}
	if tree.Counts.Findings != len(tree.Findings) {
		t.Errorf("Counts.Findings = %d, want %d", tree.Counts.Findings, len(tree.Findings))
	}
	total := 0
	for _, count := range tree.Counts.ByConcept {
		total += count
	}
	// Instances = documents plus directory instances; this tree declares no
	// directory concepts.
	if total != len(tree.Documents) {
		t.Errorf("ByConcept sums to %d, want %d", total, len(tree.Documents))
	}
	if tree.Counts.FindingsByKind["CROSS_REF_BROKEN"] != 3 {
		t.Errorf("FindingsByKind = %v", tree.Counts.FindingsByKind)
	}
	for _, view := range tree.Views {
		for _, id := range view.Nodes {
			if _, ok := tree.NodeByID(id); !ok {
				t.Errorf("view %q references unknown node %q", view.ID, id)
			}
		}
	}
}
