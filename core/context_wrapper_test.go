package core

import (
	"context"
	"testing"

	"github.com/v0lka/sp4rk/agent/router"
)

// TestWithDomain_RoundTrip verifies the domain context helper round-trips.
func TestWithDomain_RoundTrip(t *testing.T) {
	if got := DomainFromContext(context.Background()); got != "" {
		t.Errorf("empty ctx: DomainFromContext = %q, want empty", got)
	}

	cases := []string{router.DomainCode, router.DomainResearch, router.DomainGeneral, router.DomainMixed, "custom"}
	for _, want := range cases {
		ctx := WithDomain(context.Background(), want)
		if got := DomainFromContext(ctx); got != want {
			t.Errorf("WithDomain(%q): DomainFromContext = %q, want %q", want, got, want)
		}
	}
}

// TestWithDomain_Immutable confirms parent ctx is not affected by child overlay.
func TestWithDomain_Immutable(t *testing.T) {
	parent := WithDomain(context.Background(), router.DomainCode)
	child := WithDomain(parent, router.DomainResearch)

	if got := DomainFromContext(parent); got != router.DomainCode {
		t.Errorf("parent mutated: got %q, want %q", got, router.DomainCode)
	}
	if got := DomainFromContext(child); got != router.DomainResearch {
		t.Errorf("child override failed: got %q, want %q", got, router.DomainResearch)
	}
}

// TestWithComplexity_RoundTrip covers the complexity helper.
func TestWithComplexity_RoundTrip(t *testing.T) {
	if got := ComplexityFromContext(context.Background()); got != 0 {
		t.Errorf("empty ctx: got %d, want 0", got)
	}

	for _, want := range []int{1, 2, 3, 4, 5} {
		ctx := WithComplexity(context.Background(), want)
		if got := ComplexityFromContext(ctx); got != want {
			t.Errorf("WithComplexity(%d): got %d, want %d", want, got, want)
		}
	}
}

// TestWithUserSkills_RoundTrip covers user-activated skill list helper.
func TestWithUserSkills_RoundTrip(t *testing.T) {
	if got := UserSkillsFromContext(context.Background()); got != nil {
		t.Errorf("empty ctx: got %v, want nil", got)
	}

	skills := []string{"go-testing", "vibespec-check"}
	ctx := WithUserSkills(context.Background(), skills)
	got := UserSkillsFromContext(ctx)
	if len(got) != len(skills) {
		t.Fatalf("got %d skills, want %d", len(got), len(skills))
	}
	for i, s := range skills {
		if got[i] != s {
			t.Errorf("got[%d] = %q, want %q", i, got[i], s)
		}
	}
}

// TestWithVectorSearchHints_RoundTrip covers the vector hints carrier.
func TestWithVectorSearchHints_RoundTrip(t *testing.T) {
	if got := VectorSearchHintsFromContext(context.Background()); got != nil {
		t.Errorf("empty ctx: got %v, want nil", got)
	}

	hints := &VectorSearchHints{
		Files: []VectorSearchHint{{FilePath: "main.go", Summary: "entry"}},
	}
	ctx := WithVectorSearchHints(context.Background(), hints)
	got := VectorSearchHintsFromContext(ctx)
	if got == nil {
		t.Fatal("got nil, want hints")
	}
	if len(got.Files) != 1 || got.Files[0].FilePath != "main.go" {
		t.Errorf("got %+v, want files[0].FilePath = main.go", got.Files)
	}
}

// TestWithAgentsMD_RoundTrip covers the AGENTS.md context wrapper.
func TestWithAgentsMD_RoundTrip(t *testing.T) {
	if got := AgentsMDFromContext(context.Background()); got != nil {
		t.Errorf("empty ctx: got %v, want nil", got)
	}

	amd := &AgentsMD{Content: "# Project Rules\nUse Go 1.26."}
	ctx := WithAgentsMD(context.Background(), amd)
	got := AgentsMDFromContext(ctx)
	if got == nil {
		t.Fatal("got nil, want AgentsMD")
	}
	if got.Content != amd.Content {
		t.Errorf("got Content = %q, want %q", got.Content, amd.Content)
	}
}

// TestWithActiveSkills_RoundTrip covers the active skills carrier.
func TestWithActiveSkills_RoundTrip(t *testing.T) {
	if got := ActiveSkillsFromContext(context.Background()); got != nil {
		t.Errorf("empty ctx: got %v, want nil", got)
	}

	as := &ActiveSkills{}
	ctx := WithActiveSkills(context.Background(), as)
	got := ActiveSkillsFromContext(ctx)
	if got == nil {
		t.Fatal("got nil, want ActiveSkills")
	}
	if got != as {
		t.Error("ActiveSkills retrieved by reference does not match what was stored")
	}
}
