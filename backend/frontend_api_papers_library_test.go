package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/papers"
	"github.com/v0lka/c0wrk/core/workspace"
)

// ---------------------------------------------------------------------------
// Paper-library RPC tests (GetPapers / GetPaper / SetPaperPinned + watcher)
// ---------------------------------------------------------------------------

// papersChanged returns every captured papers:changed payload in emission
// order. (researchEventRecorder lives in frontend_api_research_root_test.go and
// captures any map[string]string payload.)
func (r *researchEventRecorder) papersChanged() []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]string
	for _, e := range r.events {
		if e.name == EventPapersChanged {
			out = append(out, e.payload)
		}
	}
	return out
}

// papersTestFrontend wires a FrontendAPI with one real project whose workspace
// lives at <base>/ws. RESEARCH is always on for real projects, so the research
// root is the canonical <ws>/.research and the library lives at
// <ws>/.research/papers. It returns the API, the fixed project id, the
// workspace, and the effective research root.
func papersTestFrontend(t *testing.T, _ string, pins project.ResearchPins) (api *FrontendAPI, projectID, ws, effectiveRoot string) {
	t.Helper()
	base := t.TempDir()
	ws = filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}
	db := openResearchTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("create project store: %v", err)
	}
	if err := store.SaveProject(context.Background(), project.ProjectInfo{
		ID:            "proj-1",
		Name:          "Papers",
		WorkspacePath: ws,
		ResearchPins:  pins,
	}); err != nil {
		t.Fatalf("save project: %v", err)
	}
	// RESEARCH is always on for real projects: the research root is the
	// canonical <ws>/.research (there is no per-project persisted root, so it
	// can never escape the workspace).
	effectiveRoot = config.ProjectResearchPath(ws)
	recorder := &researchEventRecorder{}
	api = &FrontendAPI{
		projectManager: project.NewManager(store, base, nil),
		projStore:      store,
		emitEvent:      recorder.emit,
	}
	return api, "proj-1", ws, effectiveRoot
}

// seedTestPaper writes a paper's three artifacts into libraryRoot via the real
// core/papers writer and returns the absolute paper directory.
func seedTestPaper(t *testing.T, libraryRoot string, rec papers.PaperRecord) string {
	t.Helper()
	if err := os.MkdirAll(libraryRoot, 0o755); err != nil {
		t.Fatalf("mkdir library: %v", err)
	}
	if err := papers.WritePaper(libraryRoot, rec); err != nil {
		t.Fatalf("WritePaper: %v", err)
	}
	return filepath.Join(libraryRoot, rec.ResolvedSlug())
}

