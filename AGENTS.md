# AGENTS.md

Guidance for coding agents working on **c0wrk** — a desktop AI coding-agent built with Wails v2 (Go backend + React 19 / Vite 6 / TS frontend).

## Security Policy

This project maintains a security policy in [SECURITY.md](./SECURITY.md).
All AI coding agents MUST read and follow SECURITY.md before making changes.
It contains:

- Threat model and trust boundaries
- Secure coding guidelines specific to this project's stack
- Hard constraints and forbidden patterns for AI agents
- Vulnerability reporting procedures
- Agentic security controls (OWASP Top 10 for Agentic Applications ASI01–ASI10)

Any code contribution that violates the rules in SECURITY.md will be rejected.

## Specifications

Detailed system specs live in `specs/`. Before making structural changes, read the relevant spec:

- Start with `specs/INDEX.md` to find the right document for your task.
- `specs/META.md` defines spec formats and update rules — read before creating/updating specs.
- `specs/contracts/` define cross-boundary interface rules.
- `specs/domains/` explain subsystem behavior and invariants.
- `specs/decisions/` explain why things are designed the way they are.

## Project shape

- Go module: `github.com/v0lka/c0wrk` (root: `core/`, `backend/`, `desktop/`, `frontend/`). Binary/app name is `c0wrk-desktop` (see `wails.json`).
- Entry point: `main.go` → `desktop.NewApp()` → Wails runs with `OnStartup = app.Startup` (`desktop/startup.go`). Build metadata is injected into `core/version` by Makefile/release `-ldflags` (`Version`, `GitCommit`, `BuildDate`).
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
- `make lint` — `make fmt-check` + `golangci-lint run` (root) + `cd frontend && npm run lint` (config at `.golangci.yml`, v2 schema)
- `make fmt-check` — fails when `gofmt -l` reports any Go file under the root package, `internal/`, `core/`, `backend/`, or `desktop/`
- `make build` — installs frontend deps, runs `wails build` with version ldflags, then `make fetch-onnx` + `make fetch-embedding-model`
- `make dev-desktop` — Vite dev server only (`cd frontend && npm run dev`); for full hot-reload use `wails dev` from repo root
- `make fetch-onnx` — downloads ONNX Runtime 1.28.1 into `.cache/` and copies it into the platform build output. **Required after every direct `wails build`** or the app won't launch.
- `make bump` — resolves the latest `github.com/v0lka/sp4rk` remote commit and updates the module with `GOWORK=off`; use only at the release point of a cross-repo development cycle
- `make clean` — removes `build/bin`, `.cache`, `frontend/dist`

Frontend-only: `cd frontend && npm run lint | build | dev | test`. Frontend tests use **vitest** (`npm test` / `npm run test:watch`); test files live alongside source (`*.test.ts`).

### Focused Go workflows

- Single package (root module): `go test ./core/...`
- Single test (root module): `go test ./core -run TestOrchestrator_PlanExecuteMode -v`
- Tests use in-package style (`package agent`, not `agent_test`); many packages have a `testhelpers_test.go`.

## Config & runtime

