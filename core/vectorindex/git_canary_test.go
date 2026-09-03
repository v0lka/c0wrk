package vectorindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// TestRunGitNeutralizesRepoConfig proves the vectorindex git choke point
// routes through the per-repo neutralization: a repository armed with a
// canary filter (config planted after setup) must not execute the canary
// while runGit and CurrentBranch still return usable results.
func TestRunGitNeutralizesRepoConfig(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	canary := gittest.NewCanary(t)
	canary.RequireArmed(t)

	script := canary.Plant(t, "filter", gittest.FilterBody)
	repo.AppendConfig(t, "[filter \"canary\"]\n"+
		"\tprocess = "+script+"\n"+
		"\tclean = "+script+"\n"+
		"\tsmudge = "+script+"\n")
	repo.Write(t, ".gitattributes", "*.txt filter=canary\n")
	repo.Write(t, "file.txt", "hello\nworld\n")

	ctx := context.Background()
	// Armed diff: the clean filter would run on the modified work-tree file
	// without per-repo neutralization.
	out, err := runGit(ctx, root, "diff", "--", "file.txt")
	if err != nil {
		t.Fatalf("runGit diff on filter-armed repo: %v", err)
	}
	if !strings.Contains(out, "+world") {
		t.Fatalf("runGit diff missing expected change:\n%s", out)
	}

	branch, err := CurrentBranch(ctx, root)
	if err != nil {
		t.Fatalf("CurrentBranch on filter-armed repo: %v", err)
	}
	if branch != "main" {
		t.Fatalf("unexpected branch: %s", branch)
	}
	canary.RequireNotFired(t)
}

// TestRunGitFailsClosedOnUnscannableConfig pins the fail-closed behavior of
// the vectorindex path: an unreadable .git/config must produce an error,
// never an un-neutralized git invocation.
func TestRunGitFailsClosedOnUnscannableConfig(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")

	configPath := repo.GitDirFile("config")
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("removing config: %v", err)
	}
	if err := os.Mkdir(configPath, 0o755); err != nil {
		t.Fatalf("replacing config with directory: %v", err)
	}

	if _, err := runGit(context.Background(), root, "status", "--porcelain"); err == nil {
		t.Fatal("expected runGit to fail closed on unscannable config")
	}
}