// TestGetPapers_NormalizedCollections pins the DTO boundary: every collection
// is non-nil, the parsed fields round-trip, the card path is the pin-path form,
// and roots are reported. The library lives at the default research root
// because RESEARCH is off for the fixture project.
func TestGetPapers_NormalizedCollections(t *testing.T) {
	api, projectID, ws, effectiveRoot := papersTestFrontend(t, "", project.ResearchPins{})
	libraryRoot := config.PaperLibraryPath(ws)

	full := papers.PaperRecord{
		ID:            "P-001",
		Slug:          "vaswani-2017-attention",
		Title:         "Attention Is All You Need",
		Authors:       []string{"Vaswani", "Shazeer"},
		Year:          2017,
		Venue:         "NeurIPS",
		Identifiers:   []papers.Identifier{{Scheme: "arxiv", Value: "1706.03762"}},
		Mode:          papers.ModeDeep,
		Reading:       papers.ReadingSelective,
		Verdict:       papers.VerdictAccepted,
		Confidence:    papers.ConfidenceHigh,
		ResearchIDs:   []string{"H-001"},
		Anchors:       []papers.Anchor{{Label: "sec3", Ref: "§3"}},
		Claims:        []papers.Claim{{Claim: "c", Evidence: "e", Location: "§3", Stance: "supports"}},
		RedFlags:      []papers.RedFlag{{Flag: "f", Detail: "d", Severity: "high"}},
		Uncertainties: []papers.Uncertainty{{Item: "i", Detail: "d"}},
	}
	seedTestPaper(t, libraryRoot, full)
	seedTestPaper(t, libraryRoot, papers.PaperRecord{Title: "Minimal Paper"})

	dto, err := api.GetPapers(projectID)
	if err != nil {
		t.Fatalf("GetPapers: %v", err)
	}
	if dto.ProjectID != projectID {
		t.Errorf("ProjectID = %q, want %q", dto.ProjectID, projectID)
	}
	if dto.Root != libraryRoot {
		t.Errorf("Root = %q, want %q", dto.Root, libraryRoot)
	}
	if dto.ResearchRoot != effectiveRoot {
		t.Errorf("ResearchRoot = %q, want %q", dto.ResearchRoot, effectiveRoot)
	}
	if len(dto.Papers) != 2 {
		t.Fatalf("len(Papers) = %d, want 2", len(dto.Papers))
	}
	if dto.Pinned == nil {
		t.Error("Pinned must be non-nil")
	}

	for _, p := range dto.Papers {
		if p.Authors == nil || p.Identifiers == nil || p.ResearchIDs == nil ||
			p.Anchors == nil || p.Claims == nil || p.RedFlags == nil ||
			p.Uncertainties == nil || p.LinkedResearch == nil {
			t.Errorf("paper %q has a nil collection (must be []): %+v", p.Slug, p)
		}
		if p.CardPath == "" {
			t.Errorf("paper %q has an empty CardPath", p.Slug)
		}
	}

	// Ordered by ID then slug: P-001 sorts before the id-less paper.
	got := dto.Papers[0]
	if got.ID != "P-001" || got.Slug != "vaswani-2017-attention" {
		t.Fatalf("Papers[0] = %s/%s, want P-001/vaswani-2017-attention", got.ID, got.Slug)
	}
	if got.Title != "Attention Is All You Need" || got.Year != 2017 || got.Venue != "NeurIPS" {
		t.Errorf("identity fields not round-tripped: %+v", got)
	}
	if got.Mode != "deep" || got.Reading != "selective" || got.Verdict != "accepted" || got.Confidence != "high" {
		t.Errorf("enums not round-tripped: mode=%q reading=%q verdict=%q confidence=%q", got.Mode, got.Reading, got.Verdict, got.Confidence)
	}
	if len(got.Claims) != 1 || got.Claims[0].Location != "§3" {
		t.Errorf("claims not round-tripped: %+v", got.Claims)
	}
	if len(got.Identifiers) != 1 || got.Identifiers[0].Value != "1706.03762" {
		t.Errorf("identifiers not round-tripped: %+v", got.Identifiers)
	}
	wantCard := "papers/vaswani-2017-attention/paper.md"
	if got.CardPath != wantCard {
		t.Errorf("CardPath = %q, want %q", got.CardPath, wantCard)
	}
	if got.Dir != filepath.Join(libraryRoot, "vaswani-2017-attention") {
		t.Errorf("Dir = %q", got.Dir)
	}
	if got.Pinned {
		t.Error("no paper should be pinned initially")
	}
	// H-001 cannot resolve to an R-NNN here (no research project parsed): the
	// raw id is preserved and the link carries an empty ResearchID.
	if len(got.LinkedResearch) != 1 || got.LinkedResearch[0].HypothesisID != "H-001" || got.LinkedResearch[0].ResearchID != "" {
		t.Errorf("LinkedResearch = %+v, want a single dangling H-001 link", got.LinkedResearch)
	}
}

