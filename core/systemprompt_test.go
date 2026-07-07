package core

import (
	"context"
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/skills"
)

func TestFormatVectorSearchHints_Empty(t *testing.T) {
	if got := formatVectorSearchHints(context.Background(), ""); got != "" {
		t.Errorf("expected empty for missing hints, got %q", got)
	}

	ctx := WithVectorSearchHints(context.Background(), &VectorSearchHints{})
	if got := formatVectorSearchHints(ctx, ""); got != "" {
		t.Errorf("expected empty for hints with no files, got %q", got)
	}
}

func TestFormatVectorSearchHints_WithFiles(t *testing.T) {
	ctx := WithVectorSearchHints(context.Background(), &VectorSearchHints{
		Files: []VectorSearchHint{
			{FilePath: "main.go", Summary: "entry point"},
			{FilePath: "lib/util.go"},
		},
	})

	got := formatVectorSearchHints(ctx, "")
	if !strings.Contains(got, "Relevant Project Files") {
		t.Error("missing section header")
	}
	if !strings.Contains(got, "main.go: entry point") {
		t.Error("missing first file entry")
	}
	if !strings.Contains(got, "lib/util.go") {
		t.Error("missing second file entry")
	}
	if strings.Contains(got, "footer:") {
		t.Error("should not include footer when none provided")
	}
}

func TestFormatVectorSearchHints_Footer(t *testing.T) {
	ctx := WithVectorSearchHints(context.Background(), &VectorSearchHints{
		Files: []VectorSearchHint{{FilePath: "main.go"}},
	})

	got := formatVectorSearchHints(ctx, "\nUse semantic_search for more.")
	if !strings.Contains(got, "semantic_search") {
		t.Error("expected footer text in output")
	}
}

func TestFormatActiveSkills_Empty(t *testing.T) {
	if got := formatActiveSkills(context.Background(), "preamble"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}

	ctx := WithActiveSkills(context.Background(), &ActiveSkills{})
	if got := formatActiveSkills(ctx, "preamble"); got != "" {
		t.Errorf("expected empty for ActiveSkills with no skills, got %q", got)
	}
}

func TestFormatActiveSkills_WithSkills(t *testing.T) {
	ctx := WithActiveSkills(context.Background(), &ActiveSkills{
		Skills: []*skills.Skill{
			{
				Metadata: skills.SkillMetadata{Name: "go-testing", Description: "Idiomatic Go tests."},
				Body:     "Step 1: write a table.\nStep 2: subtests.",
			},
		},
	})

	got := formatActiveSkills(ctx, "Test preamble.")
	if !strings.Contains(got, "Active Skills") {
		t.Error("missing Active Skills heading")
	}
	if !strings.Contains(got, "Test preamble.") {
		t.Error("missing preamble")
	}
	if !strings.Contains(got, "go-testing") {
		t.Error("missing skill name")
	}
	if !strings.Contains(got, "Idiomatic Go tests.") {
		t.Error("missing skill description")
	}
	if !strings.Contains(got, "Step 1") {
		t.Error("missing skill body content")
	}
}

func TestFormatAgentsMD_Untrusted(t *testing.T) {
	if got := formatAgentsMD(context.Background()); got != "" {
		t.Errorf("expected empty for missing AgentsMD, got %q", got)
	}

	if got := formatAgentsMD(WithAgentsMD(context.Background(), &AgentsMD{})); got != "" {
		t.Errorf("expected empty for blank AgentsMD content, got %q", got)
	}

	ctx := WithAgentsMD(context.Background(), &AgentsMD{Content: "Use Go 1.26."})
	got := formatAgentsMD(ctx)
	if !strings.Contains(got, "advisory") {
		t.Error("expected advisory framing in AGENTS.md prompt section")
	}
	if !strings.Contains(got, `<untrusted-content source="AGENTS.md">`) {
		t.Error("expected untrusted-content tag wrapping AGENTS.md content")
	}
	if !strings.Contains(got, "Use Go 1.26.") {
		t.Error("expected verbatim AGENTS.md content")
	}
	// Regression guard: must NOT use the old "MUST strictly follow" wording.
	if strings.Contains(got, "MUST strictly follow") {
		t.Error("AGENTS.md prompt section reverted to authoritative wording")
	}
}
