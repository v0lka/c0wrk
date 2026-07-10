# sp4rk Agent SDK — Examples
A progression of 11 examples that show how to build AI agents with the `github.com/v0lka/sp4rk` Go framework, from a minimal single-call agent through focused subsystem deep-dives to a full-stack system that exercises every major SDK subsystem.

## Layout

```
sdk/examples/
├── go.mod                      # standalone module — imports the SDK as an external dep
├── README.md                   # this file
├── 01-minimal-agent/           # minimal full agent (Framework + finish + Execute)
├── 02-custom-tools/            # custom Tool implementation + built-in tools
├── 03-event-streaming/         # custom Events sink for live execution observability
├── 04-human-in-the-loop/       # custom HITLHandler for tool-call confirmation
├── 05-mcp-integration/         # Model Context Protocol server integration
├── 06-plan-and-reflect/        # Planner → DAG → Conductor → Reflector orchestration
├── 07-multi-provider-routing/  # multi-provider Router, runtime model switching
├── 08-parallel-subagents/      # RunSubAgent / RunSubAgentsParallel concurrency
├── 09-context-memory/          # ContextWindow, compaction strategies, fill events
├── 10-security-and-safety/     # prompt-injection defense, ToolJudger, ToolPolicy
└── 11-full-power/              # every SDK subsystem combined in one agent
```

## Progression

| #  | Example              | Concepts introduced                                             |
|----|----------------------|-----------------------------------------------------------------|
| 01 | minimal-agent        | `sp4rk.New`, `Framework.Execute`, `FinishTool`, system prompt     |
| 02 | custom-tools         | `tools.Tool` interface, `BaseTool`, built-in tools, workspace   |
| 03 | event-streaming      | `Events` interface, live thought/tool/result observation        |
| 04 | human-in-the-loop    | `HITLHandler`, tool-call confirmation, step-limit decisions     |
| 05 | mcp-integration      | `MCPConfig`, stdio/HTTP MCP servers, external tool discovery    |
| 06 | plan-and-reflect     | `Planner`, DAG execution, `Reflector`, retry/replan loop        |
| 07 | multi-provider-routing | `Router`, `SetModel`, composite IDs, phase-based `Models()`   |
| 08 | parallel-subagents   | `RunSubAgent`/`RunSubAgentsParallel`, per-step Executor/CM     |
| 09 | context-memory       | `ContextWindow`, compaction strategies, `ContextFill` events   |
| 10 | security-and-safety  | `WrapUntrustedContent`, `ToolJudger`, `ToolPolicy`, `ConfirmFunc` |
| 11 | full-power           | multi-provider, Plan→Reflect, MCP, event streaming, HITL, skills, compaction, fact memory |

Each example is a self-contained `package main` with its own `README.md`.

## Prerequisites

### Go

Go 1.26+ is required (matches the SDK's `go.mod`).

### API keys

Every example needs at least one LLM provider API key:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
# or
export OPENAI_API_KEY="sk-..."
```

Examples default to Anthropic (`claude-sonnet-4-5`). See each example's README for provider-specific configuration.

### Module setup

The examples live in a **separate Go module** (`sp4rk-examples`) that imports the SDK as an external dependency via a `replace` directive:

```go
// go.mod
require github.com/v0lka/sp4rk v0.0.0-00010101000000-000000000000
replace github.com/v0lka/sp4rk => ..
```

This lets the examples compile against the local SDK source tree without publishing to a module proxy. The `..` points to the SDK module root (`sdk/`), one directory above `sdk/examples/`. A real consumer would simply run:

```bash
go get github.com/v0lka/sp4rk@latest
```

Before running an example for the first time, resolve dependencies:

```bash
cd sdk/examples
go mod tidy
```

## Running an example

Most examples ship in **two equivalent variants** selected by a build tag:

| Command                 | Variant  | File            |
|-------------------------|----------|-----------------|
| `go run -tags fluent .` | Fluent   | `main_fluent.go` — recommended, concise |
| `go run .`              | Classic  | `main.go` — full low-level control        |

```bash
cd sdk/examples/01-minimal-agent
go run -tags fluent .   # recommended (fluent API)
go run .                # classic SDK API
```

Both variants build the same `*sp4rk.Framework` and produce the same result — the fluent builders live in the root `sp4rk` package and return the original SDK types. See [`docs/fluent-api.md`](../docs/fluent-api.md).

Each example prints its output to stdout. Examples that require interactive input (e.g. 04-human-in-the-loop) will prompt on stdin.

## Key SDK packages

| Package                              | Purpose                                             |
|--------------------------------------|-----------------------------------------------------|
| `github.com/v0lka/sp4rk`         | Top-level `Framework`, `Config`, `Execute`; fluent builders `NewF`/`RunF`/`TaskF` + bundles |
| `…/agent`                            | `Executor`, `Events`, `HITLHandler`, `FinishTool`   |
| `…/llm`                              | `Router`, `ProviderEntry`, `ModelMetadata`          |
| `…/tools`                            | `Tool` interface, `ToolRegistry`, `BaseTool`        |
| `…/tools/builtins`                   | Built-in tools (read_file, write_file, bash, …)     |
| `…/tools/mcp`                        | MCP gateway, `ServerEntry`                          |
| `…/orchestration`                    | `Conductor`, `Blackboard`, `Plan`, DAG utilities    |
| `…/planner`                          | `Planner`, `Config`, `PromptSet`                    |
| `…/agent/reflector`                  | `Reflector` for failure analysis                    |
| `…/prompt`                           | `SystemPromptBuilder`, cache-break support          |
| `…/skills`                           | `SkillManager`, skill discovery                     |
| `…/memory`                           | `ContextWindow`, compaction strategies              |
