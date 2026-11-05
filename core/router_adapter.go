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
// routing system prompt; historyWindow recent messages join the routing
// context (default 10 when <= 0). Semantic tool matching stays off — the
// placeholders resolve empty and the essential-tools narrowing is static:
// specs/domains/orchestration/router.md.
func newCoreRouter(caller agent.LLMCaller, historyWindow int) *router.Router {
	return router.New(caller, router.Config{
		SystemPrompt:          prompts.RouterSystem,
		HistoryWindow:         historyWindow,
		AppendContextSections: formatRouterContextSections,
	})
}

// formatRouterContextSections returns the router's extra prompt sections:
// AGENTS.md content plus, in RESEARCH mode, the research-awareness block and
// the advisory skill-matching hints (specs/contracts/core-sp4rk.md).
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

// formatResearchRouterHints emits the advisory intent→research-*-skill mapping,
// appended to the router context only in RESEARCH mode; it nudges matching and
// explicit /-invocations always take precedence — specs/domains/research.md.
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
