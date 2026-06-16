package core

import (
	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/agent/reflector"
)

// newCoreReflector creates an sdk/agent/reflector.Reflector wired with the
// c0wrk reflection system prompt.
func newCoreReflector(caller agent.LLMCaller) *reflector.Reflector {
	return reflector.NewReflector(caller, reflector.Config{
		SystemPrompt: prompts.ReflectorSystem,
	})
}
