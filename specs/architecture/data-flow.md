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
RPC: window.go.desktop.App.SendMessage(sessionId, text, mode)
         │
         ▼
backend/frontend_api_session.go: FrontendAPI.SendMessage()
         │
         ▼
backend/session/manager.go: Manager.SendMessage()
  ├─ Creates/reuses Orchestrator for session
  ├─ Wraps emitter (EventPersister + WailsEmitter)
  └─ Calls orchestrator.HandleMessage()
         │
         ▼
core/orchestrator.go: Orchestrator.HandleMessage(ctx, msg, opts)
  │
  ├─ 1. ROUTE: Router.Route(ctx, msg, tools, history, skills)
  │      → RoutingDecision {domain, complexity, matchedSkills}
  │      → Emitter.Routing(mode, domain, complexity)
  │
  ├─ 2. PLAN:
  │      ├─ Normal mode (mode == "normal"):
  │      │    → Planner.Plan(singleStep=true) → exactly 1 step
  │      ├─ Advanced mode:
  │      │    → Planner.Plan(singleStep=false) → DAG of PlanSteps
  │      └─ → Emitter.PlanGenerated(stepCount, steps)
  │
  ├─ 3. EXECUTE: engine.Execute(ctx, plan, task)
  │      ├─ For each ready step (parallel if DAG allows):
  │      │    → Executor.Run(ctx, stepTask, tools, systemPrompt)
  │      │    → ReAct loop: LLM → ToolCall → Observe → repeat
  │      │    → Emitter: AssistantChunk, ToolCall, ToolResult, etc.
  │      └─ Step results stored on Blackboard
  │
  ├─ 4. REFLECT (on failure):
  │      → Reflector.Reflect(ctx, trajectory, plan, prevReflections)
  │      → Emitter.Reflection(reflection, attempt, maxAttempts)
  │      → Retry or replan (back to step 2)
  │
  └─ 5. RETURN: HandleResult {output, routingDecision, plan, blackboard}
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
  → Single conversion point (all config mapping here)
         │
         ▼
core/builder.go: NewOrchestratorBuilder(builderCfg)
  ├─ Synchronous: tool registry, built-in tools, security policies
  ├─  Creates ToolResultCache with cacheTTLSeconds
  ├─  Converts perToolTruncation map to per-tool truncation config
  └─ Async (goroutine): MCP gateway, LLM router, model registry
         │
         ▼
core/builder.go: Build() → per-session Orchestrator
  → Blocks until async init complete (initDone channel)
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

## Tool Execution Flow

```
Executor decides to call a tool
         │
         ▼
sdk/agent/executor.go: injects ToolResultCache into context
         │
         ▼
sdk/agent/executor.go: calls ToolExecutor.Execute(ctx, name, input)
         │
         ▼
core/tools/registry.go: ToolRegistry.Execute(ctx, name, input)
  │
  ├─ 1. Lookup tool by name
  ├─ 2. Internal tool? → execute immediately, skip all checks
  ├─ 3. PreExecuteHook? → call (may block for indexing gate)
  ├─ 4. ParamInjector? → transform input
  ├─ 5. Symlink gate: detect symlinks in input paths → force confirmation
  ├─ 6. Resolve policy: per-tool > skill > default > tool's own
  ├─ 7. Auto-approval check: paths in workspace/temp? → execute
  └─ 8. Apply policy:
       ├─ AlwaysAllow → execute (unless ToolJudger flags it)
       ├─ AlwaysDeny → return error result
       └─ UserConfirm → confirmFunc() blocks until user responds
                │
                ▼ (if confirmed)
         tool.Execute(ctx, input)
                │
                ▼
         ToolResult {Content, IsError}
                │
                ▼ (back in executor)
sdk/agent/executor.go: cache + two-stage truncation
  │
  ├─ Skip if tool is non-cacheable (tool_result_read, finish, ask_user, etc.)
  ├─ Store full result in ToolResultCache keyed by SHA256(toolName + content)
  │    └─ Metadata: file path+mtime+size (file tools) or TTL (MCP tools)
  ├─ Stage 1: Apply per-tool line/byte truncation (configurable per tool)
  │    └─ Append fragmentation nudge with hash: "[truncated... tool_result_read(hash=...)]"
  └─ Stage 2: Apply ToolResultBudget (token-based hard cap)
```

## Blackboard Flow

The Blackboard is shared state for the Plan&Execute loop:

```
Orchestrator.HandleMessage()
  │
  ├─ Creates Blackboard via bbFactory(taskID)
  │   (PersistentBlackboard wraps MapBlackboard + SQLite store)
  │
  ├─ Engine stores plan on Blackboard
  │
  ├─ Each step executor:
  │   ├─ Reads dependency outputs: bb.GetStepResult(depID)
  │   ├─ Writes own result: bb.SetStepResult(stepID, output, err, steps)
  │   └─ Stores facts: bb.StoreFact(fact)
  │
  ├─ Reflector reads: bb.GetAllStepResults(), bb.GetReflections()
  │
  └─ Final: bb.SetFinalResult(output)
       → PersistentBlackboard.Save() persists to SQLite
```

## Invariants

- Every user message passes through the full stack (no shortcuts from frontend to core)
- Events are always both persisted AND emitted to frontend (dual write)
- Config changes require explicit reload (no hot-watching of config file)
- Blackboard is created per-task, never shared across tasks
- Async init in builder MUST complete before any Build() call returns

## Anti-Patterns

### ❌ Frontend directly calling `sdk/*` packages

The data flow requires all requests to go through Wails RPC → backend → core → sdk. Skipping layers breaks security, event tracking, and session isolation.

### ❌ Bypassing config adapter

Never read `config.yaml` directly from core or sdk packages. All config flows through `backend/configadapter.go → core.BuilderConfig`. Adding a field to config requires updates to all three layers.

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
