package core

import (
	"github.com/v0lka/c0wrk/core/prompts"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/reflector"
)

// newCoreReflector wires sp4rk's Reflector with the c0wrk reflection system
// prompt (specs/contracts/core-sp4rk.md).
func newCoreReflector(caller agent.LLMCaller) *reflector.Reflector {
	return reflector.New(caller, reflector.Config{
		SystemPrompt: prompts.ReflectorSystem,
	})
}
