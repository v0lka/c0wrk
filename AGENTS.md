# AGENTS.md

Guidance for coding agents working on **c0wrk** — a desktop AI coding-agent built with Wails v2 (Go backend + React 19 / Vite 6 / TS frontend).

## Specifications

Detailed system specs live in `specs/`. Before making structural changes, read the relevant spec:

- Start with `specs/INDEX.md` to find the right document for your task.
- `specs/META.md` defines spec formats and update rules — read before creating/updating specs.
- `specs/contracts/` define cross-boundary interface rules.
- `specs/domains/` explain subsystem behavior and invariants.
- `specs/decisions/` explain why things are designed the way they are.

## Project shape

- Two Go modules: `github.com/v0lka/c0wrk` (root: `core/`, `backend/`, `desktop/`, `frontend/`) and the sp4rk module `github.com/v0lka/sp4rk` (local `sdk/` directory). The root module depends on sp4rk via `replace github.com/v0lka/sp4rk => ./sdk` for local development. See ADR-014. Binary/app name is `c0wrk-desktop` (see `wails.json`).
- Entry point: `main.go` → `desktop.NewApp()` → Wails runs with `OnStartup = app.Startup` (`desktop/startup.go`).
- Go `1.26.3` (`go.mod` in both modules). Frontend uses React 19, Tailwind v4, Vite 6, TS ~5.7.

### Layered architecture (import direction matters)

```
desktop/   Wails bindings + app lifecycle (UI callbacks)  →  depends on backend, core
backend/   "Application" ViewModel, config, session mgr, persistence, MCP installer, workspace watcher
core/      Orchestrator / planner / router / reflector / tool registry / MCP gateway / vector index / proxy / c0wrk-specific tools
sdk/       sp4rk (github.com/v0lka/sp4rk) — reusable engine: agent executor, llm providers, memory compaction, orchestration, tools, prompts
frontend/  React UI; talks to Go via `frontend/wailsjs/go/desktop/App` (generated)
```

Rule enforced by layout: `backend/` and `desktop/` import `core` and `sdk/` directly. `core/` remains the primary consumer of sp4rk. No convenience re-export layers exist — all types are imported from their source packages. See ADR-008.

## Commands

Use the Makefile; it handles platform-specific ONNX Runtime bootstrap and runs both Go modules:

- `make test` — `go test ./...` (root) + `cd sdk && go test ./...` + `cd frontend && npm test` (vitest)
- `make lint` — `golangci-lint run` (root) + `cd sdk && golangci-lint run` + `cd frontend && npm run lint` (config at `.golangci.yml`, v2 schema)
- `make build` — installs frontend deps, runs `wails build`, then `make fetch-onnx` + `make fetch-embedding-model`
- `make dev-desktop` — Vite dev server only (`cd frontend && npm run dev`); for full hot-reload use `wails dev` from repo root
- `make fetch-onnx` — downloads ONNX Runtime 1.24.1 into `.cache/` and copies into `build/bin/c0wrk-desktop.app/Contents/MacOS/`. **Required after every `wails build`** or the app won't launch.
- `make clean` — removes `build/bin`, `.cache`, `frontend/dist`

Frontend-only: `cd frontend && npm run lint | build | dev | test`. Frontend tests use **vitest** (`npm test` / `npm run test:watch`); test files live alongside source (`*.test.ts`).

### Focused Go workflows

- Single package (root module): `go test ./sdk/agent/...` — note: `sdk/` is the sp4rk module (a separate Go module), so run from the `sdk/` dir: `cd sdk && go test ./agent/...`
- Single package (sp4rk module): `cd sdk && go test ./agent/...`
- Single test (root module): `go test ./core -run TestOrchestrator_PlanExecuteMode -v`
- Single test (sp4rk module): `cd sdk && go test ./agent -run TestExecutor -v`
- Tests use in-package style (`package agent`, not `agent_test`); many packages have a `testhelpers_test.go`.

## Config & runtime

