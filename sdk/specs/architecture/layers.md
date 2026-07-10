# Layers

## Context

sp4rk is a reusable agent-execution engine. It is organized as a single Go module (`github.com/v0lka/sp4rk`) whose packages fall into a strict dependency hierarchy. Import direction flows downward — orchestration packages depend on agent and primitive packages, never the reverse. Respecting this hierarchy keeps each package focused and prevents the import cycles that would otherwise make the engine unmaintainable.

## Layer Diagram

```
┌──────────────────────────────────────────────────────────────────────┐
│  github.com/v0lka/sp4rk   (root)                                      │
│    Framework, Config, Execute, NewConductor, RunF                     │
│    Entry point; owns shared infrastructure (LLM router, tool          │
│    registry, MCP gateway, tool cache); creates per-task conductors    │
└──────────────────────────────────┬───────────────────────────────────┘
                                   │ Go imports
                                   ▼
┌──────────────────────────────────────────────────────────────────────┐
│  orchestration   orchestration.Conductor, Blackboard, DAG,            │
│                   Checkpointer                                        │
│  planner         planner.Planner (Plan/Replan)                        │
│  agent/reflector agent/reflector.Reflector (failure analysis)         │
│  agent/router    agent/router.Router (request classification)         │
└──────────────────────────────────┬───────────────────────────────────┘
                                   │ Go imports
                                   ▼
┌──────────────────────────────────────────────────────────────────────┐
│  agent           Executor (ReAct loop), RunSubAgent, FinishTool,      │
│                   Events, HITLHandler, ToolResultCache                │
└──────────────────────────────────┬───────────────────────────────────┘
                                   │ Go imports
                                   ▼
┌──────────────────────────────────────────────────────────────────────┐
│  llm             Router, providers (OpenAI, Anthropic, Responses API),│
│                   ModelRegistry, token counting                       │
│  tools           ToolRegistry, Tool/ToolJudger interfaces, judge,     │
│                   symlink gate, param manager                          │
│  tools/builtins  file/shell/search built-in tools                     │
│  tools/mcp       MCP gateway and dynamic tools                        │
└──────────────────────────────────┬───────────────────────────────────┘
                                   │ consumed across layers
                                   ▼
┌──────────────────────────────────────────────────────────────────────┐
│  memory   ContextWindow, compaction strategies (sliding/summary/      │
│           hierarchical), Steps                                       │
│  prompt   PromptBuilder, sampling helpers                            │
│  skills   SkillManager, parser, resource tool                        │
│  security untrusted-content wrapping (prompt-injection defense)      │
│  embedding Embedder, chunker, ONNX runtime, tokenizer                │
│  pathutil pure path algorithmic primitives                           │
│  strutil  pure string utilities                                      │
└──────────────────────────────────────────────────────────────────────┘
```

## Import Rules

| Package                    | May Import                                                                                  | Must NOT Import                          |
| -------------------------- | ------------------------------------------------------------------------------------------- | ---------------------------------------- |
| root `sp4rk`               | `agent`, `llm`, `memory`, `orchestration`, `tools`, `tools/mcp`, `skills`, `embedding`, `prompt`, `security` | External libs only (no upward cycle)     |
| `orchestration`            | `agent`, `llm`, `tools`, `skills`                                                           | `planner`, root, `backend`-equivalents   |
| `planner`                  | `agent`, `llm`, `orchestration`, `prompt`, `skills`, `tools`                                | root                                     |
| `agent`                    | `llm`, `tools`                                                                              | `orchestration`, `planner`, root         |
| `agent/reflector`          | `agent`, `llm`, `orchestration`, `prompt`, `tools`                                          | `planner`, root                          |
| `agent/router`             | `agent`, `llm`, `prompt`, `tools`                                                           | `orchestration`, `planner`, root         |
| `memory`                   | `agent`, `llm`, `prompt`, `security`, `strutil`                                             | `tools`, `orchestration`, root           |
| `llm`                      | External libs only                                                                          | Any engine package                       |
| `tools`                    | `llm`, `pathutil`, `strutil`, `tools/internal/judge_prompts`                                | `agent`, `memory`, `orchestration`, root |
| `tools/builtins`, `tools/mcp` | `tools` (and through it, `llm`, `pathutil`)                                              | siblings via the agent/orchestration layer |
| `prompt`, `skills`, `security`, `embedding`, `pathutil`, `strutil` | leaves / near-leaves only                                       | `tools`, `agent`, `orchestration`, root  |

