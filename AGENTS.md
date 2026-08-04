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

- Go module: `github.com/v0lka/c0wrk` (root: `core/`, `backend/`, `desktop/`, `frontend/`). Binary/app name is `c0wrk-desktop` (see `wails.json`).
- Entry point: `main.go` → `desktop.NewApp()` → Wails runs with `OnStartup = app.Startup` (`desktop/startup.go`).
- Go `1.26.3` (single root module; `go.mod` at repo root). Frontend uses React 19, Tailwind v4, Vite 6, TS ~5.7.

### Layered architecture (import direction matters)

```
desktop/   Wails bindings + app lifecycle (UI callbacks)  →  depends on backend, core
backend/   "Application" ViewModel, config, session mgr, persistence, MCP installer, workspace watcher
core/      Orchestrator / planner / router / reflector / tool registry / MCP gateway / vector index / proxy / c0wrk-specific tools
frontend/  React UI; talks to Go via `frontend/wailsjs/go/desktop/App` (generated)
```

Rule enforced by layout: `backend/` and `desktop/` import `core` directly. `core/` remains the primary consumer of sp4rk. No convenience re-export layers exist — all types are imported from their source packages. See ADR-008.

## Commands

Use the Makefile; it handles platform-specific ONNX Runtime bootstrap across the single root Go module and the frontend:

- `make test` — `go test ./...` (root) + `cd frontend && npm test` (vitest)
- `make lint` — `golangci-lint run` (root) + `cd frontend && npm run lint` (config at `.golangci.yml`, v2 schema)
- `make build` — installs frontend deps, runs `wails build`, then `make fetch-onnx` + `make fetch-embedding-model`
- `make dev-desktop` — Vite dev server only (`cd frontend && npm run dev`); for full hot-reload use `wails dev` from repo root
- `make fetch-onnx` — downloads ONNX Runtime 1.24.1 into `.cache/` and copies into `build/bin/c0wrk-desktop.app/Contents/MacOS/`. **Required after every `wails build`** or the app won't launch.
- `make clean` — removes `build/bin`, `.cache`, `frontend/dist`

Frontend-only: `cd frontend && npm run lint | build | dev | test`. Frontend tests use **vitest** (`npm test` / `npm run test:watch`); test files live alongside source (`*.test.ts`).

### Focused Go workflows

- Single package (root module): `go test ./core/...`
- Single test (root module): `go test ./core -run TestOrchestrator_PlanExecuteMode -v`
- Tests use in-package style (`package agent`, not `agent_test`); many packages have a `testhelpers_test.go`.

## Config & runtime

- Runtime config lives at `~/.c0wrk/config.yaml` (default dir constant `config.DefaultAgentDir = ".c0wrk"`). Copy from `config.example.yaml` — it is the authoritative reference for every tunable (LLM providers, executor loop caps, compaction thresholds, tool limits, timeouts, security policies, small-LLM profile).
- **Small-LLM profile** (`small_llm.*`): an optional, manual-only set of optimizations for running on small/local models — tool-set narrowing (`essential_tools`), system-prompt Lite swap (`system_prompt`), sampling override (`sampling`), and loop-hardening (`loop_hardening`). Each variant is gated by BOTH the master `enabled` toggle and its own sub-toggle; the whole profile is a no-op when the master toggle is off (default). Editable at runtime via `GetSmallLLMConfig`/`UpdateSmallLLMConfig`. See `specs/domains/small-llm.md` and `specs/decisions/022-small-llm-profile.md`.
- Env vars in config are expanded as `${VAR}`. On macOS, `startup.go` calls `config.LoadShellEnvironment()` **before** any other init because Finder-launched apps don't inherit shell env — preserve this ordering if you touch `Startup`.
- SQLite DB (via `modernc.org/sqlite`, CGO-free) defaults to `~/.c0wrk/database.db`. Wail-level file lock: a single `*sql.DB` is shared across `session`, `project` stores.
- **External runtime deps**: There are no startup-hard dependencies. `git` is checked lazily on first CODE-mode project switch via `exec.LookPath` in `backend/frontend_api_project.go` — if missing, a `runtime_error` toast is shown and the switch is rejected. CHAT mode (No Project) never requires git. All other tools — `rg` (ripgrep), `uv`, `markitdown` — are managed by the tool-manager (`core/toolmanager/`) and auto-downloaded on first run to `~/.c0wrk/tools/`. See `specs/decisions/010-tool-manager.md`.
- Vector index needs ONNX Runtime (fetched by `make fetch-onnx`) plus a quantized embedding model + tokenizer (fetched by `make fetch-embedding-model`). The embedder loads asynchronously after `EventBackendReady`; vector search RPCs return empty results until ready.