- Runtime config lives at `~/.c0wrk/config.yaml` (default dir constant `config.DefaultAgentDir = ".c0wrk"`). Copy from `config.example.yaml` — it is the authoritative reference for every tunable (LLM providers, executor loop caps, compaction thresholds, tool limits, timeouts, security policies).
- Env vars in config are expanded as `${VAR}`. On macOS, `startup.go` calls `config.LoadShellEnvironment()` **before** any other init because Finder-launched apps don't inherit shell env — preserve this ordering if you touch `Startup`.
- SQLite DB (via `modernc.org/sqlite`, CGO-free) defaults to `~/.c0wrk/database.db`. Wail-level file lock: a single `*sql.DB` is shared across `session`, `project` stores.
- **External runtime deps**: There are no startup-hard dependencies. `git` is checked lazily on first CODE-mode project switch via `exec.LookPath` in `backend/frontend_api_project.go` — if missing, a `runtime_error` toast is shown and the switch is rejected. CHAT mode (No Project) never requires git. All other tools — `rg` (ripgrep), `rtk`, `uv`, `markitdown` — are managed by the tool-manager (`core/toolmanager/`) and auto-downloaded on first run to `~/.c0wrk/tools/`. See `specs/decisions/010-tool-manager.md`.
- Vector index needs ONNX Runtime (fetched by `make fetch-onnx`) plus a quantized embedding model + tokenizer (fetched by `make fetch-embedding-model`). The embedder loads asynchronously after `EventBackendReady`; vector search RPCs return empty results until ready.

## Conventions & gotchas

- **Logging**: `log/slog` everywhere. Pass `*slog.Logger` through constructors; don't use global `slog` in new code except at the top-level boundary.
- **Errors**: `errorlint` + `perfsprint` are on. Use `%w` for wrapping, `errors.Is/As`, never `fmt.Errorf` where `errors.New` suffices, never `fmt.Sprintf("%s", s)`.
- **Linters enabled** (see `.golangci.yml`): `errcheck` (incl. type assertions), `govet`, `staticcheck`, `ineffassign`, `unused`, `errorlint`, `nilerr`, `gocritic` (diagnostic+performance+style, except `hugeParam`/`rangeValCopy`), `revive` with `exported` & `var-naming` disabled, `prealloc`, `bodyclose`, `noctx`, `sqlclosecheck`, `perfsprint`, `unconvert`, `wastedassign`, `copyloopvar`, `durationcheck`, `whitespace`, `depguard`. Run `make lint` before declaring done.
- **Tool registry pattern**: reusable built-in tools live in `github.com/v0lka/sp4rk/tools/builtins/`; c0wrk-specific tools (e.g. `ask_user`) live in `core/tools/`. MCP-backed tools are added at runtime via `github.com/v0lka/sp4rk/tools/mcp/gateway.go`. To add a new built-in tool, implement `tools.Tool` in the correct package and wire it through `core/tools.RegisterBuiltinTools` (defined in `core/tools/builtin_registration.go`, called from `core/builder.go`).
- **Prompts are data**: markdown files under `core/prompts/` are embedded via `prompts.go` in the same dir. Tests verify every `.md` file is referenced — update both when adding/removing a prompt.
- **Generated Wails bindings** at `frontend/wailsjs/go/desktop/App.{js,d.ts}` are regenerated by `wails build` / `wails dev` from the methods on `desktop.App`. Don't hand-edit them; if they drift, rebuild.
- **Desktop API surface**: `*desktop.App` embeds `*backend.FrontendAPI`; promoted methods are visible to the Wails binding generator. Frontend-callable methods are split across `backend/frontend_api_*.go` files by area (`config`, `git`, `mcp`, `plan_review`, `project`, `prompt`, `session`, `skills`, `terminal`, `vector`, `workspace`). New frontend-callable methods go in the matching `backend/frontend_api_*.go`.
- **Security/tool policies** are enforced in `core/builder.go` → `applySecurityPolicies` from `config.Security.ToolPolicies`. Default is `user_confirm`; pending confirmations flow through `App.pendingConfirmations` sync.Map back to the UI.
- **Path logic centralization**: All filesystem-path construction and containment validation must go through the centralized path API:
  - `github.com/v0lka/sp4rk/pathutil/` — pure algorithmic primitives (`IsWithinPath`, `SplitPathComponents`, `ResolveExistingPrefix`). Zero project-specific knowledge. Usable from any layer.
  - `backend/config/paths.go` — project-specific path construction (`ProjectSkillsPath`, `SessionStepDumpDir`, `IsSessionInfraPath`) and containment wrappers that delegate to `pathutil`. The ONLY package that knows c0wrk's directory layout.
  - `core/pathsegments.go` — cross-layer path segment constants (e.g., `SkillsRelativePath`).
  - NEVER inline `strings.HasPrefix(path, root+"/")`, `filepath.Rel`+`HasPrefix(rel,"..")`, or `filepath.Join(ws, ".agents", "skills")` for containment or path construction. Always use `pathutil.IsWithinPath`, `config.IsWithinPath`, `config.ProjectSkillsPath`, or the relevant constant.

