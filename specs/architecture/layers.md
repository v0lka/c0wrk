# Layers

## Context

c0wrk enforces a strict layered architecture. Each layer has a single responsibility and a clear dependency direction. Violating layer boundaries introduces coupling that makes the system fragile.

## Layer Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│  frontend/          React 19 + Zustand + TypeScript                 │
│                     Communicates via Wails RPC + Events             │
└────────────────────────────────────────┬────────────────────────────┘
                                 │ Wails IPC (generated bindings)
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  desktop/           Wails app lifecycle + native bindings           │
│                     Embeds *backend.FrontendAPI                     │
└────────────────────────────────────────┬────────────────────────────┘
                                 │ Go imports
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  backend/           Application ViewModel                           │
│                     Config, session manager, persistence,           │
│                     project lifecycle, Wails API surface             │
└────────────────────────────────────────┬────────────────────────────┘
                                 │ Go imports
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  core/              Orchestration engine + domain services          │
│                     Router, Conductor, Orchestrator,                │
│                     tool registry (policy),                         │
│                     vector index, proxy, workspace watcher, terminal│
└────────────────────────────────────────┬────────────────────────────┘
                                 │ Go imports
                                 ▼
┌─────────────────────────────────────────────────────────────────────┐
│  sp4rk (sdk/ dir)   Reusable agent execution engine                 │
│   github.com/v0lka/  Agent executor, LLM providers, memory,         │
│   sp4rk              orchestration loop, tools, skills, MCP gateway │
│                      prompts, embeddings                            │
└─────────────────────────────────────────────────────────────────────┘
```

> `sdk/` is the local directory holding the sp4rk agent engine (module `github.com/v0lka/sp4rk`). It is shown here as the lowest layer.

## Import Rules

| Layer       | May Import                           | Must NOT Import              |
| ----------- | ------------------------------------ | ---------------------------- |
| `frontend/` | Nothing (communicates via Wails IPC) | Go packages                  |
| `desktop/`  | `backend`, `core`, sp4rk             | —                            |
| `backend/`  | `core`, sp4rk                        | —                            |
| `core/`     | sp4rk                                | `backend`, `desktop`         |
| `sdk/` (sp4rk) | External libs only                | `core`, `backend`, `desktop` |

> **Module boundary** (ADR-014): sp4rk (the `sdk/` directory) is a separate Go module (`github.com/v0lka/sp4rk`). The root module (`github.com/v0lka/c0wrk`) depends on it via `replace github.com/v0lka/sp4rk => ./sdk`. The import prohibition on the sp4rk module is now enforced at the module level — sp4rk cannot import `core/`, `backend/`, or `desktop/` because they live in a different module.

> **depguard enforcement** (`.golangci.yml`): the linter currently enforces a subset of the above — sp4rk may not import `core`/`backend`, and `core` may not import `backend`. The `→desktop` prohibitions are maintained by convention (and by the import cycles they would create), not by the linter.

## Layer Responsibilities

### sp4rk (`sdk/` directory) — Reusable Agent Engine

The lowest layer. Contains the generic building blocks for an LLM agent application: executor (ReAct loop), LLM provider abstraction (OpenAI, Anthropic, Gemini, LM Studio), memory management (context window + compaction strategies), orchestration primitives (Plan&Execute DAG engine, Blackboard), tool interface and registry, skill system, MCP gateway, prompt utilities, and embedding support. Nothing in sp4rk is c0wrk-specific.

### `core/` — c0wrk Orchestration + Domain Services

The brain of c0wrk. Implements the conductor-driven orchestration cycle: the Router classifies requests (domain, complexity, matched skills, model selection), and a single Conductor ReAct loop owns the task end-to-end. Planning (`declare_plan`), subtask delegation (`delegate`), reflection (`reflect`), and user interaction (`ask_user`) are tool calls inside the Conductor loop, not pipeline phases. The top-level Orchestrator ties routing and Conductor execution together. Wraps the sp4rk tool registry with policy enforcement, confirmation flow, and the LLM judge. Wires the MCP gateway and skill system (both implemented in sp4rk) into the orchestration cycle. Contains all prompt templates (embedded markdown).

Also owns domain services:
- `core/vectorindex/` — embedding, BM25+chromem hybrid search, git branch monitoring for index freshness
- `core/proxy/` — HTTP proxy configuration with PAC support
- `core/terminal/` — PTY lifecycle management, shell environment, I/O
- `core/workspace/` — fsnotify watcher with debouncing, git status/diff operations, file tree walking
- `core/tools/` — built-in tool registration, c0wrk-specific tool types (ask_user)

### `backend/` — Application ViewModel

The "app layer" that the desktop UI interacts with. Owns the `Application` struct, configuration loading/resolution, session lifecycle management (create, handle message, persist, resume), SQLite persistence, and project lifecycle management. Exposes `FrontendAPI` methods split by concern area (`frontend_api_*.go`). Wraps `core.OrchestratorBuilder` and imports `core/` and sp4rk directly for domain types and orchestrator integration. Delegates workspace watching, vector indexing, terminal management, and git operations to `core/` domain services. Contains `backend/config/paths.go` as the single source of truth for all `~/.c0wrk/` filesystem paths.

### `desktop/` — Wails Bindings & Lifecycle

Thin layer. The `desktop.App` struct embeds `*backend.FrontendAPI` so its methods are visible to the Wails binding generator. Handles `OnStartup` (config loading, shell environment, backend init) and `OnShutdown`. Manages pending confirmations (`sync.Map`) between backend and frontend.

### `frontend/` — React UI

TypeScript/React application. Communicates with Go exclusively through:

1. **RPC**: `window.go.desktop.App.*` — async promise-based calls
2. **Events**: `window.runtime.EventsOn/EventsEmit` — real-time streaming

No direct Go imports. Auto-generated bindings at `frontend/wailsjs/go/desktop/App.{js,d.ts}`.

## Key Boundary Files

| Boundary           | Gateway File                           | Role                            |
| ------------------ | -------------------------------------- | ------------------------------- |
| sp4rk → core       | `core/builder.go`                      | Wires all sp4rk components      |
| core → backend     | `backend/configadapter.go`             | Converts config → BuilderConfig |
| core → backend     | `backend/application.go`               | Wraps OrchestratorBuilder       |
| backend → desktop  | `desktop/app.go`                       | Embeds FrontendAPI              |
| desktop → frontend | `frontend/wailsjs/go/desktop/App.d.ts` | Generated bindings              |

## Invariants

- `backend` may import sp4rk packages directly for type definitions
- `core` never imports `backend` or `desktop`
- sp4rk has zero knowledge of c0wrk-specific concepts
- sp4rk is a separate Go module; the import prohibition on `core/`/`backend/`/`desktop/` is enforced at the module level (ADR-014)
- All inter-layer communication between frontend and Go goes through Wails

## Anti-Patterns

- Importing `core/` or sp4rk packages from `desktop/` without necessity — prefer keeping desktop thin; import only when the types are genuinely needed at the UI boundary (e.g., type aliases, callback signatures)
- Putting business logic (routing, planning, tool policy) in `desktop/`
- Hand-editing files in `frontend/wailsjs/` — they are regenerated by `wails build`
- Adding backend-specific state to `core/` types (keep core reusable)
- Creating circular imports between `core/tools` and `core/` root package

## Related Specs

- [contracts/core-sp4rk.md](../contracts/core-sp4rk.md) - Detailed interface table
- [contracts/backend-core.md](../contracts/backend-core.md) - Config adapter pattern
- [contracts/desktop-frontend.md](../contracts/desktop-frontend.md) - Wails binding surface
- [sp4rk package layers](../../sdk/specs/architecture/layers.md) - canonical engine package layout and import graph
