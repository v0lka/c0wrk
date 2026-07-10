//go:build fluent

// Example 03 — Event Streaming (Fluent API)
//
// The same live-observability agent as main.go, expressed through the fluent
// façade. The shared PrintingEvents sink (from events.go) is attached via the
// Run builder's .Events(...) method.
//
// Run with: go run -tags fluent .
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/tools"
)

func run() error {
	fw, err := sp4rk.NewF().
		Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
		FileTools(). // read_file, list_directory, glob, …
		Build()
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	workspaceDir, _ := os.Getwd()
	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	task := "List the Go files in the current directory using the glob tool, " +
		"then read the first one you find and summarize what it does in one sentence."

	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  Task:", task)
	fmt.Println("═══════════════════════════════════════════════════════════")

	// Attach the shared PrintingEvents sink instead of the default NoopEvents.
	events := &PrintingEvents{}

	result, err := fw.RunF(ctx).
		Events(events).
		System("You are a code exploration assistant. " +
			"Use the available tools to investigate the codebase. " +
			"Call finish when you have your answer.").
		Ask(task)
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
