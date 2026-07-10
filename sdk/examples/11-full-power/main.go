// Example 11 — Full-Power Agent (Fluent-first hybrid)
//
// Combines every major sp4rk subsystem into one agent:
//   - Multi-provider LLM (Anthropic + OpenAI) with runtime model switching
//   - Custom tools + built-in tools + MCP server tools
//   - Custom Events for live observability
//   - Human-in-the-loop tool confirmation
//   - Planner → DAG → Conductor → Reflector orchestration
//   - Skills discovery from a local skills directory
//   - Fact memory for inter-step communication
//   - Context compaction configuration
//   - Blackboard with OnBlackboardChanged callback
//
// HYBRID APPROACH. The Framework is assembled with sp4rk.NewF and the
// orchestration runs as a single fw.TaskF chain. Where the fluent builders do not yet
// surface fine-grained control, classic escapes are used:
//   - WithConfig carries compaction/execution tuning + OnBlackboardChanged
//     (no dedicated fluent option for these).
//   - Skills are discovered via the classic SkillManager and passed to
//     TaskBuilder.Skills — discovery is pre-execution setup, not a fluent concern.
//   - The custom event sink embeds orchestration.NoopEvents so it satisfies the
//     full orchestration.Events interface required by TaskBuilder.Events.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/skills"
	"github.com/v0lka/sp4rk/strutil"
	"github.com/v0lka/sp4rk/tools"
)

// ─── Custom event sink ────────────────────────────────────────────────────
//
// Embeds orchestration.NoopEvents (which itself embeds agent.NoopEvents) so the
// type satisfies the FULL orchestration.Events interface — a requirement for
// TaskBuilder.Events. Only the methods we care about are overridden.

type consoleEvents struct {
	orchestration.NoopEvents
}

func (e *consoleEvents) StepStart(n int) { fmt.Printf("  ▶ step %d\n", n) }
func (e *consoleEvents) ToolCall(_, c int, name, args, src string) {
	fmt.Printf("    🔧 %s(%s) [%s]\n", name, trunc(args, 50), src)
}
func (e *consoleEvents) ToolResult(_, c, l int, p string, err bool) {
	icon := "✅"
	if err {
		icon = "❌"
	}
	fmt.Printf("    %s result (%d chars)\n", icon, l)
}
func (e *consoleEvents) Finishing(n int, s string) {
	fmt.Printf("  🏁 finish @%d: %s\n", n, trunc(s, 60))
}
func (e *consoleEvents) OnPlanGenerated(n int, steps []orchestration.PlanStepEvent) {
	fmt.Printf("\n📋 Plan: %d steps\n", n)
	for _, s := range steps {
		fmt.Printf("   • %s: %s\n", s.ID, s.Summary)
	}
}
func (e *consoleEvents) OnStepStarted(id, desc, summary string) {
	fmt.Printf("\n▶ %s: %s\n", id, summary)
}
func (e *consoleEvents) OnStepCompleted(id string, ok bool, d time.Duration, errMsg string) {
	if ok {
		fmt.Printf("  ✅ %s done (%v)\n", id, d)
	} else {
		fmt.Printf("  ❌ %s failed (%v): %s\n", id, d, errMsg)
	}
}
func (e *consoleEvents) OnReflected(r *orchestration.Reflection, attempt, maxAttempts int) {
	fmt.Printf("  🔍 reflection (attempt %d/%d): %s → %s\n", attempt, maxAttempts, r.Summary, r.SuggestedAction)
}

// ─── Custom HITL handler (auto-approve with a denylist) ───────────────────

type autoApproveHITL struct {
	agent.NoopHITLHandler
	deniedTools map[string]bool
}

func (h *autoApproveHITL) OnToolCall(_ context.Context, name string, _ json.RawMessage) (*agent.HITLToolDecision, error) {
	if h.deniedTools[name] {
		return &agent.HITLToolDecision{Allow: false, Reason: name + " is blocked by policy"}, nil
	}
	return &agent.HITLToolDecision{Allow: true}, nil
}