> **Module boundary.** sp4rk is a standalone Go module. It cannot import any host-application package — an embedding application imports sp4rk, never the reverse. The import prohibitions on upward engine layers are enforced both by convention and by the import cycles they would otherwise create.

> **Leaf discipline.** `llm` and `tools` are intentionally near-leaf dependencies. They depend on each other (`tools` → `llm`) and on pure utilities only. Keeping them free of orchestration concerns is what lets the `Executor` consume them through small interfaces without dragging in the whole engine.

## Layer Responsibilities

### Root — Entry Point & Shared Infrastructure

The root package (`github.com/v0lka/sp4rk`) owns the `Framework`: a container that constructs and reuses shared infrastructure (the `llm.Router`, the `tools.ToolRegistry`, the MCP gateway, the tool-result cache) and produces per-task conductors. `Framework.Execute` is the convenience path that spins up a `Conductor` and a `MapBlackboard` for one task; `Framework.NewConductor` builds a reusable conductor for repeated use. `Config` is the single point where a host application wires its callbacks (events emitter, confirm function, HITL handler, model selection). `RunF` provides a lower-level run helper.

### Orchestration Layer

Coordinates multi-step tasks across the engine.

- **`orchestration`** — the `Conductor` runs a single task end-to-end: it resolves model metadata, builds the system prompt, creates a `ContextManager` and an `Executor`, and drives the Plan&Execute loop. Holds the `Blackboard` (thread-safe shared task state), the `DAG` of plan steps, the `Checkpointer` for persistence, and `PersistentBlackboard`/`RestoreBlackboard` for state recovery.
- **`planner`** — `Planner.Plan` generates a `Plan` (a DAG of `PlanStep` values with summaries, descriptions, `DependsOn` dependencies, parallelism hints, estimated tools, and optional agent profiles); `Replan`/`PlanContinuation` regenerate plans accounting for completed steps and failures.
- **`agent/reflector`** — `Reflector.Reflect` analyzes an execution trajectory and prior reflections, then returns a `Reflection` with root cause, hypotheses, and a suggested action: `retry`, `replan`, or `abort`.
- **`agent/router`** — `Router.Route` classifies a user request with a single LLM call into `Domain` (`code`/`research`/`general`/`mixed`), `Complexity` (1–5), matched skills, and a needs-clarification flag. The decision drives compaction strategy selection and planner mode.

### Agent Layer

**`agent`** — the `Executor` runs the ReAct loop (Reason → Act → Observe). Each iteration is a `Step` capturing the model's thought, the tool call, the observation, error/untrusted flags, and token usage. The executor manages the step budget, circuit breakers (consecutive identical calls, parse errors, truncation, fruitless results), the `ToolResultCache`, and the `<untrusted-content>` trust flag. `RunSubAgent` launches a separate executor on its own goroutine with its own `ContextManager` for parallel plan steps. The `FinishTool` signals task completion; the `Events` interface streams lifecycle events (`StepStart`, `Thought`, `ToolCall`, `ToolResult`, `AssistantChunk`, `SubAgentLaunch`, `ContextCompaction`, `Finishing`, etc.); `HITLHandler` provides human-in-the-loop hooks.

### Primitives Layer

**`llm`** — provider abstraction. The `Router` routes a `ChatRequest` to the active provider/model under a `sync.RLMutex`. Providers implement OpenAI Chat Completions, OpenAI Responses API, and Anthropic Messages. `ModelRegistry` catalogs models with metadata; `tokencount` estimates usage; `reasoning` handles chain-of-thought extraction.