- Runtime config lives at `~/.c0wrk/config.yaml` (default dir constant `config.DefaultAgentDir = ".c0wrk"`). `config.example.yaml` is the authoritative reference for every tunable (LLM providers, executor loop caps, compaction thresholds, tool limits, timeouts, security policies, Small-LLM profile, vector index, experimental features, updates).
- **Experimental gate** (`experimental.enabled`, default false) is all-or-nothing. It gates RESEARCH and the entire Small-LLM profile; disabling it leaves stored per-feature config/artifacts intact but makes both features ineffective and hides their UI. `backend/frontend_api_research.go` owns RESEARCH activation, workspace containment, non-destructive skill seeding, watcher setup, and disable semantics.
- **Small-LLM profile** (`small_llm.*`): an optional, manual-only set of optimizations for running on small/local models — tool-set narrowing (`essential_tools`), system-prompt Lite swap (`system_prompt`), sampling override (`sampling`), loop-hardening (`loop_hardening`), and context-management overrides (`context`). Each variant is gated by BOTH `experimental.enabled`, the profile's master `enabled`, and its own sub-toggle. Editable at runtime via `GetSmallLLMConfig`/`UpdateSmallLLMConfig`. See `specs/domains/small-llm.md` and `specs/decisions/022-small-llm-profile.md`.
- **Verify-on-edit** (`executor.verify_on_edit`, default off) runs a config-authored command after each successful `write_file`/`edit_file` in CODE tasks and injects bounded output as a system observation. It is disabled for CHAT and goal turns. The command skips interactive confirmation because it is model-inaccessible config, but `ExecuteUnattended` still enforces required fields, disabled tools, execute-group `deny`, the shell blacklist, symlink/hard safety checks, and the `timeouts.bashMaxTimeout` ceiling. Never expose `ExecuteUnattended` to a model-facing path. See `specs/domains/verify-on-edit.md`.
- `timeouts.llmRequestTimeout` governs main agent calls; `timeouts.serviceLLMRequestTimeout` separately bounds one-shot title, commit-message, and prompt-optimization calls. Keep service calls on the service timeout path.
- Env vars in config are expanded as `${VAR}`. On macOS, `startup.go` calls `config.LoadShellEnvironment()` **before** any other init because Finder-launched apps don't inherit shell env — preserve this ordering if you touch `Startup`.
- SQLite DB (via `modernc.org/sqlite`, a CGO-free pure-Go driver) defaults to `~/.c0wrk/database.db`. Wail-level file lock: a single `*sql.DB` is shared across `session`, `project` stores. The SQLite layer is pure Go; the desktop app is not globally CGO-free because native Wails/ONNX and platform integrations use native toolchains.
- **External runtime deps**: There are no startup-hard dependencies. `git` is checked lazily on first CODE-mode project switch via `exec.LookPath` in `backend/frontend_api_project.go` — if missing, a `runtime_error` toast is shown and the switch is rejected. CHAT mode (No Project) never requires git. All other tools — `rg` (ripgrep), `uv`, `markitdown` — are managed by the tool-manager (`core/toolmanager/`) and auto-downloaded on first run to `~/.c0wrk/tools/`. See `specs/decisions/010-tool-manager.md`.
- Vector index needs ONNX Runtime plus a quantized embedding model + tokenizer (both fetched by `make build`). The embedder loads asynchronously after `EventBackendReady`. Indexing respects `.gitignore`/`.aiignore`, `vector_index.max_file_size`, `max_chunk_size`, and `max_chunks_per_file`; files that exceed safety limits are skipped rather than partially embedded. Search supports hybrid/vector/lexical modes, and the file viewer's **Find similar** action searches selected text.
- PTYs are keyed by session in `core/terminal.Manager`; switching sessions preserves each live terminal. DeleteSession stops that session's PTY, while app shutdown calls `StopAll`. Window geometry is persisted by `desktop/window_state.go`; frontend panel/viewer state uses Zustand persistence.
- In-app updates are controlled by `updates.enabled`, `updates.auto_check`, and `updates.check_interval`. Release archives are checked fail-closed against `SHA256SUMS`, staged via the two-process `--self-update` flow, and installed with a `.old` rollback tree. Artifacts remain unsigned: SHA256 verifies bytes, not release authorship. See ADR-023 and `SECURITY.md`.

## Conventions & gotchas

