# Daemon Migration Roadmap — Epic #84 (c0wrk v2: daemon architecture)

| | |
| --- | --- |
| Epic | [#84 — c0wrk v2 daemon architecture: TUI and headless/ACP clients](https://github.com/v0lka/c0wrk/issues/84) |
| Status | Approved roadmap — implementation not started |
| Grounded in | `main` @ `9bf36ea1` (layering pre-work P-01…P-14 + A-1…A-12 landed in `9e1333b6`, merged via PR #224) |
| Verified on | 2026-11-09 |
| Companion decisions | [ADR-119](../../specs/decisions/119-desktop-layer-scope-rule.md) (desktop layer scope rule, W3 sunset), ADR-120 (daemon topology — authored by Phase 0 of this roadmap) |

---

## 1. Purpose and scope

Epic #84 turns the c0wrk engine into a **daemon** that owns the engine, the SQLite DB, PTYs and HITL state, and makes the GUI, a future TUI, and a future ACP server equal **clients** of one gRPC transport. This roadmap decomposes the epic into **nine phases — one per sub-issue, one PR per phase** — each leaving the repository fully functional: `make build && make lint && make test && make vulncheck` green, **no behavior change**, no deferred work, no degraded functionality.

**In scope:** the preparatory architecture only — ADR, assembler, interaction service, event sourcing, gRPC contract, `c0wrk serve` daemon, GUI migration onto the daemon, headless permission-policy ladder, contract versioning.

**Out of scope (per the epic's non-goals):** the TUI itself, the ACP server itself, a sandbox execution provider. Phase 6 documents the ACP/TUI mappings but does not implement them.

**Pre-work is already landed.** The #42 desktop-layering audit migration (P-01…P-14) executed *before* this epic: startup phases live in `backend/startup_phases.go`, the judge core in `backend/judge.go` / `backend/step_limit_judge.go`, the event batcher in `backend/event_batcher.go` (P-09), the single-instance lock primitive in `core/instancelock/` (P-07), HITL wire parsing in `core/hitlwire/` (P-08), the web-access service core in `backend/frontend_api_webaccess.go` (P-11), the vector-index launcher in `backend/vector_launcher.go` (P-12/P-14). Nothing in this roadmap moves that code again — phases wrap, absorb or build on it. Note that the issue texts were written **against the pre-migration tree** and cite pre-migration anchors (e.g. "desktop/startup_phases.go, 1 713 lines"); §2 gives the corrected, current anchors.

---

## 2. Repository grounding — post-pre-work state (verified 2026-11-09)

All anchors below were verified against `main` @ `9bf36ea1`. Use these, not the anchors inside the GitHub issue bodies.

### 2.1 Composition root

| Concern | Current home | Anchor |
| --- | --- | --- |
| Startup orchestration (Wails lifecycle) | `desktop/startup.go` (702 lines) | `Startup` :148, `Shutdown` :482, `DomReady` :141, `reloadFrontend` :125, `wireWailsEventListeners` :646, `drainPendingGoalSuggestions` :630 |
| Host-side assembly glue | `desktop/startup_phases.go` (848 lines, down from 1 713) | `initLogger` :34, `initConfigAndDeps` :85, `buildUIEmitFunc` :168, `buildAskUserCallback` :201, `buildGoalProposalCallback` :253, `buildGoalSuggestionCallback` :277, `buildPlanApprovalCallback` :397, `buildConfirmCallback` :453, `buildStepLimitCallback` :526, `buildApplication` :620, `buildFrontendAPI` :639, `emitBackendReady` :694, `startMCPReadyNotifier` :748, `stopEmbeddedLLM` :793, `initWebAccess` :818, `stopWebAccess` :844 |
| Transport-neutral startup phases | `backend/startup_phases.go` | `SafeGo` :33, `InitTools` :111, `InitDatabase` :275, `InitTerminalManager` :311, `InitStores` :344, `PreloadProjectsAndSessions` :392, `InitEmbeddedLLM` :414, `InitChatGPTAuth` :451, `InitKimiAuth` :470, `StartUpdateCheckerBackground` :495, `StartAutoFetchBackground` :509 |
| Vector-index launcher | `backend/vector_launcher.go` (+ `vector_launcher_test.go`) | P-12/P-14 landing |
| `Application` + `ApplicationConfig` | `backend/application.go` (699 lines) | `ApplicationConfig` :30 — fields `UIEmitFunc` :40, `AskUserFunc` :41, `PlanApprovalFunc` :42, `ConfirmFunc` :43, `VectorSearchFunc` :48, `FileChangeNotifyFunc` :65 |
| `FrontendAPI` + config | `backend/frontend_api.go` (1 119 lines) | `FrontendAPIConfig` :445 — `EmitEvent func(string, ...any)` :459, `QuitApp`, `AppCtx`, `OnExperimentalChanged` |
| Entry point | `main.go` (294 lines) | `mainImpl` :61, `--self-update` branch :66–82, single-instance relay :94–142, `wails.Run` :222 |
| CLI grammar | `core/cli/cli.go`, `specs/domains/cli.md` | `ActionRun`/`ActionHelp`/`ActionVersion`/`ActionSelfUpdate` (fail-closed parser; extend with `ActionServe` in Phase 3) |

### 2.2 HITL (six funnels + judge)

| Concern | Current home | Anchor |
| --- | --- | --- |
| Six pending registries (`sync.Map` on `desktop.App`) | `desktop/app.go` | `pendingConfirmations`/`pendingAskUser`/`pendingStepLimit`/`pendingPlanApprovals`/`pendingGoalProposals`/`pendingGoalSuggestions` :78–83; `judgeWG` :89; `judgeService backend.JudgeService` :100; `wailsEmit` test seam :106 |
| Wails-side HITL handlers | `desktop/event_handlers.go` (452 lines) | `emitHitlResolved` :17, `handleToolConfirmResponse` :35, `handleToolJudgeRequest` :100, `handleAskUserResponse` :159, `handlePlanApprovalResponse` :209, `resolveGoalProposal` :267, `handleGoalProposalResponse` :316, `resolveGoalSuggestion` :342, `handleGoalSuggestionResponse` :379, `handleStepLimitResponse` :396 |
| Pending-actions snapshot (Wails binding) | `desktop/pending_actions.go` (158 lines) | `PendingActionsResponse` :9 + payload structs, `GetPendingActions` :62 |
| Judge core (transport-neutral, P-08/P-10) | `backend/judge.go`, `backend/step_limit_judge.go` | `JudgeService` :37 (`Begin`/`Finish`/`Run`), `RunJudgeEvaluation` :62; `ResolveSilentStepLimit` :39 |
| HITL wire parsing (P-08) | `core/hitlwire/wire.go` | `ExtractPayload` :22, `ConfirmDecision` :46, `StepLimitResponse` :105, `PlanApprovalDecision` :130, `GoalProposalDecision` :150, `AskUserAnswers` :169, `StringField` :216 |
| Restart recovery input | persisted pending tool-confirm rows | `resolved` flag on pending tool-confirm messages (see Phase 1b task 5) |

### 2.3 Events

| Concern | Current home | Anchor |
| --- | --- | --- |
| Session event types + payloads | `backend/session/events.go` (411 lines) | 4 exported consts (`step_limit`, `step_limit_response`, `task_started`, `autonomy_decision`) :15–29; most event names are emit-site literals — **the authoritative catalog is `specs/contracts/event-catalog.md`**; `HitlResolvedKind` :247–253 |
| Event catalog | `specs/contracts/event-catalog.md` | Global Events :9, Session-Scoped :56 (Orchestration Lifecycle :60 … Blackboard & Judge :201), Frontend-to-Backend :210, Event Batching :222, Web Transport :283, Breaking Change Checklist :360 |
| Global-event seam (partial #76 groundwork) | `backend/frontend_api.go` | `FrontendAPIConfig.EmitEvent` :459 — backend sources already emit: `config:updated`, `project:*`, `session:renamed`, `git:status_changed`, `workdirs:changed`, `skills:changed`, `update:*`, `embedded_llm:*`, `research:*`, `papers:changed`, `backend:ready` |
| Event batcher (P-09) | `backend/event_batcher.go` (290 lines) + `desktop/event_batcher.go` (30-line wrapper) | reusable coalescer for the daemon event stream |
| Still desktop-originated | `desktop/startup.go`, `desktop/startup_phases.go` | `startup_error`, `projects:loaded`, `sessions:loaded`, `tool_manager:start\|done`, `mcp:ready`, startup-path `vector_index:status`, `files:dropped`, `app:exit_requested` |

### 2.4 Platform / infra primitives

| Concern | Current home | Anchor |
| --- | --- | --- |
| Single-instance lock primitive (P-07) | `core/instancelock/` | `instancelock.go`, `lock_unix.go`, `lock_windows.go`; GUI relay in `desktop/singleinstance.go` (49 lines); semantics: [ADR-075](../../specs/decisions/075-single-instance-lock.md) |
| Path construction (the only place that knows the layout) | `backend/config/paths.go` | `AgentDir` :293, `ConfigPath` :302, `DatabasePath` :317, `LogsDir` :336 — daemon socket path helper lands here (Phase 3) |
| Terminal (PTY) manager | `core/terminal/` | `Manager` keyed by session; `StopAll` on shutdown |
| Self-update | `core/updater/` + `main.go` `--self-update` re-exec | two-process flow, SHA256SUMS fail-closed, `.old` rollback |
| Web-access sidecar service core (P-11) | `backend/frontend_api_webaccess.go` | Wails adapters: `desktop/webaccess_extra.go`, `desktop/webaccess_inbound.go` |
| Vector-index master switch | `vector_index.enabled` (pointer bool, default on, applies live) | [ADR-103](../../specs/decisions/103-vector-index-master-switch.md) — daemon must skip ONNX load when off |
| GUI-only policy cores kept down-layer by ADR-119 refinement 1 | `backend/window_state.go`, `backend/webview_gpu*.go`, `core/csp/`, `core/webviewgpu/` | ownership stays with the GUI host; "moved to backend" ≠ "owned by the daemon" |

### 2.5 Specs & decisions

- **ADR numbering: the daemon ADR is ADR-120.** ADR-119 is taken by `specs/decisions/119-desktop-layer-scope-rule.md`; the current max is 119. Issue #73's "next free is 119" predates that landing — write `specs/decisions/120-<slug>.md`. Re-verify against `specs/decisions/` + `specs/INDEX.md` at PR time.
- [ADR-119](../../specs/decisions/119-desktop-layer-scope-rule.md) defines the W-criterion (W1 Wails lifecycle/API, W2 Wails binding, W3 HITL plumbing — **sunset when Phase 1b lands**, W4 native OS-shell) and the zero-Wails invariant (no `github.com/wailsapp/wails` import under `backend/` or `core/` — 0 today, keep it 0). Phase 1b must execute the sunset obligation: dated Historical Note on ADR-119 + drop W3 from `specs/architecture/layers.md` in the same change.
- `specs/architecture/layers.md` — Layer Diagram :7, Import Rules :58, Layer Responsibilities :75 (`desktop/` :119), Invariants :158, Anti-Patterns :168.
- `specs/INDEX.md` — Navigation by Task :7, Directory Listing :128 (`contracts/` :208, `decisions/` :216). `specs/META.md` ADR template :247.
- `SECURITY.md` — Threat Model :33, Trust Boundaries :94, Autonomy :238, ASI09 — Human-Agent Trust Exploitation :364.
- Related decisions: [ADR-024](../../specs/decisions/024-group-policies.md) (group policies — policy ladder rung C source), [ADR-053](../../specs/decisions/053-silent-mode.md) (autonomy modes), ADR-075 (single-instance), ADR-101 (web access), ADR-103 (vector master switch).
- `go.mod` carries **no** `google.golang.org/grpc` (only `google.golang.org/protobuf v1.36.6 // indirect` :122) — Phase 2 adds it.

---

## 3. Ground rules (binding for every phase)

1. **One phase = one issue = one PR.** The PR implements exactly its phase's tasks; no forward-implementing later phases, no leftovers. Any discovered out-of-scope work becomes a follow-up issue, not a rider.
2. **Per-phase gate:** `make build && make lint && make test && make vulncheck` all green before the PR is mergeable — for docs-only phases too (cheap, and it keeps the habit). A stale Go toolchain fails `vulncheck` even when lint/test are clean; keep the `go.mod` `go` directive and the CI `go-version` pins in lockstep.
3. **No behavior change, no degradation.** The desktop app behaves identically after every phase (the sole intended exception: Phase 4 changes *where* the GUI's calls execute — daemon — while every observable behavior stays the same; its PR carries a manual verification checklist). Desktop tests move with the code they cover.
4. **Layering:** new code lives in `backend/` (+ sub-packages) and `desktop/`; `core/` gains no desktop/transport notions ([layers.md](../../specs/architecture/layers.md)). The zero-Wails invariant holds: no `github.com/wailsapp/wails` import under `backend/` or `core/` (ADR-119).
5. **ADR-119 W-criterion is the standing review rule** for anything added to `desktop/`: name the W-category or move it down-layer. Do not regress the P-01…P-14 gains: `desktop/` = lifecycle + bindings + OS-shell + (until Phase 1b) HITL plumbing.
6. **Path logic:** all filesystem-path construction and containment goes through `backend/config/paths.go` and `sp4rk/pathutil` — never inline `strings.HasPrefix`/`filepath.Join` root concatenation (including the daemon socket path).
7. **Testing conventions:** new Go tests run clean (zero unexpected diagnostics), use in-package style, no sleeps to "give the scheduler a chance" (watchdogs/bars instead — see `specs/domains/testing.md` and `docs/development/test-timing-migration.md`); every "runs without Wails" acceptance test is an ordinary `go test` package test.
8. **Specs stay synchronized in the same PR** that changes the behavior they describe (`specs/META.md` update rules); each phase's tasks name their spec footprint.
9. **Prompts are data / comment policy / generated bindings:** unchanged rules apply (`CODING_STANDARDS.md`; `frontend/wailsjs` bindings are only regenerated by `wails build`/`wails dev` — Phase 4 must not hand-edit them).

### 3.1 Decisions pre-made by this roadmap (ratify or amend in ADR-120, Phase 0)

The issues leave a few choices open; to keep later phases unambiguous this roadmap fixes them. ADR-120 records each with rationale; if ADR-120 decides differently, amend the affected phase tasks in that PR.

| # | Decision | Affects |
| --- | --- | --- |
| D1 | **Daemon ADR number is 120** (119 taken by the desktop layer scope rule). | Phase 0 |
| D2 | **Second daemon process against the same agent dir exits with a clear error** (fail-fast); clients — never a second daemon — connect to the running instance. Rule recorded in ADR-120, tested in Phase 3. | Phases 0, 3 |
| D3 | **GUI shell auto-spawns the daemon** if no healthy instance answers on the socket, **connects** if one does; the shell that spawned it stops it on quit (spawn-ownership flag), a connecting shell leaves it running. Crash supervision: bounded restart with backoff + a surfaced UI error state. | Phases 0, 4 |
| D4 | **`files:dropped` and `app:exit_requested` are client-native events** (their true source is the OS window shell, per the ADR-120 API split); every other global event moves to its backend/appbuild source in Phase 1c. The catalog documents them as client-native instead of relocating them. | Phases 0, 1c |
| D5 | **Generated Go from `.proto` lives at `proto/gen/…`** in-module (package importable by `backend`, `desktop` and future TUI/ACP binaries alike — not `internal/`), with `.proto` sources at `proto/`; a `make proto` drift check (`make proto && git diff --exit-code proto/`) runs in CI. | Phase 2 |
| D6 | **Typed per-service RPCs** (not a JSON pass-through): the Phase 2 proto declares per-area services mirroring `backend/frontend_api_*.go`; the Phase 4 proxy layer is **generated** from the proto + a method manifest (generator in `internal/genproxy`, output committed, drift-checked), preserving Wails method signatures and error mapping. | Phases 2, 4 |
| D7 | **Minimal Phase-3 security posture:** the socket is owner-only (0600 / ACL'd pipe); an attached local-owner client is trusted (policy ladder's rung B); the full ladder (incl. fail-closed deny without a client) lands in Phase 5. ADR-120 records this staging. | Phases 0, 3, 5 |

---

## 4. Phase dependency graph

```
        ┌────────────────────────────────────────────────────────────┐
        │ Phase 0 — #73 — ADR-120 (topology, transport, HostServices,│
        │           API split, policy ladder, HITL→ACP mapping)      │
        └───────────────┬────────────────────────────┬───────────────┘
                        │                            │
                ┌───────▼───────┐                    │ (parallel)
                │ Phase 1a — #74│                    │
                │ assembler +   │                    │
                │ HostServices  │                    │
                └───┬───────┬───┘                    │
        ┌───────────▼┐   ┌──▼─────────┐   ┌──────────▼──────────┐
        │Phase 1b #75│   │Phase 1c #76│   │ Phase 2 — #77 —     │
        │interaction │   │global      │   │ .proto + codegen    │
        │service     │   │events      │   │ + daemon-rpc spec   │
        └─────┬──────┘   └─────┬──────┘   └──────────┬──────────┘
              └───────┬────────┴─────────────────────┘
                ┌─────▼──────────────┐        ┌─────────────────────┐
                │ Phase 3 — #79 —    │        │ Phase 6 — #83 —     │
                │ `c0wrk serve`      │        │ versioned event     │
                │ daemon             │        │ contract + ACP/TUI  │
                └───┬──────────┬─────┘        │ mapping (needs 0+2; │
          ┌─────────▼──┐   ┌───▼──────────┐   │ parallel to 3–5)    │
          │Phase 4 #80 │   │Phase 5 — #81 │   └─────────────────────┘
          │GUI on      │   │policy ladder │
          │daemon      │   │(fail-closed) │
          └────────────┘   └──────────────┘
```

Reading order below = dependency order: **0 → 1a → {1b, 1c, 2} → 3 → {4, 5}**, with **6** after {0, 2}, runnable in parallel with 3–5. Phases 1b/1c/2 are mutually independent once 1a lands; if executed strictly sequentially, use the numbering order (1b, then 1c, then 2).

---

## 5. Phase 0 — ADR-120: daemon topology, transport, policy ladder, client API split

**Issue:** [#73](https://github.com/v0lka/c0wrk/issues/73) · **PR:** docs-only (specs + SECURITY.md) · **Depends on:** nothing · **Unblocks:** all other phases.

### Context

`specs/META.md` makes architectural decisions ADRs, and `needs-design` work is not implementable in this repo. Every later phase consumes ADR-120's shapes: `HostServices` (1a/1c/3), the API split (2/4), the policy ladder (5), the HITL→ACP mapping (6). This phase writes no code.

### Task 0.1 — Author ADR-120

- **What:** One ADR at `specs/decisions/120-daemon-topology-grpc-transport-policy-ladder.md` (verify at PR time that 120 is still free — §2.5) using the `specs/META.md` ADR template (:247): Status / Context / Decision / Consequences / Alternatives Considered. It fixes, unambiguously:
  1. **Topology** — one daemon process per agent dir; GUI/TUI/ACP are clients; the daemon owns the engine, SQLite DB, PTYs, HITL state and single-instance.
  2. **Transport** — gRPC + protobuf over a Unix domain socket under `~/.c0wrk/` (macOS/Linux) and a named pipe (Windows); rationale: typed server-streaming for events, bidirectional streaming for the terminal.
  3. **Host seam** — the `HostServices` interface a host must provide: session-event emit, global-event emit, quit/shutdown, optional readiness/logging hooks (the shape Phase 1a implements).
  4. **API split** — which `backend/frontend_api_*.go` methods are *daemon* API vs *client-native* (dialogs, clipboard, window/geometry, power state, notifications, exit guard, CSP, file drop).
  5. **Headless permission-policy ladder** — A fail-closed deny (default) / B delegated to a trusted client / C allowlist from `security.groups` (ADR-024) / D pre-authorized session grant (scope + budget + TTL) / E sandbox (explicitly out of scope) / F async escalation (optional, default off). Rule: the ACP client is the trust boundary; **no attached client ⇒ deny**, never auto-approve. Includes the interaction with `security.autonomy_mode` (ADR-053) and the Phase-3→5 staging (decision D7).
  6. **HITL → ACP mapping** — `tool_confirm` and `ask_user` map to ACP permission requests; `step_limit` and `plan_approval` are daemon policy, not external prompts; goal proposals map to a permission-style sign-off.
  7. **Operational decisions D2–D7 from §3.1** (second-process rule, spawn-ownership, client-native events, codegen placement, typed RPCs, staging) — each with rationale and the alternative rejected.
- **How:** Follow the ADR template; keep the Decision section normative (MUST/SHOULD wording) because Phases 1–6 quote it. Cross-reference ADR-119 (W-criterion, zero-Wails invariant, W3 sunset), ADR-024, ADR-053, ADR-075, ADR-103, `specs/architecture/layers.md`, `specs/contracts/event-catalog.md`, `SECURITY.md`.
- **Where:** `specs/decisions/120-daemon-topology-grpc-transport-policy-ladder.md`.
- **Acceptance Criteria:** ADR exists with all five template sections; states topology, transport, `HostServices`, API split, ladder with fail-closed default, HITL→ACP mapping, and decisions D2–D7; every relative cross-reference resolves (paths relative to `specs/`).

### Task 0.2 — Extend `specs/architecture/layers.md`

- **What:** Add the Hosts/Clients layer and the daemon boundary to the Layer Diagram (:7) and Import Rules (:58): GUI (`desktop/`), future TUI and ACP server are clients of one daemon; clients import `backend/`/`core/` (and the generated RPC package) but never each other's internals; the daemon is a `backend`-layer process. Keep the W3 clause for now — its removal is Phase 1b's sunset change (ADR-119 refinement 2 forbids doing it early).
- **How:** Amend the diagram and the `desktop/` responsibility section (:119); add the client import rules as new Invariants (:158) entries.
- **Where:** `specs/architecture/layers.md`.
- **Acceptance Criteria:** The diagram shows GUI/TUI/ACP as clients of one daemon; client import rules are stated as invariants; W3 still present (with its sunset note pointing at Phase 1b / issue #75).

### Task 0.3 — Extend `SECURITY.md` with the headless trust model

- **What:** Extend **ASI09 — Human-Agent Trust Exploitation** (:364) and **Trust Boundaries** (:94) with the headless model: no interactive window; the client is the trust boundary; fail-closed deny with no attached client (never auto-approve); interaction with `security.autonomy_mode` (ADR-053) and the ladder from ADR-120; socket-permission posture (owner-only socket; local same-user access).
- **How:** Amend the two sections; reference ADR-120 and the ladder rungs; state explicitly what a confirmation does when no UI client is attached (deny by default).
- **Where:** `SECURITY.md` (§ Trust Boundaries :94, § ASI09 :364; § Autonomy :238 if the interaction needs a sentence).
- **Acceptance Criteria:** `SECURITY.md` documents confirmation behavior with no UI client (deny by default) and the ACP trust boundary; no contradiction with ADR-053's autonomy ladder.

### Task 0.4 — Note headless policy profiles on ADR-024

- **What:** Add a note to `specs/decisions/024-group-policies.md` that a future headless profile sources rung-C allowlists from `security.groups`, per ADR-120.
- **How:** Append a dated "Related" note (do not rewrite the ADR body — `specs/META.md` update rules).
- **Where:** `specs/decisions/024-group-policies.md`.
- **Acceptance Criteria:** Note present, dated, references ADR-120; ADR body unchanged.

### Task 0.5 — Index the ADR

- **What:** List ADR-120 in `specs/INDEX.md` (decisions listing :216) and add navigation entries (Navigation by Task :7) for daemon/headless work pointing at ADR-120 + (future) `specs/contracts/daemon-rpc.md` and `specs/domains/headless-runtime.md` as "planned, Phase 2/3".
- **How:** Follow the existing INDEX entry format.
- **Where:** `specs/INDEX.md`.
- **Acceptance Criteria:** INDEX lists ADR-120; `spec` tooling/links resolve.

### Phase 0 exit gate

Docs-only PR. `make build && make lint && make test && make vulncheck` green (unchanged code); all cross-references resolve; no code files touched.

---

## 6. Phase 1a — #74: transport-neutral assembler (`backend/appbuild` + `HostServices`)

**Issue:** [#74](https://github.com/v0lka/c0wrk/issues/74) · **PR:** code + tests · **Depends on:** Phase 0 (`HostServices` shape) · **Unblocks:** 1b, 1c, 2, 3.

### Context

`desktop/startup_phases.go` (848 lines) still owns host-side assembly: config resolution (`initConfigAndDeps` :85), the callback builders (:201–:576), `buildApplication` :620, `buildFrontendAPI` :639, background starters (`emitBackendReady` :694, `startMCPReadyNotifier` :748, `stopEmbeddedLLM` :793, `initWebAccess` :818, `stopWebAccess` :844). `desktop/startup.go` orchestrates them. The transport-neutral phases (P-12/P-14) already sit in `backend/startup_phases.go` and `backend/vector_launcher.go` — **this phase wraps them, it does not move them again.**

### Task 1a-1 — Define `HostServices`

- **What:** A transport-neutral interface (per ADR-120) that any host — Wails GUI today, headless daemon in Phase 3 — supplies: session-event emit `EmitSessionEvent(session.Event)`, global-event emit `EmitGlobal(name string, data ...any)`, `Quit()`, plus optional readiness/logging hooks (e.g. `OnBackendReady`, `SetLogger`) exactly as ADR-120 fixes them.
- **How:** Small interface in the new `backend/appbuild` package; no Wails types; document each method's threading/delivery expectations (the batcher contract from `backend/event_batcher.go` applies unchanged).
- **Where:** new `backend/appbuild/hostservices.go`.
- **Acceptance Criteria:** Interface compiles with zero Wails imports; every ADR-120-mandated member present; doc comments state delivery semantics; `desktop` remains compilable (it does not implement it yet — that is Task 1a-3).

### Task 1a-2 — Build the assembler behind one entry point

- **What:** Move assembly out of `desktop/startup_phases.go` into `backend/appbuild` as assembler methods behind a single entry point (e.g. `appbuild.Assembler.Build(ctx, HostServices, Options) (*Assembly, error)` — final name per ADR-120), producing the built `*backend.Application`, `*backend.FrontendAPI`, stores, terminal manager, and started background services. Assembly steps that move: config resolution (`initConfigAndDeps`), DB open, store construction, terminal manager, `buildApplication`, `buildFrontendAPI`, the background starters (`emitBackendReady`, `startMCPReadyNotifier`, `stopEmbeddedLLM`, web-access lifecycle `initWebAccess`/`stopWebAccess`), and the tool-manager progress/fatal plumbing (`emitToolManagerProgress` :120, `buildToolFatalActions` :134).
- **How:** The assembler *calls* the exported phases already in `backend/startup_phases.go` (`InitTools`, `InitDatabase`, `InitStores`, `InitTerminalManager`, `PreloadProjectsAndSessions`, `InitEmbeddedLLM`, `InitChatGPTAuth`, `InitKimiAuth`, `StartUpdateCheckerBackground`, `StartAutoFetchBackground`) and `backend/vector_launcher.go` — wrapping, not re-homing. Host-specific behavior arrives only through `HostServices` + an `Options` struct (paths, flags desktop used to read directly). The six HITL callback builders stay in `desktop` for now (W3 until Phase 1b); the assembler accepts them as injected `ApplicationConfig` function fields, exactly as `backend/application.go` already does.
- **Where:** new `backend/appbuild/*.go`; deletions/moves out of `desktop/startup_phases.go`; `desktop/startup.go` (orchestration now delegates).
- **Acceptance Criteria:** `desktop/` contains no assembly steps — only Wails specifics + a `HostServices` implementation + the (interim) HITL callback builders; one non-Wails entry point builds a working assembly; `backend/startup_phases.go` is unchanged in location and export surface (wrapping only).

### Task 1a-3 — Wails host adapter

- **What:** A `desktop` type implementing `HostServices`: session events → Wails emission via the existing `buildUIEmitFunc` wiring (now the adapter's interior), global events → `wailsEmit`/`EventsEmit`, `Quit` → `wailsRuntime.Quit` (the seam `FrontendAPIConfig.QuitApp` already uses), readiness hooks → existing `emitBackendReady` behavior. Re-point `desktop/startup.go` `Startup`/`Shutdown` (:148/:482) at the assembler.
- **How:** Keep Wails-only concerns in `desktop` untouched: window creation/geometry (`desktop/window_state.go`), `EventsOn` listeners (`wireWailsEventListeners` :646), native dialogs (`desktop/dialog_save.go`), clipboard, notifications (`desktop/notifications*.go`), power state (`desktop/powerstate_*.go`), CSP (`desktop/csp.go`), exit guard (`desktop/exit_guard.go`), `wailsEmit` seam.
- **Where:** `desktop/hostservices_wails.go` (new), `desktop/startup.go`, `desktop/startup_phases.go` (residual W1/W2 glue only).
- **Acceptance Criteria:** App launches and behaves identically (manual smoke: startup splash → backend:ready → chat usable); `desktop` no longer imports stores/managers directly for construction (it consumes the `Assembly`); zero-Wails invariant intact under `backend/`/`core/`.

### Task 1a-4 — Tests: fake-host assembly coverage

- **What:** Move/adapt `desktop/startup_phases_test.go` so assembly stays covered without Wails, and add a fake-host test that builds a working `backend.Application` via the assembler with a fake `HostServices` (capturing emissions), no Wails.
- **How:** Tests become `backend/appbuild` package tests; desktop keeps only adapter tests. No sleeps/timing debt (`specs/domains/testing.md`); assertions on emitted event sequences use captured slices from the fake host.
- **Where:** `backend/appbuild/appbuild_test.go` (+ fakes), `desktop/startup_phases_test.go` (adapted residue), `desktop/app_test.go`.
- **Acceptance Criteria:** A `go test ./backend/appbuild/...` test constructs a working `Application` with a fake host; `make test` green including `-skip '^TestStress'` default suite; coverage of the moved steps not reduced vs. the pre-move tests (moved, not dropped).

### Task 1a-5 — Zero-Wails guard test

- **What:** A standing test that walks every `*.go` file under `backend/` and `core/` (excluding vendored/none today) and fails on any `github.com/wailsapp/wails` import — mechanizing ADR-119's invariant so the assembler work can never silently regress it.
- **How:** Small `go/parser`-based import walker as a test in `internal/layerguard` (new leaf package, no dependencies on backend/core — it only parses files).
- **Where:** new `internal/layerguard/layerguard_test.go`.
- **Acceptance Criteria:** Test passes on the current tree (0 violations), fails when a synthetic wails import is added in a scratch check (verified once locally during the PR), runs in the default `make test` suite.

### Phase 1a exit gate

`make build && make lint && make test && make vulncheck` green; desktop behavior unchanged; PR contains assembler + adapter + tests only. Spec footprint: none mandatory (layers.md already updated in Phase 0); note the new package in `specs/contracts/backend-core.md` only if that contract enumerates backend packages (check at PR time; if yes, one-line addition).

---

## 7. Phase 1b — #75: HITL funnels + judge → `backend/interaction`

**Issue:** [#75](https://github.com/v0lka/c0wrk/issues/75) · **PR:** code + tests + ADR-119 sunset note · **Depends on:** Phase 0, Phase 1a (the assembler wires the service) · **Unblocks:** 3, 5.

### Context

The six pending registries are `sync.Map` fields on `desktop.App` (`desktop/app.go:78–83`); handlers live in `desktop/event_handlers.go`; the snapshot binding is `desktop/pending_actions.go` `GetPendingActions` :62; the judge core is already transport-neutral in `backend/judge.go` (`JudgeService` :37) + `backend/step_limit_judge.go`, but its lifecycle is driven from `desktop` (`judgeWG` :89, `judgeService` :100 on `desktop.App`; orchestration inside `desktop/startup_phases.go` builders). Wire parsing already lives in `core/hitlwire` (P-08) — reuse it, don't duplicate it.

### Task 1b-1 — Create `backend/interaction` with the six registries

- **What:** A transport-neutral service owning the six pending registries keyed by session id — tool confirmations, ask-user, step-limit, plan approvals, goal proposals, goal suggestions — with `Register*` / `Resolve*` / `Snapshot(sessionID)` APIs. The snapshot JSON shape is byte-identical to today's `PendingActionsResponse` (`desktop/pending_actions.go` :9–:61) so the frontend is unchanged.
- **How:** New package `backend/interaction`; move the payload structs from `desktop/pending_actions.go` into it (same JSON tags); registry semantics preserved (pending entry lifecycle, duplicate-resolution behavior, `hitl_resolved` emission via an injected emit func — delivered through the host seam, not Wails directly).
- **Where:** new `backend/interaction/service.go`, `backend/interaction/snapshot.go`; `desktop/pending_actions.go` becomes a thin binding that calls the service.
- **Acceptance Criteria:** All six funnels expose the transport-neutral API; package tests run without Wails and cover register→snapshot→resolve for each funnel; `GetPendingActions` returns the same JSON for the same state as before (golden-shape test).

### Task 1b-2 — Move the callback builders into the service

- **What:** Move the six builders from `desktop/startup_phases.go` (`buildAskUserCallback` :201, `buildGoalProposalCallback` :253, `buildGoalSuggestionCallback` :277, `buildPlanApprovalCallback` :397, `buildConfirmCallback` :453, `buildStepLimitCallback` :526) into `backend/interaction` as the canonical implementations of `AskUserFunc` / `ConfirmFunc` / `HITLHandler` / `ApprovalFunc` / `GoalProposer` (+ goal-suggestion proposer). The assembler (Phase 1a) wires the service as the backend callback provider via `ApplicationConfig`'s existing function fields.
- **How:** The service's implementations register the pending prompt, emit the prompt event through the host seam, and block/await resolution exactly as the desktop builders do today (same channels/timeouts, no behavior change); desktop's W3 plumbing (sync.Maps, handler bodies) is deleted, not adapted.
- **Where:** `backend/interaction/callbacks*.go`; deletions in `desktop/startup_phases.go`; wiring in `backend/appbuild`.
- **Acceptance Criteria:** `desktop` contains none of the six registries or builders; a fake-host test drives ask-user/confirm/plan/goal round-trips through `backend/interaction` alone; `backend/application.go`'s callback field types unchanged.

### Task 1b-3 — Absorb the judge lifecycle

- **What:** Move judge orchestration — the `judgeWG` shutdown barrier, in-flight claims, `runJudgeEvaluation` invocation — from `desktop` into `backend/interaction`, absorbing `backend/judge.go`'s `JudgeService` (its Begin/Finish/Run contract stays; ownership moves so any host gets identical judge behavior).
- **How:** `backend/interaction` owns judge spawning and the one-evaluation-per-confirmation bound (the contract documented at `desktop/app.go` :92–:99); expose a `WaitJudges()` (or equivalent) the hosts call during shutdown instead of `desktop`'s `judgeWG`.
- **Where:** `backend/interaction/judge.go` (absorbing `backend/judge.go`), `desktop/app.go` (fields removed), `desktop/startup.go` `Shutdown` :482 (calls the service).
- **Acceptance Criteria:** Judge logic lives outside `desktop`; `desktop` makes no decisions; a test exercises judge begin/finish/run + double-frame drop without Wails; shutdown ordering (drain judges before teardown) preserved by test.

### Task 1b-4 — Desktop becomes a translate-and-call adapter

- **What:** Reduce `desktop/event_handlers.go` (452 lines) to translation: Wails event payload → `core/hitlwire` parse → `interaction.Resolve*`; service-prompted events → Wails emission. Same event names, same payload shapes.
- **How:** Handlers keep their names/signatures (W2 binding surface) but their bodies become parse+call; `emitHitlResolved` :17 moves its logic into the service (which emits `hitl_resolved` through the seam) with the desktop side only relaying.
- **Where:** `desktop/event_handlers.go`; tests `desktop/event_handlers*_test.go` adapted (they keep passing — same wire shapes).
- **Acceptance Criteria:** Desktop handler files contain only translate-and-call logic (no pending-state mutation, no decision branches beyond payload validation); all existing `desktop/event_handlers*_test.go` pass unmodified in their wire assertions.

### Task 1b-5 — Restart recovery through the service

- **What:** On startup, reconcile persisted pending tool-confirm messages (the `resolved` flag on tool-confirm chat messages) through `backend/interaction`, so pending-action recovery works identically for any host.
- **How:** Move the recovery pass (currently reachable from the desktop startup path) into the service as `RecoverPersisted(ctx, store, …)` invoked by the assembler; test with a store fixture containing one resolved + one unresolved pending confirm.
- **Where:** `backend/interaction/recovery.go`; wiring in `backend/appbuild`; the desktop-side recovery call site removed.
- **Acceptance Criteria:** A test boots the service against a fixture DB and produces the same `Snapshot` a desktop restart shows today; recovery runs without Wails.

### Task 1b-6 — Execute the ADR-119 W3 sunset (spec footprint)

- **What:** Add the dated Historical Note to ADR-119 (`specs/decisions/119-desktop-layer-scope-rule.md`) recording that Phase 1b superseded the W3 clause, and drop W3 from `specs/architecture/layers.md`'s desktop section + invariants in the same change — the obligation ADR-119 refinement 2 imposes.
- **How:** Per `specs/META.md` update rules: append the Historical Note, do not rewrite the decision body; edit layers.md desktop section (:119) and the W-criterion mention.
- **Where:** `specs/decisions/119-desktop-layer-scope-rule.md`, `specs/architecture/layers.md`; touch `SECURITY.md` ASI09 only if it names W3 (check; expected no).
- **Acceptance Criteria:** ADR-119 carries a dated sunset note; layers.md no longer lists W3 as a valid desktop category; `specs/INDEX.md` unchanged (no new docs); cross-references resolve.

### Phase 1b exit gate

`make build && make lint && make test && make vulncheck` green; all six funnels + judge live in `backend/interaction` with Wails-free tests; ADR-119 sunset executed. Manual smoke: tool confirmation, ask-user form, plan approval, goal proposal/suggestion, step-limit prompt each round-trip in the running app.

---

## 8. Phase 1c — #76: global events from their source

**Issue:** [#76](https://github.com/v0lka/c0wrk/issues/76) · **PR:** code + tests + catalog · **Depends on:** Phase 0, Phase 1a (HostServices seam) · **Unblocks:** 3. Independent of 1b.

### Context

Partial groundwork already landed: backend sources emit most global events through `FrontendAPIConfig.EmitEvent` (`backend/frontend_api.go:459`) and `specs/contracts/event-catalog.md` carries an emitter column. Still open: (a) the seam is a raw callback on `FrontendAPI`, not the assembler-level `HostServices.EmitGlobal`; (b) startup-phase events still originate in `desktop` — `startup_error`, `projects:loaded`, `sessions:loaded`, `tool_manager:start|done`, `mcp:ready`, the startup-path `vector_index:status`; (c) `files:dropped` and `app:exit_requested` originate in `desktop`; (d) no assembler-level fake-host test for global emissions.

### Task 1c-1 — `HostServices.EmitGlobal` as the assembler-level seam

- **What:** Make `HostServices.EmitGlobal(name, data...)` (declared in Phase 1a per ADR-120) the single assembler-level route for global events; the assembler injects it into `FrontendAPIConfig.EmitEvent`, so backend sources keep emitting unchanged while the abstraction level rises from "callback on FrontendAPI" to "host service".
- **How:** `backend/appbuild` passes `host.EmitGlobal` as `FrontendAPIConfig.EmitEvent` when building the `FrontendAPI`; no source-site changes needed for the already-migrated events; desktop's Wails host implements `EmitGlobal` with today's `EventsEmit` behavior.
- **Where:** `backend/appbuild` (wiring), `desktop/hostservices_wails.go`, `backend/frontend_api.go` (doc comment on the seam only).
- **Acceptance Criteria:** No `desktop`-owned func is passed as `FrontendAPIConfig.EmitEvent` anymore (the assembler owns the injection); a fake-host test asserts a representative already-migrated event (`config:updated`) arrives through `EmitGlobal`.

### Task 1c-2 — Startup-phase events emit from `backend`/appbuild

- **What:** Move the origin of `startup_error`, `projects:loaded`, `sessions:loaded`, `tool_manager:start|done`, `mcp:ready`, and the startup-path `vector_index:status` from `desktop` to their backend sources (`backend/startup_phases.go` phases and `backend/vector_launcher.go` via the assembler), so a headless host gets them identically. Desktop only relays.
- **How:** The phases already receive `emit func(string, ...any)` hooks (P-12/P-14 shape) — the assembler now feeds them `host.EmitGlobal`; `desktop/startup_phases.go` residual emitters (`emitBackendReady` :694 → emits `backend:ready` + project/session loads; `startMCPReadyNotifier` :748 → `mcp:ready`; tool-manager progress :120) move their emit origin into backend (emit *calls* already parameterized; the *construction* of what to emit moves). Event names, payloads, ordering unchanged.
- **Where:** `backend/startup_phases.go`, `backend/vector_launcher.go`, `backend/appbuild`, `desktop/startup_phases.go` (shrink), `desktop/startup.go`.
- **Acceptance Criteria:** Grepping `desktop/` for the six event names finds no emission sites (only relay/listener registrations); a fake-host assembly test captures the startup sequence (`tool_manager:*`, `projects:loaded`, `sessions:loaded`, `mcp:ready`, `vector_index:status`, `backend:ready`) in order; the running app's startup toast/splash behavior identical.

### Task 1c-3 — Classify `files:dropped` and `app:exit_requested` as client-native

- **What:** Per decision D4 (ratified in ADR-120): these two events' true source is the OS window shell (Wails `OnFileDrop`, the window close interceptor), so they stay client-native and are documented as such rather than relocated; when Phase 4 lands, the shell keeps emitting them locally exactly as today.
- **How:** Update `specs/contracts/event-catalog.md`: mark both events "Emitter: client shell (client-native per ADR-120)" in the Global Events table (:9); no code change beyond a doc comment where they are emitted (`desktop/app.go` / `desktop/exit_guard.go`).
- **Where:** `specs/contracts/event-catalog.md`, comments at the desktop emit sites.
- **Acceptance Criteria:** Catalog rows for both events state the client-native classification and reference ADR-120; no behavior change.

### Task 1c-4 — Assembler-level fake-host global-emission test

- **What:** One test that builds the assembly with a fake host and asserts the complete set of global events the startup path emits (names + minimal payload shapes), locking the seam so the daemon (Phase 3) inherits a proven emission surface.
- **How:** Extend the Phase 1a fake host with a capture slice; drive `Assembler.Build` with a temp agent dir (existing test helpers pattern); assert against the event-catalog's Global Events list.
- **Where:** `backend/appbuild/appbuild_test.go` (or a sibling `_events_test.go`).
- **Acceptance Criteria:** Test runs without Wails, covers every startup-path global event, and fails if a new desktop-originated global event appears undocumented (assert the captured set ⊆ catalog).

### Task 1c-5 — Sync the event catalog (spec footprint)

- **What:** Update `specs/contracts/event-catalog.md` emitter column for every event whose origin moved in this phase; keep the "Breaking Change Checklist" (:360) entry for global events satisfied (emitters change, wire contract does not).
- **How:** Edit the Global Events table (:9) and any per-event notes; no schema/compat version changes (that is Phase 6).
- **Where:** `specs/contracts/event-catalog.md`.
- **Acceptance Criteria:** Every global event row names its true emitter (backend source or client shell); catalog matches the code (spot-check three events by grep).

### Phase 1c exit gate

`make build && make lint && make test && make vulncheck` green; every global event originates at its source or is documented client-native; fake-host test proves the emission surface. Manual smoke: startup splash, MCP-ready notice, tool-manager progress, project/session preload, vector-index status indicator all behave as before.

---

## 9. Phase 2 — #77: daemon gRPC contract (`.proto`) + codegen pipeline

**Issue:** [#77](https://github.com/v0lka/c0wrk/issues/77) · **PR:** proto + codegen + docs, no runtime wiring · **Depends on:** Phase 0 (API split), Phase 1a (callable surface) · **Unblocks:** 3, 4, 6. Independent of 1b/1c once 1a lands.

### Context

No gRPC/protobuf service exists today: `go.mod` has only `google.golang.org/protobuf v1.36.6 // indirect` (:122) and no `grpc`; the Makefile has no codegen target (targets end at `bump` :635). The contract must mirror the RPC surface the assembler exposes and every session event type the engine produces.

### Task 2-1 — Author the `.proto` sources

- **What:** Proto package `c0wrk.daemon.v1` defining the services of the ADR-120 API split: `Daemon` (health/version/shutdown), `Session` (create/send/pause/resume/list/delete — mirroring the session lifecycle RPCs of `backend/frontend_api_session*.go`), `Events` (server-stream of typed events), `Project`, `Config`, `Workspace`, `Git`, `Vector`, `ToolManager` (each mirroring its `backend/frontend_api_<area>.go` method set — the daemon-API half of the split; client-native methods excluded per ADR-120), `Terminal` (bidirectional stream), `Interaction` (permission-request register/respond, pending snapshot).
- **How:** One `proto/daemon/v1/daemon.proto` (or a small set of per-area files under `proto/daemon/v1/` — pick at PR time, keep it uniform); messages mirror `backend/api_types.go` (842 lines of response DTOs) and the session manager's arguments; field names use snake_case mapping to the existing JSON tags so the Phase 4 proxy needs no renaming table.
- **Where:** new `proto/daemon/v1/*.proto`.
- **Acceptance Criteria:** Every service above is declared; every daemon-API method of the `backend/frontend_api_*.go` surface has a corresponding RPC (audit table in the PR description: area → methods → RPC names); client-native methods (dialogs, clipboard, window, power, notifications, exit guard, CSP, file drop) are absent by design, listed as excluded.

### Task 2-2 — Typed event envelope covering the whole catalog

- **What:** An `Events` stream carrying **every** session event type declared in `backend/session/events.go` — cross-checked against `specs/contracts/event-catalog.md`'s Session-Scoped Events section (Orchestration Lifecycle :60 through Blackboard & Judge :201) — as a typed envelope.
- **How:** Envelope message with session id, sequence number, and a `oneof` payload covering each event's payload message (mirroring the payload structs in `backend/session/events.go`, e.g. `SessionCreatedData`, `AutonomyDecisionData`); adding a new event later = adding a `oneof` member (additive-only, feeds Phase 6); global events ride the same stream with a scope discriminator. Map Wails event-name strings ↔ enum in one Go helper.
- **Where:** `proto/daemon/v1/events.proto` + mapping helper next to the generated code.
- **Acceptance Criteria:** A test (Go, over the generated types) asserts the oneof covers every event name listed in the catalog's session-scoped sections — the audit is executable, not a doc claim; unknown-event handling defined (fail the stream, never drop silently).

### Task 2-3 — `make proto` codegen pipeline with pinned toolchain

- **What:** A Makefile target that regenerates the Go bindings deterministically: pinned `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc` versions (downloaded + SHA256-verified into `.cache/`, following the existing `fetch-onnx` pattern :406); generated code committed at `proto/gen/daemon/v1` (decision D5); a drift check `make proto-check` = `make proto && git diff --exit-code proto/` wired into CI.
- **How:** Generated Go package `daemonv1` importable as `github.com/v0lka/c0wrk/proto/gen/daemon/v1` (not `internal/` — future TUI/ACP binaries in this module import it; decision D5); regenerate → `gofmt` clean; document the pinned versions in the Makefile header comment.
- **Where:** `Makefile` (new `proto` + `proto-check` targets), new `proto/gen/daemon/v1/*.go` (committed), `.github/workflows/*.yml` (add `make proto-check` to an existing CI job).
- **Acceptance Criteria:** `make proto` from a clean checkout is byte-identical to the committed files (run twice, `git diff` empty); CI runs `proto-check`; `go build ./...` compiles the generated package.

### Task 2-4 — Promote gRPC dependency

- **What:** Add `google.golang.org/grpc` as a direct dependency (it is absent today); keep `protobuf` and promote it to direct (the generated code imports it directly).
- **How:** `go get google.golang.org/grpc@<pinned-minor>`; `go mod tidy`; confirm the `go` directive / CI `go-version` lockstep rule (AGENTS.md) is respected; run `make vulncheck` — grpc's stdlib surface must be clean at the pinned version.
- **Where:** `go.mod`, `go.sum`.
- **Acceptance Criteria:** `go build ./...` green with grpc direct; `make vulncheck` green; `make lint` green (new dependency does not trip depguard).

### Task 2-5 — Document the contract (spec footprint)

- **What:** New `specs/contracts/daemon-rpc.md`: service inventory with per-service method tables, streaming semantics (`Events` server-stream; `Terminal` bidi), the socket/pipe transport + owner-only permission posture, error-mapping conventions (gRPC status ↔ the error strings the frontend expects), version policy hook (package `v1`; additive-only evolution — detailed in Phase 6), and the audit table from Task 2-1. Listed in `specs/INDEX.md` (contracts :208) and the Navigation by Task.
- **How:** Follow the existing contract format (`specs/contracts/desktop-frontend.md` as template); link ADR-120 and the event catalog.
- **Where:** new `specs/contracts/daemon-rpc.md`, `specs/INDEX.md`.
- **Acceptance Criteria:** Contract documented and indexed; every RPC cross-references its `backend/frontend_api_*.go` origin; reviewers can answer "which methods are daemon API?" from the doc alone.

### Phase 2 exit gate

`make build && make lint && make test && make vulncheck` green; generated code committed + drift-checked; no runtime wiring yet (the daemon lands in Phase 3 — this PR is contract + codegen only, so nothing behavioral changes).

---

## 10. Phase 3 — #79: `c0wrk serve` — the daemon

**Issue:** [#79](https://github.com/v0lka/c0wrk/issues/79) · **PR:** code + tests + spec · **Depends on:** Phases 0, 1a, 1b, 1c, 2 · **Unblocks:** 4, 5.

### Context

`main.go` (294 lines) is hardwired to `wails.Run` with only the `--self-update` branch; `core/cli` provides the fail-closed argv grammar (`ActionRun`/`ActionHelp`/`ActionVersion`/`ActionSelfUpdate`, spec `specs/domains/cli.md`); the single-instance primitive is `core/instancelock` (P-07, ADR-075) with the GUI relay in `desktop/singleinstance.go`; `vector_index.enabled` (ADR-103) already allows skipping ONNX load.

### Task 3-1 — `serve` subcommand dispatch

- **What:** Extend `core/cli` with `ActionServe` (`c0wrk serve`) alongside the existing actions; `main.go` routes `ActionServe` to the daemon runner instead of `wails.Run`; default argv still launches the GUI (`ActionRun`), and `--self-update` plumbing is untouched.
- **How:** New action constant + grammar entry in `core/cli/cli.go` (fail-closed: unknown args still error naming the arg); `main.go` gains the branch; `core/cli` tests extend for the new grammar; update `specs/domains/cli.md` in the same change.
- **Where:** `core/cli/cli.go` (+ `cli_test.go`), `main.go`, `specs/domains/cli.md`.
- **Acceptance Criteria:** `c0wrk serve` parses to `ActionServe`; `c0wrk` (no args) still parses to `ActionRun`; `c0wrk --bogus` still fails closed; `go run . --help` lists `serve`.

### Task 3-2 — Socket path + permissions

- **What:** `DaemonSocketPath(agentDir)` in `backend/config/paths.go` returning `~/.c0wrk/daemon.sock` on macOS/Linux and a named-pipe path (`\\.\pipe\c0wrk-daemon`) on Windows; the listener creates the socket with owner-only permissions (0600 / restrictive pipe ACL) and removes it on shutdown.
- **How:** Follow the existing helpers' pattern (`DatabasePath` :317 et al.); no inline path construction anywhere else (ground rule 6); socket file removed on graceful stop and tolerated-absent on startup (stale socket detection: probe connect → remove if dead).
- **Where:** `backend/config/paths.go`, new `backend/daemon/listener.go` (+ `_windows.go` variant if needed).
- **Acceptance Criteria:** Path helper unit-tested for all three GOOS (build tags); listener test asserts the 0600 mode on Unix; stale-socket recovery tested; `desktop`/GUI code paths never call the helper.

### Task 3-3 — Single-instance lock + second-process rule

- **What:** The daemon claims the agent-dir lock via `core/instancelock` (the P-07 absorption point ADR-119 names); a second daemon against the same agent dir **exits with a clear error** (decision D2, ADR-120); the rule is tested.
- **How:** Reuse `core/instancelock` exactly as `desktop/singleinstance.go` does for the GUI relay; distinct lock identity for "daemon" vs "GUI relay" so the GUI's existing second-instance focus behavior is untouched; error message names the socket path so a user can inspect.
- **Where:** new `backend/daemon/singleinstance.go` (+ test), `core/instancelock/` (reuse only).
- **Acceptance Criteria:** A test spawns/locks twice and asserts the second fails with the documented error; the GUI two-instance relay behavior (focus existing window) is unchanged (existing `desktop/singleinstance_test.go` passes).

### Task 3-4 — Headless `HostServices` + gRPC server wiring

- **What:** A headless `HostServices` implementation — session events → `Events` stream via the P-09 batcher (`backend/event_batcher.go`), global events → stream, `Quit` → context cancel on SIGINT/SIGTERM — plus the gRPC server serving the full Phase 2 contract (`Daemon`, `Session`, `Events`, `Project`, `Config`, `Workspace`, `Git`, `Vector`, `ToolManager`, `Terminal`, `Interaction`) over the assembly built by `backend/appbuild` (Phase 1a) with `backend/interaction` (Phase 1b) wired in.
- **How:** New `backend/daemon` package: server construction from the `Assembly`, per-session event fan-out (subscription registry keyed by session id, sequence numbers from the batcher), the health/version/shutdown RPCs (version from `core/version` ldflags metadata), and handler methods that delegate to the same `FrontendAPI`/manager methods the GUI calls today — one implementation, two transports, zero behavior fork.
- **Where:** new `backend/daemon/*.go` (server, events, terminal stream, handlers per area), `backend/appbuild` (a `Build` variant/options for headless: no window/native services).
- **Acceptance Criteria:** An integration test (in-process or loopback socket, temp agent dir) starts the daemon, calls health/version, creates a session, streams events, and shuts down cleanly; every service from Phase 2 answers at least one smoke RPC; no Wails import anywhere in the daemon path (zero-Wails guard from Task 1a-5 keeps this true).

### Task 3-5 — Background services + web-access ownership in the daemon

- **What:** The daemon runs the background services through the assembler: vector index (respecting `vector_index.enabled: false` ⇒ no ONNX/embedder load, ADR-103), git auto-fetch, tool manager, crash capture, update checker; the web-access sidecar lifecycle is owned by the daemon (P-11 moved the service core to `backend/frontend_api_webaccess.go`) when `experimental.enabled && web_access.enabled`.
- **How:** Assembler options select headless mode (skip GUI-only services per ADR-119 refinement 1 — e.g. webview-GPU watchdog never starts); everything else identical to the GUI path. The daemon must start without `make fetch-onnx` artifacts when the vector switch is off.
- **Where:** `backend/appbuild` (options), `backend/daemon` (runner), `backend/frontend_api_webaccess.go` (ownership call sites).
- **Acceptance Criteria:** Test: daemon starts with `vector_index.enabled: false` in a temp agent dir with no ONNX artifacts present and serves sessions; test: web-access sidecar mounts/unmounts with the compound gate in headless mode (reuse the existing web-access test fixtures).

### Task 3-6 — Lifecycle: signals, PTY cleanup, DB close

- **What:** Graceful shutdown on SIGINT/SIGTERM (and the `Daemon.Shutdown` RPC): stop accepting streams, drain judges + pending prompts via `backend/interaction`, `terminal.Manager.StopAll()` (PTY cleanup), close the SQLite DB, stop background services, release the lock, remove the socket. Logging to stderr/file via `slog` (no Wails logger); bounded shutdown (watchdog, not sleeps — testing conventions).
- **How:** Signal handling with `signal.NotifyContext`; shutdown ordering mirrors `desktop/startup.go` `Shutdown` :482 (proven order) minus window concerns; exit code 0 on graceful stop, non-zero on startup failures.
- **Where:** `backend/daemon/runner.go`, `backend/daemon/lifecycle.go` (+ tests).
- **Acceptance Criteria:** Integration test sends SIGINT (or cancels the context) to a running daemon with an active PTY + open session and asserts: streams end cleanly, PTYs stopped, DB closed (subsequent open is clean), lock released, socket removed, process exits 0; `go run . serve` manual check documented in the PR.

### Task 3-7 — Headless-runtime spec (spec footprint)

- **What:** New `specs/domains/headless-runtime.md`: the `serve` command, socket layout + permissions, single-instance rule (D2), lifecycle/shutdown ordering, background-service ownership (incl. vector-skip and web-access), the pre-Phase-5 security posture (owner-only socket; attached local-owner client trusted — D7) with an explicit pointer that the policy ladder lands in Phase 5. Indexed in `specs/INDEX.md`.
- **How:** Domain-spec format per `specs/META.md`; link ADR-120, ADR-075, ADR-103, `specs/contracts/daemon-rpc.md`, `specs/domains/cli.md`.
- **Where:** new `specs/domains/headless-runtime.md`, `specs/INDEX.md`.
- **Acceptance Criteria:** Spec exists, indexed, and matches the shipped behavior (each claim traceable to a test named in the doc); no contradiction with ADR-119 refinement 1 (GUI-only services never daemon-owned).

### Phase 3 exit gate

`make build && make lint && make test && make vulncheck` green. Acceptance scenario from the issue, executed and documented in the PR: `go run . serve` starts, serves gRPC, a client script creates a session, executes a trivial task under the owner-trust policy (D7), and the daemon exits cleanly on SIGINT. The GUI path is untouched and still works (manual smoke).

---

## 11. Phase 4 — #80: GUI migration onto the daemon

**Issue:** [#80](https://github.com/v0lka/c0wrk/issues/80) · **PR:** code + tests · **Depends on:** Phase 3 · **Unblocks:** nothing (terminal state of the topology migration; 5 and 6 are independent).

### Context

`desktop.App` embeds `*backend.FrontendAPI`; Wails binds its promoted methods; the frontend calls `window.go.desktop.App.*` via generated bindings (`frontend/wailsjs/go/desktop/App.{js,d.ts}` — regenerated only by `wails build`/`wails dev`). The frontend must stay untouched: same bindings, same event names (`session:${id}:${type}` + global names). The exit guard (`desktop/exit_guard.go` `ShouldPreventClose` :64 → `lookupActiveSessions` :98) and the self-update two-process flow (`core/updater/`, `ApplyUpdate` + `QuitApp`) are the two trickiest client concerns.

### Task 4-1 — Daemon lifetime management in the shell

- **What:** At startup the shell **auto-spawns** the daemon when no healthy instance answers on the socket, **connects** when one does; the spawning shell owns stopping it on quit; a connecting shell leaves it running (decision D3). Crash supervision: bounded restart with backoff and a surfaced UI error state (the existing `runtime_error` toast channel).
- **How:** Health probe via the `Daemon.Health` RPC with a short deadline before deciding spawn-vs-connect; spawn = `os/exec` of the same binary with `serve` (env-scrubbed like the updater's re-exec); ownership flag drives `Shutdown` behavior; supervision loop with capped retries then a permanent error surface.
- **Where:** new `desktop/daemonclient/supervisor.go` (new package under `desktop/` — W4/W1 host integration), `desktop/startup.go` (integration point).
- **Acceptance Criteria:** Tests (fake spawner/socket) cover: cold start spawns, warm start connects, owned-daemon stops on quit, non-owned survives, crash → restart → cap → error state; manual: launching the app twice shares one daemon, quitting the owning app keeps a manually-started `c0wrk serve` alive.

### Task 4-2 — Generated RPC proxy for the Wails binding surface

- **What:** Every daemon-API method on the `backend/frontend_api_*.go` surface gets a proxy: Wails RPC → gRPC → daemon handler, preserving method names, signatures and error strings so the generated Wails bindings and the frontend stay byte-identical. Generated, not hand-written (decision D6).
- **How:** Generator `internal/genproxy`: input = the Phase 2 proto service definitions + a manifest mapping Wails method name → (service, RPC); output = `desktop/proxy/*.go` implementing each method by forwarding through the gRPC client with typed requests built from the existing Go arg types (mapping via the proto field-name parity from Task 2-1) and translating gRPC status errors into the same error values the local calls returned (error-mapping table in `specs/contracts/daemon-rpc.md` is the contract). Committed output + `make genproxy-check` drift gate, mirroring `proto-check`. `desktop.App` switches from embedding `*backend.FrontendAPI` to embedding the proxy facade (binding generator sees the same method surface — verify with a no-diff regen of `frontend/wailsjs`).
- **Where:** new `internal/genproxy/`, new `desktop/proxy/` (generated) + `desktop/proxy/facade.go`, `desktop/app.go` (embedding switch), `Makefile` (`genproxy` + `genproxy-check`), CI wiring.
- **Acceptance Criteria:** `wails build` regenerates `frontend/wailsjs/go/desktop/App.{js,d.ts}` with **zero diff** (the decisive no-frontend-change proof — run it in the PR); every method in `ls backend/frontend_api_*.go`'s bound surface is either proxied or on the client-native exclusion list from ADR-120 (audit table in the PR); error mapping table satisfied by tests (a daemon-side error surfaces with the same string the local call produced).

### Task 4-3 — Event stream → Wails re-emission

- **What:** The shell subscribes to the daemon `Events` stream and re-emits every event via Wails under unchanged names: `session:${id}:${type}` for session events, global names verbatim; the batcher contract (coalescing/barriers) is preserved end-to-end so streaming behavior (token deltas, terminal output) is indistinguishable from today.
- **How:** One subscriber per app lifetime, resilient to stream breaks (resubscribe with sequence-number catch-up using the envelope sequence from Task 2-2, no silent drops); the daemon-side batcher already coalesces; the shell does not re-batch (it relays); `hitl_resolved` and prompt events flow through `backend/interaction`'s stream path.
- **Where:** `desktop/daemonclient/events.go`, `desktop/startup.go` (wire-in), reuse of `desktop/event_batcher.go` wrapper only if measurements require it (otherwise delete with rationale in the PR).
- **Acceptance Criteria:** A round-trip test (fake daemon emitting a scripted event set) asserts Wails-emitted names/payloads match the pre-migration goldens; manual: streaming chat, terminal output, plan updates, HITL prompts all live in the GUI with no visible latency regression.

### Task 4-4 — Client-native concerns stay local

- **What:** Dialogs (`desktop/dialog_save.go`), clipboard, window/geometry (`desktop/window_state.go`), power state (`desktop/powerstate_*.go`), notifications (`desktop/notifications*.go`), CSP (`desktop/csp.go`), `files:dropped`, and the exit guard remain shell-local — none is proxied. Where a client-native action must notify the engine (e.g. a saved dialog path used by a session), the existing RPC carries it, unchanged.
- **How:** Audit the binding surface against ADR-120's client-native list; those methods keep their direct local implementations on `desktop.App` next to the proxy facade; no behavior change.
- **Where:** `desktop/app.go`, the ADR-120 exclusion list, `specs/contracts/desktop-frontend.md` (note which methods are daemon-proxied vs client-native — spec footprint).
- **Acceptance Criteria:** The audit table in the PR maps every bound method to proxied|client-native; manual checklist items for each client-native feature pass.

### Task 4-5 — Exit guard consults the daemon

- **What:** `ShouldPreventClose` (:64) / `lookupActiveSessions` (:98) read live-session state from the daemon (`Session` list/active RPC) instead of the in-process manager, so quit-guarding reflects daemon truth (sessions may outlive the window when the daemon was started externally).
- **How:** Swap the local lookup for the RPC with a bounded timeout; on RPC failure fail-safe (prevent close + surface the error — never silently allow quit when state is unknown); `ConfirmExit` :113 flow unchanged.
- **Where:** `desktop/exit_guard.go` (+ `exit_guard_test.go` extended with a fake client).
- **Acceptance Criteria:** Tests: active daemon session → close prevented; none → allowed; unreachable daemon → fail-safe prevention + error surface; manual: closing the window with a running task shows the guard, quitting an owned daemon stops it after guard passes.

### Task 4-6 — Self-update across the daemon boundary

- **What:** The update flow becomes daemon-coordinated: clients disconnect → the daemon restages/applies the update (the existing `core/updater` two-process `--self-update` flow, SHA256SUMS fail-closed, `.old` rollback — unchanged mechanics) → the shell respawns the daemon (spawn-ownership) from the new binary → the frontend reconnects. Coordinate with `core/updater/` so the re-exec waits on the daemon PID, not the GUI's.
- **How:** `ApplyUpdate`'s daemon-side implementation triggers restage + graceful stop; the supervisor's restart path re-execs the updated binary; the GUI's `QuitApp` seam semantics preserved for the update window; update events (`update:*`) already flow through the event stream.
- **Where:** `backend/daemon` (update RPC handler), `core/updater/` (PID-target parameterization only if required — no behavior change to verification), `desktop/daemonclient/supervisor.go` (restart), `desktop/exit_guard.go` (`markUpdateQuit` :38 interplay).
- **Acceptance Criteria:** A test with a fake updater exercises disconnect → restage → restart → reconnect with the staged-binary swap simulated; manual: an in-app update applied end-to-end leaves a working app on the new version with the rollback tree intact.

### Task 4-7 — Manual visual verification (merge-blocking checklist)

- **What:** The PR's merge-blocking checklist: full GUI pass through the daemon — chat (streaming, thinking, tool calls), plan view, E2S panel (if enabled), git panel (changes/branch/history), terminal, file viewer + Find similar, research panel, papers, settings (config save, model profiles, embedded LLM surface), pending-actions bar (all six HITL kinds), exit guard, update toast, multi-window/second-instance relay.
- **How:** Execute against a dev build (`make dev-desktop`); record results in the PR body; any red item blocks merge — no exceptions, no "follow-up" (ground rule 3).
- **Where:** PR description (checklist template committed as part of this phase's PR notes or `docs/development/` verification snippet in the PR).
- **Acceptance Criteria:** Every checklist item green and recorded; frontend diff is empty (Task 4-2's regen proof).

### Phase 4 exit gate

`make build && make lint && make test && make vulncheck` green; frontend untouched (bindings regen zero-diff); GUI fully functional through the daemon; `specs/contracts/desktop-frontend.md` updated with the proxied/client-native split.

---

## 12. Phase 5 — #81: headless permission-policy ladder (fail-closed)

**Issue:** [#81](https://github.com/v0lka/c0wrk/issues/81) · **PR:** code + tests + specs · **Depends on:** Phase 1b (interaction service), Phase 3 (daemon) · **Unblocks:** nothing further (ACP-ready shape is the deliverable).

### Context

Headless mode has no human watching a window; the six HITL funnels (now in `backend/interaction`) and `security.autonomy_mode` (ADR-053) assume an interactive user. The ladder replaces the Phase-3 interim posture (D7) with the ADR-120 policy: **no attached trusted client ⇒ deny** — a headless agent never silently mutates state. The GUI is unaffected: an attached GUI client is the trusted client (rung B), so desktop behavior is unchanged.

### Task 5-1 — Effective-policy resolution engine

- **What:** At session start (and on client attach/detach), resolve the effective policy per capability group: **D** session grant → **B** delegated trusted client → **C** allowlist (`security.groups` via a headless profile) → **A** deny (fail-closed terminal rung). The resolution order and precedence are data, decided once per session entry, not per tool call (per-call lookups only for budget/TTL accounting).
- **How:** New `backend/interaction/policy.go`: resolution takes (session grant set, attached-client trust set, headless profile allowlist) and yields a decision function per security group (`allow` / `confirm-forward` / `deny`); pure and table-tested; interaction with `security.autonomy_mode` documented (autonomy decisions still emit `autonomy_decision` audit events — ASI10 — regardless of rung).
- **Where:** `backend/interaction/policy.go` (+ `policy_test.go`), wired at session entry in `backend/daemon` + `backend/appbuild`.
- **Acceptance Criteria:** A table test covers every precedence combination (grant overrides delegate; delegate beats allowlist; nothing ⇒ deny); resolution is deterministic and pure; runs without Wails.

### Task 5-2 — Rung A: fail-closed deny (default)

- **What:** With no trusted client attached, every mutation request (tool confirmation in a mutating group — ADR-024's group semantics) is denied, with the denial logged and audited (the `autonomy_decision` audit record naming the rung), and never auto-approved.
- **How:** The deny decision returns the same denial shape a user "Deny" produces today so the agent loop handles it through the existing path; the daemon emits the denial to any later-attached client (feeds rung F).
- **Where:** `backend/interaction/policy.go`, `backend/daemon` (wiring), audit emission via the existing `autonomy_decision` event path.
- **Acceptance Criteria:** Test: headless session, default config, no client — a `write_file`-class confirmation is denied and audited; nothing in the daemon can bypass the rung (no debug flag).

### Task 5-3 — Rung B: delegation to a trusted client

- **What:** With a trusted client attached (Phase-3 owner-trust, D7, extended by ADR-120's trust rules), permission requests forward to it over the `Interaction` service; **timeout ⇒ deny** (never queue indefinitely, never approve).
- **How:** Forward via the Phase 2 `Interaction` RPC stream; bounded timeout from config; denial on timeout audited; replaces Phase 3's interim always-trust behavior for mutating groups while read-only groups can remain allow-by-profile (C).
- **Where:** `backend/interaction/delegate.go`, `backend/daemon` (interaction handler), `desktop/daemonclient` (GUI receives and answers — same payloads as its Wails HITL events).
- **Acceptance Criteria:** Tests: forwarded request honored (allow and deny paths); timeout denies; GUI round-trip for each of the six funnels unchanged from the user's perspective; runs without Wails on the service side.

### Task 5-4 — Rung C: allowlist from `security.groups`

- **What:** A headless profile section sourcing allowlists from `security.groups` (ADR-024): groups named `allow` in the profile execute without confirmation in headless mode; `user_confirm`/`deny` keep their meaning.
- **How:** Config schema addition (below) resolved into the rung-C set consumed by Task 5-1; validation rejects unknown groups; the ADR-024 note from Phase 0 is the spec anchor.
- **Where:** `backend/config` (schema + validation + `config.example.yaml`), `backend/interaction/policy.go`.
- **Acceptance Criteria:** Test: profile allows `local_read`-group reads, denies `local_write` writes with no client; invalid profile (unknown group) fails config load with a visible warning (fail-soft-to-deny for the rung, never fail-open).

### Task 5-5 — Rung D: pre-authorized session grant (scope + budget + TTL)

- **What:** A session grant pre-authorizes mutations: **scope** (capability groups and/or tool targets), **budget** (max mutations), **TTL** (wall-clock expiry); enforcement of all three, exhaustion ⇒ fall through to the next rung (not silent approval).
- **How:** Grant created via a daemon RPC (trusted-client-only) at session start or resume; stored in memory keyed by session id (not persisted — a restart drops grants, fail-closed); counters decremented atomically; expiry checked per use.
- **Where:** `backend/interaction/grant.go` (+ tests), `proto` `Interaction` service extension (additive — Phase 2 contract allows oneof/field additions), `backend/daemon`.
- **Acceptance Criteria:** Tests: in-scope mutation passes without prompt; out-of-scope denied; budget exhaustion mid-session falls to rung B/A; TTL expiry denies; restart drops grants.

### Task 5-6 — Rung F (optional, default off): async escalation

- **What:** When enabled, an unresolved permission request is logged and queued; a client attaching later receives the backlog and its resolutions are applied to *future* prompts (never retroactively approving an already-denied action).
- **How:** Bounded queue per session; delivery on attach; strictly informational backfill for past denials; default off in config.
- **Where:** `backend/interaction/escalation.go` (+ tests), config flag.
- **Acceptance Criteria:** Test: default off ⇒ no queueing; on ⇒ attach delivers backlog; a denial that already happened is never reversed.

### Task 5-7 — Config schema + specs (spec footprint)

- **What:** Config schema additions for the ladder (headless profile, delegation timeout, grant defaults, rung-F switch) in `backend/config` + `config.example.yaml` (authoritative tunables reference); update `specs/domains/headless-runtime.md` with the ladder; note the `security.autonomy_mode` interaction (ADR-053) there and in `SECURITY.md` ASI09; extend the Phase 2 `Interaction` proto docs (`specs/contracts/daemon-rpc.md`) with the permission-request payloads (question/options/ids, resolution round-trip — ACP-shaped per the ADR-120 mapping).
- **How:** Follow existing config-section conventions (env expansion, validation, fail-soft load warnings); spec updates in the same PR.
- **Where:** `backend/config/*.go`, `config.example.yaml`, `specs/domains/headless-runtime.md`, `SECURITY.md`, `specs/contracts/daemon-rpc.md`.
- **Acceptance Criteria:** `config.example.yaml` documents every new knob with defaults; a config with the new section round-trips load/save; specs match behavior; permission-request payload shapes are ACP-mappable (validated against Phase 6's table when it lands).

### Phase 5 exit gate

`make build && make lint && make test && make vulncheck` green; each rung covered by Wails-free tests; the GUI's interactive behavior unchanged (manual smoke of one confirmation round-trip); the Phase-3 acceptance scenario re-run now shows default-deny with no client and honored delegation with the GUI attached.

---

## 13. Phase 6 — #83: versioned event contract + ACP/TUI mapping tables

**Issue:** [#83](https://github.com/v0lka/c0wrk/issues/83) · **PR:** docs + (only if the audit finds drift) minimal code annotations · **Depends on:** Phase 0 (ADR-120), Phase 2 (proto API reference) · **Parallel to:** 3, 4, 5 (no code coupling; schedule after 2).

### Context

`specs/contracts/event-catalog.md` documents events but is version-agnostic and Wails-shaped (`session:${id}:${type}`). TUI and ACP are external consumers needing stability guarantees and a mapping to their models (ACP `session/update` kinds; ACP permission requests). This phase freezes the catalog as a **versioned, additive-only** contract and documents the consumer mappings — no TUI/ACP implementation (epic non-goal).

### Task 6-1 — Version the event catalog

- **What:** Add a schema **version** and an **additive-only compatibility policy** to `specs/contracts/event-catalog.md`: new events/fields allowed; renames, removals and semantic changes forbidden within a version; bumps require a migration note. Record the policy in the Breaking Change Checklist (:360).
- **How:** Introduce the version header (e.g. `Event Contract v1`) tied to the proto package `v1` (Task 2-2): the Wails-name catalog and the gRPC oneof are two projections of one versioned contract; state that both must move together.
- **Where:** `specs/contracts/event-catalog.md`.
- **Acceptance Criteria:** The catalog carries a version and policy; the policy text names both projections (Wails names + proto oneof) and the review rule for adding events (catalog row + oneof member + emitter, in one PR).

### Task 6-2 — Audit JSON field-name stability

- **What:** Audit the struct tags in `backend/session/events.go` (411 lines) — every payload struct's JSON field names become frozen contract surface; flag any unstable/duplicated tags now, before external consumers exist.
- **How:** Mechanical audit (list struct → tags); if drift is found (e.g. a field whose tag differs from the documented payload), fix the doc or (only if trivially safe and code-side wrong) the tag in this PR with a catalog note; no event renames.
- **Where:** `backend/session/events.go` (annotations/fixes only if needed), `specs/contracts/event-catalog.md` (payload tables refreshed).
- **Acceptance Criteria:** Every catalog payload row matches the struct tags (spot-checkable by grep); any tag fixed in this PR is listed in the PR body with its before/after.

### Task 6-3 — ACP mapping table (`specs/contracts/acp-mapping.md`)

- **What:** New contract doc mapping: `session.Event` → ACP `session/update` kind (`agent_message_chunk`, `tool_call`, `tool_call_update`, `plan`, …) covering **the whole catalog** including completion/stop reasons ↔ idle `state_update`; the six HITL funnels → ACP permission requests (per the ADR-120 mapping); task complete/stop ↔ idle state; session resume ↔ SQLite restore.
- **How:** One table per catalog section (streaming, tool execution, user interaction, task lifecycle, goal mode, E2S, plan review, blackboard/judge, …); every row names the ACP kind or an explicit "no ACP projection — daemon-internal"; permission-request shapes reference the Phase 5 payload spec.
- **Where:** new `specs/contracts/acp-mapping.md`.
- **Acceptance Criteria:** The table covers every session-scoped event from the catalog (completeness asserted by a doc-lint-style check or an explicit enumerated list reconciled against the catalog headings); completion/stop reasons each map; the six funnels each map; indexed in `specs/INDEX.md`.

### Task 6-4 — TUI view-model mapping + minimal API subsets

- **What:** A TUI mapping note (either a view-model table or the explicit statement that the TUI consumes the daemon `Events` stream directly — decide per ADR-120) and the **minimal daemon-API subsets** each future client needs: TUI (e.g. `Daemon` + `Session` + `Events` + `Terminal` + `Interaction`) and ACP server (e.g. `Daemon` + `Session` + `Events` + `Interaction` + the config read surface), each subset justified by its consumer's duties.
- **How:** Subset tables referencing `specs/contracts/daemon-rpc.md` service/method names; note which RPCs are explicitly excluded for each client (principle-of-least-surface for future reviewers).
- **Where:** `specs/contracts/acp-mapping.md` (ACP subset) + a TUI section in the same doc or `specs/contracts/daemon-rpc.md` (TUI subset — keep both subsets in one place for discoverability).
- **Acceptance Criteria:** Both subsets documented with method-level granularity; no subset includes a client-native or clearly-unnecessary RPC; `specs/INDEX.md` lists the new doc.

### Phase 6 exit gate

`make build && make lint && make test && make vulncheck` green (docs-dominant PR; any code touch is tag annotations); catalog versioned; mappings complete and indexed.

---

## 14. End-state and completion criteria

When all nine phases merge:

- One daemon (`c0wrk serve`) owns the engine, SQLite DB, PTYs, HITL state and single-instance; the GUI runs on it as a thin Wails client with an **unchanged frontend**.
- `backend/appbuild` + `HostServices` is the single composition root for every host; `backend/interaction` owns all six HITL funnels, the judge, and the policy ladder; every global event originates at its source or is documented client-native.
- The gRPC contract is generated, pinned, drift-checked, documented (`daemon-rpc.md`) and versioned (`event-catalog.md` v1 + `acp-mapping.md`).
- The layering rule holds everywhere: zero Wails imports under `backend/`/`core/` (guarded by a test), `desktop/` = lifecycle + bindings + OS-shell (W3 sun-setted), paths centralized.
- Nothing regressed: every phase's gate ran green, the desktop behavior is unchanged, and the TUI/ACP follow-ups can start from a frozen, documented contract.

**Definition of done for the epic (this roadmap):** all nine phase PRs merged, each satisfying its phase exit gate, plus the Phase 4 manual verification checklist recorded green.

---

## Appendix A — Verification commands (run per phase)

```sh
make build && make lint && make test && make vulncheck   # the per-phase gate (ground rule 2)
go test ./backend/appbuild/... -count=1 -v               # assembler fake-host tests (Phases 1a+)
go test ./backend/interaction/... -count=1 -v            # funnels/judge/policy without Wails (1b, 5)
go test ./internal/layerguard/... -count=1               # zero-Wails invariant guard (1a+)
make proto && git diff --exit-code proto/                # codegen drift (Phase 2+)
make genproxy-check                                      # proxy drift (Phase 4+)
go run . serve &                                         # manual daemon smoke (Phase 3+)
```

## Appendix B — Issue → phase index

| Phase | Issue | Title | Depends on |
| --- | --- | --- | --- |
| 0 | [#73](https://github.com/v0lka/c0wrk/issues/73) | ADR: daemon topology, gRPC transport, headless permission-policy ladder, client API split | — |
| 1a | [#74](https://github.com/v0lka/c0wrk/issues/74) | Extract the composition root into a transport-neutral assembler with a HostServices seam | 0 |
| 1b | [#75](https://github.com/v0lka/c0wrk/issues/75) | Extract the HITL funnels and the LLM judge into a transport-neutral interaction service | 0, 1a |
| 1c | [#76](https://github.com/v0lka/c0wrk/issues/76) | Emit global (non-session) events from their source instead of desktop | 0, 1a |
| 2 | [#77](https://github.com/v0lka/c0wrk/issues/77) | Define the daemon gRPC contract (.proto) and add the codegen pipeline | 0, 1a |
| 3 | [#79](https://github.com/v0lka/c0wrk/issues/79) | Implement the c0wrk daemon (`c0wrk serve`): headless host, single-instance lock, lifecycle | 0, 1a, 1b, 1c, 2 |
| 4 | [#80](https://github.com/v0lka/c0wrk/issues/80) | Migrate the GUI onto the daemon (Wails shell as a gRPC client) | 3 |
| 5 | [#81](https://github.com/v0lka/c0wrk/issues/81) | Headless runtime: permission-policy ladder with a fail-closed default (ACP-ready) | 1b, 3 |
| 6 | [#83](https://github.com/v0lka/c0wrk/issues/83) | Version the event contract and add ACP/TUI mapping tables + minimal daemon API subsets | 0, 2 (parallel to 3–5) |
