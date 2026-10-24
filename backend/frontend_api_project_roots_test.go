package backend

import (
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
)

// TestTrackedSessionRoots pins the root derivation a project switch tracks
// for the watcher callbacks. The guard is the point: an empty effective
// research root (the No Project pseudo-project, a workspace-less project)
// must leave the papers/comparisons roots EMPTY — the watcher emit gates
// treat "" as "root not tracked", and the RELATIVE "papers"/"comparisons"
// an unguarded filepath.Join("", ...) silently produces would break that
// cross-goroutine contract (a relative root can never contain an absolute
// changed path, so events would be dropped by containment luck instead of
// by design).
func TestTrackedSessionRoots(t *testing.T) {
	noProject := &project.ProjectInfo{ID: project.NoProjectID, IsNoProject: true}
	workspaceless := &project.ProjectInfo{ID: "p1", WorkspacePath: ""}
	checkout := &project.ProjectInfo{ID: "p1", WorkspacePath: "/ws"}

	t.Run("no project tracks nothing", func(t *testing.T) {
		research, papers, comparisons := trackedSessionRoots(noProject, "/np/s1/workspace", true)
		if research != "" || papers != "" || comparisons != "" {
			t.Fatalf("No Project roots = (%q, %q, %q), want all empty", research, papers, comparisons)
		}
	})

	t.Run("workspace-less project tracks nothing", func(t *testing.T) {
		research, papers, comparisons := trackedSessionRoots(workspaceless, "/any/s1/workspace", true)
		if research != "" || papers != "" || comparisons != "" {
			t.Fatalf("workspace-less roots = (%q, %q, %q), want all empty", research, papers, comparisons)
		}
	})

	t.Run("no session falls back to the checkout", func(t *testing.T) {
		wantResearch := config.ProjectResearchPath("/ws")
		research, papers, comparisons := trackedSessionRoots(checkout, "", false)
		if research != wantResearch {
			t.Fatalf("research root = %q, want the checkout's %q", research, wantResearch)
		}
		if papers != config.PaperLibraryPathIn(wantResearch) {
			t.Fatalf("papers root = %q, want %q", papers, config.PaperLibraryPathIn(wantResearch))
		}
		if comparisons != config.ComparisonsPathIn(wantResearch) {
			t.Fatalf("comparisons root = %q, want %q", comparisons, config.ComparisonsPathIn(wantResearch))
		}
	})

	t.Run("session workspace wins over the checkout", func(t *testing.T) {
		wantResearch := config.ProjectResearchPath("/trees/wt-1")
		research, papers, comparisons := trackedSessionRoots(checkout, "/trees/wt-1", true)
		if research != wantResearch {
			t.Fatalf("research root = %q, want the session tree's %q", research, wantResearch)
		}
		if papers != config.PaperLibraryPathIn(wantResearch) {
			t.Fatalf("papers root = %q, want %q", papers, config.PaperLibraryPathIn(wantResearch))
		}
		if comparisons != config.ComparisonsPathIn(wantResearch) {
			t.Fatalf("comparisons root = %q, want %q", comparisons, config.ComparisonsPathIn(wantResearch))
		}
	})
}
