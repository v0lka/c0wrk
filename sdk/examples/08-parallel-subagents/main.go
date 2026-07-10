//go:build !fluent

// Example 08 — Parallel Subagents (Classic API).
//
// The SDK exposes a low-level parallel-execution primitive that no other
// example touches: agent.RunSubAgent / agent.RunSubAgentsParallel. (The
// Conductor and fw.TaskF execute plan steps SEQUENTIALLY — PlanStep.Parallelible
// is defined but never consumed.) This example shows the primitive directly.
//
// Two genuinely independent file-generation tasks run concurrently, each in its
// own goroutine. Because an Executor is NOT safe for concurrent use on a single
// instance, each subagent gets its OWN Executor + its OWN ContextManager. The
// latency win is real: running the two tasks in parallel roughly halves the
// wall-clock vs. the sequential equivalent.
//
// Run with: go run .
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// taskSpec describes one independent unit of parallel work.
type taskSpec struct {
	id     string
	desc   string
	prompt string
}

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
		Execution: sp4rk.ExecutionConfig{MaxSteps: 12},
		// The registry is FAIL-CLOSED for write tools; this runs in a throwaway
		// temp workspace, so we auto-approve mutations.
		ConfirmFunc: func(_ context.Context, req tools.ConfirmationRequest) (tools.ConfirmationResponse, error) {
			return tools.ConfirmAllowOnce, nil
		},
	})
	if err != nil {
		return fmt.Errorf("framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	// File tools + finish. Each subagent must be able to signal completion.
	registry := fw.ToolRegistry()
	registry.Register(builtins.NewCreateDirectoryTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewReadFileTool())
	registry.Register(agent.NewFinishTool())

	workspaceDir, err := os.MkdirTemp("", "sp4rk-subagents-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()
	fmt.Println("Workspace:", workspaceDir)

	// Shared base context. context.Context is safe for concurrent use, and
	// RunSubAgent derives a fresh child per call (it sets the task/step context
	// internally) — it never mutates the parent.
	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	// The Framework's ContextManagerFactory, wired with its compaction/safety
	// config. The Framework exposes no ModelRegistry accessor, so we construct
	// the model metadata directly — exactly the fallback the Conductor applies
	// when it cannot resolve a metadata entry.
	cmFactory := fw.ContextFactory()
	modelMeta := llm.ModelMetadata{
		ContextWindow: 200000,
		OutputLimit:   8192,
		TokenizerType: "approximate",
	}

	// Two genuinely independent tasks: disjoint subdirectories, no shared files,
	// so parallelism is legitimate (not contrived fan-out over one artifact).
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

	// taskTools is a read-only slice of descriptors — safe to share across
	// goroutines. RunSubAgent appends the finish tool internally too.
	taskTools := registry.List()

	systemPrompt := func(desc string) string {
		return fmt.Sprintf(`You are a task execution agent. Task: %s.
Use the available tools. Verify your work, then call finish with a short summary.`, desc)
	}

	results := make([]agent.SubAgentResult, len(tasks))
	var wg sync.WaitGroup
	start := time.Now()

	for i, t := range tasks {
		wg.Add(1)
		go func(i int, t taskSpec) {
			defer wg.Done()
			// CRITICAL: fresh Executor + fresh ContextManager PER subagent.
			// Sharing one Executor across goroutines violates its
			// single-execution invariant.
			cm := cmFactory(systemPrompt(t.desc), modelMeta, "sliding_window")
			exec := agent.NewExecutor(fw.LLMRouter(), fw.ToolRegistry(), 12,
				agent.WithEvents(&agent.NoopEvents{}))

			// Launch in a goroutine; read exactly one result from the channel.
			ch := agent.RunSubAgent(ctx, t.id, exec, cm, taskTools, t.prompt,
				&agent.NoopEvents{}, nil)
			results[i] = <-ch // each index is written by exactly one goroutine
		}(i, t)
	}
	wg.Wait()

	fmt.Printf("\n═══════ Parallel execution done in %v ══════\n", time.Since(start).Round(time.Millisecond))
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
