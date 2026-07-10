//go:build fluent

// Example 08 — Parallel Subagents (Fluent API, hybrid).
//
// The Framework is assembled fluently with sp4rk.NewF, but the actual parallel
// run uses the low-level agent.RunSubAgentsParallel escape hatch — there is no
// pure-fluent API for parallel subagent execution (fw.TaskF runs steps
// sequentially). This mirrors the capstone's "fluent assembly + classic escape"
// philosophy.
//
// Run with: go run -tags fluent .
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
)

// taskSpec describes one independent unit of parallel work.
type taskSpec struct {
	id     string
	desc   string
	prompt string
}

func run() error {
	workspaceDir, err := os.MkdirTemp("", "sp4rk-subagents-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	// Fluent assembly. FileTools bundles read/write/edit/list/glob/mkdir;
	// AutoApprove satisfies the fail-closed registry for the throwaway workspace.
	// The finish tool is auto-registered by NewF.
	fw, err := sp4rk.NewF().
		Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
		FileTools().
		AutoApprove().
		MaxSteps(12).
		Build()
	if err != nil {
		return fmt.Errorf("framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	fmt.Println("Workspace:", workspaceDir)
	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	cmFactory := fw.ContextFactory()
	modelMeta := llm.ModelMetadata{
		ContextWindow: 200000,
		OutputLimit:   8192,
		TokenizerType: "approximate",
	}
	taskTools := fw.ToolRegistry().List() // read-only descriptors; finish included

	systemPrompt := func(desc string) string {
		return fmt.Sprintf(`You are a task execution agent. Task: %s.
Use the available tools. Verify your work, then call finish with a short summary.`, desc)
	}

	tasks := []taskSpec{
		{
			id:   "go-app",
			desc: "Build a Go hello-world",
			prompt: fmt.Sprintf(`In the directory %s/go-app, create main.go that prints "Hello from subagent A!"
and a go.mod with module example.com/goapp. Read main.go back to verify it was written. Then call finish.`, workspaceDir),
		},
		{
			id:   "py-app",
			desc: "Build a Python hello-world",
			prompt: fmt.Sprintf(`In the directory %s/py-app, create app.py that prints "Hello from subagent B!"
and a requirements.txt (it may be empty). Read app.py back to verify it was written. Then call finish.`, workspaceDir),
		},
	}

	// Build one SubAgentTask per unit of work: each gets its OWN fresh
	// Executor + ContextManager (an Executor cannot be shared concurrently).
	agents := make([]agent.SubAgentTask, len(tasks))
	for i, t := range tasks {
		cm := cmFactory(systemPrompt(t.desc), modelMeta, "sliding_window")
		exec := agent.NewExecutor(fw.LLMRouter(), fw.ToolRegistry(), 12,
			agent.WithEvents(&agent.NoopEvents{}))
		agents[i] = agent.SubAgentTask{
			StepID:    t.id,
			Executor:  exec,
			CM:        cm,
			TaskTools: taskTools,
			TaskDesc:  t.prompt,
			Emitter:   &agent.NoopEvents{},
		}
	}

	// RunSubAgentsParallel launches every subagent concurrently and blocks
	// until all report. Results come back in INPUT order (not completion
	// order): a slow agent blocks later results from being returned.
	start := time.Now()
	results := agent.RunSubAgentsParallel(ctx, agents)

	fmt.Printf("\n══════ Parallel execution done in %v ══════\n", time.Since(start).Round(time.Millisecond))
	for _, r := range results {
		if r.Error != nil {
			fmt.Printf("❌ %s failed: %v\n", r.StepID, r.Error)
			continue
		}
		fmt.Printf("✅ %s done (%d executor steps): %s\n", r.StepID, len(r.Steps), r.Output)
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}
