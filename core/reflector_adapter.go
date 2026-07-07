package core

import (
	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
)

// newCoreReflector creates an sdk/agent/reflector.Reflector wired with the
// c0wrk reflection system prompt.
func newCoreReflector(caller agent.LLMCaller) *reflector.Reflector {
	return reflector.New(caller, reflector.Config{
		SystemPrompt: prompts.ReflectorSystem,
	})
}
