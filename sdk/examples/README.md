# c0wrk Agent SDK — Examples

A progression of 7 examples that show how to build AI agents with the
`github.com/v0lka/c0wrk/sdk` Go framework, from a minimal single-call agent to
a full-stack system that exercises every major SDK subsystem.

## Layout

```
sdk/examples/
├── go.mod                      # standalone module — imports the SDK as an external dep
├── README.md                   # this file
├── 01-minimal-agent/           # minimal full agent (Framework + finish + Execute)
├── 02-custom-tools/            # custom Tool implementation + built-in tools
├── 03-event-streaming/         # custom AgentEvents for live execution observability
├── 04-human-in-the-loop/       # custom HITLHandler for tool-call confirmation
├── 05-mcp-integration/         # Model Context Protocol server integration
├── 06-plan-and-reflect/        # Planner → DAG → Conductor → Reflector orchestration
└── 07-full-power/              # every SDK subsystem combined in one agent
```

## Progression

| #  | Example              | Concepts introduced                                             |
|----|----------------------|-----------------------------------------------------------------|
| 01 | minimal-agent        | `sdk.New`, `Framework.Execute`, `FinishTool`, system prompt     |
| 02 | custom-tools         | `tools.Tool` interface, `BaseTool`, built-in tools, workspace   |
| 03 | event-streaming      | `AgentEvents` interface, live thought/tool/result observation   |
| 04 | human-in-the-loop    | `HITLHandler`, tool-call confirmation, step-limit decisions     |
| 05 | mcp-integration      | `MCPConfig`, stdio/HTTP MCP servers, external tool discovery    |
| 06 | plan-and-reflect     | `Planner`, DAG execution, `Reflector`, retry/replan loop        |
| 07 | full-power           | multi-provider, skills, checkpointer, compaction, fact memory   |

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

Examples default to Anthropic (`claude-sonnet-4-5`). See each example's
README for provider-specific configuration.

### Module setup

The examples live in a **separate Go module** (`c0wrk-sdk-examples`) that
imports the SDK as an external dependency via a `replace` directive:

```go
// go.mod
require github.com/v0lka/c0wrk/sdk v0.0.0
replace github.com/v0lka/c0wrk/sdk => ..
```

This lets the examples compile against the local SDK source tree without
publishing to a module proxy. The `..` points to the SDK module root
(`sdk/`), one directory above `sdk/examples/`. A real consumer would
simply run:

```bash
go get github.com/v0lka/c0wrk/sdk@latest
```

Before running an example for the first time, resolve dependencies:

```bash
cd sdk/examples
go mod tidy
```

## Running an example

```bash
cd sdk/examples/01-minimal-agent
go run main.go
```

Each example prints its output to stdout. Examples that require interactive
input (e.g. 04-human-in-the-loop) will prompt on stdin.

## Key SDK packages

| Package                              | Purpose                                             |
|--------------------------------------|-----------------------------------------------------|
| `github.com/v0lka/c0wrk/sdk`         | Top-level `Framework`, `Config`, `Execute`          |
| `…/sdk/agent`                        | `Executor`, `AgentEvents`, `HITLHandler`, `FinishTool` |
| `…/sdk/llm`                          | `Router`, `ProviderEntry`, `ModelMetadata`          |
| `…/sdk/tools`                        | `Tool` interface, `ToolRegistry`, `BaseTool`        |
| `…/sdk/tools/builtins`               | Built-in tools (read_file, write_file, bash, …)     |
| `…/sdk/tools/mcp`                    | MCP gateway, `ServerEntry`                          |
| `…/sdk/orchestration`                | `Conductor`, `Blackboard`, `Plan`, DAG utilities    |
| `…/sdk/planner`                      | `Planner`, `Config`, `PromptSet`                    |
| `…/sdk/agent/reflector`              | `Reflector` for failure analysis                    |
| `…/sdk/prompt`                       | `SystemPromptBuilder`, cache-break support          |
| `…/sdk/skills`                       | `SkillManager`, skill discovery                     |
| `…/sdk/memory`                       | `ContextWindow`, compaction strategies              |