- **Logging**: `log/slog` everywhere. Pass `*slog.Logger` through constructors; don't use global `slog` in new code except at the top-level boundary.
- **Errors**: `errorlint` + `perfsprint` are on. Use `%w` for wrapping, `errors.Is/As`, never `fmt.Errorf` where `errors.New` suffices, never `fmt.Sprintf("%s", s)`.
- **Linters enabled** (see `.golangci.yml`): `errcheck` (incl. type assertions), `govet`, `staticcheck`, `ineffassign`, `unused`, `errorlint`, `nilerr`, `gocritic` (diagnostic+performance+style, except `hugeParam`/`rangeValCopy`), `revive` with `exported` & `var-naming` disabled, `prealloc`, `bodyclose`, `noctx`, `sqlclosecheck`, `perfsprint`, `unconvert`, `wastedassign`, `copyloopvar`, `durationcheck`, `whitespace`, `depguard`. Run `make lint` before declaring done.
- **Tool registry pattern**: reusable built-in tools live in `github.com/v0lka/sp4rk/tools/builtins/`; c0wrk-specific tools (e.g. `ask_user`) live in `core/tools/`. MCP-backed tools are added at runtime via `github.com/v0lka/sp4rk/tools/mcp/gateway.go`. To add a new built-in tool, implement `tools.Tool` in the correct package and wire it through `core/tools.RegisterBuiltinTools` (defined in `core/tools/builtin_registration.go`, called from `core/builder.go`). **Group contract (ADR-024)**: every new tool MUST declare its capability group — set `ToolGroup` on `BaseTool` (sp4rk groups: `execute`, `local_read`, `local_write`, `remote_read`, `remote_write`, `local_mcp`, `remote_mcp`; c0wrk orchestration/infra tools use the reserved `sdktools.GroupSystem`, which bypasses policy and requires security review). The group alone determines the tool's security policy (`security.groups`), its availability in subagent tool budgets (`AGENT.md` `tools:`, `delegate` `tools` — group tokens, not tool names), and the verifier toolsets. An undeclared group matches no allow-list (fail-closed). Never key policy or tool budgets off tool names.
- **Subagent Profiles**: a specialized subagent persona/budget is a markdown file at `<workspace>/.agents/agents/<name>/AGENT.md` (the `AgentsRelativePath = ".agents/agents"` constant in `core/pathsegments.go`, paralleling `.agents/skills/` for skills). YAML frontmatter declares `name` (must match dir), `description`, and optional `tools` (`all` default | `read-only` | comma-list of tool-group tokens: `execute, local-read, local-write, remote-read, remote-write, local-mcp, remote-mcp, system` — the `system` group is always included on top; unknown tokens fail closed, ADR-024), `max-steps`, `model`, `allow-redelegate`, `hidden`, `color`; the body is the agent's core directive (it replaces the orchestrator system prompt at delegation time via `buildSpecializedSystemPrompt`). Parsed/managed by the self-contained `github.com/v0lka/sp4rk/agents` package (`AgentManager`, `ParseAgent`); applied in `core/conductor.go` `buildSubAgentTask`. Two targeting modes: (1) **explicit `#agent-name` mention** — the user types `#code-reviewer`, the frontend extracts it (`lib/parseReferences.ts` `extractAgentRefs`), `sendMessage` threads it as `activeAgents` (arg 4 → Go `SendMessage` arg 4 → `HandleOptions.UserAgents`), `PreprocessMessageText` strips the ref, and `enrichAgentContext` attaches it as a `## Requested Subagents` directive the Conductor MUST delegate to; (2) **implicit/discovery** — a non-empty catalog renders `## Available Subagents` so the Conductor may delegate via `delegate(agent: "name")` at its discretion. `#` is used (not `@`) because `@` is the file-ref trigger and `@file#L20` line anchors must not collide; `#review`, `/review`, `@review` are three distinct refs. Plan steps target an agent via `declare_plan`'s per-step `agent` field (`PlanStep.Agent` survives JSON restore + the blackboard `copyPlan` fix). See [ADR-021](specs/decisions/021-subagents.md).
- **Prompts are data**: markdown files under `core/prompts/` are embedded via `prompts.go` in the same dir. Tests verify every `.md` file is referenced — update both when adding/removing a prompt.
- **Generated Wails bindings** at `frontend/wailsjs/go/desktop/App.{js,d.ts}` are regenerated by `wails build` / `wails dev` from the methods on `desktop.App`. Don't hand-edit them; if they drift, rebuild.
- **Desktop API surface**: `*desktop.App` embeds `*backend.FrontendAPI`; promoted methods are visible to the Wails binding generator. Frontend-callable methods are split across `backend/frontend_api_*.go` files by area (`agents`, `attachment`, `config`, `git`, `goal`, `mcp`, `project`, `prompt`, `research`, `review`, `session`, `skills`, `terminal`, `updater`, `vector`, `workdirs`, `workspace`). New frontend-callable methods go in the matching `backend/frontend_api_*.go` and frontend calls go through `frontend/src/api/*`, never directly to generated bindings.
- **Security/tool policies** are enforced in `core/builder.go` → `applySecurityPolicies` from `config.Security.Groups` (`security.groups.<group>.{policy, blacklist?}` — per-capability-group policies, ADR-024; short enum `allow`/`user_confirm`/`deny`; mutating groups default to `user_confirm`; blacklist patterns must compile and are execute-only). Pending confirmations flow through `App.pendingConfirmations` sync.Map back to the UI.
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

