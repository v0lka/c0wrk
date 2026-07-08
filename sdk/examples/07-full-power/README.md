# Example 07 — Full-Power Agent

The "kitchen sink" example: every major SDK subsystem combined into one agent.

This example is a **fluent-first hybrid**: the Framework is assembled with `fluent.New` and the orchestration runs as a single `fluent.Task` chain. Where fluent does not surface fine-grained control, **classic escapes** are used. This is the recommended pattern for real applications — fluent for the common path, classic API for advanced tuning.

| Concern                | Path      | How                                                    |
|------------------------|-----------|--------------------------------------------------------|
| Framework assembly     | **Fluent**| `fluent.New` + `.Providers`/`.MCPStdio`/`.Tools`/`.HITL` |
| Plan → Execute → Reflect | **Fluent**| `fluent.Task(...).Plan().Reflect().Models(...).Execute()` |
| Compaction / execution tuning | **Classic**| `.Config(base sdk.Config)` escape hatch |
| `OnBlackboardChanged`  | **Classic**| rides in the `WithConfig` base (no dedicated fluent option) |
| Skills discovery       | **Classic**| `skills.SkillManager` → passed to `fluent.Task.Skills` |
| Custom events sink     | **Classic**| `consoleEvents` (embeds `orchestration.NoopEvents`) → `fluent.Task.Events` |

## What you will learn

- How to combine all SDK features in a single application
- When to use the fluent façade vs. fall back to the classic API
- Multi-provider LLM configuration with runtime model switching (Claude for planning/reflection, GPT-4o for execution)
- Custom + built-in + MCP tools in one registry
- Skills discovery, fact memory, and blackboard callbacks
- Full Plan → Execute → Reflect orchestration with events

## Subsystems exercised

| Subsystem          | Fluent or classic | Configuration                                    |
|--------------------|-------------------|--------------------------------------------------|
| Multi-provider LLM | Fluent            | `.Providers(Anthropic + OpenAI?)`; switching via `fluent.Task.Models` |
| Custom tools       | Fluent            | `timestamp` tool registered via `.Tools(...)`      |
| Built-in tools     | Fluent            | `fluent.FileTools()` + `fluent.MemoryTools()` bundles |
| MCP integration    | Fluent            | `.MCPStdio(...)`              |
| Event streaming    | Classic escape    | `consoleEvents` (embeds `orchestration.NoopEvents`) → `fluent.Task.Events` |
| Human-in-the-loop  | Fluent            | `.HITL(autoApproveHITL)` (blocks `delete_directory`) |
| Planner            | Fluent            | `.Plan()` with `fluent.DefaultPromptSet`         |
| Conductor          | Fluent            | created internally by `fluent.Task`              |
| Reflector          | Fluent            | `.Reflect()` with `fluent.DefaultReflectorPrompt`|
| Skills             | Classic escape    | `skills.SkillManager` → `.Skills(discovered)`    |
| Fact memory        | Fluent            | `MemoryTools()` bundle + blackboard              |
| Compaction         | Classic escape    | `.Config(base)` carrying `CompactionConfig`      |
| Blackboard         | Classic escape    | `OnBlackboardChanged` in the `.Config(base)` base |

## Architecture

```
fluent.New().
    Config(base).               // classic escape: compaction, execution tuning, OnBlackboardChanged
    Providers(anthropic, openai?).
    MCPStdio("filesystem", …).
    Tools(timestamp + FileTools + MemoryTools).
    HITL(autoApproveHITL).AutoApprove().
    Build()
    │
    ├─ ToolRegistry: [custom] timestamp + [core] file/memory tools + [mcp] filesystem tools
    ├─ SkillManager: discovers go-testing skill   (classic — pre-execution)
    │
    └─ fluent.Task(...).Workspace(...).Skills(...).Events(consoleEvents)
           .Plan().Reflect().MaxRetries(2).Models(claude, gpt-4o?).Execute()
              │
              ├─ Planner: generates DAG (Claude)
              ├─ Conductor: executes each step (GPT-4o when available)
              ├─ Reflector: analyzes failures → retry/abort (Claude)
              └─ consoleEvents: prints live trace
```

## Code walkthrough

This example combines patterns from all previous examples. See each example's README for detailed explanations:

- **Multi-provider** → `LLMConfig.Providers` with two entries (example 01 + 07)
- **Custom tool** → `timestampTool` embedding `BaseTool` (example 02)
- **Events** → `consoleEvents` embedding `NoopEvents` (example 03)
- **HITL** → `autoApproveHITL` embedding `NoopHITLHandler` (example 04)
- **MCP** → `MCPConfig.Servers` with stdio transport (example 05)
- **Planner + Reflector** → full DAG execution loop (example 06)

### New concepts in this example

#### The hybrid: fluent façade + classic escapes

The Framework is built with `fluent.New`, but fields that have no dedicated fluent option (compaction tuning, `OnBlackboardChanged`) ride in a `WithConfig` base — a classic escape hatch that fluent layers its options on top of:

```go
base := sdk.Config{
    Execution:  sdk.ExecutionConfig{ /* tuning */ },
    Compaction: sdk.CompactionConfig{ /* thresholds */ },
    OnBlackboardChanged: func(ct string) { fmt.Println("blackboard:", ct) },
}
fw, _ := fluent.New().
    Config(base).             // escape hatch: advanced tuning
    Providers(providers...).
    MCPStdio("filesystem", "npx", "-y", "@modelcontextprotocol/server-filesystem", workspaceDir).
    FileTools().MemoryTools().
    Tools(allTools...).
    HITL(&autoApproveHITL{...}).
    AutoApprove().
    Build()
```

The orchestration cycle is one `fluent.Task` chain (replacing the ~80-line hand-rolled loop of example 06):

```go
result, err := fluent.Task(ctx, fw, task).
    Workspace(workspaceDir).
    Skills(discoveredSkills).
    Events(&consoleEvents{}).
    Plan().Reflect().MaxRetries(2).
    Models(plannerModel, executorModel). // runtime switching
    Execute()
```

#### Runtime model switching

`.Models(plannerModel, executorModel)` switches the shared LLM router between phases automatically: the planner and reflector run on the strong-reasoning model (Claude), while the Conductor's per-step ReAct loops run on the executor model (GPT-4o). Because every LLM-calling component routes through the single router, one call switches the active provider+model for all subsequent calls. The router is restored to the planning model after execution. When `OPENAI_API_KEY` is unset, `.Models(...)` is omitted and execution stays on the single Anthropic model.

#### Skills discovery (classic escape)

```go
skillMgr := skills.NewSkillManager([]string{skillsDir}, nil)
skillMgr.Scan()
discoveredSkills := skillMgr.List()
// passed to the planner via fluent.Task.Skills(discoveredSkills)
```

Skills are markdown files (`SKILL.md`) with YAML frontmatter. The `SkillManager` scans directories in priority order and parses each skill's metadata. Discovered skills are passed to the Planner (via `fluent.Task.Skills`) so it can assign them to steps. Discovery is pre-execution setup, so it stays in the classic API.

#### Fact memory

```go
fluent.New().FileTools().MemoryTools()
```

The `MemoryTools()` bundle registers `store_fact` and `search_facts`. Facts are keyword-tagged pieces of information stored on the blackboard; steps can share findings without passing large outputs through the context window. Stored facts are readable from `result.Blackboard.GetFacts()`.

#### Blackboard change callback

```go
OnBlackboardChanged: func(changeType string) {
    fmt.Printf("blackboard: %s\n", changeType)
}
```

Fires after every successful blackboard write (`plan`, `step_result`, `fact`, `reflection`). Useful for UI integration or audit logging.

#### Compaction configuration

```go
Compaction: sdk.CompactionConfig{
    Strategy:          "sliding_window",
    PredictivePercent: 85,
    WarningPercent:    92,
    EmergencyPercent:  98,
},
```

Controls when the context window compacts. As the conversation grows, the `ContextWindow` checks fill percentage and triggers compaction at the configured thresholds.

## Prerequisites

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
# Optional — enables multi-provider:
export OPENAI_API_KEY="sk-..."
# Optional — MCP filesystem server needs Node.js:
node --version
```

## Run

This example is a single fluent-first hybrid `main.go` (no build tag):

```bash
cd sdk/examples
go mod tidy          # first time only
cd 07-full-power
go run .
```

## Expected output

```
Workspace: /tmp/sp4rk-example-07-123456
Skills dir: /tmp/sp4rk-example-07-123456/.agents/skills

Active LLM: anthropic/claude-sonnet-4-5 (provider: anthropic)
Runtime model switching enabled: claude-sonnet-4-5 → openai/gpt-4o for execution

Available tools:
  [core] timestamp
  [core] read_file
  [core] write_file
  [core] edit_file
  [core] list_directory
  [core] glob
  [core] create_directory
  [core] store_fact
  [core] search_facts
  [core] finish
  [filesystem] search_files
  [filesystem] get_file_info
  …

Discovered skills: 1
  • go-testing: Use when writing Go tests with the standard testing package.

📋 Planning...
Plan: 4 steps
   • step_1: Create project directory
   • step_2: Write main.go with timestamp
   • step_3: Verify file contents
   • step_4: Store summary fact

🔄 Switched executor to openai/gpt-4o (provider: openai)

▶ step_1: Create project directory
  ▶ step 1
    🔧 create_directory(…) [core]
    ✅ result (28 chars)
  📝 blackboard: step_result
  ✅ step_1 done

▶ step_2: Write main.go with timestamp
  ▶ step 1
    🔧 timestamp(…) [core]
    ✅ result (25 chars)
  ▶ step 2
    🔧 write_file(…) [core]
    ✅ result (18 chars)
  🏁 finish @3: Wrote main.go…
  ✅ step_2 done

…

═══════════════════════════════════════════
Steps: 4/4 | Reflections: 0 | Facts: 1
Models: planning=claude-sonnet-4-5 | execution=openai/gpt-4o

Final output:
Created myproject/main.go that prints the current timestamp and a greeting…

Facts stored:
  [go-project, main-go, timestamp] Created myproject/main.go printing timestamp + greeting
═══════════════════════════════════════════
```

## Summary

This example demonstrates the full power of the sp4rk Agent SDK. A real application would typically use a subset of these features — but the SDK is designed so they all compose cleanly when you need them.
