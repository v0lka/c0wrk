// Example 01 — Minimal Agent
//
// The smallest possible full agent: configure one LLM provider, register the
// finish tool, and execute a single user message. No custom tools, no event
// handling, no orchestration — just the bare ReAct loop.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/c0wrk/sdk"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
)

func main() {
	// 1. Create the Framework with a single Anthropic provider.
	//    The Framework owns shared infrastructure: LLM router, tool registry,
	//    and (optionally) an MCP gateway. At least one provider is required.
	fw, err := sdk.New(sdk.Config{
		LLM: sdk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
			DefaultModel: "claude-sonnet-4-5",
		},
	})
	if err != nil {
		log.Fatalf("failed to create framework: %v", err)
	}
	defer fw.Shutdown()

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
		log.Fatalf("execution failed: %v", err)
	}

	// 5. Inspect the result.
	fmt.Println("Status:", result.Status)
	fmt.Println("Output:", result.Output)
}
