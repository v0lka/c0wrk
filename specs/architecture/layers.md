# Layers

## Context

c0wrk enforces a strict layered architecture. Each layer has a single responsibility and a clear dependency direction. Violating layer boundaries introduces coupling that makes the system fragile.

## Layer Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│  frontend/          React 19 + Zustand + TypeScript                 │
│                     Communicates via Wails RPC + Events             │
└────────────────────────────────┬────────────────────────────────────┘
                                 │ Wails IPC (generated bindings)
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  desktop/           Wails app lifecycle + native bindings           │
│                     Embeds *backend.FrontendAPI                     │
└────────────────────────────────┬────────────────────────────────────┘
                                 │ Go imports
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  backend/           Application ViewModel                           │
│                     Config, session manager, persistence,           │
│                     workspace watcher, vector index, terminal       │
└────────────────────────────────┬────────────────────────────────────┘
                                 │ Go imports
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  core/              Orchestration engine                            │
│                     Router, Planner, Reflector, Orchestrator,       │
│                     tool registry (policy), MCP gateway, skills     │
└────────────────────────────────┬────────────────────────────────────┘
                                 │ Go imports (ONLY layer that imports sdk)
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  sdk/               Reusable agent execution engine                 │
│                     Agent executor, LLM providers, memory,          │
│                     orchestration loop, tools, prompts, embeddings  │
└─────────────────────────────────────────────────────────────────────┘
```

## Import Rules

| Layer       | May Import                           | Must NOT Import              |
| ----------- | ------------------------------------ | ---------------------------- |
| `frontend/` | Nothing (communicates via Wails IPC) | Go packages                  |
| `desktop/`  | `backend`                            | `core`, `sdk`                |
| `backend/`  | `core`                               | `sdk`                        |
| `core/`     | `sdk`                                | `backend`, `desktop`         |
| `sdk/`      | External libs only                   | `core`, `backend`, `desktop` |

## Layer Responsibilities

### `sdk/` — Reusable Agent Engine

The lowest layer. Contains the generic building blocks for an LLM agent application: executor (ReAct loop), LLM provider abstraction (OpenAI, Anthropic, Gemini, LM Studio), memory management (context window + compaction strategies), orchestration primitives (Plan&Execute DAG engine, Blackboard), tool interface and registry, prompt utilities, and embedding support. Nothing in sdk/ is c0wrk-specific.

### `core/` — c0wrk Orchestration

The brain of c0wrk. Implements the specific orchestration cycle: Router (classifies requests), Planner (generates DAG plans), Reflector (analyzes failures), and the top-level Orchestrator that ties them together. Wraps the sdk tool registry with policy enforcement, confirmation flow, and the LLM judge. Manages the MCP gateway and skill system. Contains all prompt templates (embedded markdown). This is the sole layer that imports `sdk`.

### `backend/` — Application ViewModel

The "app layer" that the desktop UI interacts with. Owns the `Application` struct, configuration loading/resolution, session lifecycle management (create, handle message, persist, resume), SQLite persistence, workspace watching, vector indexing, and terminal management. Exposes `FrontendAPI` methods split by concern area (`frontend_api_*.go`). Wraps `core.OrchestratorBuilder` without touching sdk directly.

### `desktop/` — Wails Bindings & Lifecycle

Thin layer. The `desktop.App` struct embeds `*backend.FrontendAPI` so its methods are visible to the Wails binding generator. Handles `OnStartup` (config loading, shell environment, backend init) and `OnShutdown`. Manages pending confirmations (`sync.Map`) between backend and frontend.

### `frontend/` — React UI

TypeScript/React application. Communicates with Go exclusively through:

1. **RPC**: `window.go.desktop.App.*` — async promise-based calls
2. **Events**: `window.runtime.EventsOn/EventsEmit` — real-time streaming

No direct Go imports. Auto-generated bindings at `frontend/wailsjs/go/desktop/App.{js,d.ts}`.

## Type Re-Export Pattern

`backend` cannot import `sdk`, but needs to reference sdk types (e.g., `agent.Step`, `orchestration.Plan`). Solution: `core/types.go` defines type aliases:

```go
// core/types.go
type Step = agent.Step
type Plan = orchestration.Plan
type Blackboard = orchestration.Blackboard
```

Backend imports these types from `core`, never from `sdk` directly.

## Key Boundary Files

| Boundary           | Gateway File                           | Role                            |
| ------------------ | -------------------------------------- | ------------------------------- |
| sdk → core         | `core/builder.go`                      | Wires all sdk components        |
| sdk → core         | `core/types.go`                        | Re-exports sdk types            |
| core → backend     | `backend/configadapter.go`             | Converts config → BuilderConfig |
| core → backend     | `backend/application.go`               | Wraps OrchestratorBuilder       |
| backend → desktop  | `desktop/app.go`                       | Embeds FrontendAPI              |
| desktop → frontend | `frontend/wailsjs/go/desktop/App.d.ts` | Generated bindings              |

## Invariants

- `backend` never imports any package under `sdk/`
- `core` never imports `backend` or `desktop`
- `sdk` has zero knowledge of c0wrk-specific concepts
- All inter-layer communication between frontend and Go goes through Wails
- Type aliases in `core/types.go` are the ONLY mechanism for backend to access sdk types

## Anti-Patterns

- Importing `sdk/llm` or `sdk/agent` from `backend/` — use core type aliases instead
- Putting business logic (routing, planning, tool policy) in `desktop/`
- Hand-editing files in `frontend/wailsjs/` — they are regenerated by `wails build`
- Adding backend-specific state to `core/` types (keep core reusable)
- Creating circular imports between `core/tools` and `core/` root package

## Related Specs

- [contracts/core-sdk.md](../contracts/core-sdk.md) - Detailed interface table
- [contracts/backend-core.md](../contracts/backend-core.md) - Config adapter pattern
- [contracts/desktop-frontend.md](../contracts/desktop-frontend.md) - Wails binding surface
