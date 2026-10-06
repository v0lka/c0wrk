package core

import (
	"testing"

	coretools "github.com/v0lka/c0wrk/core/tools"
)

// TestManagedWorktreesSegmentMatchesCore pins the core/tools duplicate of
// the managed-worktree container segment against the single source of truth
// here: core/tools cannot import this package (the root core package imports
// core/tools; an import back would cycle), so its registry-level traversal
// gate keeps a local literal that MUST stay identical to
// core.WorktreesRelativePath. Mirrors the GitSafeHooksRelativePath pin kept
// for internal/sysproc.
func TestManagedWorktreesSegmentMatchesCore(t *testing.T) {
	if coretools.ManagedWorktreesSegment != WorktreesRelativePath {
		t.Fatalf("core/tools.ManagedWorktreesSegment = %q, want core.WorktreesRelativePath = %q",
			coretools.ManagedWorktreesSegment, WorktreesRelativePath)
	}
}
