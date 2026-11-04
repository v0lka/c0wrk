package backend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/specs"
)

// specsTestFrontend wires a FrontendAPI with the given projects persisted to
// an in-memory store (the research-root test bootstrap, minus the research
// seeding — callers build whatever specs/ tree a case needs under the
// workspace). Projects without an explicit WorkspacePath get
// <base>/ws-<ID>; the map returns each project's resolved workspace path.
func specsTestFrontend(t *testing.T, infos ...project.ProjectInfo) (api *FrontendAPI, workspaces map[string]string) {
	t.Helper()
	base := t.TempDir()

	db := openResearchTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("create project store: %v", err)
	}
	workspaces = make(map[string]string, len(infos))
	for _, info := range infos {
		if info.WorkspacePath == "" {
			info.WorkspacePath = filepath.Join(base, "ws-"+info.ID)
		}
		workspaces[info.ID] = info.WorkspacePath
		if err := store.SaveProject(context.Background(), info); err != nil {
			t.Fatalf("save project %s: %v", info.ID, err)
		}
	}

	api = &FrontendAPI{
		projectManager: project.NewManager(store, base, nil),
		projStore:      store,
	}
	api.seedPublished.Store(true)
	return api, workspaces
}

// writeSpecsTree seeds a structurally complete specs/ tree under ws plus the
// workspace Key File it references. The tree deliberately carries five
// findings: decisions/001-first.md and decisions/001-dup.md share the
// leading number (DUPLICATE_ADR_NUMBER), domains/orchestration/conductor.md
// and the unlisted duplicate are missing from INDEX.md's directory listing
// (2× INDEX_STALE), the supersession link of 001-dup.md points at a file
// that does not exist (CROSS_REF_BROKEN), and its successor number has no
// carrier either (SUPERSEDED_TARGET_MISSING). Everything else is clean:
// the contracts→architecture cross-reference resolves, the Key File exists.
func writeSpecsTree(t *testing.T, ws string) {
	t.Helper()
	files := map[string]string{
		"INDEX.md": "# Spec Index\n\n" +
			"## Navigation by Task\n\n" +
			"| Task | Read these specs |\n" +
			"|---|---|\n" +
			"| Add a tool | [tools](contracts/tools.md) |\n\n" +
			"## Directory Listing\n\n" +
			"- [architecture/layers.md](architecture/layers.md)\n" +
			"- [contracts/tools.md](contracts/tools.md)\n" +
			"- [decisions/001-first.md](decisions/001-first.md)\n",
		"META.md":                            "# Meta\n\nSpec formats.\n",
		"architecture/layers.md":             "# Layers\n\n## Key Files\n\n- `core/tools/registry.go` - the registry\n",
		"contracts/tools.md":                 "# Tool contract\n\nSee [layers](../architecture/layers.md).\n",
		"decisions/001-first.md":             "# ADR-001: First decision\n\n## Status\n\nAccepted\n",
		"decisions/001-dup.md":               "# ADR-001: Duplicate number\n\n## Status\n\nSuperseded by [ADR-002](002-elsewhere.md)\n",
		"domains/orchestration/conductor.md": "# Conductor\n\nNot listed in INDEX.\n",
	}
	for rel, content := range files {
		abs := filepath.Join(ws, "specs", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	// The Key Files entry's first segment must exist as a directory for the
	// PATH_MISSING candidate filter, and the file itself must exist so the
	// seeded tree stays clean of that finding kind.
	if err := os.MkdirAll(filepath.Join(ws, "core", "tools"), 0o755); err != nil {
		t.Fatalf("mkdir key-file dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "core", "tools", "registry.go"), []byte("package tools\n"), 0o644); err != nil {
		t.Fatalf("write key file: %v", err)
	}
}

// assertSpecsWireShape pins the stable non-null wire contract: every
// collection serializes as an array (never null) under its snake_case key,
// and the counts histogram serializes as a map (never null).
func assertSpecsWireShape(t *testing.T, dto *SpecsStatusDTO) {
	t.Helper()
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal DTO: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	for _, key := range []string{"present", "root", "files", "index_tasks", "adrs", "findings", "counts"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("wire missing key %q (have %v)", key, wireKeys(wire))
		}
	}
	for _, key := range []string{"files", "index_tasks", "adrs", "findings"} {
		if string(wire[key]) == "null" {
			t.Errorf("wire key %q is null — the DTO must serialize stable non-nil collections", key)
		}
	}
	var counts map[string]json.RawMessage
	if err := json.Unmarshal(wire["counts"], &counts); err != nil {
		t.Fatalf("unmarshal counts: %v", err)
	}
	for _, key := range []string{"files", "adrs", "findings", "findings_by_kind"} {
		if _, ok := counts[key]; !ok {
			t.Errorf("counts wire missing key %q", key)
		}
	}
	if string(counts["findings_by_kind"]) == "null" {
		t.Error("counts.findings_by_kind is null — the histogram must be a non-nil map")
	}
}

// wireKeys returns the sorted top-level keys of a wire map (failure output).
func wireKeys(wire map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(wire))
	for k := range wire {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestGetSpecsStatus_RealSpecsTree pins the populated RPC contract: the full
// parsed model of a real specs/ tree (files incl. INDEX.md/META.md, the
// INDEX task table, ADR metadata with status classification) plus exactly
// the five seeded findings, consistent counts, and the stable wire shape.
func TestGetSpecsStatus_RealSpecsTree(t *testing.T) {
	api, workspaces := specsTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})
	writeSpecsTree(t, workspaces["proj-1"])

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus: %v", err)
	}

	if !status.Present {
		t.Fatal("Present = false, want true for an existing specs/ tree")
	}
	wantRoot := filepath.Join(workspaces["proj-1"], "specs")
	if status.Root != wantRoot {
		t.Errorf("Root = %q, want %q", status.Root, wantRoot)
	}

	wantPaths := []string{
		"INDEX.md",
		"META.md",
		"architecture/layers.md",
		"contracts/tools.md",
		"decisions/001-dup.md",
		"decisions/001-first.md",
		"domains/orchestration/conductor.md",
	}
	gotPaths := make([]string, 0, len(status.Files))
	for _, f := range status.Files {
		gotPaths = append(gotPaths, f.Path)
	}
	if !sort.StringsAreSorted(gotPaths) {
		t.Errorf("Files not sorted by path: %v", gotPaths)
	}
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("Files = %v, want exactly %v", gotPaths, wantPaths)
	}
	for i := range wantPaths {
		if gotPaths[i] != wantPaths[i] {
			t.Errorf("Files[%d] = %q, want %q (got %v)", i, gotPaths[i], wantPaths[i], gotPaths)
		}
	}

	// The Key Files entry parses and does NOT become a finding (the file
	// exists under the workspace).
	for _, f := range status.Files {
		if f.Path == "architecture/layers.md" {
			if len(f.KeyFiles) != 1 || f.KeyFiles[0] != "core/tools/registry.go" {
				t.Errorf("KeyFiles = %v, want [core/tools/registry.go]", f.KeyFiles)
			}
		}
	}

	if len(status.IndexTasks) != 1 {
		t.Fatalf("IndexTasks = %+v, want exactly the one navigation row", status.IndexTasks)
	}
	if status.IndexTasks[0].Task != "Add a tool" ||
		len(status.IndexTasks[0].Specs) != 1 || status.IndexTasks[0].Specs[0] != "contracts/tools.md" {
		t.Errorf("IndexTasks[0] = %+v, want task \"Add a tool\" → contracts/tools.md", status.IndexTasks[0])
	}

	if len(status.ADRs) != 2 {
		t.Fatalf("ADRs = %+v, want both decisions/ files (number-sorted, dups adjacent)", status.ADRs)
	}
	first := status.ADRs[0]
	if first.Number != "001" || first.Slug != "dup" || first.Status != specs.StatusSuperseded || first.SupersededBy != "002" {
		t.Errorf("ADRs[0] = %+v, want number 001, slug dup, superseded by 002", first)
	}
	if second := status.ADRs[1]; second.Slug != "first" || second.Status != specs.StatusAccepted {
		t.Errorf("ADRs[1] = %+v, want slug first, accepted", second)
	}

	// Exactly the five seeded findings (the unlisted duplicate is
	// INDEX_STALE too — the listing names only 001-first.md; the dangling
	// supersession is both a broken link and a missing successor number).
	if len(status.Findings) != 5 {
		t.Fatalf("Findings = %+v, want exactly the five seeded findings", status.Findings)
	}
	gotKinds := map[string]int{}
	staleFiles := map[string]bool{}
	for _, f := range status.Findings {
		gotKinds[string(f.Kind)]++
		switch {
		case f.Kind == specs.FindingCrossRefBroken && f.File != "decisions/001-dup.md":
			t.Errorf("cross-ref finding File = %q, want decisions/001-dup.md (the dangling supersession link)", f.File)
		case f.Kind == specs.FindingDuplicateADRNumber && f.File != "decisions/001-dup.md":
			t.Errorf("duplicate-number finding File = %q, want the sorted-first file decisions/001-dup.md", f.File)
		case f.Kind == specs.FindingIndexStale:
			staleFiles[f.File] = true
		}
	}
	wantStale := map[string]bool{
		"decisions/001-dup.md":               true,
		"domains/orchestration/conductor.md": true,
	}
	if !reflect.DeepEqual(staleFiles, wantStale) {
		t.Errorf("INDEX_STALE files = %v, want %v", staleFiles, wantStale)
	}
	wantKinds := map[string]int{
		string(specs.FindingCrossRefBroken):          1,
		string(specs.FindingDuplicateADRNumber):      1,
		string(specs.FindingIndexStale):              2,
		string(specs.FindingSupersededTargetMissing): 1,
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Errorf("Findings kinds = %v, want %v", gotKinds, wantKinds)
	}

	if status.Counts.Files != 7 || status.Counts.ADRs != 2 || status.Counts.Findings != 5 {
		t.Errorf("Counts = %+v, want Files=7 ADRs=2 Findings=5", status.Counts)
	}
	wantByKind := map[string]int{
		string(specs.FindingCrossRefBroken):          1,
		string(specs.FindingDuplicateADRNumber):      1,
		string(specs.FindingIndexStale):              2,
		string(specs.FindingSupersededTargetMissing): 1,
	}
	if status.Counts.FindingsByKind == nil {
		t.Fatal("Counts.FindingsByKind is nil — the histogram must be allocated even for a populated tree")
	}
	for kind, want := range wantByKind {
		if got := status.Counts.FindingsByKind[kind]; got != want {
			t.Errorf("Counts.FindingsByKind[%s] = %d, want %d", kind, got, want)
		}
	}
	if len(status.Counts.FindingsByKind) != len(wantByKind) {
		t.Errorf("Counts.FindingsByKind = %v, want exactly %v", status.Counts.FindingsByKind, wantByKind)
	}

	assertSpecsWireShape(t, status)
}

