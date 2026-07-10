# Fluent API

A concise, method-chain (fluent) builder API that lives **in the root `sdk`
package** — there is no separate `fluent` package. Every method returns the real
SDK type (`*sdk.Framework`, `*orchestration.ExecutionResult`), so you can mix
fluent calls with the classic [`sdk.Config`](../framework.go) API at any point.

## Why fluent?

The classic entry point `sdk.New(cfg)` is a plain struct + constructor: every
setting is a field. The fluent API layers a **method-chain builder** on top so a
build reads as one unbroken chain with a **single** `sdk.` prefix:

```go
fw, err := sdk.NewF().
    Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
    FileTools().
    MaxSteps(15).
    AutoApprove().
    Build()
```

## Why the `F` postfix?

The fluent entry points share the root package with the classic API, so the
chain starts/stops need names that do **not** collide with the existing
`sdk.New`, `Framework.Execute`, or `Framework.NewConductor`. The convention is a
single **`F` postfix** on the three fluent entry points:

| Classic (unchanged)             | Fluent (method-chain)                 |
|---------------------------------|---------------------------------------|
| `sdk.New(cfg)`                  | `sdk.NewF()` → `*FrameworkBuilder`    |
| `fw.Execute(ctx, sys, ev, msg)` | `fw.RunF(ctx)` → `*RunBuilder`        |
| `fw.NewConductor(sys)`          | `fw.TaskF(ctx, task)` → `*TaskBuilder` |

The package-level helper constructors (`sdk.Anthropic`, `sdk.FileTools`,
`sdk.MCPStdio`, …) keep their plain names — they have no classic collision, so
no postfix is needed.

## Layers

| Layer | Purpose | Entry | Terminal |
|-------|---------|-------|----------|
| 1 — Builder | Configure the framework | `sdk.NewF()` | `.Build()` → `(*Framework, error)` |
| 2 — Single task | One ReAct loop | `fw.RunF(ctx)` | `.Ask(msg)` |
| 3 — Orchestration | Plan → Execute → Reflect | `fw.TaskF(ctx, task)` | `.Execute()` |

## FrameworkBuilder surface

### Providers
- `.Anthropic(key, models…)` / `.OpenAI(key, models…)` / `.OpenAICompatible(name, baseURL, key, models…)`
- `.Provider(entry)` / `.Providers(ps…)` — append one or many pre-built entries
- `.DefaultModel("claude-sonnet-4-5")` — override the auto-selected default

### Tools
- `.FileTools()` / `.MemoryTools()` / `.CodeTools()` / `.AllBuiltinTools()` — register a bundle
- `.Tools(ts…)` — append arbitrary tools (custom tools, pre-assembled slices)

### MCP
- `.MCPStdio(name, cmd, args…)` — register a stdio MCP server **inline** (no tuple)
- `.MCPHTTP(name, url)` — register an HTTP MCP server inline
- `.MCPServer(name, entry)` — register a pre-built entry
- `.MCPWorkDir(dir)` — fallback working directory for stdio servers

### Security / HITL
- `.AutoApprove()` — always-approve callback (throwaway/sandboxed workspaces)
- `.ConfirmFunc(fn)` — custom confirmation callback
- `.HITL(handler)` — human-in-the-loop handler

### Execution / misc
- `.MaxSteps(n)` — per-step ReAct budget (0 = SDK default)
- `.Logger(*slog.Logger)` — structured logger
- `.NoAutoFinish()` — suppress auto-registration of the finish tool

### Escape hatches
- `.Options(opts…)` — apply functional options (`WithProvider`, `WithTools`, …)
- `.Config(sdk.Config)` — supply a full classic config as the base

## RunBuilder surface

Layer 2 — a single ReAct loop over the framework. Created with
`fw.RunF(ctx)`; terminate with `.Ask(msg)`.

- `.System(prompt)` — static system prompt
- `.SystemFactory(fn)` — factory receiving the task + model metadata
- `.Events(e agent.Events)` — subscribe to thought/tool/result streaming
- `.Ask(message)` — run one ReAct loop → `(*ExecutionResult, error)`

## TaskBuilder surface

Layer 3 — Plan → Execute → Reflect orchestration. Created with
`fw.TaskF(ctx, task)`; terminate with `.Execute()`. Without `.Plan()`,
`.Execute()` runs a single ReAct loop (like `RunF`) but returns an orchestrated
result; with `.Plan()` it builds a DAG and runs it with retry + reflection.

- `.System(prompt)` / `.SystemFactory(fn)` — system prompt (required) or factory
- `.Events(e orchestration.Events)` — plan/step/reflection/replan events
- `.Plan()` / `.Planner(*planner.Planner)` — enable the default planner or inject one
- `.Reflect()` / `.Reflector(*reflector.Reflector)` — enable reflection (default prompt) or inject one
- `.MaxRetries(n)` — per-step retry budget (default 2)
- `.Models(planModel, execModel)` — separate models for planning/reflection vs. execution (runtime switching)
- `.Workspace(dir)` — workspace path for tool execution
- `.Skills([]skills.SkillDescriptor)` — skills made available to the planner
- `.Compaction(strategy)` — context-compaction strategy (default `sliding_window`)
- `.Execute()` — run the DAG → `(*ExecutionResult, error)`

On reflection, a failing step's suggested action drives the loop: `retry`
re-runs the step, `replan` re-derives the remaining plan (via `Planner.Replan`,
carrying forward completed work), and `abort` halts execution.

## Single-use pipeline

For one-shot scripts, transition methods `.Run(ctx)` / `.Task(ctx, task)` on the
`FrameworkBuilder` build the framework implicitly, so the whole program is a
single chain:

```go
result, err := sdk.NewF().
    Anthropic(key, model).
    FileTools().
    Task(ctx, task).
    System("You are a task execution agent.").
    Plan().
    Reflect().
    Execute()
```

**Tradeoff:** the pipeline form loses the explicit `*Framework` handle, so there
is no `defer Shutdown()`. If a build error occurs inside the transition, it
surfaces at the terminal (`.Ask()` / `.Execute()`) instead of panicking.
Callers needing lifecycle control use `.Build()` then `fw.RunF(ctx)` /
`fw.TaskF(ctx, task)`.

## Errors

Chain methods never panic. The first error accumulates in the builder and
surfaces once, at `.Build()` (or the pipeline terminal), wrapped as
`NewF: …`.

## Before / after

Example 05 (MCP integration) — the headline tuple elimination:

**Before** (classic `sdk.New` constructor — tuple + separate registration)
```go
name, entry := sdk.MCPStdio("filesystem", "npx",
    "-y", "@modelcontextprotocol/server-filesystem", mcpRoot)
fw, err := sdk.New(sdk.Config{
    LLM: sdk.LLMConfig{
        Providers: []llm.ProviderEntry{sdk.Anthropic(key, model)},
    },
    MCP: &sdk.MCPConfig{
        Servers:        map[string]mcp.ServerEntry{name: entry},
        DefaultWorkDir: mcpRoot,
    },
})
// built-ins registered separately; MCP tools need a ConfirmFunc/policy override
fw.ToolRegistry().Register(sdk.FileTools()...)
```

**After** (fluent builder)
```go
fw, err := sdk.NewF().
    Anthropic(key, model).
    MCPStdio("filesystem", "npx", "-y", "@modelcontextprotocol/server-filesystem", mcpRoot).
    MCPWorkDir(mcpRoot).
    AutoApprove().
    FileTools().
    Build()
```

The `sdk.` prefix is a single import, the `(name, entry)` tuple is registered
inline (no local variable), and the `WithX` nesting is gone.
