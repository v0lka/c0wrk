package core

import (
	"context"
	"strings"

	"github.com/v0lka/c0wrk/core/prompts"
	coretools "github.com/v0lka/c0wrk/core/tools"
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
// derived from the request context. It injects AGENTS.md content (so the
// router can consider project conventions, tech stack, and tooling) and, in
// RESEARCH mode, the research-awareness block plus lightweight keyword hints
// that help natural-language messages match the research-* skills without an
// explicit "/" invocation. No core state machine is introduced — the hints are
// advisory; all enforcement stays in the skill bodies.
func formatRouterContextSections(ctx context.Context) string {
	var sections []string
	if s := formatAgentsMD(ctx); s != "" {
		sections = append(sections, s)
	}
	if s := formatResearchContext(ctx); s != "" {
		sections = append(sections, s)
	}
	if s := formatResearchRouterHints(ctx); s != "" {
		sections = append(sections, s)
	}
	return strings.Join(sections, "")
}

// formatResearchRouterHints emits a concise, advisory mapping from common
// natural-language research intents to research-* skills, so the router can
// surface the right skill for messages like "start an experiment" or "add a
// hypothesis" without requiring a "/research-*" prefix. It is appended to the
// router context only in RESEARCH mode. This is intentionally lightweight — it
// nudges matching; it is NOT a state machine and does not override explicit
// "/skill" invocations (those still take precedence).
func formatResearchRouterHints(ctx context.Context) string {
	if !coretools.IsResearch(ctx) {
		return ""
	}
	return "\n\n## Research Skill Matching (RESEARCH mode)\n\n" +
		"When the user message is a natural-language research intent (no explicit /skill prefix), " +
		"prefer the matching research-* skill:\n" +
		"- \"start a research\", \"new investigation\", \"set up research\", \"create a sub-research\" → research-init\n" +
		"- \"hypothesis\", \"formulate\", \"add/update a hypothesis\", \"hypothesis graph\" → research-hypothesis\n" +
		"- \"experiment\", \"run a test\", \"start an experiment\", \"set up environment\", \"record results\", \"timebox\" → research-experiment\n" +
		"- \"prior art\", \"literature\", \"references\", \"related work\", \"CVE\" → research-prior-art\n" +
		"- \"status\", \"progress\", \"where are we\", \"review the research\" → research-status\n" +
		"- \"decision\", \"continue/pivot/kill/fork\", \"what's next\" → research-decision\n" +
		"- \"synthesize\", \"final report\", \"conclude\", \"wrap up the research\" → research-synthesis\n" +
		"Explicit /research-* invocations always take precedence.\n"
}