// TestGetSpecsStatus_MissingSpecsDirIsNeutral pins the missing-tree
// contract: a real project without a specs/ directory returns present=false
// with the resolved root path and the stable empty collections — never an
// error (ADR-106 D2).
func TestGetSpecsStatus_MissingSpecsDirIsNeutral(t *testing.T) {
	api, workspaces := specsTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})

	status, err := api.GetSpecsStatus("proj-1")
	if err != nil {
		t.Fatalf("GetSpecsStatus (missing specs/): %v", err)
	}
	if status.Present {
		t.Error("Present = true, want false for a workspace without specs/")
	}
	wantRoot := filepath.Join(workspaces["proj-1"], "specs")
	if status.Root != wantRoot {
		t.Errorf("Root = %q, want %q (the resolved root is reported even when absent)", status.Root, wantRoot)
	}
	if status.Files == nil || len(status.Files) != 0 {
		t.Errorf("Files = %v, want a non-nil empty slice", status.Files)
	}
	if status.IndexTasks == nil || len(status.IndexTasks) != 0 {
		t.Errorf("IndexTasks = %v, want a non-nil empty slice", status.IndexTasks)
	}
	if status.ADRs == nil || len(status.ADRs) != 0 {
		t.Errorf("ADRs = %v, want a non-nil empty slice", status.ADRs)
	}
	if status.Findings == nil || len(status.Findings) != 0 {
		t.Errorf("Findings = %v, want a non-nil empty slice", status.Findings)
	}
	if status.Counts.Files != 0 || status.Counts.ADRs != 0 || status.Counts.Findings != 0 {
		t.Errorf("Counts = %+v, want all zero", status.Counts)
	}
	if status.Counts.FindingsByKind == nil || len(status.Counts.FindingsByKind) != 0 {
		t.Errorf("Counts.FindingsByKind = %v, want a non-nil empty map", status.Counts.FindingsByKind)
	}
	assertSpecsWireShape(t, status)
}

