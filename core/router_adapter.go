package core

import (
	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/agent/router"
)

// newCoreRouter creates an sdk/agent/router.Router wired with the c0wrk
// routing system prompt. historyWindow controls how many recent messages
// are included in the routing context (default 10 when <= 0).
func newCoreRouter(caller agent.LLMCaller, historyWindow int) *router.Router {
	return router.NewRouter(caller, router.Config{
		SystemPrompt:  prompts.RouterSystem,
		HistoryWindow: historyWindow,
	})
}
