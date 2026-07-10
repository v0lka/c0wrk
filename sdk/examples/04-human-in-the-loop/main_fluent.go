//go:build fluent

// Example 04 — Human-in-the-Loop (Fluent API)
//
// The same interactive HITL agent as main.go, expressed through the fluent
// façade. The shared ConfirmingHITL handler (from hitl.go) is wired in via
// .HITL(). Because the registry stays fail-closed for PolicyUserConfirm
// tools even with a HITL handler attached, we relax those tools with a
// classic escape (registry.SetPolicyOverride) after sp4rk.NewF — this is the
// intended hybrid: fluent for construction, classic for fine-grained control.
//
// This example is interactive — it reads y/n from stdin.
// Run with: go run -tags fluent .
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

func run() error {
	workspaceDir, err := os.MkdirTemp("", "sp4rk-example-04-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	// sp4rk.NewF: provider + HITL handler + a low step budget (to demonstrate
	// OnStepLimit) + bundled file tools plus delete_file. Finish is auto.
	fw, err := sp4rk.NewF().
		Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
		HITL(NewConfirmingHITL([]string{
			"write_file",
			"delete_file",
			"create_directory",
			"bash_exec",
		})).
		MaxSteps(10).
		FileTools().                         // read, write, edit, list, glob, mkdir
		Tools(builtins.NewDeleteFileTool()). // add delete so the agent can complete the task
		Build()
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	// Classic escape: the registry is FAIL-CLOSED for PolicyUserConfirm tools.
	// Our HITL handler already gates these at the executor level, so relax the
	// registry policy to avoid double-asking (or an outright deny).
	for _, name := range []string{"write_file", "delete_file", "create_directory"} {
		fw.ToolRegistry().SetPolicyOverride(name, tools.PolicyAlwaysAllow)
	}

	fmt.Printf("Workspace: %s\n", workspaceDir)
	fmt.Println("This example is INTERACTIVE — you will be asked to approve tool calls.")
	fmt.Println("Press y + Enter to allow, or just Enter to deny.")
	fmt.Println()

	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)
	task := "Create a file called notes.txt with the text 'Hello HITL!', then delete it."

	result, err := fw.RunF(ctx).
		System(fmt.Sprintf(`You are a file management assistant working in %s.
Create a file called "notes.txt" with some content, then delete it.
Use the available file tools. Call finish when done.`, workspaceDir)).
		Ask(task)
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
