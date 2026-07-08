package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/strutil"
)

// PrintingEvents implements agent.Events by embedding agent.NoopEvents
// (which provides no-op stubs for every method) and overriding the methods
// we want to observe. This is the recommended pattern: embed NoopEvents,
// override only what you need.
//
// This type lives in a tagless file so that both the classic (main.go) and
// fluent (main_fluent.go) example variants can use it.
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
	// Streaming chunks — print without newline for a live-typing effect.
	fmt.Print(content)
}

func (e *PrintingEvents) AssistantDone(content string, inputTokens, outputTokens int) {
	fmt.Printf("\n│ 📝 Assistant done: %d input / %d output tokens\n", inputTokens, outputTokens)
}

// --- Context window ---

func (e *PrintingEvents) ContextFill(fillPercent float64, usedTokens, maxTokens int, status, stepID string) {
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

// truncate shortens a string to maxLen bytes, flattening newlines and
// appending "…" if truncated. The cut is UTF-8-safe (delegates to
// strutil.TruncateUTF8) so multi-byte runes are never split.
func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return strutil.TruncateUTF8(s, maxLen-1) + "…"
}
