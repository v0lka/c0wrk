//go:build !fluent

// Example 03 — Event Streaming (Classic API)
//
// Demonstrates how to observe the agent's execution in real time by
// implementing the agent.Events interface. A PrintingEvents sink
// formats each lifecycle event (thoughts, tool calls, results, context
// fill) and writes it to stdout, giving full visibility into the ReAct loop.
//
// PrintingEvents lives in events.go (shared with the fluent variant).
// For the concise recommended path, see main_fluent.go (run with `-tags fluent`).
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

func run() error {
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

	// Register tools
	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewGlobTool())
	registry.Register(agent.NewFinishTool())

	// Use the current directory as the workspace
	workspaceDir, _ := os.Getwd()
	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return "You are a code exploration assistant. " +
			"Use the available tools to investigate the codebase. " +
			"Call finish when you have your answer."
	}

	task := "List the Go files in the current directory using the glob tool, " +
		"then read the first one you find and summarize what it does in one sentence."

	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  Task:", task)
	fmt.Println("═══════════════════════════════════════════════════════════")

	// Pass our custom events implementation instead of NoopEvents.
	events := &PrintingEvents{}

	result, err := fw.Execute(ctx, systemPrompt, events, task)
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	fmt.Println("\n═══════════════════════════════════════════════════════════")
	fmt.Println("Final Status:", result.Status)
	fmt.Println("Final Output:", result.Output)
	fmt.Println("═══════════════════════════════════════════════════════════")
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
