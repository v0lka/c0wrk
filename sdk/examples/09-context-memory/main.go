//go:build !fluent

// Example 09 — Context Window & Memory Management (Classic API)
//
// Demonstrates the SDK's memory subsystem: the compaction configuration, the
// three compaction strategies, and the ContextFill / ContextCompaction events
// that surface how full the context window is and when space is reclaimed.
//
// Real compaction needs a lot of tokens (the predictive threshold defaults to
// 85% of the model window). This demo keeps the threshold low and gives the
// agent several files to read so the ContextFill stream is visible; if the fill
// crosses the predictive threshold, a real ContextCompaction event fires.
//
// Run with: go run .
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

func run() error {
	// ── Framework with compaction configured ──
	//
	// memory.NewCompactionStrategy recognises three strategy names:
	//   "sliding_window" — keep the first N + last M messages; no LLM call.
	//   "summarization"  — LLM-summarize older blocks (needs a summarizer dep).
	//   "hierarchical"   — tiered distant/middle/recent resolution (needs a summarizer dep).
	// Anything else falls back to sliding_window. We use sliding_window here
	// (no summarizer is wired by default); the fluent variant switches to
	// "hierarchical" to show the alternative name in use.
	fw, err := sp4rk.New(sp4rk.Config{
		LLM: sp4rk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
		Compaction: sp4rk.CompactionConfig{
			Strategy:          "sliding_window", // try also "summarization" / "hierarchical"
			PredictivePercent: 10,               // lowered so a short demo can reach it
			WarningPercent:    50,
			EmergencyPercent:  70,
		},
	})
	if err != nil {
		return fmt.Errorf("framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewGlobTool())
	registry.Register(agent.NewFinishTool())

	// Seed several files in a throwaway workspace so the task generates many
	// read steps that accumulate context.
	workspaceDir, err := os.MkdirTemp("", "sp4rk-example-09-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()
	for i := 1; i <= 6; i++ {
		name := filepath.Join(workspaceDir, fmt.Sprintf("note_%d.md", i))
		// ~1 KB of content each — enough that reading several fills context.
		content := fmt.Sprintf("# Note %d\n\n", i)
		for j := 0; j < 60; j++ {
			content += fmt.Sprintf("- Line %d of note %d: reusable context-window filler.\n", j, i)
		}
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			return fmt.Errorf("seed file: %w", err)
		}
	}

	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return "You are a context-exercise assistant. Use list_directory and " +
			"read_file to read the note files, then call finish with a one-line summary."
	}
	task := fmt.Sprintf(`List the files in %s, then read every note_*.md file.
Watch the context-fill events as you go. Call finish with a one-sentence summary when done.`,
		workspaceDir)

	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  Strategy: sliding_window | predictive @ 10% fill")
	fmt.Println("═══════════════════════════════════════════════════════════")

	result, err := fw.Execute(ctx, systemPrompt, &printingEvents{}, task)
	if err != nil {
		return fmt.Errorf("execution: %w", err)
	}

	fmt.Println("\n═══════════════════════════════════════════════════════════")
	fmt.Println("Status:", result.Status)
	fmt.Println("Output:", result.Output)
	fmt.Println("═══════════════════════════════════════════════════════════")
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
