package planner

import (
	"context"
	"log/slog"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/skills"
)

// Config holds all configuration for the Planner.
// The Config separates stable sp4rk interfaces from application-specific wiring.
type Config struct {
	// Prompts holds all parameterizable prompt templates.
	Prompts PromptSet

	// --- Injected context functions (framework-specific, provided by core) ---

	// DomainFromContext extracts the routing domain from the context.
	DomainFromContext func(ctx context.Context) string
	// ComplexityFromContext extracts the routing complexity from the context.
	ComplexityFromContext func(ctx context.Context) int
	// UserSkillsFromContext extracts explicitly user-activated skill names from context.
	UserSkillsFromContext func(ctx context.Context) []string
	// FormatSkillList formats available skills for prompt injection. Must handle nil/empty.
	FormatSkillList func(ctx context.Context, availableSkills []skills.SkillDescriptor) string
	// FormatWorkspacePath returns the workspace instruction block or "".
	FormatWorkspacePath func(ctx context.Context) string
	// AppendContextSections appends env/vector/AGENTS/skills sections to the base prompt.
	AppendContextSections func(ctx context.Context, base string) string

	// --- Tool configuration ---

	// ToolRegistry provides tools for the exploration executor and listing.
	ToolRegistry ToolRegistry
	// PlannerToolNames is the set of tool names allowed for planner exploration.
	// Empty means no exploration tools available.
	PlannerToolNames map[string]bool

	// --- Model resolution ---

	// ModelRegistry is used to resolve model metadata (family, context window).
	ModelRegistry *llm.ModelRegistry
	// Model is the active LLM model name for family resolution.
	Model string

	// --- Optional dependencies ---

	Logger          *slog.Logger
	Emitter         Events
	TokenCounter    llm.TokenCounter
	ContextFactory  ContextManagerFactory
	CallerForStep   func(cm agent.ContextManager, stepID string) agent.LLMCaller
	MaxExploreSteps int
	ReasoningEffort string
}
