# Fluent API

A concise, method-chain (fluent) façade over the sp4rk SDK. Every method returns
the real SDK type — there are no shadow types, so you can mix fluent calls with
the classic [`sdk.Config`](../framework.go) API at any point.

## Why fluent?

The classic entry point `sdk.New(cfg)` is a plain struct + constructor: every
setting is a field, and every "fluent" helper used to wrap a constructor inside
an option, forcing `fluent.WithProvider(fluent.Anthropic(...))` double-nesting
and a `fluent.` prefix on every line.

The fluent API replaces that with a **method-chain builder**. A build reads as
one unbroken chain with a **single** `fluent.` prefix:

```go
fw, err := fluent.New().
    Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5").
    FileTools().
    MaxSteps(15).
    AutoApprove().
    Build()
```

## Layers

| Layer | Purpose | Entry | Terminal |
|-------|---------|-------|----------|
| 1 — Builder | Configure the framework | `fluent.New()` | `.Build()` → `(*Framework, error)` |
| 2 — Single task | One ReAct loop | `fluent.Run(ctx, fw)` | `.Ask(msg)` |
| 3 — Orchestration | Plan → Execute → Reflect | `fluent.Task(ctx, fw, task)` | `.Execute()` |

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
`fluent.Run(ctx, fw)`; terminate with `.Ask(msg)`.

- `.System(prompt)` — static system prompt
- `.SystemFactory(fn)` — factory receiving the task + model metadata
- `.Events(e agent.Events)` — subscribe to thought/tool/result streaming
- `.Ask(message)` — run one ReAct loop → `(*ExecutionResult, error)`

## TaskBuilder surface

Layer 3 — Plan → Execute → Reflect orchestration. Created with
`fluent.Task(ctx, fw, task)`; terminate with `.Execute()`. Without `.Plan()`,
`.Execute()` runs a single ReAct loop (like `Run`) but returns an orchestrated
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

For one-shot scripts, transition methods `.Run(ctx)` / `.Task(ctx, task)` build
the framework implicitly, so the whole program is a single chain:

```go
result, err := fluent.New().
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
Callers needing lifecycle control use `.Build()` then the `Run`/`Task`
constructors.

## Errors

Chain methods never panic. The first error accumulates in the builder and
surfaces once, at `.Build()` (or the pipeline terminal), wrapped as
`fluent.New: …`.

## Before / after

Example 05 (MCP integration) — the headline tuple elimination:

**Before**
```go
name, entry := fluent.MCPStdio("filesystem", "npx",
    "-y", "@modelcontextprotocol/server-filesystem", mcpRoot)
fw, err := fluent.New(
    fluent.WithProvider(fluent.Anthropic(key, model)),
    fluent.WithMCPServer(name, entry),
    fluent.WithMCPWorkDir(mcpRoot),
    fluent.WithAutoApprove(),
    fluent.WithTools(fluent.FileTools()...),
)
```

**After**
```go
fw, err := fluent.New().
    Anthropic(key, model).
    MCPStdio("filesystem", "npx", "-y", "@modelcontextprotocol/server-filesystem", mcpRoot).
    MCPWorkDir(mcpRoot).
    AutoApprove().
    FileTools().
    Build()
```

The `fluent.` prefix count drops from 5 to 1, the `WithX(X(...))` nesting is
gone, and the `(name, entry)` tuple is eliminated.
