package workspace

import (
	"context"
	"os"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// The hardening tests pin the exact argv and environment the workspace git
// layer executes through the shared fake-git harness in internal/gittest
// (see that package for the helper-process pattern).

func TestMain(m *testing.M) {
	gittest.MaybeRecordFakeGit()
	os.Exit(m.Run())
}

func TestGitStatusCarriesHardening(t *testing.T) {
	t.Setenv(gittest.SentinelEnvName, "1")
	readLog := gittest.InstallFakeGit(t)

	dir := t.TempDir()
	entries, err := GitStatus(context.Background(), dir)
	if err != nil {
		t.Fatalf("GitStatus against fake git: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("GitStatus entries = %v, want empty (fake git prints nothing)", entries)
	}
	gittest.AssertHardenedInvocation(t, readLog(), "status", "--porcelain", "-uall")
}

func TestIsGitTrackedCarriesHardening(t *testing.T) {
	t.Setenv(gittest.SentinelEnvName, "1")
	readLog := gittest.InstallFakeGit(t)

	dir := t.TempDir()
	if !IsGitTracked(context.Background(), dir, "file.txt") {
		t.Fatal("IsGitTracked = false, want true (fake git exits 0)")
	}
	gittest.AssertHardenedInvocation(t, readLog(), "ls-files", "--error-unmatch", "file.txt")
}
