//go:build fluent

// Example 02 — Custom Tools (Fluent API)
//
// The same custom-tool agent as main.go, expressed through the fluent façade.
// Built-in file tools come from a bundle ([sp4rk.FileTools]); the custom
// CalculatorTool (shared via calculator_tool.go) is registered alongside.
//
// Run with: go run -tags fluent .
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/tools"
)

func run() error {
	// Set up a throwaway workspace so file tools know where to read/write.
	workspaceDir, err := os.MkdirTemp("", "sp4rk-example-02-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()
	fmt.Println("Workspace:", workspaceDir)

	// sp4rk.NewF: provider + bundled file tools + our custom calculator tool.
	// The finish tool is auto-registered. AutoApprove satisfies the
	// fail-closed registry for write_file (sandboxed throwaway workspace).
	fw, err := sp4rk.NewF().
		Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
		FileTools().
		Tools(NewCalculatorTool()).
		AutoApprove().
		Build()
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	task := fmt.Sprintf(
		"Use the calculator tool to compute 17 * 23 + 100, "+
			"then write the result to a file called 'result.txt' in the workspace (%s). "+
			"Finally, read the file back to verify its contents.",
		workspaceDir,
	)

	// Run has no .Workspace() helper; inject the workspace path into the
	// context instead (built-in file tools read it via tools.WorkspacePathFrom).
	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	result, err := fw.RunF(ctx).
		System(fmt.Sprintf(`You are a coding assistant working in the directory %s.
You have a calculator tool for arithmetic and file tools for reading/writing files.
When you have completed the task, call the finish tool with a summary.`, workspaceDir)).
		Ask(task)
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	fmt.Println("\nStatus:", result.Status)
	fmt.Println("Output:", result.Output)

	// Verify the file was created.
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
