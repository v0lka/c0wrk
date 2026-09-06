package backend

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/research"
	"github.com/v0lka/c0wrk/core/workspace"
	_ "modernc.org/sqlite"
)

// TestResearchFileChanged_NestedSubdir verifies that the research panel update
// mechanism works: a hypothesis card at <ws>/.research/R-001/hypotheses/H-001.md
// (a deeply nested path) is detected by the watcher and the CODE-mode callback
// emits research:file_changed. This exercises the WatchTree fix — without it,
// fsnotify only watches the workspace root and nested research edits go
// undetected.
func TestResearchFileChanged_NestedSubdir(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "project", "workspace")
	// Create the nested research directory BEFORE the watcher starts.
	hypDir := filepath.Join(ws, ".research", "R-001-test", "hypotheses")
	if err := os.MkdirAll(hypDir, 0o755); err != nil {
		t.Fatalf("MkdirAll hyp dir: %v", err)
	}
	researchRoot := filepath.Join(ws, ".research")

	// Seed a hypothesis file so the edit below is a modify, not a create.
	hypFile := filepath.Join(hypDir, "H-001.md")
	if err := os.WriteFile(hypFile, []byte("# H-001: old\n"), 0o644); err != nil {
		t.Fatalf("seed H-001: %v", err)
	}

	var treeChanged atomic.Int32
	var researchChanged atomic.Int32
	f := &FrontendAPI{
		agentDir: base,
		emitEvent: func(name string, _ ...any) {
			switch name {
			case EventWorkspaceTreeChanged:
				treeChanged.Add(1)
			case EventResearchFileChanged:
				researchChanged.Add(1)
			}
		},
	}
	f.activeProjectMu.Lock()
	f.activeProjectID = "real-project"
	f.activeProjectPath = ws
	f.activeResearchRoot = researchRoot
	f.activeProjectMu.Unlock()
	t.Cleanup(func() {
		if f.watcher != nil {
			_ = f.watcher.Close()
		}
	})

	// Replicate the CODE-mode callback from switchProjectSetupWatcher.
	// Uses the same emitResearchFileChanged helper as production code so the
	// test exercises the real path (DRY — the helper was extracted from this
	// duplicated logic).
	watcher, err := workspace.NewWatcher(ws, func(changedPaths []string) {
		f.activeProjectMu.RLock()
		snapProjectID := f.activeProjectID
		snapResearchRoot := f.activeResearchRoot
		f.activeProjectMu.RUnlock()

		researchScoped := f.emitResearchFileChanged(snapResearchRoot, snapProjectID, changedPaths)
		f.emitEvent(EventWorkspaceTreeChanged, map[string]bool{
			"research_scoped": researchScoped,
		})
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	f.watcherMu.Lock()
	f.watcher = watcher
	f.watcherMu.Unlock()

	// THE FIX: recursively watch the research tree so nested hypothesis
	// edits are detected (mirrors switchProjectSetupWatcher / EnableResearch).
	if err := watcher.WatchTree(researchRoot); err != nil {
		t.Fatalf("WatchTree: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Modify the nested hypothesis file — must now be detected.
	if err := os.WriteFile(hypFile, []byte("# H-001: updated content\n"), 0o644); err != nil {
		t.Fatalf("modify H-001: %v", err)
	}
	if !waitForEmission(&researchChanged, 1, 3*time.Second) {
		t.Fatal("research:file_changed NOT emitted for nested research file — WatchTree fix did not work")
	}
	t.Logf("PASS: research:file_changed emitted for nested hypothesis edit (research=%d, tree=%d)",
		researchChanged.Load(), treeChanged.Load())

	// Verify auto-add: create a brand-new research project subdir + hypothesis.
	researchChanged.Store(0)
	newHypDir := filepath.Join(ws, ".research", "R-002-new", "hypotheses")
	if err := os.MkdirAll(newHypDir, 0o755); err != nil {
		t.Fatalf("MkdirAll new hyp dir: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // let mkdir events + auto-add propagate

	newHypFile := filepath.Join(newHypDir, "H-001.md")
	if err := os.WriteFile(newHypFile, []byte("# H-001: brand new\n"), 0o644); err != nil {
		t.Fatalf("write new H-001: %v", err)
	}
	if !waitForEmission(&researchChanged, 1, 3*time.Second) {
		t.Fatal("research:file_changed NOT emitted for new research subdir — auto-add did not work")
	}
	t.Logf("PASS: research:file_changed emitted for newly-created research subdir")
}

// ---------------------------------------------------------------------------
// Structured mutation RPCs (UpdateHypothesis / CreateHypothesis)
// ---------------------------------------------------------------------------

// openResearchTestDB opens an in-memory SQLite DB with the pragmas the
// project store expects (mirrors openProjectSwitchTestDB). Connections are
// capped at one: an in-memory DB is per-connection, so a second pooled
// connection would see an empty database (no tables) — which concurrent RPC
// tests (goroutines sharing the FrontendAPI) would hit immediately.
func openResearchTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		t.Fatalf("failed to enable WAL: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		t.Fatalf("failed to enable foreign keys: %v", err)
	}
	return db
}

// seedResearchGraphAndCard writes a minimal hypotheses/graph.md (Mermaid node
// + catalog row) and one open H-001 card into hypDir.
func seedResearchGraphAndCard(t *testing.T, hypDir string) {
	t.Helper()
	graph := `# Hypothesis Graph

## Diagram

` + "```mermaid\n" + `graph TD
    classDef confirmed fill:#4CAF50,color:#fff
    classDef refuted fill:#F44336,color:#fff
    classDef in_progress fill:#FF9800,color:#fff
    classDef open fill:#2196F3,color:#fff
    classDef cancelled fill:#9E9E9E,color:#fff

    H001["H-001: Static bundle parsing"]:::open
` + "```\n" + `
## Hypothesis Catalog

| ID | Hypothesis | Status | Decision | Parent(s) |
|---|---|---|---|---|
| [H-001](H-001.md) | Static bundle parsing | open | — | — |

---

[Back to Brief](../brief.md)
`
	if err := os.WriteFile(filepath.Join(hypDir, "graph.md"), []byte(graph), 0o644); err != nil {
		t.Fatalf("graph: %v", err)
	}
	card := `# H-001: Static bundle parsing

| Field | Value |
|---|---|
| **Identifier** | H-001 |
| **Status** | open |
| **Timebox** | 5 days |
| **Parent(s)** | — |
| **Created** | 2025-04-02 |
| **Completed** | — |
| **Decision** | — |

## Statement

Static analysis of webpack bundles can recover the module graph.

## Verification Criterion

Recover >= 95% of modules.

## Experiment Notes

*Not yet started.*

## Result

**Finding:** —

---

[Back to Hypothesis Graph](graph.md) | [Back to Brief](../brief.md)
`
	if err := os.WriteFile(filepath.Join(hypDir, "H-001.md"), []byte(card), 0o644); err != nil {
		t.Fatalf("card: %v", err)
	}
}

// seedResearchProjectDir writes a minimal research project directory
// (brief + graph + one open H-001 card) under root/dirName. The brief's
// R-NNN header is derived from dirName so the parsed project ID always
// matches the directory numbering.
func seedResearchProjectDir(t *testing.T, root, dirName string) {
	t.Helper()
	rid := research.NormalizeResearchID(dirName)
	if rid == "" {
		t.Fatalf("seed dir %q carries no R-NNN id", dirName)
	}
	hypDir := filepath.Join(root, dirName, "hypotheses")
	if err := os.MkdirAll(hypDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	brief := fmt.Sprintf("# [%s] Test\n", rid)
	if err := os.WriteFile(filepath.Join(root, dirName, "brief.md"), []byte(brief), 0o644); err != nil {
		t.Fatalf("brief: %v", err)
	}
	seedResearchGraphAndCard(t, hypDir)
}

// researchMutationTestFrontend builds a FrontendAPI wired with a real project
// manager (backed by an in-memory SQLite store, experimental features enabled)
// and a project whose workspace contains a minimal nested research root
// (R-001-test with one open hypothesis). It returns the API, the project ID,
// and the research root path.
func researchMutationTestFrontend(t *testing.T) (api *FrontendAPI, projectID, root string) {
	t.Helper()
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	researchRoot := filepath.Join(ws, ".research")

	seedResearchProjectDir(t, researchRoot, "R-001-test")

	db := openResearchTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("create project store: %v", err)
	}
	if err := store.SaveProject(context.Background(), project.ProjectInfo{
		ID:            "proj-1",
		Name:          "Research",
		WorkspacePath: ws,
		ResearchRoot:  researchRoot,
	}); err != nil {
		t.Fatalf("save project: %v", err)
	}

	mgr := project.NewManager(store, base, nil)
	f := &FrontendAPI{
		projectManager: mgr,
		config:         &config.Config{Experimental: config.ExperimentalConfig{Enabled: true}},
		emitEvent:      func(_ string, _ ...any) {},
	}
	return f, "proj-1", researchRoot
}

// TestResearchRPC_UpdateAndCreateRoundTrip verifies the structured mutation
// RPCs: UpdateHypothesis changes the card + graph, and CreateHypothesis assigns
// the next H-NNN id and wires edges from parents — both reflected by a fresh
// ParseProject.
func TestResearchRPC_UpdateAndCreateRoundTrip(t *testing.T) {
	f, projectID, researchRoot := researchMutationTestFrontend(t)

	status := "in-progress"
	title := "Refined bundle parsing"
	result := "Recovered 97% of modules."
	dto, err := f.UpdateHypothesis(projectID, "R-001", "H-001", HypothesisUpdateFields{
		Status: &status,
		Title:  &title,
		Result: &result,
	})
	if err != nil {
		t.Fatalf("UpdateHypothesis: %v", err)
	}
	if dto == nil {
		t.Fatal("UpdateHypothesis returned nil DTO")
	}

	projectDir, err := research.ActiveProjectDir(researchRoot)
	if err != nil {
		t.Fatalf("ActiveProjectDir: %v", err)
	}
	proj, err := research.ParseProject(projectDir)
	if err != nil {
		t.Fatalf("ParseProject after update: %v", err)
	}
	n := proj.Graph.Node("H-001")
	if n == nil {
		t.Fatal("H-001 missing after update")
	}
	if n.Status != research.StatusInProgress {
		t.Errorf("status = %q, want in-progress", n.Status)
	}
	if n.Title != title || n.Result != result {
		t.Errorf("title/result = %q/%q, want %q/%q", n.Title, n.Result, title, result)
	}

	// Create H-002 as a child of H-001.
	dto2, err := f.CreateHypothesis(projectID, NewHypothesisCard{
		Title:   "Runtime interception",
		Parents: []string{"H-001"},
	})
	if err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}
	if dto2 == nil {
		t.Fatal("CreateHypothesis returned nil DTO")
	}

	proj2, err := research.ParseProject(projectDir)
	if err != nil {
		t.Fatalf("ParseProject after create: %v", err)
	}
	n2 := proj2.Graph.Node("H-002")
	if n2 == nil {
		t.Fatal("H-002 missing after create")
	}
	if len(n2.Parents) != 1 || n2.Parents[0] != "H-001" {
		t.Errorf("H-002 parents = %v, want [H-001]", n2.Parents)
	}
	foundEdge := false
	for _, e := range proj2.Graph.Edges {
		if e.From == "H-001" && e.To == "H-002" {
			foundEdge = true
		}
	}
	if !foundEdge {
		t.Errorf("edge H-001→H-002 missing; edges=%v", proj2.Graph.Edges)
	}
}

// TestResearchRPC_RejectsInvalidInput verifies the guards: illegal/backward
// transitions error without mutating files, and missing/invalid hypothesis ids
// are rejected.
func TestResearchRPC_RejectsInvalidInput(t *testing.T) {
	f, projectID, researchRoot := researchMutationTestFrontend(t)

	projectDir, err := research.ActiveProjectDir(researchRoot)
	if err != nil {
		t.Fatalf("ActiveProjectDir: %v", err)
	}
	cardPath := filepath.Join(projectDir, "hypotheses", "H-001.md")
	graphPath := filepath.Join(projectDir, "hypotheses", "graph.md")
	cardBefore, _ := os.ReadFile(cardPath)
	graphBefore, _ := os.ReadFile(graphPath)

	// Illegal transition: open → confirmed (must go through in-progress).
	bad := "confirmed"
	if _, err := f.UpdateHypothesis(projectID, "R-001", "H-001", HypothesisUpdateFields{Status: &bad}); err == nil {
		t.Fatal("expected error for open→confirmed")
	}

	cardAfter, _ := os.ReadFile(cardPath)
	graphAfter, _ := os.ReadFile(graphPath)
	if !bytes.Equal(cardBefore, cardAfter) {
		t.Error("card changed despite failed transition")
	}
	if !bytes.Equal(graphBefore, graphAfter) {
		t.Error("graph changed despite failed transition")
	}

	// Missing / invalid hypothesis ids.
	if _, err := f.UpdateHypothesis(projectID, "R-001", "H-999", HypothesisUpdateFields{}); err == nil {
		t.Error("expected error for missing hypothesis id")
	}
	if _, err := f.UpdateHypothesis(projectID, "R-001", "not-an-id", HypothesisUpdateFields{}); err == nil {
		t.Error("expected error for invalid hypothesis id")
	}
}

// TestResearchRPC_RejectsOutOfWorkspaceRoot verifies the workspace-containment
// guard: a persisted research root outside the workspace is rejected.
func TestResearchRPC_RejectsOutOfWorkspaceRoot(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	outsideRoot := filepath.Join(base, "outside", ".research")
	if err := os.MkdirAll(outsideRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll outside root: %v", err)
	}

	db := openResearchTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	store, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("create project store: %v", err)
	}
	if err := store.SaveProject(context.Background(), project.ProjectInfo{
		ID:            "proj-out",
		Name:          "Out",
		WorkspacePath: ws,
		ResearchRoot:  outsideRoot,
	}); err != nil {
		t.Fatalf("save project: %v", err)
	}
	mgr := project.NewManager(store, base, nil)
	f := &FrontendAPI{
		projectManager: mgr,
		config:         &config.Config{Experimental: config.ExperimentalConfig{Enabled: true}},
		emitEvent:      func(_ string, _ ...any) {},
	}

	if _, err := f.UpdateHypothesis("proj-out", "R-001", "H-001", HypothesisUpdateFields{}); err == nil {
		t.Fatal("expected error for out-of-workspace research root")
	}
	if _, err := f.CreateHypothesis("proj-out", NewHypothesisCard{Title: "X"}); err == nil {
		t.Fatal("expected error for out-of-workspace research root")
	}
}
