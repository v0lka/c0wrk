package backend

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/session"
)

// TestListDirectory_SharesManagerIgnoreCache proves the file-tree RPC consumes
// the session Manager's per-root resolver cache rather than rebuilding per
// call: a resolver cached BEFORE an .aiignore deletion (no watcher event in
// this test) still flags the file — a fresh per-call build would not.
func TestListDirectory_SharesManagerIgnoreCache(t *testing.T) {
	agentDir := t.TempDir()
	ws := filepath.Join(agentDir, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	aiignore := filepath.Join(ws, ".aiignore")
	if err := os.WriteFile(aiignore, []byte("secret.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"secret.txt", "normal.txt"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mgr := session.NewManager(nil, func(session.Event) {}, agentDir)
	f := &FrontendAPI{agentDir: agentDir, app: &Application{manager: mgr}}
	f.seedPublished.Store(true)
	f.activeProjectMu.Lock()
	f.activeProjectID = "test-project"
	f.activeProjectPath = ws
	f.activeProjectMu.Unlock()

	// Warm the shared cache exactly as the agent path / prewarm would.
	if _, err := mgr.IgnoreResolverFor(context.Background(), ws); err != nil {
		t.Fatalf("warm IgnoreResolverFor: %v", err)
	}

	// The rule file disappears with no watcher event: only a cached resolver
	// still flags the file.
	if err := os.Remove(aiignore); err != nil {
		t.Fatal(err)
	}

	nodes, err := f.ListDirectory(ws, false)
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}
	flagged := map[string]bool{}
	for _, n := range nodes {
		flagged[n.Name] = n.GitIgnored
	}
	if !flagged["secret.txt"] {
		t.Error("secret.txt must stay flagged — the cached resolver, not a fresh walk, served the listing")
	}
	if flagged["normal.txt"] {
		t.Error("normal.txt must not be flagged")
	}
}
