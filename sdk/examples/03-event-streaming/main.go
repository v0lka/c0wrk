// Example 03 — Event Streaming
//
// Demonstrates how to observe the agent's execution in real time by
// implementing the agent.AgentEvents interface. A PrintingEvents sink
// formats each lifecycle event (thoughts, tool calls, results, context
// fill) and writes it to stdout, giving full visibility into the ReAct loop.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/sdk"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/tools"
	"github.com/v0lka/c0wrk/sdk/tools/builtins"
)

// PrintingEvents implements agent.AgentEvents by embedding agent.NoopEvents
// (which provides no-op stubs for every method) and overriding the methods
// we want to observe. This is the recommended pattern: embed NoopEvents,
// override only what you need.
type PrintingEvents struct {
	agent.NoopEvents
}

// --- Step lifecycle ---

func (e *PrintingEvents) StepStart(stepNum int) {
	fmt.Printf("\n┌─ Step %d ─────────────────────────────\n", stepNum)
}

func (e *PrintingEvents) StepComplete(stepNum int, duration time.Duration) {
	fmt.Printf("└─ Step %d complete (%v) ──────────────\n", stepNum, duration)
}

// --- Reasoning ---

func (e *PrintingEvents) Thought(stepNum int, content, reasoning string) {
	fmt.Printf("│ 💭 Thought: %s\n", truncate(content, 120))
	if reasoning != "" {
		fmt.Printf("│    (reasoning: %s)\n", truncate(reasoning, 80))
	}
}

// --- Tool calls ---

func (e *PrintingEvents) ToolCall(stepNum, callIdx int, toolName, argsPreview, source string) {
	fmt.Printf("│ 🔧 ToolCall #%d: %s(%s) [source: %s]\n", callIdx, toolName, truncate(argsPreview, 80), source)
}

func (e *PrintingEvents) ToolResult(stepNum, callIdx, resultLen int, preview string, isError bool) {
	icon := "✅"
	if isError {
		icon = "❌"
	}
	fmt.Printf("│ %s Result #%d (%d chars): %s\n", icon, callIdx, resultLen, truncate(preview, 100))
}

// --- Assistant output ---

func (e *PrintingEvents) AssistantChunk(content string) {
	// Streaming chunks — print without newline for a live-typing effect
	fmt.Print(content)
}

func (e *PrintingEvents) AssistantDone(content string, inputTokens, outputTokens int) {
	fmt.Printf("\n│ 📝 Assistant done: %d input / %d output tokens\n", inputTokens, outputTokens)
}

// --- Context window ---

func (e *PrintingEvents) ContextFill(fillPercent float64, usedTokens, maxTokens int, status string, stepID string) {
	fmt.Printf("│ 📊 Context: %.1f%% (%d/%d tokens) — %s\n", fillPercent, usedTokens, maxTokens, status)
}

func (e *PrintingEvents) ContextCompaction(beforePercent, afterPercent float64, stepID string) {
	fmt.Printf("│ ♻️  Compaction: %.1f%% → %.1f%%\n", beforePercent, afterPercent)
}

// --- Completion ---

func (e *PrintingEvents) Finishing(stepNum int, summary string) {
	fmt.Printf("│ 🏁 Finishing at step %d: %s\n", stepNum, truncate(summary, 100))
}

// --- Diagnostics ---

func (e *PrintingEvents) ExecutorDiagnostic(stepNum int, event string, details map[string]any) {
	fmt.Printf("│ ⚠️  Diagnostic (step %d): %s %v\n", stepNum, event, details)
}

// --- Sub-agent events (not used in this example but required by the interface) ---

func (e *PrintingEvents) SubAgentLaunch(stepID, description string) {
	fmt.Printf("│ 🚀 SubAgent launched: %s — %s\n", stepID, truncate(description, 80))
}

func (e *PrintingEvents) SubAgentComplete(stepID string, success bool, duration time.Duration) {
	status := "succeeded"
	if !success {
		status = "failed"
	}
	fmt.Printf("│ 📥 SubAgent %s %s (%v)\n", stepID, status, duration)
}

// truncate shortens a string to maxLen characters, appending "…" if truncated.
func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

func main() {
	fw, err := sdk.New(sdk.Config{
		LLM: sdk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
			DefaultModel: "claude-sonnet-4-5",
		},
	})
	if err != nil {
		log.Fatalf("failed to create framework: %v", err)
	}
	defer fw.Shutdown()

	// Register tools
	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewGlobTool())
	registry.Register(agent.NewFinishTool())

	// Use the current directory as the workspace
	workspaceDir, _ := os.Getwd()
	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)

	systemPrompt := func(_ context.Context, _ string, _ llm.ModelMetadata) string {
		return "You are a code exploration assistant. " +
			"Use the available tools to investigate the codebase. " +
			"Call finish when you have your answer."
	}

	task := "List the Go files in the current directory using the glob tool, " +
		"then read the first one you find and summarize what it does in one sentence."

	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  Task:", task)
	fmt.Println("═══════════════════════════════════════════════════════════")

	// Pass our custom events implementation instead of NoopEvents.
	events := &PrintingEvents{}

	result, err := fw.Execute(ctx, systemPrompt, events, task)
	if err != nil {
		log.Fatalf("execution failed: %v", err)
	}

	fmt.Println("\n═══════════════════════════════════════════════════════════")
	fmt.Println("Final Status:", result.Status)
	fmt.Println("Final Output:", result.Output)
	fmt.Println("═══════════════════════════════════════════════════════════")
}
