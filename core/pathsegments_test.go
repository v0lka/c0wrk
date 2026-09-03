package core

import (
	"testing"

	"github.com/v0lka/c0wrk/internal/sysproc"
)

// TestGitSafeHooksRelativePathMirrorsSysproc pins the canonical segment
// constant to the duplicate internal/sysproc carries. sysproc cannot import
// this package to read the constant directly (core imports core/markitdown,
// which imports internal/sysproc — an import would cycle), so the duplicate
// is pinned equal here to keep sysproc.GitCmd's "-c core.hooksPath=<dir>"
// argument pointing at <agentDir>/git/safe-hooks.
func TestGitSafeHooksRelativePathMirrorsSysproc(t *testing.T) {
	if sysproc.GitSafeHooksSegment != GitSafeHooksRelativePath {
		t.Errorf("sysproc.GitSafeHooksSegment = %q, want GitSafeHooksRelativePath %q",
			sysproc.GitSafeHooksSegment, GitSafeHooksRelativePath)
	}
}