## Things NOT to do

- Don't add `go.work` — the two-module setup uses `replace` directives in the root `go.mod`, not `go.work`. `go.work` is a local-development tool that is not published and would conflict with the `replace` directive. See ADR-014.
- Don't add `vendor/` or change the module path (either module).
- Don't add a new frontend test framework; vitest is already configured. Add tests as `*.test.ts` alongside the source file.
- Don't `go install` the ONNX runtime differently per-machine — always go through `make fetch-onnx` so `.cache/` stays consistent.
- Don't commit `coverage*.out`, `*_cov.out`, `config.local.yaml`, `.cache/`, `build/bin/`, or anything matched in `.gitignore`.
- Don't create new arrays or objects inside Zustand selectors (e.g. `useStore(s => s.items.map(…))` or `useStore(s => condition ? derive(s) : [])`). React 19's `useSyncExternalStore` compares snapshots by reference — a new object/array on every call causes an infinite re-render loop (React error #185). Return direct store references from selectors and derive values with `useMemo` in a custom hook. See `.qoder/specs/frontend-anti-patterns.md §2.7`.

## Frontend architecture

### Stack

React 19 + TypeScript ~5.7 + Vite 6 + Tailwind CSS v4 + Zustand 5. UI primitives from shadcn/ui (new-york style) + Radix UI. Icons via lucide-react. Markdown rendered with react-markdown 10 + remark-gfm/emoji/breaks + rehype-highlight/sanitize/external-links/slug/autolink-headings. Syntax highlighting via highlight.js 11 (selective language registration). Mermaid 11 lazy-loaded for diagrams. File tree icons via Nerd Fonts (SauceCodePro NF).

### Layout

Three-column panel layout (no router): Sidebar (300px default, collapsible to 40px) | Main Chat Area | File Viewer (500px default, collapsible to 40px). Resize handles between panels (4px, drag + keyboard). File viewer only visible when files are open. Sidebar and file viewer states persist via `localStorage`.

### Communication with Go backend

1. **RPC**: `window.go.desktop.App.*` — async promise-based calls for CRUD and data fetching.
2. **Events**: `window.runtime.EventsOn/EventsEmit` — real-time streaming during task execution.
   - **Session-scoped** events: `session:${sessionId}:${eventType}` (25+ event types for task lifecycle).
   - **Global** events: `startup_error`, `backend:ready`, `projects:loaded`, `sessions:loaded`, `project:*`, `workspace:tree_changed`, `vector_index:status`.

### State management (Zustand stores)

| Store                | Responsibility                                                                                 |
| -------------------- | ---------------------------------------------------------------------------------------------- |
| `chatStore`          | Messages per session, streaming text, thinking/activity/task flags, context fill, token counts |
| `planStore`          | Execution plan groups (DAG items), session stats (routing, attempts)                           |
| `sessionStore`       | Session list (sorted by last_active_at), active session ID                                     |
| `projectStore`       | Project list (sorted by last_active_at), active project ID                                     |
| `fileTreeStore`      | Lazy-loaded directory tree, expanded dirs, search entries, git status                          |
| `fileViewerStore`    | Open files (content/diff/language), tabs, panel width, collapsed state                         |
| `inputModeStore`     | Chat/terminal input mode, panel height, expanded state (persisted)                             |
| `executionModeStore` | Normal/advanced execution mode toggle (persisted)                                              |
| `blackboardStore`    | Blackboard facts and metadata for current session                                              |
| `planReviewStore`    | Plan review toggle state (persisted)                                                          |
| `settingsStore`      | Settings modal open/close, active tab                                                          |
| `uiStore`            | Sidebar collapsed state, log level                                                             |
| `vectorIndexStore`   | Vector index status/progress                                                                   |

Cross-component scroll coordination uses a React context (`ScrollContext.tsx`), not a Zustand store.

### Data model

- Backend persists `ChatMessage` (id, session_id, role, content, reasoning_content, tool_calls, metadata JSON, created_at).
- Frontend converts to `ChatMessageUI` (semantic string ID, sessionId, MessageType, content, metadata, timestamp).
- `groupMessages()` transforms flat `ChatMessageUI[]` into a `DisplayItem[]` tree (18 kinds: user, assistant, thought, thought_group, tool, tool_confirm, ask_user, step_limit, resume_action, error, service, plan_step, plan_review, reflection, step_finish, memory_read, context_compaction, action_placeholder).
- Grouping handles: plan step nesting, tool call/result correlation (via tool_call_id or composite key), thought collapsing, pending action extraction, special tool handling (subagent skipped, finish/memory compact).

### Key components

- **Sidebar**: Project selector + session selector (dropdowns with context menus, inline rename, search for 5+ sessions) + file tree workspace panel.
- **Chat Area**: Pinned last user message (sticky, collapsible) + scrollable message list + smart auto-scroll (50px threshold, "New activity" pill) + activity indicator.
- **Chat Input**: Auto-resize textarea (max 6 lines), Enter sends / Shift+Enter newline, auto-creates session if needed, cancel button during task.
- **Pending Actions Bar**: Sticky bar for unresolved prompts — tool confirmations (allow/deny/judge), ask-user multi-question forms, step limit, resume after failure.
- **Execution Panels**: Collapsible plan view with DAG graph (SVG, lane allocation) + item list, click-to-scroll to chat.
- **File Viewer**: Tab bar + syntax-highlighted content + unified diff overlay (character-level) + markdown preview toggle. Auto-refreshes on `workspace:tree_changed`. Binary detection (null bytes in first 8KB). State persisted to localStorage.

### Design system

One Dark theme. All colors as Tailwind v4 `@theme` custom properties (background `#282c34`, foreground `#abb2bf`, primary `#abb2bf` with primary-rgb `82,139,255` for rgba), destructive `#e06c75`, success `#98c379`, warning `#d19a66`, info `#61afef`, highlight `#e5c07b`). Base font 14px, dark color-scheme. Focus outlines globally suppressed. Custom scrollbar class (`.custom-scrollbar`, 8px, semi-transparent thumb).

