//go:build !fluent

// Example 02 — Custom Tools (Classic API)
//
// Demonstrates how to implement a custom tool and register built-in tools
// alongside it, using the classic sp4rk.Config API. For the concise recommended
// path, see main_fluent.go (run with `-tags fluent`).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// (CalculatorTool lives in calculator_tool.go — shared with the fluent variant.)

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
		// The tool registry is FAIL-CLOSED: tools with PolicyUserConfirm
		// (like write_file below) are denied unless a ConfirmFunc is set.
		// This example runs in a throwaway temp workspace, so we auto-approve.
		// In an interactive app, prompt the user here instead.
		ConfirmFunc: func(_ context.Context, req tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
			fmt.Printf("[auto-approving %s]\n", req.ToolName)
			return tools.ConfirmAllowOnce, nil
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	// Register tools — the agent can only use tools that are in the registry.
	registry := fw.ToolRegistry()

	// Built-in tools from github.com/v0lka/sp4rk/tools/builtins
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewListDirectoryTool())

	// The finish tool (required for task completion)
	registry.Register(agent.NewFinishTool())

	// Our custom calculator tool
	registry.Register(NewCalculatorTool())

	// Set up a workspace directory so file tools know where to read/write.
	workspaceDir, err := os.MkdirTemp("", "sp4rk-example-02-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	fmt.Println("Workspace:", workspaceDir)

	// Inject the workspace path into the context. Built-in file tools
	// retrieve it via tools.WorkspacePathFrom(ctx).
	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return fmt.Sprintf(`You are a coding assistant working in the directory %s.
You have a calculator tool for arithmetic and file tools for reading/writing files.
When you have completed the task, call the finish tool with a summary.`, workspaceDir)
	}

	task := fmt.Sprintf(
		"Use the calculator tool to compute 17 * 23 + 100, "+
			"then write the result to a file called 'result.txt' in the workspace (%s). "+
			"Finally, read the file back to verify its contents.",
		workspaceDir,
	)

	result, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{}, task)
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	fmt.Println("\nStatus:", result.Status)
	fmt.Println("Output:", result.Output)

	// Verify the file was created
	resultPath := filepath.Join(workspaceDir, "result.txt")
	if content, err := os.ReadFile(resultPath); err == nil {
		fmt.Printf("\nFile %s contains:\n%s\n", resultPath, string(content))
	} else {
		fmt.Printf("\nFile %s was not created: %v\n", resultPath, err)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
