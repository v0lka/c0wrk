//go:build fluent

// Example 09 — Context Window & Memory Management (Fluent API)
//
// Same demo as main.go, assembled with sp4rk.NewF + fw.TaskF. The thresholds
// ride in the base Config; the per-task compaction strategy is overridden via
// TaskBuilder.Compaction("hierarchical").
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
)

func run() error {
	// Thresholds ride in the base Config (no dedicated fluent option for them).
	base := sp4rk.Config{
		Compaction: sp4rk.CompactionConfig{
			Strategy:          "sliding_window", // default; overridden per-task below
			PredictivePercent: 10,               // lowered so a short demo can reach it
			WarningPercent:    50,
			EmergencyPercent:  70,
		},
	}

	workspaceDir, err := os.MkdirTemp("", "sp4rk-example-09-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()
	for i := 1; i <= 6; i++ {
		name := filepath.Join(workspaceDir, fmt.Sprintf("note_%d.md", i))
		content := fmt.Sprintf("# Note %d\n\n", i)
		for j := 0; j < 60; j++ {
			content += fmt.Sprintf("- Line %d of note %d: reusable context-window filler.\n", j, i)
		}
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			return fmt.Errorf("seed file: %w", err)
		}
	}

	fw, err := sp4rk.NewF().
		Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
		Config(base).
		FileTools().  // read_file / list_directory / glob for context accumulation
		AutoApprove().
		MaxSteps(30).
		Build()
	if err != nil {
		return fmt.Errorf("framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	task := fmt.Sprintf(`List the files in %s, then read every note_*.md file.
Watch the context-fill events as you go. Call finish with a one-sentence summary when done.`,
		workspaceDir)

	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  Strategy: hierarchical (per-task) | predictive @ 10% fill")
	fmt.Println("═══════════════════════════════════════════════════════════")

	result, err := fw.TaskF(context.Background(), task).
		System("You are a context-exercise assistant. Use list_directory and " +
			"read_file to read the note files, then call finish with a one-line summary.").
		Workspace(workspaceDir).
		Events(&printingEvents{}).
		Compaction("hierarchical"). // per-task override of the base strategy
		Execute()
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
