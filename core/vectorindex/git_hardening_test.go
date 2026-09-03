package vectorindex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// The hardening test pins the exact argv and environment the vector index's
// git layer (runGit via CurrentBranch) executes through the shared fake-git
// harness in internal/gittest (see that package for the helper-process
// pattern).

func TestMain(m *testing.M) {
	gittest.MaybeRecordFakeGit()
	os.Exit(m.Run())
}

// TestCurrentBranchCarriesHardening pins the hardened invocation of the
// vectorindex git layer: [git, -c overrides…, -C repo, symbolic-ref,
// --short, HEAD] with a valid safe hooks dir and GIT_EDITOR=true in a
// preserved parent environment.
func TestCurrentBranchCarriesHardening(t *testing.T) {
	t.Setenv(gittest.SentinelEnvName, "1")
	readLog := gittest.InstallFakeGit(t)

	repo := t.TempDir()
	// CurrentBranch fast-paths out when .git is absent; create it so the
	// git invocation actually happens.
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	// The fake git exits 0 with no output; CurrentBranch treats that as an
	// empty branch name without error.
	if _, err := CurrentBranch(context.Background(), repo); err != nil {
		t.Fatalf("CurrentBranch against fake git: %v", err)
	}

	gittest.AssertHardenedInvocation(t, readLog(), "-C", repo, "symbolic-ref", "--short", "HEAD")
}
