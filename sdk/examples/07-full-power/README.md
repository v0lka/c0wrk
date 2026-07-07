# Example 07 — Full-Power Agent

The "kitchen sink" example: every major SDK subsystem combined into one agent.

## What you will learn

- How to combine all SDK features in a single application
- Multi-provider LLM configuration with runtime model switching (Claude for planning/reflection, GPT-4o for execution)
- Custom + built-in + MCP tools in one registry
- Skills discovery, fact memory, and blackboard callbacks
- Full Plan → Execute → Reflect orchestration with events

## Subsystems exercised

| Subsystem          | Configuration                                    |
|--------------------|--------------------------------------------------|
| Multi-provider LLM | Anthropic (planning/reflection) + OpenAI (execution, if `OPENAI_API_KEY` is set) — runtime switching via `router.SetModel` |
| Custom tools       | `timestamp` tool (implements `tools.Tool`)       |
| Built-in tools     | read_file, write_file, edit_file, glob, …        |
| MCP integration    | `@modelcontextprotocol/server-filesystem` (stdio)|
| Event streaming    | `consoleEvents` — prints plan/step/tool events   |
| Human-in-the-loop  | `autoApproveHITL` — blocks `delete_directory`    |
| Planner            | `planner.Planner` with custom `PromptSet`        |
| Conductor          | `fw.NewConductor` — per-step ReAct execution     |
| Reflector          | `reflector.Reflector` — failure analysis + retry |
| Skills             | `skills.SkillManager` — discovers `go-testing`   |
| Fact memory        | `store_fact` / `search_facts` tools              |
| Compaction         | Sliding-window strategy with custom thresholds   |
| Blackboard         | `OnBlackboardChanged` callback for live updates  |

## Architecture

```
sdk.New(Config{
    LLM:         multi-provider,
    MCP:         filesystem server,
    Execution:   { MaxSteps, PreWarning, ToolCache },
    Compaction:  { sliding, 85/92/98% },
    HITL:        autoApproveHITL,
    OnBlackboardChanged: callback,
})
    │
    ├─ ToolRegistry: [custom] timestamp + [core] file tools + [mcp] filesystem tools
    ├─ SkillManager: discovers go-testing skill
    ├─ Planner: generates DAG from task
    ├─ Conductor: executes each step (ReAct loop)
    ├─ Reflector: analyzes failures → retry/replan/abort
    └─ Events: consoleEvents prints live trace
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

#### Runtime model switching

```go
// Two providers: Claude for planning/reflection, GPT-4o for execution.
router := fw.LLMRouter()
router.SetModel(ctx, "openai/gpt-4o")   // switch before execution
// … Conductor runs steps on GPT-4o …
router.SetModel(ctx, "claude-sonnet-4-5") // switch back for reflection
```

The `Framework` exposes the shared LLM router via `fw.LLMRouter()`. Because every LLM-calling component (Planner, Conductor, Reflector) routes through this single router, calling `SetModel` switches the active provider+model for all subsequent calls. The example uses Claude for planning and reflection (strong reasoning) and GPT-4o for step execution — switching before the execution loop and temporarily switching back during reflection. When `OPENAI_API_KEY` is unset, execution falls back to the single Anthropic model and switching is skipped.

#### Skills discovery

```go
skillMgr := skills.NewSkillManager([]string{skillsDir}, nil)
skillMgr.Scan()
discoveredSkills := skillMgr.List()
```

Skills are markdown files (`SKILL.md`) with YAML frontmatter. The `SkillManager` scans directories in priority order and parses each skill's metadata. Discovered skills are passed to the Planner so it can assign them to steps.

#### Fact memory

```go
registry.Register(builtins.NewStoreFactTool())
registry.Register(builtins.NewSearchFactsTool())
```

Facts are keyword-tagged pieces of information stored on the blackboard. Steps can `store_fact` to share findings and `search_facts` to retrieve them — enabling inter-step communication without passing large outputs through the context window.

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
    Strategy:          "sliding",
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

```bash
cd sdk/examples
go mod tidy          # first time only
cd 07-full-power
go run main.go
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
  [mcp:filesystem] read_file
  [mcp:filesystem] write_file
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