// TestGetSpecsStatus_NoProjectIsNeutral pins the No-Project contract: the
// pseudo-project yields the neutral present=false state without an error
// (the defensive shape — the Specs panel is not rendered in CHAT mode at
// all, ADR-106 D1).
func TestGetSpecsStatus_NoProjectIsNeutral(t *testing.T) {
	api, _ := specsTestFrontend(t, project.ProjectInfo{
		ID:   project.NoProjectID,
		Name: "No Project",
	})

	status, err := api.GetSpecsStatus(project.NoProjectID)
	if err != nil {
		t.Fatalf("GetSpecsStatus (No Project): %v", err)
	}
	if status.Present {
		t.Error("Present = true, want false for No Project")
	}
	if status.Root != "" {
		t.Errorf("Root = %q, want \"\" for No Project", status.Root)
	}
	if status.Files == nil || len(status.Files) != 0 {
		t.Errorf("Files = %v, want a non-nil empty slice", status.Files)
	}
	if status.IndexTasks == nil || len(status.IndexTasks) != 0 {
		t.Errorf("IndexTasks = %v, want a non-nil empty slice", status.IndexTasks)
	}
	if status.ADRs == nil || len(status.ADRs) != 0 {
		t.Errorf("ADRs = %v, want a non-nil empty slice", status.ADRs)
	}
	if status.Findings == nil || len(status.Findings) != 0 {
		t.Errorf("Findings = %v, want a non-nil empty slice", status.Findings)
	}
	if status.Counts.FindingsByKind == nil || len(status.Counts.FindingsByKind) != 0 {
		t.Errorf("Counts.FindingsByKind = %v, want a non-nil empty map", status.Counts.FindingsByKind)
	}
	assertSpecsWireShape(t, status)
}

// TestGetSpecsStatus_Guards pins the argument and lookup guards: an empty
// project id and an unknown project are errors, and a FrontendAPI without a
// project subsystem refuses.
func TestGetSpecsStatus_Guards(t *testing.T) {
	api, _ := specsTestFrontend(t, project.ProjectInfo{ID: "proj-1", Name: "P"})

	if _, err := api.GetSpecsStatus(""); err == nil {
		t.Error("empty project_id: got no error, want one")
	}
	if _, err := api.GetSpecsStatus("no-such-project"); err == nil {
		t.Error("unknown project: got no error, want one")
	}

	bare := &FrontendAPI{}
	bare.seedPublished.Store(true)
	if _, err := bare.GetSpecsStatus("proj-1"); err == nil {
		t.Error("nil project manager: got no error, want one")
	}
}