## Conventions & gotchas

- **Logging**: `log/slog` everywhere. Pass `*slog.Logger` through constructors; don't use global `slog` in new code except at the top-level boundary.
- **Errors**: `errorlint` + `perfsprint` are on. Use `%w` for wrapping, `errors.Is/As`, never `fmt.Errorf` where `errors.New` suffices, never `fmt.Sprintf("%s", s)`.
- **Linters enabled** (see `.golangci.yml`): `errcheck` (incl. type assertions), `govet`, `staticcheck`, `ineffassign`, `unused`, `errorlint`, `nilerr`, `gocritic` (diagnostic+performance+style, except `hugeParam`/`rangeValCopy`), `revive` with `exported` & `var-naming` disabled, `prealloc`, `bodyclose`, `noctx`, `sqlclosecheck`, `perfsprint`, `unconvert`, `wastedassign`, `copyloopvar`, `durationcheck`, `whitespace`, `depguard`. Run `make lint` before declaring done.
- **Tool registry pattern**: reusable built-in tools live in `github.com/v0lka/sp4rk/tools/builtins/`; c0wrk-specific tools (e.g. `ask_user`) live in `core/tools/`. MCP-backed tools are added at runtime via `github.com/v0lka/sp4rk/tools/mcp/gateway.go`. To add a new built-in tool, implement `tools.Tool` in the correct package and wire it through `core/tools.RegisterBuiltinTools` (defined in `core/tools/builtin_registration.go`, called from `core/builder.go`).
- **Subagent Profiles**: a specialized subagent persona/budget is a markdown file at `<workspace>/.agents/agents/<name>/AGENT.md` (the `AgentsRelativePath = ".agents/agents"` constant in `core/pathsegments.go`, paralleling `.agents/skills/` for skills). YAML frontmatter declares `name` (must match dir), `description`, and optional `tools` (`all` default | `read-only` | comma-list), `max-steps`, `model`, `allow-redelegate`, `hidden`, `color`; the body is the agent's core directive (it replaces the orchestrator system prompt at delegation time via `buildSpecializedSystemPrompt`). Parsed/managed by the self-contained `github.com/v0lka/sp4rk/agents` package (`AgentManager`, `ParseAgent`); applied in `core/conductor.go` `buildSubAgentTask`. Two targeting modes: (1) **explicit `#agent-name` mention** — the user types `#code-reviewer`, the frontend extracts it (`lib/parseReferences.ts` `extractAgentRefs`), `sendMessage` threads it as `activeAgents` (arg 4 → Go `SendMessage` arg 4 → `HandleOptions.UserAgents`), `PreprocessMessageText` strips the ref, and `enrichAgentContext` attaches it as a `## Requested Subagents` directive the Conductor MUST delegate to; (2) **implicit/discovery** — a non-empty catalog renders `## Available Subagents` so the Conductor may delegate via `delegate(agent: "name")` at its discretion. `#` is used (not `@`) because `@` is the file-ref trigger and `@file#L20` line anchors must not collide; `#review`, `/review`, `@review` are three distinct refs. Plan steps target an agent via `declare_plan`'s per-step `agent` field (`PlanStep.Agent` survives JSON restore + the blackboard `copyPlan` fix). See [ADR-021](specs/decisions/021-subagents.md).
- **Prompts are data**: markdown files under `core/prompts/` are embedded via `prompts.go` in the same dir. Tests verify every `.md` file is referenced — update both when adding/removing a prompt.
- **Generated Wails bindings** at `frontend/wailsjs/go/desktop/App.{js,d.ts}` are regenerated by `wails build` / `wails dev` from the methods on `desktop.App`. Don't hand-edit them; if they drift, rebuild.
- **Desktop API surface**: `*desktop.App` embeds `*backend.FrontendAPI`; promoted methods are visible to the Wails binding generator. Frontend-callable methods are split across `backend/frontend_api_*.go` files by area (`agents`, `attachment`, `config`, `git`, `goal`, `mcp`, `project`, `prompt`, `review`, `session`, `skills`, `terminal`, `vector`, `workdirs`, `workspace`). New frontend-callable methods go in the matching `backend/frontend_api_*.go`.
- **Security/tool policies** are enforced in `core/builder.go` → `applySecurityPolicies` from `config.Security.ToolPolicies`. Default is `user_confirm`; pending confirmations flow through `App.pendingConfirmations` sync.Map back to the UI.
- **Path logic centralization**: All filesystem-path construction and containment validation must go through the centralized path API:
  - `github.com/v0lka/sp4rk/pathutil/` — pure algorithmic primitives (`IsWithinPath`, `SplitPathComponents`, `ResolveExistingPrefix`). Zero project-specific knowledge. Usable from any layer.
  - `backend/config/paths.go` — project-specific path construction (`ProjectSkillsPath`, `ProjectAgentsPath`, `SessionStepDumpDir`, `IsSessionInfraPath`) and containment wrappers that delegate to `pathutil`. The ONLY package that knows c0wrk's directory layout.
  - `core/pathsegments.go` — cross-layer path segment constants (e.g., `SkillsRelativePath`, `AgentsRelativePath`).
  - NEVER inline `strings.HasPrefix(path, root+"/")`, `filepath.Rel`+`HasPrefix(rel,"..")`, or `filepath.Join(ws, ".agents", "skills")` for containment or path construction. Always use `pathutil.IsWithinPath`, `config.IsWithinPath`, `config.ProjectSkillsPath`, or the relevant constant.

