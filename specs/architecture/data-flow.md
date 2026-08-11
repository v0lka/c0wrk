# Data Flow

## Context

Understanding how data moves through c0wrk is essential for safely modifying any layer. This spec traces the major flows from initiation to completion.

## Request Lifecycle

A user message travels through the full stack:

```
User types message in frontend
         │
         ▼
Frontend: chatStore.sendMessage(text)
         │
         ▼
RPC: window.go.desktop.App.SendMessage(id, text, activeSkills, activeAgents, modelOverride, reasoningEffort, goal, goalBudget, reviewMode)
         │
         ▼
backend/frontend_api_session.go: FrontendAPI.SendMessage()
         │
         ▼
backend/session/manager_execution.go: Manager.SendMessage()
  ├─ Creates/reuses Orchestrator for session
  ├─ Wraps emitter (EventPersister + WailsEmitter)
  └─ Calls orchestrator.HandleMessage()
         │
         ▼
core/orchestrator.go: Orchestrator.HandleMessage(ctx, msg, sessionID, opts)
  │
  ├─ 0. SETUP: setupBlackboard()
  │      ├─ New task: create Blackboard via bbFactory(taskID) (PersistentBlackboard
  │      │   wraps MapBlackboard + SQLite store); set original request; flush attachments
  │      └─ Continuation (opts.TaskID set): restore Blackboard from persistence,
  │          emit MemoryRead for restored facts, reactivate task
  │
  ├─ 1. ROUTE (routeOrContinue):
  │      ├─ Continuation fast-path: restored plan + routing → router is skipped,
  │      │   existing RoutingDecision is reused
  │      └─ Otherwise: Router.Route(ctx, msg, tools, history, skills)
  │           → RoutingDecision {domain, complexity, matchedSkills}
  │           → Emitter.Routing("conductor", domain, complexity)
  │           → Activate matched skills + apply skill-derived tool policy overrides
  │
  ├─ 2. CONDUCTOR: runConductor() — a single ReAct loop owns the task end-to-end
  │      ├─ Executor drives the Conductor: LLM ↔ tool-call ↔ observe ↔ repeat
  │      ├─ Planning (declare_plan), delegation (delegate), reflection (reflect),
  │      │   and user interaction (ask_user) are TOOL CALLS inside the loop —
  │      │   NOT separate pipeline phases
  │      └─ Emitter: AssistantChunk, ToolCall, ToolResult, PlanStep, Reflection, etc.
  │
  └─ 3. RETURN: finalizeResult() → HandleResult
         {output, routingDecision, plan, blackboard, reflections, status}
         │
         ▼
backend: Emitter.TaskComplete(output) → persists to SQLite
         │
         ▼
Frontend: event handler receives session:${id}:task_complete
         → chatStore updates, activity indicator stops
```

## Event Flow

Events stream from backend to frontend in real-time during task execution:

```
core/Orchestrator (via Emitter interface)
         │
         ▼
backend/session/emitter.go: EventEmitter
  ├─ Formats event data as typed struct
  └─ Calls emit callback → runtime.EventsEmit(ctx, eventName, payload)
         │
         ▼
Wails runtime (IPC bridge)
         │
         ▼
frontend: window.runtime.EventsOn(eventName, handler)
  ├─ useSessionEvents hook dispatches to type-specific hooks
  ├─ Type guard validates payload
  └─ Updates Zustand store (chatStore, planStore, etc.)
         │
         ▼
React re-renders affected components
```

Persistence is a **separate concern** from event emission. Task state (step results, facts, reflections) is persisted by `PersistentBlackboard` on each write. Chat messages are persisted by `FrontendAPI.SendMessage()` before orchestration starts. Token counts are persisted via a callback wired through `emitter.SetTokenPersist()`. The emitter itself only emits — it does not write to SQLite.

Event naming:

- Session-scoped: `session:${sessionId}:${eventType}` (e.g., `session:abc123:assistant_chunk`)
- Global: bare name (e.g., `backend:ready`, `workspace:tree_changed`)

## Configuration Flow