**`tools`** — the `ToolRegistry` holds and executes tools. Defines the `Tool` interface, the `ToolPolicy` enum, the optional `ToolJudger` interface, `ConfirmFunc`/`ConfirmationRequest`/`ConfirmationResponse` for confirmation flow, the `ToolJudge` (LLM-backed advisory safety evaluation), the symlink gate, and the param manager. `tools/builtins` ships file, shell (`bash_exec`), search (`ripgrep`, `glob`), web, and agent-infrastructure tools. `tools/mcp` proxies external MCP servers and exposes their tools dynamically.

### Supporting Packages

- **`memory`** — `ContextWindow` manages the LLM context window (`BuildPrompt`, `AddStep`, `Compact`, `CheckFill`). Compaction strategies: sliding window, summarization, hierarchical. Applies the `<untrusted-content>` wrapping at prompt-build time when `InjectionDefenseEnabled` is set.
- **`prompt`** — `PromptBuilder` composes system prompts from segments; `sampling` helpers. No engine-internal imports.
- **`skills`** — `SkillManager` scans and activates skills; the resource tool reads skill files. Imports `pathutil` and `tools` only.
- **`security`** — prompt-injection defense primitives: `UntrustedTag`, `WrapUntrustedContent`, `StripUntrustedTags`.
- **`embedding`** — `Embedder` over ONNX Runtime; `chunker` for text segmentation; tokenizer. Thread-safe.
- **`pathutil`** — pure path algorithmic primitives (containment checks, component splitting, prefix resolution). Zero engine-specific knowledge; usable from any layer.
- **`strutil`** — pure string utilities.

## Key Boundary Packages

| Boundary                | Gateway                                  | Role                                                                   |
| ----------------------- | ---------------------------------------- | ---------------------------------------------------------------------- |
| host → engine           | `github.com/v0lka/sp4rk` root `Framework` | Constructs the engine; host wires callbacks via `Config`               |
| orchestration → agent   | `orchestration.Conductor`                | Creates an `Executor` per plan step and drives the ReAct loop          |
| agent → primitives      | `agent.Executor` via `LLMCaller`/`ToolExecutor` | Consumes `llm` and `tools` through small interfaces, not concrete types |
| context build → defense | `memory.ContextWindow`                   | Calls `security.WrapUntrustedContent` for untrusted tool output         |

## Invariants

- sp4rk has zero knowledge of any host-application concept; it is engine-generic.
- Import direction is strictly downward: orchestration → agent → primitives → supporting utilities.
- `llm` and `tools` never import `agent`, `orchestration`, `planner`, or the root package.
- `pathutil` and `strutil` are pure-logic leaves with no engine imports.
- The host application imports sp4rk; sp4rk never imports the host application.
- The `Executor` consumes `llm` and `tools` only through the `LLMCaller`, `ToolExecutor`, and `ContextManager` interfaces, not concrete types.

## Anti-Patterns

- Importing an orchestration package (`orchestration`, `planner`) from a primitive package (`agent`, `tools`, `llm`) — breaks the downward-only rule and creates cycles.
- Adding host-application-specific behavior (session persistence, UI events, workspace policy) into an sp4rk package — these belong in the embedding application.
- Reaching for concrete `llm.Router` or `tools.ToolRegistry` types from the `Executor` instead of the small interfaces (`LLMCaller`, `ToolExecutor`).
- Giving `pathutil` or `strutil` any engine-specific knowledge — they must stay pure and reusable.
- Introducing a new cross-cutting dependency that bypasses the layered hierarchy (e.g., `tools` depending on `memory`).

## Related Specs

- [data-flow.md](data-flow.md) - How a request travels through these layers
- [security-model.md](security-model.md) - Tool policies and prompt-injection defense at the engine boundary
- [../contracts/agent-execution.md](../contracts/agent-execution.md) - What a host application must provide