## Codebase navigation via `codebase-memory-mcp` (if available)

If the `codebase-memory-mcp` MCP server is connected (e.g. its tools appear with an `[MCP]` prefix), prefer it for code discovery, call-graph tracing, and architecture questions instead of broad manual `grep`/`glob` sweeps. **Every project-scoped tool requires a project identifier.** Calling such a tool without the correct identifier returns an error of the form `project not found or not indexed` (with a hint listing the available projects).

### Project identifier

The identifier is derived from the repository's **absolute path**: replace every path separator (`/`) with `-` and drop the leading slash.

| Repository path                     | Project identifier                       |
| ----------------------------------- | ---------------------------------------- |
| `/path/to/repo`                     | `path-to-repo`                           |

`index_repository` accepts an optional `name` argument that overrides this derived identifier. **Do not set it** — it creates a separate project entry alongside the path-derived one and breaks the predictable identifier rule. Always let the identifier be derived from the path.

### Required workflow

1. **Verify the project is indexed** — call `list_projects` first (it takes no arguments). Compute the expected identifier from the repo path (rule above) and check it against the returned `projects[].name` list (or match by `root_path`).
2. **Index if absent** — if the project is missing, call `index_repository(repo_path=<absolute path>)` (omit `name`). Read the returned `project` field to confirm the actual identifier; it equals the path-derived form. Use `mode="fast"` for a quick pass, `"full"` when you need similarity/semantic edges.
3. **Pass the identifier to the tools** — supply it as the `project` argument to every project-scoped tool. Without it, the tool errors out and will not query the graph.

### Tools by purpose

- **Discover** — `search_graph` (BM25 + semantic + regex over functions/classes/routes), `search_code` (grep augmented by the call graph), `get_architecture` (packages, clusters, layers, hotspots).
- **Read code** — `get_code_snippet` (read a function/class by `qualified_name`; resolve it first via `search_graph`).
- **Trace relationships** — `trace_path` (callers/callees, data flow, cross-service hops), `query_graph` (raw Cypher for multi-hop/aggregate queries).
- **Change & impact** — `detect_changes` (diff vs a git ref + blast radius), `get_graph_schema` (node labels / edge types).
- **Index management** — `index_repository`, `index_status`, `list_projects`, `delete_project`, `ingest_traces` (runtime traces), `manage_adr` (Architecture Decision Records).

## Things NOT to do

