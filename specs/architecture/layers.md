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
│  sp4rk (ext. repo)  Reusable agent execution engine                 │
│   github.com/v0lka/  Agent executor, LLM providers, memory,         │
│   sp4rk              orchestration loop, tools, skills, MCP gateway │
│                      prompts, embeddings                            │
└─────────────────────────────────────────────────────────────────────┘
```

> sp4rk (module `github.com/v0lka/sp4rk`) lives in its [own repository](https://github.com/v0lka/sp4rk); c0wrk consumes it as an external dependency. It is shown here as the lowest layer.

## Import Rules

| Layer       | May Import                           | Must NOT Import              |
| ----------- | ------------------------------------ | ---------------------------- |
| `frontend/` | Nothing (communicates via Wails IPC) | Go packages                  |
| `desktop/`  | `backend`, `core`, sp4rk             | —                            |
| `backend/`  | `core`, sp4rk                        | —                            |
| `core/`     | sp4rk                                | `backend`, `desktop`         |
| sp4rk (ext. repo) | External libs only                | `core`, `backend`, `desktop` |

> **Module boundary** (ADR-015): sp4rk is a separate Go module (`github.com/v0lka/sp4rk`) living in its [own repository](https://github.com/v0lka/sp4rk). The root module (`github.com/v0lka/c0wrk`) depends on it as a normal external dependency (`require github.com/v0lka/sp4rk`, no `replace` directive). The import prohibition on sp4rk is enforced at the repository level — sp4rk cannot import `core/`, `backend/`, or `desktop/` because they live in a different repository.

> **depguard enforcement** (`.golangci.yml`): the linter enforces one layering rule here — `core` may not import `backend`. sp4rk isolation is now enforced at the repository level (sp4rk lives in a separate repo, ADR-015), so no sp4rk-targeted depguard rule remains. All other prohibitions (`→desktop`) are maintained by convention (and by the import cycles they would create), not by the linter.

## Layer Responsibilities

### sp4rk (external repository) — Reusable Agent Engine

The lowest layer. Contains the generic building blocks for an LLM agent application: executor (ReAct loop), LLM provider abstraction (OpenAI, Anthropic, Gemini, LM Studio), memory management (context window + compaction strategies), orchestration primitives (Plan&Execute DAG engine, Blackboard), tool interface and registry, skill system, MCP gateway, prompt utilities, and embedding support. Nothing in sp4rk is c0wrk-specific.

### `core/` — c0wrk Orchestration + Domain Services

The brain of c0wrk. Implements the conductor-driven orchestration cycle: the Router classifies requests (domain, complexity, matched skills, model selection), and a single Conductor ReAct loop owns the task end-to-end. Planning (`declare_plan`), subtask delegation (`delegate`), reflection (`reflect`), and user interaction (`ask_user`) are tool calls inside the Conductor loop, not pipeline phases. The top-level Orchestrator ties routing and Conductor execution together. Wraps the sp4rk tool registry with policy enforcement, confirmation flow, and the LLM judge. Wires the MCP gateway and skill system (both implemented in sp4rk) into the orchestration cycle. Contains all prompt templates (embedded markdown).

Also owns domain services:
- `core/vectorindex/` — embedding, BM25+chromem hybrid search, git branch monitoring for index freshness
- `core/proxy/` — HTTP proxy configuration with bypass list and custom TLS CA certificates
- `core/terminal/` — PTY lifecycle management, shell environment, I/O. Unix PTY (`manager.go`, build tag `!windows`) and Windows ConPTY (`manager_windows.go`, build tag `windows`) twins behind a common `Manager`/`Session` surface; exactly one compiles per OS
- `core/workspace/` — fsnotify watcher with debouncing, git status/diff operations, file tree walking
- `core/tools/` — built-in tool registration, c0wrk-specific tool types (ask_user)
- `core/goal/` — Goal domain: the declared success condition, the budget constraining work toward it, and the runtime state machine tracking progress
- `core/markitdown/` — document-to-Markdown conversion via the managed markitdown CLI; optional vision-assisted conversion through the markitdown Python API (embedded driver) that captions embedded document images with the active vision-capable model (`core/visionresolver.go` maps the active model/provider onto the OpenAI-compatible captioning endpoint; the driver additionally extracts and captions PDF-embedded images itself — markitdown's PDF path is text-only — and replaces docx/html/epub base64 data-URI images with captions; degrades to the plain CLI on any failure)
- `core/smallllm/` — tool-set narrowing for running the conductor against small/local LLMs
- `core/toolmanager/` — managed external binary tools (rg, uv, markitdown): version checking, download, install, first-run bootstrap
- `core/updater/` — application self-update: GitHub release checking, signature verification, asset download/install
- `core/version/` — build-time metadata (Version, GitCommit) injected via linker flags

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
- sp4rk is a separate Go module in its own repository; the import prohibition on `core/`/`backend/`/`desktop/` is enforced at the repository level (ADR-015)
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
- [sp4rk package layers](https://github.com/v0lka/sp4rk/blob/main/specs/architecture/layers.md) - canonical engine package layout and import graph