// TestGetPapers_EmptyLibraryIsNotAnError: a library that does not exist yet
// yields an empty, non-nil collection rather than an error.
func TestGetPapers_EmptyLibraryIsNotAnError(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})

	dto, err := api.GetPapers(projectID)
	if err != nil {
		t.Fatalf("GetPapers: %v", err)
	}
	if dto.Papers == nil || len(dto.Papers) != 0 {
		t.Errorf("Papers = %#v, want an empty non-nil slice", dto.Papers)
	}
	if dto.Pinned == nil {
		t.Error("Pinned must be non-nil")
	}
	if dto.Root != config.PaperLibraryPath(ws) {
		t.Errorf("Root = %q, want %q", dto.Root, config.PaperLibraryPath(ws))
	}
}

// TestGetPapers_RequiresActiveProject: the RPC rejects a missing project id, an
// unknown project, and the No Project pseudo-project.
func TestGetPapers_RequiresActiveProject(t *testing.T) {
	api, projectID, _, _ := papersTestFrontend(t, "", project.ResearchPins{})

	if _, err := api.GetPapers(""); err == nil {
		t.Error(`GetPapers("") should require a project_id`)
	}
	if _, err := api.GetPapers("does-not-exist"); err == nil {
		t.Error("GetPapers(unknown) should error")
	}
	if _, err := api.GetPaper(projectID, ""); err == nil {
		t.Error("GetPaper with an empty id should error")
	}

	// The No Project pseudo-project has no workspace, so it is not a valid
	// paper-library subject.
	noProj := project.ProjectInfo{
		ID:            project.NoProjectID,
		Name:          "No Project",
		WorkspacePath: filepath.Join(t.TempDir(), "np"),
	}
	if err := api.projStore.SaveProject(context.Background(), noProj); err != nil {
		t.Fatalf("save No Project row: %v", err)
	}
	if _, err := api.GetPapers(project.NoProjectID); err == nil {
		t.Error("GetPapers(No Project) should error")
	}
}

// TestGetPaper_ByIDSlugAndNotFound: lookup accepts the canonical id, a
// non-normalized spelling, and the slug, and errors for an unknown paper.
func TestGetPaper_ByIDSlugAndNotFound(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
	libraryRoot := config.PaperLibraryPath(ws)
	seedTestPaper(t, libraryRoot, papers.PaperRecord{
		ID: "P-001", Slug: "vaswani-2017-attention", Title: "Attention",
	})

	for _, key := range []string{"P-001", "P-1", "vaswani-2017-attention"} {
		dto, err := api.GetPaper(projectID, key)
		if err != nil {
			t.Fatalf("GetPaper(%q): %v", key, err)
		}
		if dto.Slug != "vaswani-2017-attention" {
			t.Errorf("GetPaper(%q).Slug = %q", key, dto.Slug)
		}
	}
	if _, err := api.GetPaper(projectID, "no-such-paper"); err == nil {
		t.Error("GetPaper(unknown) should error")
	}
}

// TestGetPapers_LinkedResearchResolvesRNNN: a paper's research_ids (H-NNN) are
// resolved to the owning R-NNN project when the research root is parseable, and
// an unresolvable id becomes a dangling link.
func TestGetPapers_LinkedResearchResolvesRNNN(t *testing.T) {
	api, projectID, researchRoot, _ := researchRootTestFrontend(t,
		[]string{"R-001-test"}, project.ResearchPins{})
	libraryRoot := config.PaperLibraryPathIn(researchRoot)
	seedTestPaper(t, libraryRoot, papers.PaperRecord{
		ID: "P-001", Slug: "linked", Title: "Linked",
		ResearchIDs: []string{"H-001", "H-099"},
	})

	dto, err := api.GetPapers(projectID)
	if err != nil {
		t.Fatalf("GetPapers: %v", err)
	}
	if len(dto.Papers) != 1 {
		t.Fatalf("len(Papers) = %d, want 1", len(dto.Papers))
	}
	links := dto.Papers[0].LinkedResearch
	var wired, dangling bool
	for _, l := range links {
		// ResearchProject.ID is the normalized R-NNN (the brief header), so
		// "R-001-test" resolves to "R-001" — the same id the research panel
		// and the pin RPCs use.
		if l.HypothesisID == "H-001" && l.ResearchID == "R-001" {
			wired = true
		}
		if l.HypothesisID == "H-099" && l.ResearchID == "" {
			dangling = true
		}
	}
	if !wired {
		t.Errorf("H-001 not linked to R-001: %+v", links)
	}
	if !dangling {
		t.Errorf("H-099 should be a dangling link: %+v", links)
	}
	if len(dto.Papers[0].ResearchIDs) != 2 {
		t.Errorf("raw ResearchIDs not preserved: %+v", dto.Papers[0].ResearchIDs)
	}
}

