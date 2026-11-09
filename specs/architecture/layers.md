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

## Repository Layout

Top-level entries outside the layer stack:

- `main.go` — the binary's entry point: CLI parse, single-instance lock, crash capture, window-bounds load, then `wails.Run`
- `internal/` — shared leaf utilities importable from every Go layer (see below)
- `scripts/` — helper scripts: `fetch-onnx.ps1` and `fetch-embedding-model.ps1` sit behind Makefile targets (the Windows branch of `fetch-onnx` / `fetch-embedding-model` delegates to them; `build.ps1` mirrors the same targets), while `benchmark-vector-index.sh` has no Makefile/CI wiring and is run manually — see `core/vectorindex/BENCHMARKS.md`
- `build/` — Wails build assets: app icons, the `darwin/` bundle metadata, the `bin/` output directory
- `specs/`, `docs/`, `research/` — the specification system, developer guides, and the research catalog
- `config.example.yaml`, `Makefile`, `wails.json`, `go.mod` — the authoritative config reference, the build/test entry points, the Wails project metadata, and the single Go module

## Import Rules

| Layer       | May Import                           | Must NOT Import              |
| ----------- | ------------------------------------ | ---------------------------- |
| `frontend/` | Nothing (communicates via Wails IPC) | Go packages                  |
| `desktop/`  | `backend`, `core`, `internal`, sp4rk | —                            |
| `backend/`  | `core`, `internal`, sp4rk            | —                            |
| `core/`     | `internal`, sp4rk                     | `backend`, `desktop`         |
| `internal/` | Standard library, external libs, `backend/config` (`internal/webaccess` only); `internal/webaccess` test files additionally import the `backend` root package for the surface contract | `core`, `desktop`, the `backend` root package from production code |

