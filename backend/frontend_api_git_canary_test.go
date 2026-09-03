package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// TestRunGitCmdNeutralizesRepoConfig proves the git panel's runGitCmd layer
// routes through the per-repo neutralization: a repository armed with a
// canary textconv (config planted after setup, mid-session) must not execute
// the canary while runGitCmd still returns usable diff output.
func TestRunGitCmdNeutralizesRepoConfig(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	canary := gittest.NewCanary(t)
	canary.RequireArmed(t)

	script := canary.Plant(t, "textconv", gittest.TextconvBody)
	repo.AppendConfig(t, "[diff \"canary\"]\n\ttextconv = "+script)
	repo.Write(t, ".gitattributes", "*.txt diff=canary\n")
	repo.Write(t, "file.txt", "hello\nworld\n")

	f := &FrontendAPI{} // ctx() falls back to context.Background()

	out, err := f.runGitCmd(root, "diff", "--numstat", "HEAD")
	if err != nil {
		t.Fatalf("runGitCmd on textconv-armed repo: %v", err)
	}
	if !strings.Contains(out, "file.txt") {
		t.Fatalf("runGitCmd output missing changed file:\n%s", out)
	}

	combined, err := f.runGitCmdCombined(root, 10*time.Second, "status", "--porcelain")
	if err != nil {
		t.Fatalf("runGitCmdCombined on textconv-armed repo: %v", err)
	}
	if !strings.Contains(combined, "file.txt") {
		t.Fatalf("runGitCmdCombined output missing modified file:\n%s", combined)
	}
	canary.RequireNotFired(t)
}

// TestRunGitCmdFailsClosedOnUnscannableConfig pins the fail-closed behavior
// of the backend path: an unreadable .git/config must produce an error,
// never an un-neutralized git invocation.
func TestRunGitCmdFailsClosedOnUnscannableConfig(t *testing.T) {
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

	f := &FrontendAPI{}
	if _, err := f.runGitCmd(root, "status", "--porcelain"); err == nil {
		t.Fatal("expected runGitCmd to fail closed on unscannable config")
	}
}