// TestSetPaperPinned_PinUnpinPersists pins the pin lifecycle: it persists under
// ResearchPins.Papers, is idempotent, shows up in GetPapers, removes cleanly,
// and rejects unknown papers and papers whose card is missing.
func TestSetPaperPinned_PinUnpinPersists(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
	libraryRoot := config.PaperLibraryPath(ws)
	dir := seedTestPaper(t, libraryRoot, papers.PaperRecord{
		ID: "P-001", Slug: "pinme", Title: "Pin Me",
	})
	wantPath := "papers/pinme/paper.md"

	if err := api.SetPaperPinned(projectID, "P-001", true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if got := researchPinsOf(t, api, projectID).Papers; len(got) != 1 || got[0] != wantPath {
		t.Fatalf("pins = %v, want [%s]", got, wantPath)
	}

	// Idempotent.
	if err := api.SetPaperPinned(projectID, "P-001", true); err != nil {
		t.Fatalf("re-pin: %v", err)
	}
	if got := researchPinsOf(t, api, projectID).Papers; len(got) != 1 {
		t.Fatalf("re-pin changed pins: %v", got)
	}

	dto, err := api.GetPapers(projectID)
	if err != nil {
		t.Fatalf("GetPapers: %v", err)
	}
	if len(dto.Pinned) != 1 || dto.Pinned[0] != wantPath {
		t.Errorf("dto.Pinned = %v, want [%s]", dto.Pinned, wantPath)
	}
	if len(dto.Papers) != 1 || !dto.Papers[0].Pinned {
		t.Errorf("paper not marked pinned: %+v", dto.Papers)
	}

	// Unpin removes the entry.
	if err := api.SetPaperPinned(projectID, "P-001", false); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if got := researchPinsOf(t, api, projectID).Papers; len(got) != 0 {
		t.Errorf("unpin left pins: %v", got)
	}

	// Unknown paper.
	if err := api.SetPaperPinned(projectID, "nope", true); err == nil {
		t.Error("pinning an unknown paper should error")
	}

	// Pinning fails closed when the card file is gone (the record is still
	// parsed from note.md/appraisal.md, addressed by slug).
	if err := os.Remove(filepath.Join(dir, papers.PaperFileName)); err != nil {
		t.Fatalf("remove card: %v", err)
	}
	if err := api.SetPaperPinned(projectID, "pinme", true); err == nil {
		t.Error("pinning a paper whose card is missing should error")
	}
}

// TestEmitPapersChanged_ScopesToLibrary unit-tests the watcher helper: it emits
// only for paths inside the library, and always reports the project id.
func TestEmitPapersChanged_ScopesToLibrary(t *testing.T) {
	rec := &researchEventRecorder{}
	f := &FrontendAPI{emitEvent: rec.emit}
	// Arbitrary (non-existent) POSIX paths: emitPapersChanged only does
	// containment prefix-matching, it never touches the filesystem.
	root := "/ws/.research/papers"
	card := root + "/vaswani-2017-attention/paper.md"
	research := "/ws/.research/R-001-test/brief.md"
	comparisonsRoot := "/ws/.research/comparisons"
	comparison := comparisonsRoot + "/a-vs-b.md"

	if f.emitPapersChanged("", "", "p", []string{card}) {
		t.Error("empty roots must not emit")
	}
	if !f.emitPapersChanged(root, comparisonsRoot, "p", []string{research, card}) {
		t.Fatal("a library path should emit")
	}
	got := rec.papersChanged()
	if len(got) != 1 {
		t.Fatalf("want 1 papers:changed, got %d", len(got))
	}
	if got[0]["project_id"] != "p" || !strings.Contains(got[0]["paths"], "paper.md") {
		t.Errorf("payload = %v", got[0])
	}
	if strings.Contains(got[0]["paths"], "brief.md") {
		t.Errorf("a non-library path leaked into the payload: %v", got[0])
	}
	if f.emitPapersChanged(root, comparisonsRoot, "p", []string{research}) {
		t.Error("a non-library path must not emit")
	}
	if got := rec.papersChanged(); len(got) != 1 {
		t.Errorf("unexpected extra emission: %v", got)
	}
	// The comparisons root is scoped too: a comparison artifact emits.
	if !f.emitPapersChanged(root, comparisonsRoot, "p", []string{comparison}) {
		t.Fatal("a comparisons path should emit")
	}
	if got := rec.papersChanged(); len(got) != 2 {
		t.Fatalf("want 2 papers:changed, got %d", len(got))
	}
	if last := rec.papersChanged()[1]; !strings.Contains(last["paths"], "a-vs-b.md") {
		t.Errorf("comparisons payload = %v", last)
	}
	// A papers-only context (empty comparisons root) still scopes the library.
	if !f.emitPapersChanged(root, "", "p", []string{card}) {
		t.Fatal("a library path should emit without a comparisons root")
	}
}

// TestPapersFileChanged_EmitsWithoutResearch covers the hybrid path through a
// test-built watcher: with RESEARCH OFF, a paper-card edit still emits
// papers:changed (the library is watched independently of the toggle), while
// research:file_changed stays silent. The PRODUCTION watcher wiring in
// switchProjectSetupWatcher is covered separately by
// TestSwitchProjectSetupWatcher_WatchesHybridLibrary (Issue 107).
func TestPapersFileChanged_EmitsWithoutResearch(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "project", "workspace")
	papersRoot := config.PaperLibraryPath(ws)
	cardDir := filepath.Join(papersRoot, "vaswani-2017-attention")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatalf("mkdir paper dir: %v", err)
	}
	card := filepath.Join(cardDir, papers.PaperFileName)
	if err := os.WriteFile(card, []byte("---\nid: P-001\n---\n"), 0o644); err != nil {
		t.Fatalf("seed paper.md: %v", err)
	}

	var treeChanged, researchChanged, papersChangedCount atomic.Int32
	f := &FrontendAPI{
		agentDir: base,
		emitEvent: func(name string, _ ...any) {
			switch name {
			case EventWorkspaceTreeChanged:
				treeChanged.Add(1)
			case EventResearchFileChanged:
				researchChanged.Add(1)
			case EventPapersChanged:
				papersChangedCount.Add(1)
			}
		},
	}
	f.activeProjectMu.Lock()
	f.activeProjectID = "real-project"
	f.activeProjectPath = ws
	f.activeResearchRoot = "" // RESEARCH OFF — hybrid mode
	f.activePapersRoot = papersRoot
	f.activeProjectMu.Unlock()
	t.Cleanup(func() {
		if f.watcher != nil {
			_ = f.watcher.Close()
		}
	})

	watcher, err := workspace.NewWatcher(ws, func(changedPaths []string) {
		f.activeProjectMu.RLock()
		snapProjectID := f.activeProjectID
		snapResearchRoot := f.activeResearchRoot
		snapPapersRoot := f.activePapersRoot
		snapComparisonsRoot := f.activeComparisonsRoot
		f.activeProjectMu.RUnlock()

		researchScoped := f.emitResearchFileChanged(snapResearchRoot, snapProjectID, changedPaths)
		f.emitPapersChanged(snapPapersRoot, snapComparisonsRoot, snapProjectID, changedPaths)
		f.emitEvent(EventWorkspaceTreeChanged, map[string]bool{"research_scoped": researchScoped})
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	f.watcherMu.Lock()
	f.watcher = watcher
	f.watcherMu.Unlock()

	// THE FIX: the library is watched even though RESEARCH is off.
	if err := watcher.WatchTree(papersRoot); err != nil {
		t.Fatalf("WatchTree: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if err := os.WriteFile(card, []byte("---\nid: P-001\ntitle: updated\n---\n"), 0o644); err != nil {
		t.Fatalf("modify paper.md: %v", err)
	}
	if !waitForEmission(&papersChangedCount, 1, 3*time.Second) {
		t.Fatal("papers:changed NOT emitted for a paper edit with RESEARCH off")
	}
	if researchChanged.Load() != 0 {
		t.Error("research:file_changed must not fire when RESEARCH is off")
	}
	if treeChanged.Load() == 0 {
		t.Error("workspace:tree_changed should still fire")
	}
}

// TestComparisonsFileChanged_EmitsWithoutResearch covers the hybrid path for the
// comparisons sibling through a test-built watcher: with RESEARCH OFF, writing
// <research-root>/comparisons/<slug>.md still emits papers:changed, so the
// Compare section refreshes without reopening the tab. The PRODUCTION watcher
// wiring in switchProjectSetupWatcher is covered separately by
// TestSwitchProjectSetupWatcher_WatchesHybridLibrary (Issue 107).
func TestComparisonsFileChanged_EmitsWithoutResearch(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "project", "workspace")
	comparisonsRoot := config.ComparisonsPath(ws)
	comparison := filepath.Join(comparisonsRoot, "a-vs-b.md")
	if err := os.MkdirAll(comparisonsRoot, 0o755); err != nil {
		t.Fatalf("mkdir comparisons dir: %v", err)
	}
	if err := os.WriteFile(comparison, []byte("# A vs B\n"), 0o644); err != nil {
		t.Fatalf("seed comparison: %v", err)
	}

	var papersChangedCount atomic.Int32
	f := &FrontendAPI{
		agentDir: base,
		emitEvent: func(name string, _ ...any) {
			if name == EventPapersChanged {
				papersChangedCount.Add(1)
			}
		},
	}
	f.activeProjectMu.Lock()
	f.activeProjectID = "real-project"
	f.activeProjectPath = ws
	f.activeResearchRoot = "" // RESEARCH OFF — hybrid mode
	f.activeComparisonsRoot = comparisonsRoot
	f.activeProjectMu.Unlock()
	t.Cleanup(func() {
		if f.watcher != nil {
			_ = f.watcher.Close()
		}
	})

	watcher, err := workspace.NewWatcher(ws, func(changedPaths []string) {
		f.activeProjectMu.RLock()
		snapProjectID := f.activeProjectID
		snapPapersRoot := f.activePapersRoot
		snapComparisonsRoot := f.activeComparisonsRoot
		f.activeProjectMu.RUnlock()

		f.emitPapersChanged(snapPapersRoot, snapComparisonsRoot, snapProjectID, changedPaths)
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	f.watcherMu.Lock()
	f.watcher = watcher
	f.watcherMu.Unlock()

	// THE FIX: the comparisons dir is watched even though RESEARCH is off.
	if err := watcher.WatchTree(comparisonsRoot); err != nil {
		t.Fatalf("WatchTree: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if err := os.WriteFile(comparison, []byte("# A vs B (revised)\n"), 0o644); err != nil {
		t.Fatalf("modify comparison: %v", err)
	}
	if !waitForEmission(&papersChangedCount, 1, 3*time.Second) {
		t.Fatal("papers:changed NOT emitted for a comparison write with RESEARCH off")
	}
}

// TestSetPaperPinned_UnpinAfterDirectoryDeleted pins Issue 5: a pinned paper
// whose whole directory was deleted (so lib.Get returns nil) must still be
// unpinnable — otherwise the persisted pin is orphaned forever (no other RPC
// removes a paper pin).
func TestSetPaperPinned_UnpinAfterDirectoryDeleted(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
	libraryRoot := config.PaperLibraryPath(ws)
	dir := seedTestPaper(t, libraryRoot, papers.PaperRecord{
		ID: "P-001", Slug: "gone", Title: "Gone",
	})
	if err := api.SetPaperPinned(projectID, "P-001", true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if got := researchPinsOf(t, api, projectID).Papers; len(got) != 1 || got[0] != "papers/gone/paper.md" {
		t.Fatalf("pins = %v, want [papers/gone/paper.md]", got)
	}

	// Delete the entire paper directory: lib.Get("P-001") is now nil.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove paper dir: %v", err)
	}
	if err := api.SetPaperPinned(projectID, "gone", false); err != nil {
		t.Fatalf("unpin after the directory was deleted: %v", err)
	}
	if got := researchPinsOf(t, api, projectID).Papers; len(got) != 0 {
		t.Fatalf("stale pin survived the directory deletion: %v", got)
	}
}

// TestSetPaperPinned_UnpinByStoredCardPath pins the path-keyed half of Issue 5's
// fix: a caller may pass the stored card path itself (what a
// pinned-but-missing list would do), and the pin must still be removed even
// though no record resolves.
func TestSetPaperPinned_UnpinByStoredCardPath(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
	libraryRoot := config.PaperLibraryPath(ws)
	dir := seedTestPaper(t, libraryRoot, papers.PaperRecord{
		ID: "P-001", Slug: "gone", Title: "Gone",
	})
	if err := api.SetPaperPinned(projectID, "P-001", true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove paper dir: %v", err)
	}
	if err := api.SetPaperPinned(projectID, "papers/gone/paper.md", false); err != nil {
		t.Fatalf("unpin by the stored card path: %v", err)
	}
	if got := researchPinsOf(t, api, projectID).Papers; len(got) != 0 {
		t.Fatalf("stale pin survived: %v", got)
	}
}

// TestSetPaperPinned_UnpinAfterDirectoryRenamed pins Issue 53: a pin whose card
// directory was renamed (slug changed) while the card kept its declared
// identity must still be removable — the stored pin keys off the old path, so
// matching on the current on-disk card path alone silently leaves it.
func TestSetPaperPinned_UnpinAfterDirectoryRenamed(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
	libraryRoot := config.PaperLibraryPath(ws)
	dir := seedTestPaper(t, libraryRoot, papers.PaperRecord{
		ID: "P-001", Slug: "old-slug", Title: "Renamed",
	})
	if err := api.SetPaperPinned(projectID, "P-001", true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if got := researchPinsOf(t, api, projectID).Papers; len(got) != 1 || got[0] != "papers/old-slug/paper.md" {
		t.Fatalf("pins = %v, want [papers/old-slug/paper.md]", got)
	}

	// Rename the directory; the card (id + declared slug) is preserved, so
	// lib.Get("P-001") resolves to the NEW directory while the stored pin still
	// keys off the old path.
	if err := os.Rename(dir, filepath.Join(libraryRoot, "new-slug")); err != nil {
		t.Fatalf("rename paper dir: %v", err)
	}
	if err := api.SetPaperPinned(projectID, "P-001", false); err != nil {
		t.Fatalf("unpin after the directory was renamed: %v", err)
	}
	if got := researchPinsOf(t, api, projectID).Papers; len(got) != 0 {
		t.Fatalf("stale pin survived the rename: %v", got)
	}
}

// TestGetPapers_UnreadableLibraryIsAnError pins Issue 37: an unreadable library
// root (anything other than "does not exist") must surface an error rather than
// degrade to an empty library indistinguishable from "no papers studied yet".
func TestGetPapers_UnreadableLibraryIsAnError(t *testing.T) {
	api, projectID, ws, _ := papersTestFrontend(t, "", project.ResearchPins{})
	// Put a regular FILE where the library DIRECTORY is expected — a genuine
	// read failure (not fs.ErrNotExist).
	libraryRoot := config.PaperLibraryPath(ws)
	if err := os.MkdirAll(filepath.Dir(libraryRoot), 0o755); err != nil {
		t.Fatalf("mkdir research root: %v", err)
	}
	if err := os.WriteFile(libraryRoot, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write library path: %v", err)
	}

	if _, err := api.GetPapers(projectID); err == nil {
		t.Fatal("GetPapers must surface an unreadable library root, not render it as empty")
	}
	if _, err := api.GetPaper(projectID, "P-001"); err == nil {
		t.Fatal("GetPaper must surface an unreadable library root")
	}
}

// TestSwitchProjectSetupWatcher_WatchesHybridLibrary is the production-wiring
// acceptance test for Issue 107: it drives the REAL f.switchProjectSetupWatcher
// for a CODE project (RESEARCH off) and asserts a paper-card and a comparison
// write under the effective research root still emit papers:changed. The two
// self-wiring tests above build their own watcher, so the production WatchTree
// call in switchProjectSetupWatcher had no coverage and could regress silently.
func TestSwitchProjectSetupWatcher_WatchesHybridLibrary(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "project", "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir ws: %v", err)
	}

	var papersChanged atomic.Int32
	f := &FrontendAPI{
		agentDir: base,
		emitEvent: func(name string, _ ...any) {
			if name == EventPapersChanged {
				papersChanged.Add(1)
			}
		},
	}
	// The production callback snapshots the active fields (set by
	// switchProjectActivate in the real switch); set them directly so this test
	// stays focused on the watcher wiring.
	p := &project.ProjectInfo{ID: "proj-1", Name: "Hybrid", WorkspacePath: ws}
	f.activeProjectMu.Lock()
	f.activeProjectID = p.ID
	f.activeProjectPath = ws
	f.activeResearchRoot = "" // RESEARCH off — hybrid mode
	f.activePapersRoot = papersRootForProject(p)
	f.activeComparisonsRoot = comparisonsRootForProject(p)
	f.activeProjectMu.Unlock()
	t.Cleanup(func() {
		f.watcherMu.Lock()
		if f.watcher != nil {
			_ = f.watcher.Close()
		}
		f.watcherMu.Unlock()
	})

	f.switchProjectSetupWatcher(p)
	f.watcherMu.Lock()
	created := f.watcher != nil
	f.watcherMu.Unlock()
	if !created {
		t.Fatal("switchProjectSetupWatcher did not create a watcher for a CODE project")
	}

	// Issue 52: nothing is materialized at switch time.
	if _, err := os.Stat(config.ProjectResearchPath(ws)); !os.IsNotExist(err) {
		t.Fatalf("switchProjectSetupWatcher materialized .research: err=%v", err)
	}

	// A paper-card write under the effective research root — with RESEARCH off
	// and the tree created on demand — must emit papers:changed.
	cardDir := filepath.Join(config.PaperLibraryPath(ws), "vaswani-2017-attention")
	if err := os.MkdirAll(cardDir, 0o755); err != nil {
		t.Fatalf("mkdir card dir: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // let the recursive root auto-add the new tree
	if err := os.WriteFile(filepath.Join(cardDir, papers.PaperFileName), []byte("---\nid: P-001\n---\n"), 0o644); err != nil {
		t.Fatalf("write card: %v", err)
	}
	if !waitForEmission(&papersChanged, 1, 3*time.Second) {
		t.Fatal("papers:changed NOT emitted via the production switchProjectSetupWatcher wiring (hybrid mode)")
	}

	// The comparisons sibling is watched through the same production wiring.
	comparisonsRoot := config.ComparisonsPath(ws)
	if err := os.MkdirAll(comparisonsRoot, 0o755); err != nil {
		t.Fatalf("mkdir comparisons dir: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(comparisonsRoot, "a-vs-b.md"), []byte("# A vs B\n"), 0o644); err != nil {
		t.Fatalf("write comparison: %v", err)
	}
	if !waitForEmission(&papersChanged, 2, 3*time.Second) {
		t.Fatal("papers:changed NOT emitted for a comparison write via the production wiring")
	}
}
