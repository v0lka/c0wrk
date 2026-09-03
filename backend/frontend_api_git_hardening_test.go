package backend

import (
	"os"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// The hardening test pins the exact argv and environment the git panel's
// runGitCmd layer executes through the shared fake-git harness in
// internal/gittest (see that package for the helper-process pattern).

func TestMain(m *testing.M) {
	gittest.MaybeRecordFakeGit()
	os.Exit(m.Run())
}

// TestRunGitCmdCarriesHardening pins the hardened invocation of the git
// panel's runGitCmd layer: [git, -c overrides…, log --oneline -5] with a
// valid safe hooks dir and GIT_EDITOR=true in a preserved parent
// environment.
func TestRunGitCmdCarriesHardening(t *testing.T) {
	t.Setenv(gittest.SentinelEnvName, "1")
	readLog := gittest.InstallFakeGit(t)

	f := &FrontendAPI{}
	dir := t.TempDir()
	out, err := f.runGitCmd(dir, "log", "--oneline", "-5")
	if err != nil {
		t.Fatalf("runGitCmd against fake git: %v", err)
	}
	if out != "" {
		t.Errorf("runGitCmd output = %q, want empty (fake git prints nothing)", out)
	}
	gittest.AssertHardenedInvocation(t, readLog(), "log", "--oneline", "-5")
}
