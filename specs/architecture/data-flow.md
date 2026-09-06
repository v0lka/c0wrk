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
  ├─ Uses the combined emitFunc built once at Application init
  │   (backend/application.go: cfg.UIEmitFunc + session.NewEventPersister);
  │   the Manager's EventEmitter fans out to UI + persistence through it
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
  │           → Activate matched skills (skills narrow the available toolset;
             policy comes from security.groups only — ADR-024)
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

Persistence is a **separate concern** from event emission. Task state (step results, facts, reflections) is persisted by `PersistentBlackboard` on each write. Chat messages are persisted by `FrontendAPI.SendMessage()` after the dispatch/classification returns — deliberately ordered so the `is_nudge` flag matches the actual decision (live/nudge vs fresh) and a rejected send never reaches the store. Token counts are persisted via a callback wired through `emitter.SetTokenPersist()`. The emitter itself only emits — it does not write to SQLite.

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

Application startup follows a phased approach. **The window is created visible — never with `StartHidden`.** Wails applies `StartHidden` by queueing a hide on the platform UI loop *after* the webview starts loading, while `OnStartup` already runs concurrently on its own goroutine. A fast backend start therefore gets its `WindowShow` calls queued ahead of that hide, the hide executes last, and the window stays withdrawn for the life of the process — a clean startup log and no window. Starting visible removes the race: Wails maps the window synchronously before its UI loop begins.

The reveals below all remain, but as idempotent safety nets rather than the moment the window appears. `OnDomReady` is the one that carries a guarantee: it fires after window setup has finished, so unlike the reveals issued from the `OnStartup` goroutine it can never be overtaken by that setup.

```
main.go:
  ├─ LoadWindowBounds(~/.c0wrk/window_state.json)
  │   └─ valid width/height + maximized state seed Wails options;
  │      missing/malformed/below-minimum dimensions use defaults
  └─ wails.Run(options.App{  // no StartHidden — see the race note above
       OnStartup:  app.Startup,
       OnDomReady: app.DomReady,   → showWindow() — post-window-setup guarantee
       OnShutdown: app.Shutdown, ...})
  ↓
Phase 0: resolve agent directory
Phase 1: shell env + logger (<50ms)
Phase 2: config + tools (parallel, up to 3-10 min on first run)
  │
  ├─ config.ResolveAndLoad() — YAML parse + env expansion + defaults
  └─ initTools()
      ├─ a.showWindow(ctx) — idempotent safety net; the window is already
      │   visible, so this only matters if something else hid it
      ├─ Manager.NeedsInstall() — quick check (.versions + binary existence)
      ├─ if tools needed: emit tool_manager:start(tools)  → frontend shows splash
      ├─ EnsureCriticalTools(AllowNetwork:false)  (always runs; strictly local:
      │   │   probes + installs from cached verified archives, no network I/O)
      │   └─ per-tool statuses; failures never abort startup
      ├─ if tools not Ready: background goroutine
      │     EnsureCriticalTools(AllowNetwork:true)
      │     ├─ download → emit tool_manager:progress(bytes_done, bytes_total)
      │     └─ extract / python_bootstrap; never emits tool_manager:start
      │        still-failing tools → single runtime_error toast
      └─ emit tool_manager:done()  (always; after each pass — emitted in both
          the needed and up-to-date paths so the frontend can transition
          splash → waiting_ready)
  ↓
Phase 3: database + terminal (parallel, ~100ms)
Phase 4: stores + preload (~100ms)
Phase 5: application + frontend API (~150ms)
  ↓
emitBackendReady():
  ├─ showWindow(ctx)  (last idempotent safety net; the window is created
  │   visible and initTools already re-showed it, so this is always a no-op)
  └─ emit backend:ready
```

**Frontend state machine** during startup:
- `splash` (initial): renders `<ToolInstallSplash />` if `tool_manager:start` received, otherwise a minimal spinner
- `tool_manager:done` → `waiting_ready` (spinner with "Starting c0wrk…")
- `backend:ready` → `main` (`<AppLayout />`)

**Window and shutdown state flow:**

- `AppLayout` debounces native resize events into `PersistWindowBounds`; `desktop.App.Shutdown` performs a final best-effort geometry save before teardown.
- Shutdown first marks the session manager as shutting down, cancels in-flight task contexts, waits for their goroutines, and persists every task still `in_progress` as `paused`. Completed/failed outcomes that won the race retain their actual state.
- Pending HITL channels are resolved with their stop/abandon/cancel outcomes, vector/frontend resources and terminals are cleaned up, judge goroutines drain, and the database closes last.
- The next launch restores validated width, height, and maximized state and exposes paused tasks through normal runtime-status/resume flow.

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
  ├─ 3. Disabled-tools check (No Project mode) — applies to ALL tools, including system-group
  ├─ 4. Tool's group == system? → execute immediately, bypass policy/judge (disabled check above still applies)
  ├─ 5. Register PostExecuteHook (deferred, runs on every non-early return path)
  ├─ 6. Extra shell blacklist check (per-session, via SetExtraShellBlacklist) — bash_exec/posh_exec only;
  │      hard block, reason names the matched pattern
  ├─ 7. PreExecuteHook? → call (may block for indexing gate)
  ├─ 8. Group policy == deny? → return error result (hard block, names the group)
  ├─ 9. Gather safety signals once: ToolJudger outcome (hard: blacklist/SSRF;
  │      soft: path containment) + symlink analysis (escape/unresolvable = hard;
  │      resolution staying inside the roots = not a concern)
  └─ 10. Branch on the tool's GROUP policy (security.groups, ADR-024; unconfigured
         group → fail-safe user_confirm):
       ├─ allow → hard reason ⇒ smartApproveOrConfirm (Hard) — unified funnel
       │          (ADR-026): strict judge consulted (hard-bias); a canonical
       │          reason (blacklist, SSRF, symlink escape, unassessable input)
       │          is backstopped to confirm even on ALLOW, a non-canonical
       │          hard reason may be cleared by a strict ALLOW;
       │          soft reason ⇒ Smart Approve may allow, else confirm; clean ⇒ execute
       ├─ deny → return error result (step 8)
       └─ user_confirm → confirmFunc() blocks until user responds
                (local_write + auto_approve_workspace_writes + Judge.Allow ⇒ execute;
                 hard reason ⇒ smartApproveOrConfirm (Hard) — same funnel +
                 canonical backstop; otherwise Smart Approve
                 evaluates: strict ALLOW ⇒ execute, anything else ⇒ confirm)
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
- Chat-visible (and resume-critical) events are both persisted AND emitted to frontend (dual write); transient UI-state events (`session_tokens`, `attachments:changed`, `goal_progress`, pin/archive toggles) are emit-only and intentionally not persisted (see `backend/session/event_persister.go`)
- Config changes require explicit reload (no hot-watching of config file)
- Blackboard is created per-task, never shared across tasks
- Async init in builder (LLM router, model registry, tool judge — gated by `initDone`) MUST complete before any Build() call returns; MCP gateway init (`mcpDone`) is intentionally decoupled and does NOT block Build() or session restore
- Native window geometry is saved on debounced resize and final shutdown; only dimensions at or above the minimum usable size are restored
- Graceful application shutdown converts each still-running `in_progress` task into a persisted `paused` checkpoint after its execution goroutine stops

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