- Don't add `go.work` — this is a single Go module that depends on `github.com/v0lka/sp4rk` as a normal external dependency (no `replace` directive). `go.work` is a local-development tool that is not published; keep it out of the repo. See ADR-015.
- Don't add `vendor/` or change the module path.
- Don't add a new frontend test framework; vitest is already configured. Add tests as `*.test.ts` alongside the source file.
- Don't `go install` the ONNX runtime differently per-machine — always go through `make fetch-onnx` so `.cache/` stays consistent.
- Don't commit `coverage*.out`, `*_cov.out`, `config.local.yaml`, `.cache/`, `build/bin/`, or anything matched in `.gitignore`.
- Don't create new arrays or objects inside Zustand selectors (e.g. `useStore(s => s.items.map(…))` or `useStore(s => condition ? derive(s) : [])`). React 19's `useSyncExternalStore` compares snapshots by reference — a new object/array on every call causes an infinite re-render loop (React error #185). Return direct store references from selectors and derive values with `useMemo` in a custom hook.

## Frontend architecture

### Stack

React 19 + TypeScript ~5.7 + Vite 6 + Tailwind CSS v4 + Zustand 5. UI primitives from shadcn/ui (new-york style) + Radix UI. Icons via lucide-react. Markdown rendered with react-markdown 10 + remark-gfm/emoji/breaks + rehype-highlight/sanitize/external-links/slug/autolink-headings. Syntax highlighting via highlight.js 11 (selective language registration). Mermaid 11 lazy-loaded for diagrams. In-app code/markdown editing via CodeMirror 6 (`@codemirror/*` + `@lezer/highlight`). Embedded terminal via xterm.js (`@xterm/xterm` v6 + `@xterm/addon-fit`). Virtualized lists via `@tanstack/react-virtual`. Character-level diffs via `diff` v9. File tree icons via Nerd Fonts (`@m234/nerd-fonts`, SauceCodePro NF).

### Layout

Three-column panel layout (no router): Sidebar (default innerWidth/5, clamped 180-500px, collapsible to 40px) | Main Chat Area | File Viewer (width persisted, clamped 250-900px, collapsible to 40px). Resize handles between panels (4px `w-1`/`h-1`, drag + keyboard). File viewer only visible when files are open. Sidebar and file viewer states persist via `localStorage`.

### Communication with Go backend

1. **RPC**: `window.go.desktop.App.*` — async promise-based calls for CRUD and data fetching.
2. **Events**: `window.runtime.EventsOn/EventsEmit` — real-time streaming during task execution.
   - **Session-scoped** events: `session:${sessionId}:${eventType}` (41 event types for task lifecycle).
   - **Global** events: `startup_error`, `runtime_error`, `backend:ready`, `projects:loaded`, `sessions:loaded`, `project:*`, `session:renamed`, `workspace:tree_changed`, `skills:changed`, `git:status_changed`, `vector_index:status`, `tool_manager:start`, `tool_manager:progress`, `tool_manager:done`, `workdirs:changed`.

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
| `gitPanelStore`     | Git panel: branch info (ahead/behind), merge/rebase state (persisted)                          |
| `blackboardStore`    | Blackboard facts and metadata for current session                                              |
| `settingsStore`      | Settings modal open/close, active tab                                                          |
| `uiStore`            | Sidebar collapsed state, log level                                                             |
| `themeStore`         | App theme (`dark` \| `light`), persisted; writes `data-theme` to `<html>`                       |
| `vectorIndexStore`   | Vector index status/progress                                                                   |
| `goalStore`          | Goal lifecycle: pending proposal (condition/verify/clarification), status verdict, progress    |
| `reviewStore`        | Review / human-in-the-loop prompts (plan review, review_prompt items)                         |
| `attachmentsStore`   | Message attachments (per-session file list, per-file failure tracking)                         |
| `workDirsStore`      | Additional working directories (multi-repo workspace roots)                                    |

Cross-component scroll coordination uses a React context (`ScrollContext.tsx`), not a Zustand store.

### Data model

- Backend persists `ChatMessage` (id, session_id, role, content, reasoning_content, tool_calls, metadata JSON, created_at).
- Frontend converts to `ChatMessageUI` (semantic string ID, sessionId, MessageType, content, metadata, timestamp).
- `groupMessages()` transforms flat `ChatMessageUI[]` into a `DisplayItem[]` tree (21 kinds: user, assistant, thought, thought_group, tool, tool_confirm, ask_user, step_limit, resume_action, error, service, plan_step, subagent, reflection, step_finish, context_compaction, memory_read, plan_review, review_prompt, goal_proposal, checklist).
- Grouping handles: plan step nesting, tool call/result correlation (via tool_call_id or composite key), thought collapsing, pending action extraction, special tool handling (subagent skipped, finish/memory compact).

### Key components

- **Sidebar**: Project selector + session selector (dropdowns with context menus, inline rename, search for 5+ sessions) + file tree workspace panel.
- **Chat Area**: Pinned last user message (sticky, collapsible) + scrollable message list + smart auto-scroll (50px threshold, "New activity" pill) + activity indicator.
- **Chat Input**: Auto-resize textarea (max 6 lines), Enter sends / Shift+Enter newline, auto-creates session if needed, cancel button during task.
- **Pending Actions Bar**: Sticky bar for unresolved prompts — tool confirmations (allow/deny/judge), ask-user multi-question forms, step limit, resume after failure.
- **Execution Panels**: Collapsible plan view with DAG graph (SVG, lane allocation) + item list, click-to-scroll to chat.
- **Git Panel**: CODE-mode panel with three tabs — Changes (staging/unstaging, per-file + per-hunk), Branch (checkout/create/delete, stash, merge/rebase), and a unified History tab. The History tab merges the former separate History + Graph views into one: an SVG lane gutter (`GitGraphGutter.tsx`) alongside virtualized commit rows (`GitHistoryRow.tsx`, `@tanstack/react-virtual`). Data comes from a single `getGitHistory()` call (replacing the former `GetCommitLog`/`GetGitGraph` pair); each `GitHistoryCommit` carries both log fields (author/email/date/message) and graph topology (parents/refs). Pure render logic (lane geometry, node offsets, edge routing constants) lives in `gitGraphRender.ts` — no React/DOM deps, fully unit-testable; the lane layout algorithm (`computeGraphLayout`/`computeRowYLayout`) lives in `lib/gitGraphLayout.ts`. Commits expand inline to show changed files (fetched lazily via `GetCommitFiles`); a shared glob/regex `FilterBar` narrows the list to commits that touched matching files (the lane graph is hidden while filtering). Right-click a commit for tag management and reset-to-this-commit (`GitHistoryContextMenu.tsx`).
- **File Viewer**: Tab bar + syntax-highlighted content + unified diff overlay (character-level) + markdown preview toggle. Auto-refreshes on `workspace:tree_changed`. Binary detection (null bytes in first 8KB). State persisted to localStorage.

### Design system

One Dark is the default theme; a One Light override activates under `<html data-theme="light">` (toggled via `themeStore`). All colors as Tailwind v4 `@theme` custom properties (background `#282c34`, foreground `#abb2bf`, primary `#abb2bf` with primary-rgb `82,139,255` for rgba), destructive `#e06c75`, success `#98c379`, warning `#d19a66`, info `#61afef`, highlight `#e5c07b`). Base font 14px, dark color-scheme. Focus outlines globally suppressed. Custom scrollbar class (`.custom-scrollbar`, 8px, semi-transparent thumb).

