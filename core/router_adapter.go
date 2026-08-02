package core

import (
	"context"

	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
)

// newCoreRouter creates a github.com/v0lka/sp4rk/agent/router.Router wired with the c0wrk
// routing system prompt. historyWindow controls how many recent messages
// are included in the routing context (default 10 when <= 0).
//
// The router system prompt uses conditional placeholders (TOOL-MATCHING and
// JSON-OUTPUT-SCHEMA) that the SDK resolves based on the router's tool-matching
// flag. Semantic tool selection is enabled externally via SetToolMatching from
// buildCoreAgents, gated on the SmallLLM master toggle and EssentialTools
// variant; when disabled both placeholders resolve to empty/default content and
// behavior is unchanged.
func newCoreRouter(caller agent.LLMCaller, historyWindow int) *router.Router {
	return router.New(caller, router.Config{
		SystemPrompt:          prompts.RouterSystem,
		HistoryWindow:         historyWindow,
		AppendContextSections: formatRouterContextSections,
	})
}

// formatRouterContextSections returns additional prompt sections for the router
// derived from the request context. Currently injects AGENTS.md content so the
// router can consider project conventions, tech stack, and tooling when
// matching skills and classifying the request.
func formatRouterContextSections(ctx context.Context) string {
	return formatAgentsMD(ctx)
}
