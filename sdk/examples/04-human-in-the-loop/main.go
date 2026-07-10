//go:build !fluent

// Example 04 — Human-in-the-Loop (Classic API)
//
// Demonstrates how to intercept tool calls for user confirmation before
// destructive operations execute. A custom HITLHandler (ConfirmingHITL,
// in hitl.go) prompts on stdin whenever the agent tries to use a
// "dangerous" tool and decides whether to allow, deny, or modify the call.
//
// This example is interactive — it reads y/n from stdin.
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
	// Create the Framework with our custom HITL handler.
	// The handler is passed via Config.HITL.
	fw, err := sp4rk.New(sp4rk.Config{
		LLM: sp4rk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
		Execution: sp4rk.ExecutionConfig{
			MaxSteps: 10, // low limit to demonstrate OnStepLimit
		},
		// HITL is the human-in-the-loop handler. Nil means defaults
		// (allow all tool calls, deny step extensions).
		HITL: NewConfirmingHITL([]string{
			"write_file",
			"delete_file",
			"create_directory",
			"bash_exec",
		}),
	})
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	// Register tools — including dangerous ones that will trigger confirmation.
	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewDeleteFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewCreateDirectoryTool())
	registry.Register(agent.NewFinishTool())

	// In this example, confirmation is handled by the HITL handler at the
	// executor level (OnToolCall above). The registry itself is FAIL-CLOSED
	// for PolicyUserConfirm tools, so we explicitly relax the registry-level
	// policy for the tools our HITL handler already gates — otherwise the
	// user would be asked twice (or the registry would deny the call).
	for _, name := range []string{"write_file", "delete_file", "create_directory"} {
		registry.SetPolicyOverride(name, tools.PolicyAlwaysAllow)
	}

	// Set up workspace
	workspaceDir, err := os.MkdirTemp("", "sp4rk-example-04-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	fmt.Printf("Workspace: %s\n", workspaceDir)
	fmt.Println("This example is INTERACTIVE — you will be asked to approve tool calls.")
	fmt.Println("Press y + Enter to allow, or just Enter to deny.")
	fmt.Println()

	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return fmt.Sprintf(`You are a file management assistant working in %s.
Create a file called "notes.txt" with some content, then delete it.
Use the available file tools. Call finish when done.`, workspaceDir)
	}

	task := "Create a file called notes.txt with the text 'Hello HITL!', then delete it."

	result, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{}, task)
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	fmt.Println("\n═══════════════════════════════════════════")
	fmt.Println("Status:", result.Status)
	fmt.Println("Output:", result.Output)
	fmt.Println("═══════════════════════════════════════════")
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
