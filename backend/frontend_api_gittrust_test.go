package backend

import (
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/gittrust"
)

// Spawn-layer trust-registry wiring tests ─────────────────────────────────
//
// Backend is the sole writer of the process-wide core/gittrust registry that
// core/workspace consults to decide whether a repository may spawn raw git.
// TrustGitRepo / RemoveTrustedGitRepo mirror security.trusted_git_repos into
// the registry; NewFrontendAPI seeds it from the loaded config at startup.
// The registry is process-wide, so each test clears it up front and on
// teardown.

func TestTrustGitRepo_SyncsGitTrustRegistry(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	f, _, _ := newTestAPI(t)
	dir := t.TempDir()

	if gittrust.IsTrusted(dir) {
		t.Fatal("fresh registry must not trust a never-trusted root")
	}

	if err := f.TrustGitRepo(dir); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}
	if !gittrust.IsTrusted(dir) {
		t.Fatal("TrustGitRepo did not register the root in the git trust registry")
	}

	if err := f.RemoveTrustedGitRepo(dir); err != nil {
		t.Fatalf("RemoveTrustedGitRepo: %v", err)
	}
	if gittrust.IsTrusted(dir) {
		t.Fatal("RemoveTrustedGitRepo did not unregister the root from the git trust registry")
	}
}

func TestNewFrontendAPI_SeedsGitTrustRegistry(t *testing.T) {
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)

	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Security.TrustedGitRepos = []config.TrustedGitRepo{{Path: dir}}

	NewFrontendAPI(FrontendAPIConfig{Config: cfg})
	if !gittrust.IsTrusted(dir) {
		t.Fatal("NewFrontendAPI did not seed the git trust registry from the loaded config")
	}

	// A nil config leaves the registry empty (fail-closed).
	gittrust.Clear()
	NewFrontendAPI(FrontendAPIConfig{Config: nil})
	if gittrust.IsTrusted(dir) {
		t.Fatal("NewFrontendAPI with nil config must leave the registry empty")
	}
}
