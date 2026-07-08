//go:build fluent

// Example 01 — Minimal Agent (Fluent API)
//
// The same minimal agent as main.go, expressed through the fluent façade
// ([github.com/v0lka/sp4rk/fluent]). The finish tool is auto-registered and
// the provider, tools, and execution are configured declaratively. Compare the
// line count to the classic variant — this is the recommended entry point.
//
// Run with: go run -tags fluent .
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/sp4rk/fluent"
)

func run() error {
	// fluent.New builds a real *sdk.Framework (fluent.Framework is a type
	// alias). The finish tool is auto-registered by convention, so the agent
	// can signal completion without an explicit Register call.
	fw, err := fluent.New().
		Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
		Build()
	if err != nil {
		return fmt.Errorf("failed to create framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	// Run a single ReAct loop and return the original ExecutionResult.
	// NoopEvents is the default event sink; System sets a static prompt.
	result, err := fluent.Run(context.Background(), fw).
		System("You are a helpful assistant. " +
			"Answer the user's question concisely. " +
			"When you have a final answer, call the finish tool with it.").
		Ask("What is the capital of France?")
	if err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	fmt.Println("Status:", result.Status)
	fmt.Println("Output:", result.Output)
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
