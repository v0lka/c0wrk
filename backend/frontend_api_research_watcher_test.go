package backend

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/workspace"
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