```
~/.c0wrk/config.yaml (user-edited)
         │
         ▼
backend/config/: config.Load()
  ├─ YAML parse
  ├─ Environment variable expansion (${VAR})
  └─ Apply defaults (backend/config/defaults.go)
         │
         ▼
backend/configadapter.go: ToBuilderConfig(cfg)
  → Converts config.Config → core.BuilderConfig
  → Builds ProviderConfigs map from all providers (including those with no models enabled)
  → Sets DefaultModel (cross-provider, resolves to owning provider)
  → Single conversion point (all config mapping here)
         │
         ▼
core/builder.go: NewOrchestratorBuilder(builderCfg)
  ├─ Synchronous: proxy client, tool registry, built-in tools, security policies
  ├─ Async initDone goroutine: LLM router, model registry, tool judge
  └─ Async mcpDone goroutine: MCP gateway (separately gated, intentionally decoupled from initDone — graceful degradation)
         │
         ▼
core/builder.go: Build() → per-session Orchestrator
  → Blocks until initDone completes (LLM router, model registry, tool judge ready)
  → Does NOT block on MCP gateway (mcpDone): MCP servers are discovered in the background; a server still connecting is simply not yet registered rather than blocking session creation
  → Creates ToolResultCache with cacheTTLSeconds (per-session lifetime)
  → Converts perToolTruncation map to per-tool truncation config
```

Vector index initialization is separate from the builder pipeline:

```
desktop/startup.go (background goroutine, after EventBackendReady):
  → Load ONNX embedder from disk (~500-2000ms)
  → Create vectorindex.Manager
  → Wire into FrontendAPI via SetVectorManager()
  → Emit vector_index:status (state=ready)
```

`EventBackendReady` fires without waiting for vector index or MCP gateway async init. The frontend receives projects/sessions immediately; vector search becomes available asynchronously.

## Startup Sequence

Application startup follows a phased approach. The window starts hidden (gated by the `C0WRK_START_HIDDEN` env var) and is revealed unconditionally during Phase 2 so the frontend mounts and subscribes to events before `backend:ready`:

```
main.go: wails.Run(options.App{StartHidden: os.Getenv("C0WRK_START_HIDDEN") != "false"})
  ↓
Phase 0: resolve agent directory
Phase 1: shell env + logger (<50ms)
Phase 2: config + tools (parallel, up to 3-10 min on first run)
  │
  ├─ config.ResolveAndLoad() — YAML parse + env expansion + defaults
  └─ initTools()
      ├─ wailsRuntime.WindowShow(ctx) — window shown unconditionally so the
      │   frontend mounts and subscribes to events before backend:ready
      ├─ Manager.NeedsInstall() — quick check (.versions + binary existence)
      ├─ if tools needed: emit tool_manager:start(tools)  → frontend shows splash
      ├─ EnsureCriticalTools()  (always runs)
      │   ├─ download → emit tool_manager:progress(bytes_done, bytes_total)
      │   └─ extract / python_bootstrap
      └─ emit tool_manager:done()  (always; installed_count reflects whether
          tools were installed — emitted in both the needed and up-to-date paths
          so the frontend can transition splash → waiting_ready)
  ↓
Phase 3: database + terminal (parallel, ~100ms)
Phase 4: stores + preload (~100ms)
Phase 5: application + frontend API (~150ms)
  ↓
emitBackendReady():
  ├─ WindowShow(ctx)  (idempotent no-op if already visible — window is shown
  │   unconditionally in initTools, so this is always a no-op in practice)
  └─ emit backend:ready
```

**Frontend state machine** during startup:
- `splash` (initial): renders `<ToolInstallSplash />` if `tool_manager:start` received, otherwise a minimal spinner
- `tool_manager:done` → `waiting_ready` (spinner with "Starting c0wrk…")
- `backend:ready` → `main` (`<AppLayout />`)

## Tool Execution Flow