> **`internal/` hygiene** is pinned for the sidecar by `internal/webaccess/nodeps_test.go`: no file in that package — tests included — may import `desktop` or Wails.
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
- `core/e2s/` — the E2S explicit-execution-state loop: the externalized state Σ, its merge operator, and the per-turn driver built on raw sp4rk primitives
- `core/units/` — the durable per-unit recovery ledger shared by the mainline Conductor and isolated execution contexts (ADR-048)
- `core/markitdown/` — document-to-Markdown conversion via the managed markitdown CLI; optional vision-assisted conversion through the markitdown Python API (embedded driver) that captions embedded document images with the active vision-capable model (`core/visionresolver.go` maps the active model/provider onto the OpenAI-compatible captioning endpoint; the driver additionally extracts and captions PDF-embedded images itself — markitdown's PDF path is text-only — and replaces docx/html/epub base64 data-URI images with captions; degrades to the plain CLI on any failure)
- `core/modelprofiles/` — tool-set narrowing for running the conductor against local / mid-size LLMs (model profiles; see [../domains/model-profiles.md](../domains/model-profiles.md))
- `core/toolmanager/` — managed external binary tools (rg, uv, markitdown): version checking, download, install, first-run bootstrap
- `core/updater/` — application self-update: GitHub release checking, fail-closed SHA256 verification against the release `SHA256SUMS` (artifacts stay unsigned — ADR-023), staged two-process install with a `.old` rollback tree
- `core/version/` — build-time metadata (Version, GitCommit) injected via linker flags
- `core/cli/` — command-line argument parsing (raw argv → a `Request`); fail-closed and pure (standard library + `core/version` only)
- `core/prompts/` — the embedded (`go:embed`) markdown prompt templates the prose above refers to
- `core/embeddedllm/` — embedded local-model provisioning and supervision: pinned runtime and weights, hardware-aware launch planning, loopback OpenAI-compatible server
- `core/llmbudget/` — adaptive per-model LLM request budgets learned from live traffic
- `core/llmtls/` — per-provider TLS verification overrides (certificate pinning) for self-signed LLM endpoints
- `core/gittrust/` — the process-wide registry of user-trusted git repository roots the hardened spawn layer consults
- `core/research/`, `core/papers/`, `core/specs/` — the document domains behind the Research, Papers and Specs panels: in-memory models, parsers, atomic containment-checked writers, and the embedded skill/Subagent packs
- `core/seedpack/` — the crash-safe, hash-verified global pack-seeding core shared by `core/research`, `core/papers` and `core/specs`
- `core/memlimit/` — GC soft-memory-limit policy (`runtime.memory_soft_limit_mb`): `Setup` plus the per-OS RAM probes (`memlimit_ram_*.go`)
- `core/csp/` — the production Content-Security-Policy (`Policy`) stamped by the desktop CSP middleware and the web-access sidecar
- `core/savedialog/` — pure save-dialog decisions: default-directory resolution, write-path normalization, the attachment file-filter pattern
- `core/shutdown/` — teardown deadline policy (`EffectiveShutdownDeadline`, `DefaultHardDeadline`) shared by the desktop shutdown watchdog and the GPU-relaunch handover budget
- `core/webviewgpu/` — the WebKitGTK GPU policy parsing (`FromCanonical`) and the NVIDIA explicit-sync environment workaround
- `core/instancelock/` — the single-instance advisory lock primitive (`InstanceLock`, `AcquireSingleInstanceLock`, `WaitForInstanceLockRelease`) with its per-OS locks (`lock_unix.go`, `lock_windows.go`)
- `core/hitlwire/` — the transport-neutral HITL wire protocol: payload extraction and decision parsing for confirmations, ask-user answers, plan approvals, goal decisions and step-limit responses

### `backend/` — Application ViewModel

The "app layer" that the desktop UI interacts with. Owns the `Application` struct, configuration loading/resolution, session lifecycle management (create, handle message, persist, resume), SQLite persistence, and project lifecycle management. Exposes `FrontendAPI` methods split by concern area (`frontend_api_*.go`). Also owns the transport-neutral subsystem engines extracted from the GUI host: the web-access sidecar service (`frontend_api_webaccess.go`), the tool judge (`judge.go`), the vector-index launcher (`vector_launcher.go`), the startup phase bodies (`startup_phases.go`), the event batcher (`event_batcher.go`), the webview GPU watchdog and post-mortem (`webview_gpu_watchdog.go`, `webview_gpu_postmortem.go`), and the dialog/window state persistence (`dialog_state.go`, `window_state.go`). Wraps `core.OrchestratorBuilder` and imports `core/` and sp4rk directly for domain types and orchestrator integration. Delegates workspace watching, vector indexing, terminal management, and git operations to `core/` domain services. Contains `backend/config/paths.go` as the single source of truth for all `~/.c0wrk/` filesystem paths.

### `desktop/` — Wails Bindings & Lifecycle

Thin layer. The `desktop.App` struct embeds `*backend.FrontendAPI` so its methods are visible to the Wails binding generator. Handles `OnStartup` (config loading, shell environment, backend init) and `OnShutdown`. Manages pending confirmations (`sync.Map`) between backend and frontend. Builds the callbacks the backend runs on (`UIEmitFunc`, `AskUserFunc`, `PlanApprovalFunc`, `ConfirmFunc`, `HITLHandler`) and owns the single Wails event funnel (`App.emit`) plus the session-event batcher glue (`sessionEventBatcher`/`emitBatchedEvent`/`stopEventBatcher` in `desktop/event_batcher.go`) over the backend batcher machine (`backend/event_batcher.go`), which coalesces transient streaming events into one `c0wrk:events:batch` envelope per flush. The policy cores behind its OS-shell integrations live one layer down (`core/instancelock`, `core/csp`, `core/shutdown`, `core/webviewgpu`, `core/savedialog`, `core/hitlwire`); desktop keeps the Wails-facing hosts and adapters. The scope rule deciding what stays — lifecycle, bindings, OS-shell integration, HITL plumbing (the last until epic #84 phase 1b moves the funnels into `backend/interaction`) — is pinned by ADR-119.

### `frontend/` — React UI

TypeScript/React application. Communicates with Go exclusively through:

1. **RPC**: `window.go.desktop.App.*` — async promise-based calls
2. **Events**: `window.runtime.EventsOn/EventsEmit` — real-time streaming
3. **Window control**: `window.runtime.WindowSetTitle` — native window state driven from UI state (the title tracks the active project/session)

All three go through the single gateway `frontend/src/api/runtime.ts`; components and hooks never touch `window.runtime` or `window.go` directly.

No direct Go imports. Auto-generated bindings at `frontend/wailsjs/go/desktop/App.{js,d.ts}`.

### `internal/` — Shared Leaf Utilities

Outside the stack: every Go layer may import `internal/`, and `internal/` imports no layer (the single exception is `internal/webaccess`, which reads its bind/TLS configuration types from `backend/config`).

- `internal/sysproc/` — child-process creation attributes (console hiding on Windows) and the single git spawn choke point (`GitCmd`, `GitCmdRaw`, `gitSafetyOverrides`)
- `internal/securehttp/` — HTTPS-only client hardening shared by the outbound download surfaces (`core/updater`, `core/toolmanager`)
- `internal/shellresolver/` — shell binary resolution shared by `backend/config` (shell environment loading) and `core/terminal` (PTY sessions)
- `internal/webaccess/` — the browser web-access sidecar: an HTTP + WebSocket server mirroring the desktop RPC/event surface
- `internal/gittest/` — disposable canary git repositories for the spawn-neutralization tests
- `internal/specslint/`, `internal/teststress/`, `internal/testtiming/`, `internal/vulncheck/` — repository tooling: ADR-numbering lint, the stress-run driver, the timer-debt guard, and the `govulncheck` wrapper

## Key Boundary Files

| Boundary           | Gateway File                           | Role                            |
| ------------------ | -------------------------------------- | ------------------------------- |
| sp4rk → core       | `core/builder.go`                      | Wires all sp4rk components      |
| core → backend     | `backend/configadapter.go`             | Converts config → BuilderConfig |
| core → backend     | `backend/application.go`               | Wraps OrchestratorBuilder       |
| backend → desktop  | `desktop/app.go`                       | Embeds FrontendAPI              |
| backend → desktop  | `desktop/startup_phases.go`            | Builds the UI emit + HITL callbacks the backend runs on |
| desktop → frontend | `frontend/wailsjs/go/desktop/App.d.ts` | Generated bindings              |
| desktop → frontend | `frontend/src/api/runtime.ts`          | Single RPC + event gateway (batch-envelope dispatcher) |

## Invariants

- `backend` may import sp4rk packages directly for type definitions
- `internal/` packages stay leaves: they import the standard library, external dependencies, and — for `internal/webaccess` only — `backend/config` in production code plus the `backend` root package in its surface-contract test (`internal/webaccess/contract_test.go`); `core/`, `backend/` and `desktop/` all may import them
- `core` never imports `backend` or `desktop`
- `backend` and `core` contain no `github.com/wailsapp/wails` imports — Wails types stop at `desktop/` (ADR-119)
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
