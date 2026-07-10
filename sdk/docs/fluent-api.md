# Fluent API

A concise, method-chain (fluent) builder API that lives **in the root `sp4rk`
package** — there is no separate `fluent` package. Every method returns the real
SDK type (`*sp4rk.Framework`, `*orchestration.ExecutionResult`), so you can mix
fluent calls with the classic [`sp4rk.Config`](../framework.go) API at any point.

## Why fluent?

The classic entry point `sp4rk.New(cfg)` is a plain struct + constructor: every
setting is a field. The fluent API layers a **method-chain builder** on top so a
build reads as one unbroken chain with a **single** `sp4rk.` prefix:

```go
fw, err := sp4rk.NewF().
    Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
    FileTools().
    MaxSteps(15).
    AutoApprove().
    Build()
```

## Why the `F` postfix?

The fluent entry points share the root package with the classic API, so the
chain starts/stops need names that do **not** collide with the existing
`sp4rk.New`, `Framework.Execute`, or `Framework.NewConductor`. The convention is a
single **`F` postfix** on the three fluent entry points:

| Classic (unchanged)             | Fluent (method-chain)                 |
|---------------------------------|---------------------------------------|
| `sp4rk.New(cfg)`                  | `sp4rk.NewF()` → `*FrameworkBuilder`    |
| `fw.Execute(ctx, sys, ev, msg)` | `fw.RunF(ctx)` → `*RunBuilder`        |
| `fw.NewConductor(sys)`          | `fw.TaskF(ctx, task)` → `*TaskBuilder` |

The package-level helper constructors (`sp4rk.Anthropic`, `sp4rk.FileTools`,
`sp4rk.MCPStdio`, …) keep their plain names — they have no classic collision, so
no postfix is needed.

## Layers

| Layer | Purpose | Entry | Terminal |
|-------|---------|-------|----------|
| 1 — Builder | Configure the framework | `sp4rk.NewF()` | `.Build()` → `(*Framework, error)` |
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
- `.Config(sp4rk.Config)` — supply a full classic config as the base

#### Functional options reference

The root `sp4rk` package exports functional options (`Option` values) that the
fluent builder applies via `.Options(opts…)`. They are an alternative to the
typed builder methods — useful in code that assembles configuration
generically, or to mix classic-API fields with fluent conveniences. Every
option is repeatable where it makes sense.

```go
import "github.com/v0lka/sp4rk"  // options live in the root package
```

| Option | Category | Effect |
| --- | --- | --- |
| `WithProvider(p)` | Providers | Append one LLM provider (repeatable). |
| `WithProviders(ps…)` | Providers | Append multiple providers at once. |
| `WithDefaultModel(model)` | Providers | Override the auto-selected default model; accepts a bare name or composite `provider/model` ID. |
| `WithTools(ts…)` | Tools | Register tools after the framework is built (spread a bundle: `sp4rk.WithTools(sp4rk.FileTools()...)`). |
| `WithoutAutoFinish()` | Tools | Skip auto-registering the finish tool (it is registered by default). |
| `WithMCPServer(name, entry)` | MCP | Register an MCP server (pair with `sp4rk.MCPStdio` / `sp4rk.MCPHTTP`). |
| `WithMCPWorkDir(dir)` | MCP | Set the fallback working directory for stdio MCP servers. |
| `WithConfirmFunc(fn)` | Security | Confirmation callback for `PolicyUserConfirm` tools; without it such tools are denied (fail-closed). |
| `WithAutoApprove()` | Security | Install an always-approve callback — convenient for sandboxed workspaces. |
| `WithHITL(h)` | Security | Set the human-in-the-loop handler ([HITL](hitl.md)). |
| `WithMaxSteps(n)` | Execution | Per-step ReAct loop budget (`0` = sp4rk default 50, negative = disabled). |
| `WithLogger(l)` | Misc | Structured logger (defaults to `slog.Default()`). |
| `WithConfig(cfg)` | Escape hatch | Supply a full `Config` as the base; other options apply on top. |

`Option` uses an interface-based functional-options pattern: only the `sp4rk`
package can produce `Option` values (the `apply` method is unexported), so
options from other packages cannot be applied by accident.

```go
fw := sp4rk.NewF().
    Anthropic(key, "claude-sonnet-4-5").
    FileTools().
    Options(
        sp4rk.WithMaxSteps(40),
        sp4rk.WithAutoApprove(),
    ).
    Build(ctx)
```

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
result, err := sp4rk.NewF().
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

**Before** (classic `sp4rk.New` constructor — tuple + separate registration)
```go
name, entry := sp4rk.MCPStdio("filesystem", "npx",
    "-y", "@modelcontextprotocol/server-filesystem", mcpRoot)
fw, err := sp4rk.New(sp4rk.Config{
    LLM: sp4rk.LLMConfig{
        Providers: []llm.ProviderEntry{sp4rk.Anthropic(key, model)},
    },
    MCP: &sp4rk.MCPConfig{
        Servers:        map[string]mcp.ServerEntry{name: entry},
        DefaultWorkDir: mcpRoot,
    },
})
// built-ins registered separately; MCP tools need a ConfirmFunc/policy override
fw.ToolRegistry().Register(sp4rk.FileTools()...)
```

**After** (fluent builder)
```go
fw, err := sp4rk.NewF().
    Anthropic(key, model).
    MCPStdio("filesystem", "npx", "-y", "@modelcontextprotocol/server-filesystem", mcpRoot).
    MCPWorkDir(mcpRoot).
    AutoApprove().
    FileTools().
    Build()
```

The `sp4rk.` prefix is a single import, the `(name, entry)` tuple is registered
inline (no local variable), and the `WithX` nesting is gone.