// ─── Custom tool: timestamp ───────────────────────────────────────────────

type timestampTool struct{ *tools.BaseTool }

func newTimestampTool() *timestampTool {
	return &timestampTool{BaseTool: &tools.BaseTool{
		ToolName:        "timestamp",
		ToolDescription: "Get the current timestamp in RFC3339 format. No input required.",
		Schema:          json.RawMessage(`{"type":"object","properties":{}}`),
		Policy:          tools.PolicyAlwaysAllow,
	}}
}

func (t *timestampTool) Execute(_ context.Context, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{Content: time.Now().Format(time.RFC3339)}, nil
}

func run() error {
	// ── 1. Multi-provider LLM config ──
	//
	// Two providers are configured so we can demonstrate runtime model
	// switching: Claude for planning/reflection (strong reasoning) and GPT-4o
	// for step execution. TaskBuilder.Models(...) switches the shared router
	// between phases automatically.
	const (
		plannerModel  = "claude-sonnet-4-5" // Anthropic — planning & reflection
		executorModel = "openai/gpt-4o"     // composite ID — step execution
	)

	providers := []llm.ProviderEntry{
		sp4rk.Anthropic(os.Getenv("ANTHROPIC_API_KEY"), plannerModel),
	}
	openaiAvailable := false
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		providers = append(providers, sp4rk.OpenAI(key, llm.BareModel(executorModel)))
		openaiAvailable = true
	}

	// ── 2. Workspace + skills directory ──
	workspaceDir, err := os.MkdirTemp("", "sp4rk-example-11-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	skillsDir := filepath.Join(workspaceDir, ".agents", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("skills dir: %w", err)
	}
	seedSkill(skillsDir)

	// ── 3. Framework via sp4rk.NewF ──
	//
	// CLASSIC ESCAPE (WithConfig): compaction tuning, execution tuning, and the
	// OnBlackboardChanged callback have no dedicated fluent option, so they ride
	// in a base sp4rk.Config. fluent options then layer the common wiring
	// (providers, MCP, tools, HITL, auto-approve) on top of that base.
	base := sp4rk.Config{
		LLM: sp4rk.LLMConfig{
			MaxRetries:         3,
			OutputTokenReserve: 4096,
		},
		Execution: sp4rk.ExecutionConfig{
			MaxRetries:                2,
			SafetyMarginPercent:       5,
			PreWarningPercent:         80,
			ToolCacheTTLSeconds:       300,
			MaxDependencyContextChars: 8000,
		},
		Compaction: sp4rk.CompactionConfig{
			Strategy:          "sliding_window",
			PredictivePercent: 85,
			WarningPercent:    92,
			EmergencyPercent:  98,
		},
		OnBlackboardChanged: func(changeType string) {
			fmt.Printf("  📝 blackboard: %s\n", changeType)
		},
	}

	// Tools: custom timestamp + bundled file tools + fact-memory tools.
	// The finish tool is auto-registered by sp4rk.NewF.
	fw, err := sp4rk.NewF().
		Config(base).            // escape hatch: advanced tuning
		Providers(providers...). // multi-provider (conditional OpenAI)
		DefaultModel(plannerModel).
		MCPStdio("filesystem", "npx", "-y", "@modelcontextprotocol/server-filesystem", workspaceDir). // MCP stdio server
		MCPWorkDir(workspaceDir).
		FileTools().
		MemoryTools().
		Tools(newTimestampTool()). // custom timestamp tool
		HITL(&autoApproveHITL{deniedTools: map[string]bool{"delete_directory": true}}).
		AutoApprove(). // satisfy the fail-closed registry (throwaway workspace)
		MaxSteps(20).
		Build()
	if err != nil {
		return fmt.Errorf("framework: %w", err)
	}
	defer func() { _ = fw.Shutdown() }()

	fmt.Println("Workspace:", workspaceDir)
	fmt.Println("Skills dir:", skillsDir)
	fmt.Println("\nAvailable tools:")
	for _, td := range fw.ToolRegistry().List() {
		fmt.Printf("  [%s] %s\n", td.Source, td.Name)
	}

	fmt.Printf("\nActive LLM: %s (provider: %s)\n", fw.LLMRouter().ActiveModel(), fw.LLMRouter().ActiveProviderName())
	if openaiAvailable {
		fmt.Printf("Runtime model switching enabled: %s → %s for execution (handled by TaskBuilder.Models)\n", plannerModel, executorModel)
	} else {
		fmt.Println("Runtime model switching disabled (set OPENAI_API_KEY to enable a second provider)")
	}

	// ── 4. Discover skills (classic — pre-execution setup) ──
	skillMgr := skills.NewSkillManager([]string{skillsDir}, nil)
	if err := skillMgr.Scan(); err != nil {
		log.Printf("skill scan: %v", err)
	}
	discoveredSkills := skillMgr.List()
	fmt.Printf("\nDiscovered skills: %d\n", len(discoveredSkills))
	for _, s := range discoveredSkills {
		fmt.Printf("  • %s: %s\n", s.Name, trunc(s.Description, 60))
	}

	// ── 5. The task ──
	task := fmt.Sprintf(`In the workspace %s, create a Go project:
1. Create a directory "myproject"
2. Write main.go that prints the current timestamp (use the timestamp tool) and "Hello from full-power agent!"
3. Read the file back to verify
4. Store a fact about what you created for future reference`, workspaceDir)

	// ── 6. Plan → Execute → Reflect via fw.TaskF ──
	//
	// A single chain replaces the hand-rolled loop of the classic example 06.
	// .Plan()/.Reflect() use the fluent default prompts; .Models() switches the
	// router between a strong-reasoning planner and a fast executor.
	events := &consoleEvents{}
	tb := fw.TaskF(context.Background(), task).
		System(fmt.Sprintf(`You are a task execution agent working in %s.
Complete the assigned step using the available tools.
Use store_fact to record important findings for other steps.
Call finish with a summary when done.`, workspaceDir)).
		Workspace(workspaceDir).
		Skills(discoveredSkills).
		Events(events).
		Plan().
		Reflect().
		MaxRetries(2)
	if openaiAvailable {
		// Runtime model switching: plan/reflect on Claude, execute on GPT-4o.
		tb = tb.Models(plannerModel, executorModel)
	}

	result, err := tb.Execute()
	if err != nil {
		return fmt.Errorf("execution: %w", err)
	}

	// ── 7. Aggregate + report ──
	fmt.Println("\n═══════════════════════════════════════════")
	fmt.Printf("Steps: %d total, %d failed | Reflections: %d | Facts: %d\n",
		len(result.Plan.Steps), result.FailedSteps, len(result.Reflections), len(result.Blackboard.GetFacts()))
	fmt.Printf("Models: planning=%s", plannerModel)
	if openaiAvailable {
		fmt.Printf(" | execution=%s", executorModel)
	} else {
		fmt.Print(" | execution=same as planning (single provider)")
	}
	fmt.Println()
	fmt.Println("\nFinal output:")
	fmt.Println(result.Output)
	fmt.Println("\nFacts stored:")
	for _, f := range result.Blackboard.GetFacts() {
		fmt.Printf("  [%s] %s\n", strings.Join(f.Keywords, ", "), trunc(f.Content, 80))
	}
	fmt.Println("═══════════════════════════════════════════")
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("%v", err)
	}
}

// ─── Helpers ───────────────────────────────────────────────────────────────

func seedSkill(skillsDir string) {
	skillDir := filepath.Join(skillsDir, "go-testing")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return
	}
	content := "---\nname: go-testing\ndescription: Use when writing Go tests with the standard testing package.\n---\n# Go Testing Skill\n\nWrite tests using `go test`. Place test files alongside source as `*_test.go`.\n"
	_ = os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644)
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return strutil.TruncateUTF8(s, n-1) + "…"
}