### Event handling pattern

Session event handler subscribes to all session-scoped events on session change. Each handler: validates data with type guard → updates activity status → adds/updates chat store message → updates plan store. Streaming: `assistant_chunk` sets/appends text, `assistant_done` flushes to permanent message.

### Key Principles

- Design tokens are law. Every color, spacing, radius references CSS custom property. No raw hex in component code.
- No !important. If you need it, abstraction is wrong. Fix component API, not CSS specificity.
- Single source of truth. Each piece of data lives in exactly one store. No dual bookkeeping, no parallel state trees.
- Normalized store, incremental updates. Don't rebuild trees from flat arrays on every render. Index by ID, update in place.
- Stable Zustand selectors. Every selector passed to `useStore(selector)` must return a referentially stable value — either a primitive, a direct store property, or use a custom hook with granular selectors + `useMemo`. Never allocate arrays/objects inside a selector.
- Type safety at boundaries. Validate and type event data at ingestion point. Everything downstream typed — no Record<string, unknown>.
- Small, focused components. Target no file over 200 lines; extract sub-hooks/components when a file grows (e.g. `ChatInput.tsx` keeps its surface small via sub-hooks). No component handling more than one domain concept. Extract hooks for data loading.
- One import path for backend calls. All RPC through @/api/\*. No direct imports from wailsjs/go/desktop/App.
- Declarative persistence. Use Zustand middleware, not manual localStorage calls.
- No module-level side effects. Store files define stores. Initialization happens in React lifecycle hooks after runtime readiness confirmed.
- Event handlers are testable. Each event type has focused handler function testable in isolation without React rendering.

## Pre-PR checklist

`make build` → `make lint` → `make test`. All three must be clean. CI (`.github/workflows/ci.yml`) runs the same build/lint/test matrix (Linux + macOS) on push and PR to `main`; local verification is the gate before pushing.