- Don't add `go.work` **into this repository** — this is a single Go module that depends on `github.com/v0lka/sp4rk` as a normal external dependency (no `replace` directive). `go.work` is a local-development tool that is not published; keep it out of the repo. See ADR-015. Exception: during cross-repo development cycles, c0wrk builds via a gitignored `go.work` at the repository root (`use . ../sp4rk`) while `go.mod` still pins the last published sp4rk commit — c0wrk then consumes unpublished sp4rk APIs and `GOWORK=off` builds fail until the release step (commit+push sp4rk → `go get sp4rk@main` → verify `GOWORK=off` build). This mid-cycle state is sanctioned by the owner; see ADR-031.
- Don't add `vendor/` or change the module path.
- Don't add a new frontend test framework; vitest is already configured. Add tests as `*.test.ts` alongside the source file.
- Don't `go install` the ONNX runtime differently per-machine — always go through `make fetch-onnx` so `.cache/` stays consistent.
- Don't commit `coverage*.out`, `*_cov.out`, `config.local.yaml`, `.cache/`, `build/bin/`, or anything matched in `.gitignore`.
- Don't create new arrays or objects inside Zustand selectors (e.g. `useStore(s => s.items.map(…))` or `useStore(s => condition ? derive(s) : [])`). React 19's `useSyncExternalStore` compares snapshots by reference — a new object/array on every call causes an infinite re-render loop (React error #185). Return direct store references from selectors and derive values with `useMemo` in a custom hook.

## Frontend architecture

### Stack

React 19 + TypeScript ~5.7 + Vite 6 + Tailwind CSS v4 + Zustand 5. UI primitives from shadcn/ui (new-york style) + Radix UI. Icons via lucide-react. Markdown rendered with react-markdown 10 + remark-gfm/emoji/breaks + rehype-highlight/sanitize/external-links/slug/autolink-headings. Syntax highlighting via highlight.js 11 (selective language registration). Mermaid 11 lazy-loaded for diagrams. In-app code/markdown editing via CodeMirror 6 (`@codemirror/*` + `@lezer/highlight`). Embedded terminal via xterm.js (`@xterm/xterm` v6 + `@xterm/addon-fit`). Virtualized lists via `@tanstack/react-virtual`. Character-level diffs via `diff` v9. File tree icons via Nerd Fonts (`@m234/nerd-fonts`, SauceCodePro NF).

### Layout

Three-column panel layout (no router): Sidebar (persisted width, clamped 180-500px, collapsible to 40px) | Main Chat Area | File Viewer. The viewer is unpinned/floating by default for fresh installs, can be pinned into the docked column, persists width/collapse/pin state, and auto-collapses when an unpinned viewer has no tabs. Window geometry is persisted by the desktop backend. Resize handles support drag + keyboard.

### Communication with Go backend

1. **RPC**: wrappers in `frontend/src/api/*` call `window.go.desktop.App.*` and validate boundary data.
2. **Events**: wrappers around `window.runtime.EventsOn/EventsEmit` carry real-time execution and desktop state.
   - **Session-scoped** events: `session:${sessionId}:${eventType}` for task lifecycle, pause/resume, HITL, attachments, metrics, and terminal output.
   - **Global** events include startup/runtime readiness and errors, projects/sessions/workspace/git/vector/tool-manager/workdirs state, `files:dropped`, `research:*`, and `update:*`. Treat `specs/contracts/event-catalog.md` as the authoritative catalog instead of maintaining a numeric count here.

### State management (Zustand stores)

| Store                | Responsibility                                                                                 |
| -------------------- | ---------------------------------------------------------------------------------------------- |
| `chatStore`          | Messages per session, streaming text, thinking/activity/task flags, context fill, token counts |
| `planStore`          | Execution plan groups (DAG items), session stats (routing, attempts)                           |
| `sessionStore`       | Session list (sorted by last_active_at), active session ID                                     |
| `projectStore`       | Project list (sorted by last_active_at), active project ID                                     |
| `fileTreeStore`      | Lazy-loaded directory tree, expanded dirs, search entries, git status                          |
| `fileViewerStore`    | Open files/content/diff/language, tabs, panel width, collapsed/pinned state                     |
| `inputModeStore`     | Chat/terminal input mode, panel height, expanded state (persisted)                             |
| `gitPanelStore`     | Git panel: branch info (ahead/behind), merge/rebase state (persisted)                          |
| `blackboardStore`    | Blackboard facts and metadata for current session                                              |
| `settingsStore`      | Settings modal open/close, active tab                                                          |
| `uiStore`            | Sidebar collapsed state, log level                                                             |
| `themeStore`         | App theme (`dark` \| `light`), persisted; writes `data-theme` to `<html>`                       |
| `soundStore`         | Persisted master toggle for synthesized foreground/background notification sounds               |
| `vectorIndexStore`   | Vector index status/progress                                                                   |
| `goalStore`          | Goal lifecycle: pending proposal (condition/verify/clarification), status verdict, progress    |
| `reviewStore`        | Review / human-in-the-loop prompts (plan review, review_prompt items)                         |
| `attachmentsStore`   | Message attachments (per-session file list, per-file failure tracking)                         |
| `workDirsStore`      | Additional working directories (multi-repo workspace roots)                                    |
| `experimentalStore`  | Effective Experimental Features gate exposed by runtime config                                  |
| `researchStore`      | Experimental research status, hypothesis graph, metrics, active front, and report state          |
| `updateStore`        | Running version, update availability/download progress, skipped/error state                     |
| `terminalRegistryStore` | App-lifetime per-session terminal instances; session switches do not destroy PTYs             |

Cross-component scroll coordination uses a React context (`ScrollContext.tsx`), not a Zustand store.

### Data model

- Backend persists `ChatMessage` (id, session_id, role, content, reasoning_content, tool_calls, metadata JSON, created_at).
- Frontend converts to `ChatMessageUI` (semantic string ID, sessionId, MessageType, content, metadata, timestamp).
- `groupMessages()` transforms flat `ChatMessageUI[]` into a `DisplayItem[]` tree (21 kinds: user, assistant, thought, thought_group, tool, tool_confirm, ask_user, step_limit, resume_action, error, service, plan_step, subagent, reflection, step_finish, context_compaction, memory_read, plan_review, review_prompt, goal_proposal, checklist).
- Grouping handles: plan step nesting, tool call/result correlation (via tool_call_id or composite key), thought collapsing, pending action extraction, special tool handling (subagent skipped, finish/memory compact).

### Key components

- **Sidebar**: Project selector + session selector (dropdowns with context menus, inline rename, search for 5+ sessions) + file tree workspace panel.
- **Chat Area**: Pinned last user message (sticky, collapsible) + scrollable message list + smart auto-scroll (50px threshold, "New activity" pill) + activity indicator.
- **Chat Input**: Auto-resize textarea, Enter sends / Shift+Enter newline, auto-creates a session if needed, and switches the main action between pause/resume/send according to runtime state. Picker, clipboard paste, and native `files:dropped` all stage through the same attachment pipeline with vision gating.
- **Pending Actions Bar**: Sticky bar for unresolved prompts — tool confirmations (allow/deny/judge), ask-user multi-question forms, step limit, resume after failure.
- **Execution Panels**: Collapsible plan view with DAG graph (SVG, lane allocation) + item list, click-to-scroll to chat.
- **Git Panel**: CODE-mode panel with three tabs — Changes (staging/unstaging, per-file + per-hunk), Branch (checkout/create/delete, stash, merge/rebase), and a unified History tab. The History tab merges the former separate History + Graph views into one: an SVG lane gutter (`GitGraphGutter.tsx`) alongside virtualized commit rows (`GitHistoryRow.tsx`, `@tanstack/react-virtual`). Data comes from a single `getGitHistory()` call (replacing the former `GetCommitLog`/`GetGitGraph` pair); each `GitHistoryCommit` carries both log fields (author/email/date/message) and graph topology (parents/refs). Pure render logic (lane geometry, node offsets, edge routing constants) lives in `gitGraphRender.ts` — no React/DOM deps, fully unit-testable; the lane layout algorithm (`computeGraphLayout`/`computeRowYLayout`) lives in `lib/gitGraphLayout.ts`. Commits expand inline to show changed files (fetched lazily via `GetCommitFiles`); a shared glob/regex `FilterBar` narrows the list to commits that touched matching files (the lane graph is hidden while filtering). Right-click a commit for tag management and reset-to-this-commit (`GitHistoryContextMenu.tsx`).
- **File Viewer**: Floating-by-default or pinned panel with persistent tabs/width/state, syntax-highlighted content, unified diff overlay, markdown preview, strict sanitized Mermaid rendering with pan/zoom, and a context-menu **Find similar** action for selected text. Auto-refreshes on `workspace:tree_changed`; binary detection checks null bytes in the first 8KB.
- **Terminal**: `TerminalPanel` binds to an app-lifetime registry keyed by session ID; switching sessions preserves each PTY and its frontend instance. Explicit stop, session deletion, or app shutdown ends it.
- **Research Panel**: Experimental workspace-contained hypothesis graph/metrics view. It is visible only while `experimental.enabled` is effective and refreshes from lightweight `research:*` events.
- **Update UI**: `UpdateToast` and settings consume `update:*` events and `updateStore`; checks are best-effort and never block startup, while download/apply errors remain explicit.
- **Notifications**: `useSoundEvents` handles active-session lifecycle sounds; `useBackgroundSessionWatcher` covers completion/attention events from other sessions without double-playing the foreground event.

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

`make build` → `make lint` → `make test`. All three must be clean. CI (`.github/workflows/ci.yml`) runs the corresponding build/lint/test matrix on Linux, macOS, and Windows for pushes and PRs to `main`; local verification is the gate before pushing.