```
Executor decides to call a tool
         │
         ▼
github.com/v0lka/sp4rk/agent/executor.go: injects ToolResultCache into context
         │
         ▼
github.com/v0lka/sp4rk/agent/executor.go: calls ToolExecutor.Execute(ctx, name, input)
         │
         ▼
core/tools/registry.go: ToolRegistry.Execute(ctx, name, input)
  │
  ├─ 1. Lookup tool by name
  ├─ 2. Required-field validation (defense-in-depth) — reject inputs missing
  │      a JSON Schema "required" top-level key
  ├─ 3. Disabled-tools check (No Project mode) — applies to ALL tools, including internal
  ├─ 4. Internal tool? → execute immediately, bypass policy/judge (disabled check above still applies)
  ├─ 5. Register PostExecuteHook (deferred, runs on every non-early return path)
  ├─ 6. Extra shell blacklist check (per-session, e.g. No Project mode) — bash_exec/posh_exec only
  ├─ 7. PreExecuteHook? → call (may block for indexing gate)
  ├─ 8. Symlink gate: detect symlinks in input paths → force confirmation
  ├─ 9. Resolve policy: per-tool > skill > default > tool's own
  ├─ 10. PolicyAlwaysAllow Judge gate: ToolJudger flags call (reason != "")? → confirmation
  ├─ 11. Auto-approval check (PolicyAlwaysAllow only): all paths in session roots? → execute
  └─ 12. Apply policy:
       ├─ AlwaysAllow → execute (Judge gate already ran above)
       ├─ AlwaysDeny → return error result
       └─ UserConfirm → confirmFunc() blocks until user responds
                (session-root auto-approve of write tools via autoApproveWorkspaceWrites + ToolJudger may short-circuit)
                │
                ▼ (if confirmed)
         tool.Execute(ctx, input)
                │
                ▼
         ToolResult {Content, IsError}
                │
                ▼ (back in executor)
github.com/v0lka/sp4rk/agent/executor.go: cache + two-stage truncation
  │
  ├─ Skip if tool is non-cacheable (sp4rk defaults: tool_result_read, finish, batch, etc.; extended via AddNonCacheableTools)
  ├─ Store full result in ToolResultCache; key is a short hash — the shortest unique prefix (from 4 chars) of SHA256(toolName + "\x00" + content)
  │    ├─ File-backed entries (file tools): hash is SHA256(toolName + "\x00" + filePath + "\x00" + mtime + "\x00" + size) — derived from file metadata, not content
  │    └─ Metadata: file path+mtime+size (file tools) or TTL (MCP tools)
  ├─ Stage 1: Apply per-tool line/byte truncation (configurable per tool)
  │    └─ Append fragmentation nudge with hash: "[truncated... tool_result_read(hash=...)]"
  └─ Stage 2: Apply ToolResultBudget (token-based hard cap)
```

## Blackboard Flow

The Blackboard is shared state for the Conductor loop (plan steps from `declare_plan`, delegated subagent outputs, facts, and reflections all live here):

```
Orchestrator.HandleMessage()
  │
  ├─ Creates Blackboard via bbFactory(taskID)
  │   (PersistentBlackboard wraps MapBlackboard + SQLite store)
  │
  ├─ Conductor stores plan on Blackboard (via declare_plan tool call)
  │
  ├─ Each plan step (executed inline or via a delegated subagent):
  │   ├─ Reads dependency outputs: bb.GetStepResult(depID)
  │   ├─ Writes own result: bb.SetStepResult(stepID, output, err, steps)
  │   └─ Stores facts: bb.StoreFact(fact)
  │
  ├─ reflect tool reads: bb.GetAllStepResults(), bb.GetReflections()
  │
  └─ Final: bb.SetFinalResult(output)  (in-memory only — does not persist)
       → bb.CompleteTask(attemptCount) persists final output + task completion to SQLite (PersistCompletion)
```

## Invariants

- Every user message passes through the full stack (no shortcuts from frontend to core)
- Events are always both persisted AND emitted to frontend (dual write)
- Config changes require explicit reload (no hot-watching of config file)
- Blackboard is created per-task, never shared across tasks
- Async init in builder (LLM router, model registry, tool judge — gated by `initDone`) MUST complete before any Build() call returns; MCP gateway init (`mcpDone`) is intentionally decoupled and does NOT block Build() or session restore

## Anti-Patterns

### ❌ Frontend directly calling sp4rk packages

The data flow requires all requests to go through Wails RPC → backend → core → sp4rk. Skipping layers breaks security, event tracking, and session isolation.

### ❌ Bypassing config adapter

Never read `config.yaml` directly from core or sp4rk packages. All config flows through `backend/configadapter.go → core.BuilderConfig`. Adding a field to config requires updates to all three layers.

### ❌ Emitting events without persisting

Every piece of task state must be both emitted to the frontend for live updates AND persisted to SQLite for resume. Event emission (via EventEmitter) handles the frontend channel; persistence happens through PersistentBlackboard (step results, facts), SessionStore (messages, tokens), and TaskStore (task lifecycle). Adding a new event that carries resume-critical data requires adding corresponding persistence logic.

### ❌ Sharing Blackboard across tasks

The Blackboard is per-task, lifecycle-tied to a single `HandleMessage()` invocation. Reusing a Blackboard across tasks corrupts step dependency resolution and reflection data.

### ❌ Tight coupling between frontend stores and event format

Stores should depend on the semantic meaning of events, not the raw payload structure. If an event payload changes, only the event handler hook should need updating — not the store or the component that renders it.

### ❌ Blocking the UI thread for backend RPCs

All Wails RPC calls return Promises. Never `await` in the component render path; always dispatch async state updates through hooks that set loading flags.

## Related Specs

- [domains/orchestration/README.md](../domains/orchestration/README.md) - Orchestration cycle details
- [contracts/event-catalog.md](../contracts/event-catalog.md) - Complete event reference
- [contracts/backend-core.md](../contracts/backend-core.md) - Config adapter details
