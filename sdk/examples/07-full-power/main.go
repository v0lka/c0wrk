// Example 07 — Full-Power Agent
//
// Combines every major SDK subsystem into one agent:
//   - Multi-provider LLM (Anthropic + OpenAI) with runtime model switching
//   - Custom tools + built-in tools + MCP server tools
//   - Custom AgentEvents for live observability
//   - Human-in-the-loop tool confirmation
//   - Planner → DAG → Conductor → Reflector orchestration
//   - Skills discovery from a local skills directory
//   - Fact memory for inter-step communication
//   - Context compaction configuration
//   - Blackboard with OnBlackboardChanged callback
//
// This is the "kitchen sink" example — it exercises the SDK at maximum capacity.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/sdk"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/agent/reflector"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	"github.com/v0lka/c0wrk/sdk/planner"
	"github.com/v0lka/c0wrk/sdk/skills"
	"github.com/v0lka/c0wrk/sdk/tools"
	"github.com/v0lka/c0wrk/sdk/tools/builtins"
	"github.com/v0lka/c0wrk/sdk/tools/mcp"
)

// ─── Custom event sink (from example 03, condensed) ───────────────────────

type consoleEvents struct {
	agent.NoopEvents
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
func (e *consoleEvents) OnReflected(r *orchestration.Reflection, attempt, max int) {
	fmt.Printf("  🔍 reflection (attempt %d/%d): %s → %s\n", attempt, max, r.Summary, r.SuggestedAction)
}

// ─── Custom HITL handler (from example 04, auto-approve mode) ──────────────

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

// ─── Trajectory store (from example 06) ────────────────────────────────────

type trajStore struct {
	mu    sync.Mutex
	steps []agent.Step
}

func (s *trajStore) Sync(steps []agent.Step) { s.mu.Lock(); s.steps = steps; s.mu.Unlock() }
func (s *trajStore) Steps() []agent.Step     { s.mu.Lock(); defer s.mu.Unlock(); return s.steps }

// ─── Custom tool: timestamp ────────────────────────────────────────────────

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

func main() {
	// ── 1. Multi-provider LLM config ──
	providers := []llm.ProviderEntry{{
		Name:         "anthropic",
		ProviderType: "anthropic",
		APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
		Models:       []string{"claude-sonnet-4-5"},
	}}
	// Add OpenAI if a key is available (multi-provider demonstration)
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		providers = append(providers, llm.ProviderEntry{
			Name:         "openai",
			ProviderType: "openai",
			APIKey:       key,
			Models:       []string{"gpt-4o"},
		})
	}

	// ── 2. Workspace + skills directory ──
	workspaceDir, err := os.MkdirTemp("", "c0wrk-example-07-*")
	if err != nil {
		log.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(workspaceDir)

	skillsDir := filepath.Join(workspaceDir, ".agents", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		log.Fatalf("skills dir: %v", err)
	}
	// Seed a sample skill
	seedSkill(skillsDir)

	// ── 3. Create the Framework with every subsystem ──
	fw, err := sdk.New(sdk.Config{
		LLM: sdk.LLMConfig{
			Providers:          providers,
			DefaultModel:       "claude-sonnet-4-5",
			MaxRetries:         3,
			OutputTokenReserve: 4096,
		},
		MCP: &sdk.MCPConfig{
			Servers: map[string]mcp.ServerEntry{
				"filesystem": {
					Transport: "stdio",
					Command:   "npx",
					Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", workspaceDir},
				},
			},
			DefaultWorkDir: workspaceDir,
		},
		Execution: sdk.ExecutionConfig{
			MaxSteps:                  20,
			MaxRetries:                2,
			SafetyMarginPercent:       5,
			PreWarningPercent:         80,
			ToolCacheTTLSeconds:       300,
			MaxDependencyContextChars: 8000,
		},
		Compaction: sdk.CompactionConfig{
			Strategy:          "sliding",
			PredictivePercent: 85,
			WarningPercent:    92,
			EmergencyPercent:  98,
		},
		HITL: &autoApproveHITL{
			deniedTools: map[string]bool{"delete_directory": true}, // block destructive ops
		},
		OnBlackboardChanged: func(changeType string) {
			fmt.Printf("  📝 blackboard: %s\n", changeType)
		},
	})
	if err != nil {
		log.Fatalf("framework: %v", err)
	}
	defer fw.Shutdown()

	// ── 4. Register tools: custom + built-in + finish ──
	registry := fw.ToolRegistry()
	registry.Register(newTimestampTool()) // custom
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewEditFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewGlobTool())
	registry.Register(builtins.NewCreateDirectoryTool())
	registry.Register(builtins.NewStoreFactTool())
	registry.Register(builtins.NewSearchFactsTool())
	registry.Register(agent.NewFinishTool())
	// MCP tools are auto-registered by the gateway during sdk.New()

	fmt.Println("Workspace:", workspaceDir)
	fmt.Println("Skills dir:", skillsDir)
	fmt.Println("\nAvailable tools:")
	for _, td := range registry.List() {
		fmt.Printf("  [%s] %s\n", td.Source, td.Name)
	}

	// ── 5. Discover skills ──
	skillMgr := skills.NewSkillManager([]string{skillsDir}, nil)
	if err := skillMgr.Scan(); err != nil {
		log.Printf("skill scan: %v", err)
	}
	discoveredSkills := skillMgr.List()
	fmt.Printf("\nDiscovered skills: %d\n", len(discoveredSkills))
	for _, s := range discoveredSkills {
		fmt.Printf("  • %s: %s\n", s.Name, trunc(s.Description, 60))
	}

	// ── 6. Create Planner ──
	plannerCfg := planner.DefaultPlannerConfig()
	plannerCfg.Prompts = makePlannerPromptSet()
	plannerCfg.Model = "claude-sonnet-4-5"
	pl, err := planner.NewPlanner(fw.LLMRouter(), plannerCfg)
	if err != nil {
		log.Fatalf("planner: %v", err)
	}

	// ── 7. Create Reflector ──
	rf := reflector.NewReflector(fw.LLMRouter(), reflector.Config{
		SystemPrompt: "You are a reflection agent. Analyze the failed execution and return JSON with summary, root_cause, suggested_action (retry/replan/abort), and action_plan.",
	})

	// ── 8. Create Conductor ──
	events := &consoleEvents{}
	systemPromptFactory := func(_ context.Context, stepDesc string, _ llm.ModelMetadata) string {
		return fmt.Sprintf(`You are a task execution agent working in %s.
Complete the assigned step using the available tools.
Use store_fact to record important findings for other steps.
Call finish with a summary when done.`, workspaceDir)
	}
	conductor, err := fw.NewConductor(systemPromptFactory, events)
	if err != nil {
		log.Fatalf("conductor: %v", err)
	}
	defer conductor.Cleanup()

	// ── 9. The task ──
	task := fmt.Sprintf(`In the workspace %s, create a Go project:
1. Create a directory "myproject"
2. Write main.go that prints the current timestamp (use the timestamp tool) and "Hello from full-power agent!"
3. Read the file back to verify
4. Store a fact about what you created for future reference`, workspaceDir)

	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)
	availableTools := registry.List()

	// ── 10. Plan ──
	fmt.Println("\n📋 Planning...")
	bb := orchestration.NewMapBlackboard()
	bb.SetOriginalRequest(task)

	plan, err := pl.Plan(ctx, task, availableTools, nil, discoveredSkills, false, nil)
	if err != nil {
		log.Fatalf("plan: %v", err)
	}
	events.OnPlanGenerated(len(plan.Steps), planStepsToEvents(plan))

	// ── 11. Execute the DAG with retry + reflect ──
	completed := make(map[string]orchestration.CompletedStep)
	var reflections []orchestration.Reflection
	maxRetries := 2

	for {
		ready := orchestration.FindReadySteps(plan, completed)
		if len(ready) == 0 {
			break
		}
		for _, step := range ready {
			events.OnStepStarted(step.ID, step.Description, step.Summary)
			success := false
			for attempt := 1; attempt <= maxRetries+1; attempt++ {
				store := &trajStore{}
				stepCtx := agent.WithTrajectoryStore(ctx, store)
				result, runErr := conductor.Run(stepCtx, step.Description, bb, availableTools, events, "sliding_window")
				trajectory := store.Steps()

				if runErr == nil && result.Status == orchestration.ExecutionStatusSuccess {
					completed[step.ID] = orchestration.CompletedStep{StepID: step.ID, Output: result.Output, Steps: trajectory}
					bb.SetStepResult(step.ID, result.Output, nil, trajectory)
					events.OnStepCompleted(step.ID, true, 0, "")
					success = true
					break
				}
				errMsg := "execution failed"
				if runErr != nil {
					errMsg = runErr.Error()
				} else if result != nil {
					errMsg = result.Output
				}
				if attempt <= maxRetries {
					if r, e := rf.Reflect(stepCtx, trajectory, plan, reflections); e == nil && r != nil {
						bb.AddReflection(*r)
						reflections = append(reflections, *r)
						events.OnReflected(r, attempt, maxRetries)
						if r.SuggestedAction == "abort" {
							break
						}
					}
				}
				_ = errMsg
			}
			if !success {
				events.OnStepCompleted(step.ID, false, 0, "max retries exceeded")
				completed[step.ID] = orchestration.CompletedStep{StepID: step.ID, Error: fmt.Errorf("failed after retries")}
			}
		}
	}

	// ── 12. Aggregate + report ──
	finalOutput := orchestration.AggregateOutput(completed, plan, nil)
	fmt.Println("\n═══════════════════════════════════════════")
	fmt.Printf("Steps: %d/%d | Reflections: %d | Facts: %d\n", len(completed), len(plan.Steps), len(reflections), len(bb.GetFacts()))
	fmt.Println("\nFinal output:")
	fmt.Println(finalOutput)
	fmt.Println("\nFacts stored:")
	for _, f := range bb.GetFacts() {
		fmt.Printf("  [%s] %s\n", strings.Join(f.Keywords, ", "), trunc(f.Content, 80))
	}
	fmt.Println("═══════════════════════════════════════════")
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

func makePlannerPromptSet() planner.PromptSet {
	return planner.PromptSet{
		BasePrompt: `You are a task planning agent. Break the task into concrete steps.

Available tools:
AVAILABLE-TOOLS

Available skills:
AVAILABLE-SKILLS

Create at most MAX-STEPS steps. Each step needs clear acceptance criteria.
Use depends_on for ordering.

MODE-PREAMBLE

Output ONLY valid JSON:
MODE-JSON-EXAMPLE`,
		PlanPreamble:      "Break the task into sequential steps with clear deliverables.",
		MultiStepGuidance: "Each step should produce a verifiable artifact.",
	}
}

func planStepsToEvents(plan *orchestration.Plan) []orchestration.PlanStepEvent {
	events := make([]orchestration.PlanStepEvent, len(plan.Steps))
	for i, s := range plan.Steps {
		events[i] = orchestration.PlanStepEvent{
			ID:          s.ID,
			Summary:     s.Summary,
			Description: s.Description,
			DependsOn:   s.DependsOn,
		}
	}
	return events
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
