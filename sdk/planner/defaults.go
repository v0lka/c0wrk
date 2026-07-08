package planner

import (
	"context"

	"github.com/v0lka/sp4rk/skills"
)

// DefaultConfig returns a Config with sensible defaults for standalone use,
// suitable for production framework use.
func DefaultConfig() Config {
	return Config{
		DomainFromContext:     func(context.Context) string { return "" },
		ComplexityFromContext: func(context.Context) int { return 0 },
		UserSkillsFromContext: func(context.Context) []string { return nil },
		FormatSkillList:       func(context.Context, []skills.SkillDescriptor) string { return "None" },
		FormatWorkspacePath:   func(context.Context) string { return "" },
		AppendContextSections: func(_ context.Context, base string) string { return base },
		MaxExploreSteps:       7,
	}
}
