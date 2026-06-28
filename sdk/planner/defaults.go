package planner

import (
	"context"

	"github.com/v0lka/c0wrk/sdk/skills"
)

// DefaultPlannerConfig returns a Config with sensible defaults for standalone use.
// Unlike DefaultConfig() in config.go (which provides testing stubs), this returns
// meaningful defaults suitable for production framework use.
func DefaultPlannerConfig() Config {
	return Config{
		DomainFromContext:      func(context.Context) string { return "" },
		ComplexityFromContext:  func(context.Context) int { return 0 },
		UserSkillsFromContext:  func(context.Context) []string { return nil },
		FormatSkillList:        func(context.Context, []skills.SkillDescriptor) string { return "None" },
		FormatWorkspacePath:    func(context.Context) string { return "" },
		AppendContextSections:  func(_ context.Context, base string) string { return base },
		MaxExploreSteps:        7,
	}
}
