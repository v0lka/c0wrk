//go:build fluent

// Example 07 — Multi-Provider Routing & Runtime Model Switching (Fluent API)
//
// Same scenario as main.go, expressed through the fluent API (entry point
// sp4rk.NewF). Shows BOTH switching mechanisms:
//   - manual runtime switch via fw.LLMRouter().SetModel(...), and
//   - declarative phase-based switch via fw.TaskF(...).Models(planner, executor).
//
// Run with: go run -tags fluent .
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/llm"
)

const (
	defaultModel = "claude-sonnet-4-5" // Anthropic
	secondModel  = "openai/gpt-4o"     // composite ID
)

func run() error {
	ctx := context.Background()

	// 1. Fluent provider assembly: Anthropic default, OpenAI optional.
	//    .Anthropic/.OpenAI are repeatable; each appends a provider entry.
	fb := sp4rk.NewF().
		Anthropic(os.Getenv("ANTHROPIC_API_KEY"), defaultModel)

	switchable := false
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		// llm.BareModel keeps the registered model name clean ("gpt-4o").
		fb = fb.OpenAI(key, llm.BareModel(secondModel))
		switchable = true
	}

	// DefaultModel pins the initial active selection (accepts bare or composite).
	// The finish tool is auto-registered by NewF.
	fw, err := fb.DefaultModel(defaultModel).Build()
	if err != nil {
		return fmt.Errorf("framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	router := fw.LLMRouter()
	fmt.Printf("initial  : %s (provider: %s)\n",
		router.ActiveModel(), router.ActiveProviderName())

	system := "You are a helpful assistant. Answer in one sentence, then call finish."

	// 2. Single ReAct loop on the default model.
	res1, err := fw.RunF(ctx).
		System(system).
		Ask("Name one benefit of static typing.")
	if err != nil {
		return fmt.Errorf("run (default): %w", err)
	}
	fmt.Println("[default] ->", res1.Output)

	// 3. Manual runtime switch with the shared Router, then a second run.
	if switchable {
		if err := router.SetModel(ctx, secondModel); err != nil { // <-- manual switch
			return fmt.Errorf("set model: %w", err)
		}
		fmt.Printf("switched : %s (provider: %s)\n",
			router.ActiveModel(), router.ActiveProviderName())

		res2, err := fw.RunF(ctx).
			System(system).
			Ask("Name one benefit of dynamic typing.")
		if err != nil {
			return fmt.Errorf("run (switched): %w", err)
		}
		fmt.Println("[switched] ->", res2.Output)

		_ = router.SetModel(ctx, defaultModel) // restore
	} else {
		fmt.Println("\n(set OPENAI_API_KEY to enable a second provider and runtime switching)")
	}

	// 4. Declarative PHASE-BASED switching within ONE orchestrated run (the
	//    capstone pattern): .Models(planModel, execModel) switches the shared
	//    router to execModel for the execute phase and restores planModel after.
	//    No manual SetModel is needed here.
	if switchable {
		res3, err := fw.TaskF(ctx, "In one sentence, explain why Go uses goroutines.").
			System(system).
			Plan().
			Models(defaultModel, secondModel). // planModel, execModel
			Execute()
		if err != nil {
			return fmt.Errorf("taskf (phased): %w", err)
		}
		fmt.Println("[phased]  ->", res3.Output)
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