### Event handling pattern

Session event handler subscribes to all session-scoped events on session change. Each handler: validates data with type guard → updates activity status → adds/updates chat store message → updates plan store. Streaming: `assistant_chunk` sets/appends text, `assistant_done` flushes to permanent message.

### Key Principles

- Design tokens are law. Every color, spacing, radius references CSS custom property. No raw hex in component code.
- No !important. If you need it, abstraction is wrong. Fix component API, not CSS specificity.
- Single source of truth. Each piece of data lives in exactly one store. No dual bookkeeping, no parallel state trees.
- Normalized store, incremental updates. Don't rebuild trees from flat arrays on every render. Index by ID, update in place.
- Stable Zustand selectors. Every selector passed to `useStore(selector)` must return a referentially stable value — either a primitive, a direct store property, or use a custom hook with granular selectors + `useMemo`. Never allocate arrays/objects inside a selector.
- Type safety at boundaries. Validate and type event data at ingestion point. Everything downstream typed — no Record<string, unknown>.
- Small, focused components. Target no file over 200 lines (281-line `ChatInput.tsx` uses sub-hooks to manage complexity). No component handling more than one domain concept. Extract hooks for data loading.
- One import path for backend calls. All RPC through @/api/\*. No direct imports from wailsjs/go/desktop/App.
- Declarative persistence. Use Zustand middleware, not manual localStorage calls.
- No module-level side effects. Store files define stores. Initialization happens in React lifecycle hooks after runtime readiness confirmed.
- Event handlers are testable. Each event type has focused handler function testable in isolation without React rendering.

## Pre-PR checklist

`make build` → `make lint` → `make test`. All three must be clean; CI is not configured in this repo, so local verification is the gate.
