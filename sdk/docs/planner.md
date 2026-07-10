# Planner

The `planner` package generates DAG execution plans from user tasks. A `Planner` takes a free-text task and a set of available tools, then produces an `*orchestration.Plan` — a directed acyclic graph of `PlanStep`s that an orchestrator can execute step by step.

The planner supports two strategies:

- **Direct planning** — a single LLM call produces the plan. Used for simple or general-domain tasks, or when no exploration tools are configured.
- **Informed (exploration) planning** — the planner first runs a short ReAct loop with read-only exploration tools to gather context about the codebase, then produces a plan informed by what it found. Used for complex, code-domain tasks.

```go
import "github.com/v0lka/sp4rk/planner"
```

---

## Table of contents

- [Planner](#planner-1)
  - [NewPlanner](#newplanner)
- [Config](#config)
  - [DefaultConfig](#defaultconfig)
- [PromptSet](#promptset)
  - [Placeholder system](#placeholder-system)
- [AgentProfile](#agentprofile)
- [Methods](#methods)
  - [Plan](#plan)
  - [Replan](#replan)
  - [PlanContinuation](#plancontinuation)
- [Exploration vs direct planning](#exploration-vs-direct-planning)
- [Single-step vs multi-step mode](#single-step-vs-multi-step-mode)
- [Supporting types](#supporting-types)
- [Examples](#examples)
  - [Standalone planner](#standalone-planner)
  - [Full-power planner with skills and exploration](#full-power-planner-with-skills-and-exploration)

---

## Planner

```go
type Planner struct {
    llm agent.LLMCaller
    Cfg Config // public so the builder can wire dependencies after creation
}
```

`Planner` generates DAG execution plans for complex tasks. The `Cfg` field is exported so that a builder can wire additional dependencies after construction.

### NewPlanner

```go
func NewPlanner(caller agent.LLMCaller, cfg Config) (*Planner, error)
```

Creates a new Planner with the given LLM caller and configuration. Returns an error if `caller` is `nil` (a required dependency). If `MaxExploreSteps` is zero or negative, it defaults to `7`.

```go
pl, err := planner.NewPlanner(router, planner.DefaultConfig())
if err != nil {
    log.Fatal(err)
}
```

---

## Config

`Config` holds all configuration for the Planner. It separates stable SDK interfaces from framework-specific wiring (context-extraction functions, formatters, and appenders that the host application provides).

```go
type Config struct {
    Prompts PromptSet

    // Injected context functions (framework-specific)
    DomainFromContext       func(ctx context.Context) string
    ComplexityFromContext   func(ctx context.Context) int
    UserSkillsFromContext   func(ctx context.Context) []string
    FormatSkillList         func(ctx context.Context, availableSkills []skills.SkillDescriptor) string
    FormatWorkspacePath     func(ctx context.Context) string
    AppendContextSections   func(ctx context.Context, base string) string

    // Tool configuration
    ToolRegistry     ToolRegistry
    PlannerToolNames map[string]bool

    // Model resolution
    ModelRegistry *llm.ModelRegistry
    Model         string

    // Optional dependencies
    Logger          *slog.Logger
    Emitter         Events
    TokenCounter    llm.TokenCounter
    ContextFactory  ContextManagerFactory
    CallerForStep   func(cm agent.ContextManager, stepID string) agent.LLMCaller
    MaxExploreSteps int
    ReasoningEffort string
}
```

| Field | Purpose |
| --- | --- |
| `Prompts` | All parameterizable prompt templates. See [PromptSet](#promptset). |
| `DomainFromContext` | Extracts the routing domain (`"code"`, `"research"`, `"general"`) from the context. Drives the exploration-vs-direct decision. |
| `ComplexityFromContext` | Extracts the routing complexity (an integer) from the context. Low complexity with a general domain selects direct planning. |
| `UserSkillsFromContext` | Extracts explicitly user-activated skill names from the context. |
| `FormatSkillList` | Formats available skills for prompt injection. Must handle `nil`/empty. |
| `FormatWorkspacePath` | Returns the workspace instruction block, or `""`. |
| `AppendContextSections` | Appends environment/vector/agents/skills sections to the base prompt. |
| `ToolRegistry` | Provides tools for the exploration executor and tool listing. Must satisfy the `ToolRegistry` interface (combines `agent.ToolExecutor` and `ToolLister`). |
| `PlannerToolNames` | Set of tool names allowed for planner exploration. **Empty means no exploration tools are available** — the planner falls back to direct planning. |
| `ModelRegistry` | Resolves model metadata (family, context window) for the exploration executor. |
| `Model` | Active LLM model name for family resolution. |
| `Logger` | Structured logger. Defaults to `slog.Default()` when nil. |
| `Emitter` | Event sink for planner lifecycle (`ServiceWithMeta`). Must satisfy the planner's `Events` interface (embeds `agent.Events` + `ServiceWithMeta`). Must be nil-safe. |
| `TokenCounter` | Token counter for the exploration executor. Defaults to `llm.NewSimpleTokenCounter()` when nil. |
| `ContextFactory` | Creates a `ContextManager` for the exploration loop. When nil, the planner falls back to direct planning. |
| `CallerForStep` | Returns a step-local `LLMCaller` for the given context manager and step ID. When set, the exploration executor uses this instead of the shared caller, ensuring independent context trackers. |
| `MaxExploreSteps` | Step budget for the exploration loop. Defaults to `7`. |
| `ReasoningEffort` | Reasoning effort applied to the exploration executor and plan-generation calls. |

### DefaultConfig

```go
func DefaultConfig() Config
```

Returns a `Config` with sensible defaults for **standalone use**. It provides no-op context-extraction functions (empty domain, zero complexity), a `FormatSkillList` that returns `"None"`, and `MaxExploreSteps` of `7`. Override specific fields as needed — most importantly, set `Prompts`, `Model`, and (for exploration) `ToolRegistry` + `PlannerToolNames` + `ContextFactory`.

```go
cfg := planner.DefaultConfig()
cfg.Prompts = myPromptSet
cfg.Model = "claude-sonnet-4-5"
cfg.ToolRegistry = registry
cfg.PlannerToolNames = map[string]bool{"read_file": true, "list_directory": true, "ripgrep": true}
cfg.ContextFactory = contextFactory
```

---

## PromptSet

`PromptSet` holds all parameterizable prompt templates. The host application injects its own prompts through this struct.

```go
type PromptSet struct {
    // Base templates
    BasePrompt     string
    InformedPrompt string
    ReplanPrompt   string

    // Mode sections (multi-step, single-step, continuation variants)
    PlanPreamble                   string
    SingleStepPreamble             string
    MultiStepToT                   string
    SingleStepToT                  string
    MultiStepGuidance              string
    SingleStepGuidance             string
    ContinuationPreamble           string
    ContinuationIncompletePreamble string
    ContinuationSingleStep         string

    // Shared sections
    DomainAssignment string
    AgentProfiles    string
    ExtraSections    string

    // FamilyPrompt returns the prompt adapter for the given agent role and model family.
    FamilyPrompt func(agent, family string) string

    // VerificationMandate is appended to all planner prompts.
    VerificationMandate string
}
```

| Field | Description |
| --- | --- |
| `BasePrompt` | The base template for direct and continuation planning. Uses placeholders substituted at call time. |
| `InformedPrompt` | The base template for exploration-based planning (used when exploration tools are available). |
| `ReplanPrompt` | The template for `Replan`, which receives the original plan, completed steps, the failed step, and the reflection. |
| `PlanPreamble` | Multi-step mode preamble (substituted as `MODE-PREAMBLE`). |
| `SingleStepPreamble` | Single-step mode preamble. |
| `MultiStepToT` / `SingleStepToT` | Tree-of-thought reasoning sections (substituted as `MODE-TOT`). |
| `MultiStepGuidance` / `SingleStepGuidance` | Mode-specific guidance (substituted as `MODE-GUIDANCE`). |
| `ContinuationPreamble` | Preamble for follow-up plans after a completed task. |
| `ContinuationIncompletePreamble` | Preamble used when the prior task was **not** fully completed — instructs the planner to finish only remaining work and not re-execute completed steps. |
| `ContinuationSingleStep` | Preamble for single-step continuations. |
| `DomainAssignment` | Section assigning domains to steps (substituted as `DOMAIN-ASSIGNMENT`). |
| `AgentProfiles` | Section describing available agent profiles (substituted as `AGENT-PROFILES`). |
| `ExtraSections` | Additional sections appended to every mode (substituted as `MODE-EXTRA-SECTIONS`). |
| `FamilyPrompt` | Function returning a family-specific prompt adapter for a given agent role and model family. Returns `""` when no adaptation exists. |
| `VerificationMandate` | Appended to all planner prompts. |

### Placeholder system

Prompt templates use uppercase placeholders that the planner substitutes at call time. This keeps prompts data-driven and avoids hand-building strings.

**Plan-mode placeholders** (used by `BasePrompt` / `InformedPrompt`):

| Placeholder | Replaced with |
| --- | --- |
| `AVAILABLE-TOOLS` | Grouped list of available tool descriptors. |
| `AVAILABLE-SKILLS` | Formatted skill list (via `FormatSkillList`). |
| `MAX-STEPS` | The mode's step cap (`"10"` for multi-step, `"1"` for single-step). |
| `MODE-PREAMBLE` | The active mode's preamble. |
| `MODE-TOT` | The active mode's tree-of-thought section. |
| `MODE-GUIDANCE` | The active mode's guidance section. |
| `MODE-EXTRA-SECTIONS` | Extra sections for the active mode. |
| `MODE-TAIL` | The mode tail (multi-step includes formatted reflections). |
| `MODE-JSON-EXAMPLE` | The JSON example for the active mode. |
| `DOMAIN-ASSIGNMENT` | Domain assignment section. |
| `AGENT-PROFILES` | Agent profiles section. |
| `WORKSPACE-PATH` | Workspace instruction block. |
| `RECENT-CONVERSATION` | Formatted prior conversation (for first-message plans). |

**Replan placeholders** (used by `ReplanPrompt`):

| Placeholder | Replaced with |
| --- | --- |
| `ORIGINAL-PLAN` | JSON of the original plan. |
| `COMPLETED-STEPS` | List of completed step IDs and outputs. |
| `FAILED-STEP` | The failed step ID, error, and output. |
| `CURRENT-REFLECTION` | The reflection on the failure. |
| `PREVIOUS-SESSION-REFLECTIONS` | Earlier reflections from the session. |

**Continuation placeholders** (used by `BasePrompt` in continuation mode):

| Placeholder | Replaced with |
| --- | --- |
| `ORIGINAL-REQUEST` | The original user request. |
| `COMPLETED-PLAN-SUMMARY` | The existing plan with per-step status (`[COMPLETED]`/`[FAILED]`/`[PENDING]`). |
| `TERMINAL-STEPS` | Comma-separated IDs of terminal steps (used as `depends_on` for continuation steps). |
| `RECENT-CONVERSATION` | Formatted prior conversation. |

A minimal `BasePrompt` using the core placeholders:

```go
Prompts: planner.PromptSet{
    BasePrompt: `You are a task planning agent. Break the task into concrete steps.

## Available Tools
AVAILABLE-TOOLS

## Available Skills
AVAILABLE-SKILLS

Create at most MAX-STEPS steps. Use "depends_on" for ordering.

MODE-PREAMBLE

Output ONLY valid JSON:
MODE-JSON-EXAMPLE`,
    PlanPreamble:      "Break the task into sequential steps with clear deliverables.",
    MultiStepGuidance: "Each step should produce a verifiable artifact.",
},
```

---

## AgentProfile

`AgentProfile` defines a specialised agent role for plan-step execution. It is carried in a `PlanStep`'s `Profile` field (type `any`).

```go
type AgentProfile struct {
    Role           string   `json:"role"`
    SystemPrompt   string   `json:"system_prompt,omitempty"`
    AllowedTools   []string `json:"allowed_tools,omitempty"`
    Skills         []string `json:"skills,omitempty"`
    MaxSteps       int      `json:"max_steps,omitempty"`
    Domain         string   `json:"domain,omitempty"`
    KeepLastN      int      `json:"keep_last_n,omitempty"`
    ProtectedTools []string `json:"protected_tools,omitempty"`
}
```

| Field | Description |
| --- | --- |
| `Role` | Controls system-prompt customisation. Common values: `"researcher"`, `"coder"`, `"tester"`, `"executor"` (default). |
| `SystemPrompt` | Optional role-specific prompt override. |
| `AllowedTools` | Subset of available tools for this step. Empty means all tools. |
| `Skills` | Subset of router-matched skills. Empty means use the full task-scope pool. |
| `MaxSteps` | Per-agent step budget. `0` uses the default. |
| `Domain` | `"code"`, `"research"`, or `"general"` — affects compaction strategy. |
| `KeepLastN` | Per-step tool-output pruning override. `0` uses the role default. |
| `ProtectedTools` | Per-step protected-tools override. `nil` uses the role default. |

The planner emits profiles in the JSON example so the LLM can attach them to steps. Downstream, the host application reads the profile to configure each step's executor (tool set, compaction, budget).

```json
{
  "id": "step_1",
  "summary": "Implement auth middleware",
  "description": "What: ...\nHow: ...\nAcceptance Criteria:\n- ...",
  "depends_on": [],
  "parallelizable": true,
  "estimated_tools": ["read_file", "write_file", "edit_file"],
  "profile": {
    "role": "coder",
    "allowed_tools": ["read_file", "write_file", "edit_file", "ripgrep", "glob"],
    "skills": ["go-testing"],
    "domain": "code",
    "keep_last_n": 5,
    "protected_tools": ["store_fact", "search_facts"]
  }
}
```

---

## Methods

### Plan

```go
func (p *Planner) Plan(
    ctx context.Context,
    task string,
    availableTools []tools.ToolDescriptor,
    reflections []orchestration.Reflection,
    availableSkills []skills.SkillDescriptor,
    singleStep bool,
    conversationHistory []llm.Message,
) (*orchestration.Plan, error)
```

Generates an execution plan for the given task.

| Parameter | Description |
| --- | --- |
| `ctx` | Context carrying the workspace path and routing metadata (domain, complexity). |
| `task` | The free-text user task. |
| `availableTools` | Tool descriptors the executor will have. Injected into the prompt as `AVAILABLE-TOOLS`. |
| `reflections` | Prior reflections to inform the plan (e.g. after a replan). Injected into the multi-step tail. |
| `availableSkills` | Skill descriptors available to the task. Injected as `AVAILABLE-SKILLS`. |
| `singleStep` | When `true`, forces single-step mode (max one step). |
| `conversationHistory` | Prior user/assistant exchanges so plans for follow-up requests are generated with full dialogue context. May be `nil` for genuinely first messages. |

The method selects direct or exploration planning based on the domain, complexity, and available planner tools (see [Exploration vs direct planning](#exploration-vs-direct-planning)). In single-step mode, if the LLM returns multiple steps, the plan is truncated to the first step.

```go
plan, err := pl.Plan(ctx, task, availableTools, nil, discoveredSkills, false, nil)
if err != nil {
    return err
}
for _, step := range plan.Steps {
    fmt.Printf("  • %s: %s (depends on: %v)\n", step.ID, step.Summary, step.DependsOn)
}
```

### Replan

```go
func (p *Planner) Replan(
    ctx context.Context,
    originalPlan *orchestration.Plan,
    completedSteps []orchestration.CompletedStep,
    failedStep orchestration.CompletedStep,
    reflection *orchestration.Reflection,
    sessionReflections []orchestration.Reflection,
    availableSkills []skills.SkillDescriptor,
) (*orchestration.Plan, error)
```

Generates a revised plan after a step failure. The replan prompt receives the original plan, the completed steps, the failed step, the reflection on the failure, and earlier session reflections — giving the LLM full context to produce a corrected plan.

Use this when a `Reflection.SuggestedAction` is `"replan"` (the plan itself is wrong). After replanning, use `BuildCarryForward` to preserve outputs from steps that remain valid in the new plan.

```go
newPlan, err := pl.Replan(ctx, plan, completedList, failedStep, reflection, reflections, skills)
if err != nil {
    return err
}
carried := orchestration.BuildCarryForward(completedList, newPlan)
// seed `carried` into the completed map before executing newPlan
```

### PlanContinuation

```go
func (p *Planner) PlanContinuation(
    ctx context.Context,
    originalRequest string,
    existingPlan *orchestration.Plan,
    completedSteps []orchestration.CompletedStep,
    newMessage string,
    availableTools []tools.ToolDescriptor,
    availableSkills []skills.SkillDescriptor,
    singleStep bool,
    conversationHistory []llm.Message,
    taskComplete bool,
) (*orchestration.Plan, error)
```

Generates a continuation plan for follow-up requests after a task has (fully or partially) completed.

| Parameter | Description |
| --- | --- |
| `originalRequest` | The original user request. |
| `existingPlan` | The plan from the prior turn. |
| `completedSteps` | Steps completed in the prior turn. |
| `newMessage` | The follow-up user message. |
| `availableTools` | Tool descriptors available now. |
| `availableSkills` | Skills available now. |
| `singleStep` | Force single-step mode. |
| `conversationHistory` | Prior dialogue for context. |
| `taskComplete` | Whether the prior task fully completed. When `false`, the `ContinuationIncompletePreamble` is used so the planner finishes only remaining work and does not re-execute completed steps. |

Continuation steps depend on the prior plan's **terminal steps** (steps no other step depends on), chaining new work onto completed work.

```go
contPlan, err := pl.PlanContinuation(
    ctx, originalRequest, plan, completedList, "Now add unit tests",
    availableTools, skills, false, history, true,
)
```

---

## Exploration vs direct planning

`Plan` chooses its strategy automatically:

```
domain == "general" && complexity < 4   →  direct planning
len(plannerTools) == 0                  →  direct planning
otherwise                               →  exploration planning
```

- **Direct planning** makes a single LLM call with the `BasePrompt` and parses the JSON plan. Fast and sufficient for simple or general-domain tasks.
- **Exploration planning** runs a short ReAct loop (bounded by `MaxExploreSteps`) using the tools named in `PlannerToolNames` — typically read-only tools like `read_file`, `list_directory`, `ripgrep`, `glob`. The exploration output is then parsed into a plan, and `Plan.ExplorationContext` is populated with a summary of what was explored.

If the exploration executor fails, produces no output, or returns unparseable JSON, the planner **falls back to direct planning** automatically. If `ContextFactory` is nil, exploration is skipped and direct planning is used.

To enable exploration, configure:

```go
cfg.ToolRegistry = registry
cfg.PlannerToolNames = map[string]bool{
    "read_file": true, "list_directory": true,
    "ripgrep": true, "glob": true,
}
cfg.ContextFactory = contextFactory
cfg.DomainFromContext = func(ctx context.Context) string { return "code" }
cfg.ComplexityFromContext = func(ctx context.Context) int { return 5 }
```

---

## Single-step vs multi-step mode

- **Multi-step mode** (default, `singleStep = false`): the planner may produce up to 10 steps forming a DAG. Use for complex tasks that benefit from decomposition.
- **Single-step mode** (`singleStep = true`): the planner produces at most one step. If the LLM returns multiple steps, the plan is truncated to the first. Use for simple tasks or when you want the agent to act without an explicit plan structure.

The mode controls which preamble, tree-of-thought, guidance, and JSON example are substituted into the prompt.

---

## Supporting types

### ToolLister

```go
type ToolLister interface {
    List() []tools.ToolDescriptor
}
```

Provides access to available tool descriptors. Tool registries implement this.

### Events (planner)

```go
type Events interface {
    agent.Events
    ServiceWithMeta(content string, meta map[string]any)
}
```

The minimal event interface the planner needs. It embeds `agent.Events` and adds `ServiceWithMeta` for planner-lifecycle notifications. Implementations must be nil-safe.

### ToolRegistry (planner)

```go
type ToolRegistry interface {
    agent.ToolExecutor
    ToolLister
}
```

The interface the planner needs for tool operations: executing tools during exploration and listing available tools.

### ContextManagerFactory (planner)

```go
type ContextManagerFactory func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) agent.ContextManager
```

Creates a `ContextManager` for the exploration loop.

---

## Examples

### Standalone planner

A minimal planner that produces a multi-step plan with direct planning (no exploration tools configured):

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/v0lka/sp4rk"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/planner"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

func main() {
	fw, err := sp4rk.New(sp4rk.Config{
		LLM: sp4rk.LLMConfig{
			Providers: []llm.ProviderEntry{{
				Name:         "anthropic",
				ProviderType: "anthropic",
				APIKey:       os.Getenv("ANTHROPIC_API_KEY"),
				Models:       []string{"claude-sonnet-4-5"},
			}},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = fw.Shutdown() }()

	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(agent.NewFinishTool())

	cfg := planner.DefaultConfig()
	cfg.Prompts = planner.PromptSet{
		BasePrompt: `You are a task planning agent. Break the task into steps.

Available tools:
AVAILABLE-TOOLS

Create at most MAX-STEPS steps. Use "depends_on" for ordering.

MODE-PREAMBLE

Output ONLY valid JSON:
MODE-JSON-EXAMPLE`,
		PlanPreamble:      "Break the task into sequential steps with clear deliverables.",
		MultiStepGuidance: "Each step should produce a verifiable artifact.",
	}
	cfg.Model = "claude-sonnet-4-5"

	pl, err := planner.NewPlanner(fw.LLMRouter(), cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx := tools.WithWorkspacePath(context.Background(), "/tmp/work")
	plan, err := pl.Plan(ctx, "Create a Go project that prints hello",
		registry.List(), nil, nil, false, nil)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Plan: %d steps\n", len(plan.Steps))
	for _, s := range plan.Steps {
		fmt.Printf("  • %s: %s (depends on: %v)\n", s.ID, s.Summary, s.DependsOn)
	}
}
```

### Full-power planner with skills and exploration

This example (adapted from the SDK's example 07) shows a planner configured with skills discovery, exploration tools, and a custom event sink. It is the "kitchen sink" configuration exercising the planner at maximum capacity.

```go
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
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/planner"
	"github.com/v0lka/sp4rk/skills"
	"github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// consoleEvents implements planner.Events.
type consoleEvents struct {
	agent.NoopEvents
}

func (e *consoleEvents) ServiceWithMeta(content string, meta map[string]any) {
	fmt.Printf("  📣 %s %v\n", content, meta)
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

func main() {
	workspaceDir, _ := os.MkdirTemp("", "example-*")
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	// Seed a skills directory.
	skillsDir := filepath.Join(workspaceDir, ".agents", "skills")
	_ = os.MkdirAll(skillsDir, 0o755)
	_ = os.WriteFile(filepath.Join(skillsDir, "go-testing", "SKILL.md"),
		[]byte("---\nname: go-testing\ndescription: Use when writing Go tests.\n---\n# Go Testing\n"),
		0o644)

	fw, err := sp4rk.New(sp4rk.Config{
		LLM: sp4rk.LLMConfig{
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
		log.Fatal(err)
	}
	defer func() { _ = fw.Shutdown() }()

	registry := fw.ToolRegistry()
	registry.Register(builtins.NewReadFileTool())
	registry.Register(builtins.NewWriteFileTool())
	registry.Register(builtins.NewEditFileTool())
	registry.Register(builtins.NewListDirectoryTool())
	registry.Register(builtins.NewGlobTool())
	registry.Register(builtins.NewCreateDirectoryTool())
	registry.Register(builtins.NewStoreFactTool())
	registry.Register(builtins.NewSearchFactsTool())
	registry.Register(agent.NewFinishTool())

	// Discover skills.
	skillMgr := skills.NewSkillManager([]string{skillsDir}, nil)
	_ = skillMgr.Scan()
	discoveredSkills := skillMgr.List()

	// Configure the planner with exploration tools and skills.
	cfg := planner.DefaultConfig()
	cfg.Prompts = makePlannerPromptSet()
	cfg.Model = "claude-sonnet-4-5"
	cfg.Emitter = &consoleEvents{}
	cfg.ToolRegistry = registry
	cfg.PlannerToolNames = map[string]bool{
		"read_file": true, "list_directory": true, "glob": true,
	}
	cfg.DomainFromContext = func(context.Context) string { return "code" }
	cfg.ComplexityFromContext = func(context.Context) int { return 5 }

	pl, err := planner.NewPlanner(fw.LLMRouter(), cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx := tools.WithWorkspacePath(context.Background(), workspaceDir)
	task := fmt.Sprintf("In %s, create a Go project that prints hello and store a fact about it.", workspaceDir)

	plan, err := pl.Plan(ctx, task, registry.List(), nil, discoveredSkills, false, nil)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("\nPlan: %d steps\n", len(plan.Steps))
	for _, s := range plan.Steps {
		fmt.Printf("  • %s: %s\n", s.ID, s.Summary)
	}
	if plan.ExplorationContext != "" {
		fmt.Printf("\nExploration context: %s\n", plan.ExplorationContext)
	}

	// Convert to events for an orchestrator.
	events := make([]orchestration.PlanStepEvent, len(plan.Steps))
	for i, s := range plan.Steps {
		events[i] = orchestration.PlanStepEvent{
			ID: s.ID, Summary: s.Summary, Description: s.Description, DependsOn: s.DependsOn,
		}
	}
	_ = events
}
```

The key configuration points for exploration are `ToolRegistry`, `PlannerToolNames`, `ContextFactory`, and the context-extraction functions that report a code domain and sufficient complexity. With those set, the planner explores the workspace before generating an informed plan.
