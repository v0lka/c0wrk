//go:build !fluent

// Example 07 — Multi-Provider Routing & Runtime Model Switching (Classic API)
//
// Makes multi-provider routing the CENTRAL concept: configure two LLM providers
// and switch the active (provider, model) pair at runtime via the shared Router.
// Anthropic is the default; a second OpenAI provider is added when
// OPENAI_API_KEY is set, so the example always runs on at least one provider.
//
// Contrast with the full-power capstone, which only demonstrates the declarative
// phase-based switch (TaskBuilder.Models) gated behind a second API key. Here we
// drive the low-level Router API directly: SetModel before/after an Execute,
// inspecting ActiveModel / ActiveProviderName throughout.
//
// Run with: go run .
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

// Two models: a strong-reasoning default and a faster/cheaper alternative.
// The second is expressed as a COMPOSITE identifier ("provider/model"); a bare
// name ("gpt-4o") is what gets registered under the provider.
const (
	defaultModel = "claude-sonnet-4-5" // Anthropic
	secondModel  = "openai/gpt-4o"     // composite ID — fast/cheap execution
)

func run() error {
	ctx := context.Background()

	// 1. Assemble providers: Anthropic default + an optional OpenAI second
	//    provider. The first provider's first model becomes the initial active
	//    model — i.e. claude-sonnet-4-5 on the "anthropic" provider.
	providers := []llm.ProviderEntry{{
		Name:         "anthropic",
		ProviderType: "anthropic",
		APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
		Models:       []string{defaultModel},
	}}
	switchable := false
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		// llm.BareModel strips a composite prefix; it is a no-op on bare names,
		// so the provider registers the clean model name "gpt-4o".
		providers = append(providers, llm.ProviderEntry{
			Name:         "openai",
			ProviderType: "openai",
			APIKey:       key,
			Models:       []string{llm.BareModel(secondModel)}, // "gpt-4o"
		})
		switchable = true
	}

	// 2. Build the classic Framework. The Framework owns ONE shared Router —
	//    every Execute call resolves the active (provider, model) through it.
	fw, err := sp4rk.New(sp4rk.Config{
		LLM: sp4rk.LLMConfig{Providers: providers},
	})
	if err != nil {
		return fmt.Errorf("framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	fw.ToolRegistry().Register(agent.NewFinishTool())

	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return "You are a helpful assistant. Answer in one sentence, then call finish."
	}

	// 3. Inspect the initial active selection. ActiveModel() returns the
	//    composite identifier ("anthropic/claude-sonnet-4-5"); ActiveProviderName()
	//    returns the logical provider name.
	router := fw.LLMRouter()
	fmt.Printf("initial  : %s (provider: %s)\n",
		router.ActiveModel(), router.ActiveProviderName())

	// 4. Run a task on the default model.
	res1, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{},
		"Name one benefit of static typing.")
	if err != nil {
		return fmt.Errorf("execute (default): %w", err)
	}
	fmt.Println("[default] ->", res1.Output)

	// 5. Runtime model switch via SetModel BEFORE a second Execute.
	//    Accepts a composite "provider/model" (routes directly) or a bare name
	//    (resolved to the first matching provider). The router is restored
	//    afterward so subsequent calls reuse the default.
	if switchable {
		if err := router.SetModel(ctx, secondModel); err != nil { // <-- the switch API call
			return fmt.Errorf("set model: %w", err)
		}
		fmt.Printf("switched : %s (provider: %s)\n",
			router.ActiveModel(), router.ActiveProviderName()) // "openai/gpt-4o" / "openai"

		res2, err := fw.Execute(ctx, systemPrompt, &agent.NoopEvents{},
			"Name one benefit of dynamic typing.")
		if err != nil {
			return fmt.Errorf("execute (switched): %w", err)
		}
		fmt.Println("[switched] ->", res2.Output)

		// Switch back to the Anthropic model (bare name resolves deterministically).
		if err := router.SetModel(ctx, defaultModel); err != nil {
			return fmt.Errorf("restore model: %w", err)
		}
		fmt.Printf("restored : %s (provider: %s)\n",
			router.ActiveModel(), router.ActiveProviderName())
	} else {
		fmt.Println("\n(set OPENAI_API_KEY to enable a second provider and runtime switching)")
		fmt.Println("(see main_fluent.go for the declarative phase-based switch via TaskF().Models())")
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
