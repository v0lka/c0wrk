package tools

import (
	"context"
	"testing"
)

func TestWithNoProject(t *testing.T) {
	ctx := context.Background()

	if IsNoProject(ctx) {
		t.Error("bare context: expected IsNoProject=false")
	}

	ctx = WithNoProject(ctx)
	if !IsNoProject(ctx) {
		t.Error("after WithNoProject: expected IsNoProject=true")
	}
}

// otherKey is a named context key type used in TestIsNoProject_UnrelatedContext
// to verify that unrelated context values don't interfere with IsNoProject.
type otherKey struct{}

func TestIsNoProject_UnrelatedContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), otherKey{}, "something")
	if IsNoProject(ctx) {
		t.Error("unrelated context value: expected IsNoProject=false")
	}
}

func TestWithNoProject_Idempotent(t *testing.T) {
	ctx := context.Background()
	ctx = WithNoProject(ctx)
	ctx = WithNoProject(ctx) // double-wrap
	if !IsNoProject(ctx) {
		t.Error("double-wrapped: expected IsNoProject=true")
	}
}

func TestWithResearch(t *testing.T) {
	ctx := context.Background()

	if IsResearch(ctx) {
		t.Error("bare context: expected IsResearch=false")
	}

	ctx = WithResearch(ctx)
	if !IsResearch(ctx) {
		t.Error("after WithResearch: expected IsResearch=true")
	}
}

func TestIsResearch_UnrelatedContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), otherKey{}, "something")
	if IsResearch(ctx) {
		t.Error("unrelated context value: expected IsResearch=false")
	}
}

func TestWithResearch_Idempotent(t *testing.T) {
	ctx := context.Background()
	ctx = WithResearch(ctx)
	ctx = WithResearch(ctx) // double-wrap
	if !IsResearch(ctx) {
		t.Error("double-wrapped: expected IsResearch=true")
	}
}

// TestWithResearch_DistinctFromNoProject verifies the two mode flags are
// independent: setting one does not set the other.
func TestWithResearch_DistinctFromNoProject(t *testing.T) {
	ctx := context.Background()
	ctx = WithResearch(ctx)
	if IsNoProject(ctx) {
		t.Error("WithResearch must not imply IsNoProject")
	}

	ctx2 := context.Background()
	ctx2 = WithNoProject(ctx2)
	if IsResearch(ctx2) {
		t.Error("WithNoProject must not imply IsResearch")
	}
}

func TestWithResearchRoot(t *testing.T) {
	ctx := context.Background()

	if got := ResearchRootPathFrom(ctx); got != "" {
		t.Errorf("bare context: expected empty research root, got %q", got)
	}

	ctx = WithResearchRoot(ctx, "/ws/.research")
	if got := ResearchRootPathFrom(ctx); got != "/ws/.research" {
		t.Errorf("after WithResearchRoot: got %q, want %q", got, "/ws/.research")
	}
}

// TestWithResearchRoot_EmptyIsNoOp verifies that setting an empty path is a
// no-op (no value attached), mirroring the false-by-default semantics.
func TestWithResearchRoot_EmptyIsNoOp(t *testing.T) {
	ctx := context.Background()
	ctx = WithResearchRoot(ctx, "")
	if got := ResearchRootPathFrom(ctx); got != "" {
		t.Errorf("empty WithResearchRoot must be a no-op, got %q", got)
	}
}

func TestWithResearchRoot_UnrelatedContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), otherKey{}, "something")
	if got := ResearchRootPathFrom(ctx); got != "" {
		t.Errorf("unrelated context value: expected empty research root, got %q", got)
	}
}

// TestWithResearchRoot_Overwrites verifies that a later value replaces an
// earlier one (not concatenated).
func TestWithResearchRoot_Overwrites(t *testing.T) {
	ctx := context.Background()
	ctx = WithResearchRoot(ctx, "/a/.research")
	ctx = WithResearchRoot(ctx, "/b/.research")
	if got := ResearchRootPathFrom(ctx); got != "/b/.research" {
		t.Errorf("expected overwrite to /b/.research, got %q", got)
	}
}
