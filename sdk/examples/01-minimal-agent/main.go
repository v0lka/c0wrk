//go:build !fluent

// Example 01 — Minimal Agent (Classic API)
//
// The smallest possible full agent via the classic sp4rk API: manually assemble a
// [sp4rk.Config], create the Framework, register the finish tool, and call
// Execute. For the concise recommended path, see main_fluent.go (run with
// `-tags fluent`).
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
)

func run() error {
	// 1. Create the Framework with a single Anthropic provider.
	//    The Framework owns shared infrastructure: LLM router, tool registry,
	//    and (optionally) an MCP gateway. At least one provider is required.
	fw, err := sp4rk.New(sp4rk.Config{
		LLM: sp4rk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	// 2. Register the finish tool.
	//    The agent MUST be able to call "finish" to signal task completion.
	//    Without it the ReAct loop runs until the step budget is exhausted
	//    and returns a "partial" status.
	fw.ToolRegistry().Register(agent.NewFinishTool())

	// 3. Define a system prompt factory.
	//    The factory receives the task description and model metadata so it
	//    can adapt the prompt per model. For this minimal example we return
	//    a static string.
	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return "You are a helpful assistant. " +
			"Answer the user's question concisely. " +
			"When you have a final answer, call the finish tool with it."
	}

	// 4. Execute a single user message.
	//    Execute() creates a Conductor, runs one ReAct loop, and returns
	//    the result. For repeated use, call NewConductor() once and reuse.
	result, err := fw.Execute(
		context.Background(),
		systemPrompt,
		&agent.NoopEvents{}, // no event handling — see example 03
		"What is the capital of France?",
	)
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	// 5. Inspect the result.
	fmt.Println("Status:", result.Status)
	fmt.Println("Output:", result.Output)
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
