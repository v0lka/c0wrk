# Specs Index

> **Engine behavior is canonical in sp4rk.** c0wrk's `specs/` cover c0wrk-only wiring (layering, lifecycle, session roots, the policy-enforcing registry wrapper, conductor/delegation tools, persistence, frontend). Engine primitives (Executor, Router, Planner, Reflector, Conductor, Blackboard, Tool/ToolRegistry, LLM Router, compaction, MCP gateway) are documented in [the sp4rk spec set](https://github.com/v0lka/sp4rk/blob/main/specs/INDEX.md) in the sp4rk repository. Engine-related c0wrk specs below cross-reference their canonical sp4rk counterparts.

## Navigation by Task

| If your task involves...                 | Read these specs                                                         |
| ---------------------------------------- | ------------------------------------------------------------------------ |
| Layer boundaries, import rules           | [architecture/layers.md](architecture/layers.md)                         |
| Request lifecycle end-to-end             | [architecture/data-flow.md](architecture/data-flow.md)                   |
| Tool policies, confirmations, judge      | [architecture/security-model.md](architecture/security-model.md), [decisions/026-smart-approve-unified-funnel.md](decisions/026-smart-approve-unified-funnel.md), [decisions/028-session-pinned-judge.md](decisions/028-session-pinned-judge.md)         |
| Shell path extraction, command substitution, symlink/containment false positives | [architecture/security-model.md](architecture/security-model.md), [decisions/038-validated-command-substitution.md](decisions/038-validated-command-substitution.md) |
| Prompt injection defense, content wrapping | [architecture/security-model.md](architecture/security-model.md)         |
| Orchestration overview (Conductor pipeline) | [domains/orchestration/README.md](domains/orchestration/README.md)    |
| Conductor (top-level ReAct loop)         | [domains/orchestration/conductor.md](domains/orchestration/conductor.md) |
| Subagent delegation, async, DAG          | [domains/orchestration/delegation.md](domains/orchestration/delegation.md) |
| Routing, complexity classification       | [domains/orchestration/router.md](domains/orchestration/router.md)       |
| ReAct loop, circuit breakers, step limits | [domains/orchestration/executor.md](domains/orchestration/executor.md)  |
| Conductor tool surface (delegate/declare_plan/execute_plan/reflect, goal-mode tools) | [contracts/conductor-tools.md](contracts/conductor-tools.md) |
| Plan step dependencies (`depends_on`, ordered plans, execution-wave echo, `Depends on:` in the approve-markdown) | [contracts/conductor-tools.md](contracts/conductor-tools.md) (`declare_plan`), [decisions/047-plan-step-dependencies.md](decisions/047-plan-step-dependencies.md) |
| Adding/modifying built-in tools          | [domains/tool-system/builtins.md](domains/tool-system/builtins.md)       |
| MCP servers, dynamic tools               | [domains/tool-system/mcp-gateway.md](domains/tool-system/mcp-gateway.md) |
| Tool registry, execution pipeline        | [domains/tool-system/README.md](domains/tool-system/README.md)           |
| External binary deps (rg, uv, markitdown), tool-manager | [domains/tool-manager.md](domains/tool-manager.md), [decisions/010-tool-manager.md](decisions/010-tool-manager.md), [decisions/032-offline-first-tool-reconciliation.md](decisions/032-offline-first-tool-reconciliation.md) |
| Supply-chain integrity, pinned tool versions, CVE review before bumping | [domains/tool-manager.md](domains/tool-manager.md) |
| In-app self-update (auto-update), release integrity, rollback | [decisions/023-auto-update.md](decisions/023-auto-update.md) |
| Context window, compaction               | [domains/memory/compaction.md](domains/memory/compaction.md)             |
| Blackboard, facts, persistence           | [domains/memory/blackboard.md](domains/memory/blackboard.md)             |
| LLM providers, model registry, tokens    | [domains/llm-providers.md](domains/llm-providers.md)                     |
| Session create/resume/persist/fork       | [domains/session-lifecycle.md](domains/session-lifecycle.md), [decisions/030-session-context-restore.md](decisions/030-session-context-restore.md) |
| Orchestration recovery (interrupted delegate relaunch, verifier pause, errored goal turn) | [decisions/048-unified-recovery-ledger.md](decisions/048-unified-recovery-ledger.md), [domains/orchestration/delegation.md](domains/orchestration/delegation.md), [domains/goal-mode.md](domains/goal-mode.md), [domains/session-lifecycle.md](domains/session-lifecycle.md) |
| Crash/exit diagnostics (panic capture, unclean-shutdown detection, why the app quit) | [domains/crash-logging.md](domains/crash-logging.md) |
| Linux webview GPU policy (`C0WRK_WEBVIEW_GPU_POLICY` env override, Wails enum/C mismatch compensation) | [domains/webview-gpu-policy.md](domains/webview-gpu-policy.md) |
| Goal mode (multi-turn objective loop)    | [domains/goal-mode.md](domains/goal-mode.md), [decisions/019-goal-mode.md](decisions/019-goal-mode.md), [decisions/042-goal-mode-slm-essential-tools-gate.md](decisions/042-goal-mode-slm-essential-tools-gate.md) |
| E2S mode (explicit execution state, bounded-context loop) | [domains/e2s.md](domains/e2s.md), [decisions/039-e2s-explicit-execution-state.md](decisions/039-e2s-explicit-execution-state.md), [decisions/040-e2s-stabilization.md](decisions/040-e2s-stabilization.md) |
| Model Profiles (tuning for local / mid-size models) | [domains/model-profiles.md](domains/model-profiles.md), [decisions/041-slm-profiles.md](decisions/041-slm-profiles.md), [decisions/022-small-llm-profile.md](decisions/022-small-llm-profile.md), [decisions/042-goal-mode-slm-essential-tools-gate.md](decisions/042-goal-mode-slm-essential-tools-gate.md), [decisions/043-model-profiles-rename.md](decisions/043-model-profiles-rename.md), [decisions/044-model-profiles-out-of-experimental.md](decisions/044-model-profiles-out-of-experimental.md) |
| Test environment conventions (LC_ALL=C for locale-sensitive tests, isolated $HOME for whole-app test runs) | [domains/testing.md](domains/testing.md) |
| File & image attachments (pending → blackboard / content blocks), vision-assisted document conversion & its egress  | [domains/session-lifecycle.md](domains/session-lifecycle.md), [domains/memory/blackboard.md](domains/memory/blackboard.md) |
| File tree, vector index, workspace       | [domains/workspace.md](domains/workspace.md)                             |
| Vector-index memory program (GC soft limit, content-less index, park budget, RSS indicator) | [decisions/049-vector-index-memory-reduction.md](decisions/049-vector-index-memory-reduction.md), [domains/workspace.md](domains/workspace.md) |
| Themes (built-in + custom CSS import)    | [domains/theming.md](domains/theming.md)                                 |
| Embedding execution provider (GPU/CUDA), `fetch-onnx-gpu` packaging, embedding fallback semantics, GPU release artifact & flavor-pinned updates | [decisions/045-gpu-embedding-provider.md](decisions/045-gpu-embedding-provider.md), [decisions/046-cuda-release-artifact.md](decisions/046-cuda-release-artifact.md), [domains/workspace.md](domains/workspace.md) (Embedding Execution Provider section) |
| Git subprocess hardening (untrusted repos, `.git/config` vectors, hooks/filters neutralization, `.git` write gate) + user trust/harden opt-out (snapshot-bound, recheck-with-diff, fail-closed) | [decisions/033-git-subprocess-hardening.md](decisions/033-git-subprocess-hardening.md), [decisions/034-git-trust-opt-out.md](decisions/034-git-trust-opt-out.md), [architecture/security-model.md](architecture/security-model.md) (Git Subprocess Hardening), [domains/workspace.md](domains/workspace.md) |
| Git auto-fetch (background `git fetch`, switch/ticker/focus triggers) | [domains/git-auto-fetch.md](domains/git-auto-fetch.md)             |
| Auxiliary work directories               | [architecture/security-model.md](architecture/security-model.md), [contracts/desktop-frontend.md](contracts/desktop-frontend.md) (Work Directories section), [domains/frontend/stores.md](domains/frontend/stores.md) (`workDirsStore`) |
| Frontend stores, state management        | [domains/frontend/stores.md](domains/frontend/stores.md)                 |
| UI scale / zoom-safe sizing & popover placement | [domains/frontend/ui-scale.md](domains/frontend/ui-scale.md)       |
| Frontend events, streaming               | [domains/frontend/events.md](domains/frontend/events.md)                 |
| Message rendering, display items         | [domains/frontend/rendering.md](domains/frontend/rendering.md)           |
| Sound notifications, audio reliability   | [domains/frontend/sound-notifications.md](domains/frontend/sound-notifications.md) |
| Code review feature                      | [domains/review.md](domains/review.md)                                   |
| RESEARCH mode (toggle, research skills seeding, hypothesis graph) | [domains/research.md](domains/research.md), [contracts/desktop-frontend.md](contracts/desktop-frontend.md) (Research section), [contracts/event-catalog.md](contracts/event-catalog.md) (`research:*` events) |
| Core-sp4rk interface boundary            | [contracts/core-sp4rk.md](contracts/core-sp4rk.md)                       |
| Backend-Core wiring                      | [contracts/backend-core.md](contracts/backend-core.md)                   |
| Wails bindings, frontend RPC             | [contracts/desktop-frontend.md](contracts/desktop-frontend.md)           |
| Event types, payloads, protocol          | [contracts/event-catalog.md](contracts/event-catalog.md)                 |
| Canonical engine behavior (sp4rk)        | [sp4rk/specs/INDEX.md (GitHub)](https://github.com/v0lka/sp4rk/blob/main/specs/INDEX.md) |
| macOS webview recovery / blank screen after sleep | [decisions/018-macos-webview-recovery.md](decisions/018-macos-webview-recovery.md) |
| "Why was X designed this way?"           | [decisions/](decisions/)                                                 |
| Multi-source AGENTS.md threat model     | [decisions/020-multi-source-agents-md-threat-model.md](decisions/020-multi-source-agents-md-threat-model.md) |
| Subagent Profiles (`.agents/agents`, `#agent-name` mentions, profile-required skills) | [decisions/021-subagents.md](decisions/021-subagents.md), [decisions/037-agent-profile-skills.md](decisions/037-agent-profile-skills.md) |
| Tool-capability group policies (`security.groups`, group tool budgets) | [decisions/024-group-policies.md](decisions/024-group-policies.md), [decisions/026-smart-approve-unified-funnel.md](decisions/026-smart-approve-unified-funnel.md), [architecture/security-model.md](architecture/security-model.md) |
| Dual-repo dev flow (unpublished sp4rk APIs, repo-root `go.work`, pin-advance release step) | [decisions/031-gowork-repo-root.md](decisions/031-gowork-repo-root.md) |

## Domain Dependency Graph

```
                ┌──────────┐
                │ frontend │
                └────┬─────┘
                     │ Wails events + RPC
                     ▼
┌──────────┐    ┌──────────┐    ┌──────────┐    ┌───────┐
│ desktop  │───▶│ backend  │───▶│   core   │───▶│ sp4rk │
└──────────┘    └────┬─────┘    └──────────┘    └───────┘
                     │ direct import               ▲
                     ▼                             │
                   ┌───────┐                       │ separate repository
                   │ sp4rk │  github.com/v0lka/sp4rk (external module)
                   └───┬───┘───────────────────────┘
                       │ require github.com/v0lka/sp4rk (no replace)
```

Import rule: each arrow is one-way. `backend` imports `core` AND sp4rk directly (per ADR-008). `core` remains the primary sp4rk consumer. sp4rk is a separate Go module (`github.com/v0lka/sp4rk`) living in its [own repository](https://github.com/v0lka/sp4rk) (per ADR-015); the root module depends on it as a normal external dependency (`require github.com/v0lka/sp4rk`, no `replace` directive).

## Spec Workflow and Format Reference

See [META.md](META.md) for document templates, naming rules, and update protocol.

## Directory Listing

### architecture/

- [layers.md](architecture/layers.md) - Layer hierarchy, import rules, responsibilities
- [data-flow.md](architecture/data-flow.md) - Request lifecycle, event flow, config flow
- [security-model.md](architecture/security-model.md) - c0wrk session-root/auto-approval/symlink layer + git subprocess hardening (engine ToolPolicy/judge/untrusted primitives → canonical in [sp4rk: specs/architecture/security-model.md](https://github.com/v0lka/sp4rk/blob/main/specs/architecture/security-model.md))

### domains/orchestration/

- [README.md](domains/orchestration/README.md) - c0wrk orchestration overview (Conductor pipeline, HandleMessage flow)
- [conductor.md](domains/orchestration/conductor.md) - c0wrk Conductor wiring (system prompt, tool set, inline step lifecycle)
- [delegation.md](domains/orchestration/delegation.md) - `delegate`/`cancel_delegation` tools, Delegation Registry, DAG
- [router.md](domains/orchestration/router.md) - c0wrk router adapter + routing policy (classification → canonical in [sp4rk: specs/domains/orchestration/router.md](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/router.md))
- [executor.md](domains/orchestration/executor.md) - c0wrk Executor integration (AddNonCacheableTools, callers; loop internals → canonical in [sp4rk: specs/domains/orchestration/executor.md](https://github.com/v0lka/sp4rk/blob/main/specs/domains/orchestration/executor.md))

### domains/tool-system/

- [README.md](domains/tool-system/README.md) - c0wrk policy-enforcing registry wrapper (two-layer registry)
- [builtins.md](domains/tool-system/builtins.md) - Tool catalog, registration, `ask_user`, tool-manager wiring
- [mcp-gateway.md](domains/tool-system/mcp-gateway.md) - c0wrk MCP wiring (Gateway/Server/mcp.Tool → canonical in [sp4rk: specs/domains/tool-system/mcp-gateway.md](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md))

### domains/memory/

- [README.md](domains/memory/README.md) - c0wrk context wiring (domain→strategy, config)
- [compaction.md](domains/memory/compaction.md) - c0wrk strategy selection and config
- [blackboard.md](domains/memory/blackboard.md) - c0wrk blackboard persistence/restore (interface/adapters → canonical in [sp4rk: specs/domains/memory/blackboard.md](https://github.com/v0lka/sp4rk/blob/main/specs/domains/memory/blackboard.md))

### domains/ (single-file)

- [llm-providers.md](domains/llm-providers.md) - Thin c0wrk wiring note (provider config → core/builder → sp4rk Router)
- [crash-logging.md](domains/crash-logging.md) - Crash & exit diagnostics: fd-level stderr capture (panic/signal dumps), liveness marker, unclean-shutdown detection at next start, visible shutdown records
- [webview-gpu-policy.md](domains/webview-gpu-policy.md) - Linux webview hardware-acceleration policy: `C0WRK_WEBVIEW_GPU_POLICY` env override (`always`/`on-demand`/`never`, default `never`), explicit never pinning vs Wails' nil-options workaround (wails#2977), and the Go-enum/C-layer mismatch compensation for the pinned Wails v2.15.0 with numeric test pins
- [git-auto-fetch.md](domains/git-auto-fetch.md) - Automatic background git fetch: three triggers (project switch incl. app startup, periodic ticker, window focus) through one gated quiet-failure funnel; config `git.auto_fetch` / `git.auto_fetch_interval`
- [session-lifecycle.md](domains/session-lifecycle.md) - Session and task lifecycle
- [goal-mode.md](domains/goal-mode.md) - Goal mode: multi-turn agent-driven loop over a user-approved success condition (derivation → approval → self-eval loop, budgets, anti-spin, pause/resume)
- [e2s.md](domains/e2s.md) - E2S explicit execution state mode: externalized state Σ patched every turn (O(1) context), `e2s_step` meta-tool protocol, hybrid core schema + mutable extensions (null tombstone), cache-on-truncate observations, semantic anti-spin, resumable step-limit checkpoint, per-step persistence/resume, Execution State panel
- [model-profiles.md](domains/model-profiles.md) - model profiles: predefined (read-only) + custom (`~/.c0wrk/model-profiles.yaml`) profile catalog resolved from `model_profiles.enabled` + `model_profiles.active_profile`; five variant sections (essential-tools narrowing, system-prompt Lite swap, sampling override, loop hardening, context management) for tuning c0wrk to local / mid-size models; the manual `model_profiles.enabled` master toggle is the only switch — not gated by the experimental switch ([ADR-044](decisions/044-model-profiles-out-of-experimental.md))
- [testing.md](domains/testing.md) - Test environment conventions: LC_ALL=C for locale-sensitive test suites, isolated $HOME for manual whole-app test runs (stateless `~/.c0wrk/tools/` copy)
- [verify-on-edit.md](domains/verify-on-edit.md) - Verify on edit: user-configured test/linter command runs after successful file edits, output injected as a system observation (edit → verify → result; config-only command, all hard security gates intact)
- [tool-manager.md](domains/tool-manager.md) - External binary dependency manager (rg/uv/markitdown): pinned-version reconciliation, SHA256 verification, no-auto-update supply-chain guarantee
- [workspace.md](domains/workspace.md) - File tree, vector index, workspace watcher; vector-index memory program (content-less index + one-time migration, park byte budget, GC soft limit, RSS indicator — [ADR-049](decisions/049-vector-index-memory-reduction.md))
- [theming.md](domains/theming.md) - Theming: built-in themes + custom CSS theme import (token format, tokenizer-based sanitize-on-import, storage in `~/.c0wrk/themes`, FOUC-free pre-paint apply), annotated example theme and bundled palette themes in [frontend/src/assets/themes/](../frontend/src/assets/themes/)
- [review.md](domains/review.md) - Code review feature (review sessions, diff parsing, hunk/file/general comments, clone-on-fork)
- [research.md](domains/research.md) - Project-scoped research workspace: briefs, versioned methodology skills, hypothesis DAG parsing, metrics, recursive watcher, and incremental frontend graph updates

### domains/frontend/

- [README.md](domains/frontend/README.md) - Frontend architecture overview
- [ui-scale.md](domains/frontend/ui-scale.md) - UI scale feature and the zoom-safety invariant: coordinate spaces (visual vs layout px), percentage/`--ui-vh` sizing, pointer-anchored panel placement, floating-ui compensation, guard tests
- [stores.md](domains/frontend/stores.md) - Zustand store catalog
- [events.md](domains/frontend/events.md) - Event subscription and handling
- [rendering.md](domains/frontend/rendering.md) - Message grouping and display pipeline
- [sound-notifications.md](domains/frontend/sound-notifications.md) - Web Audio notification cues: event→tone pipeline, AudioContext lifecycle, recovery/replacement guarantees

### contracts/

- [core-sp4rk.md](contracts/core-sp4rk.md) - Core layer's consumption of sp4rk interfaces
- [backend-core.md](contracts/backend-core.md) - Backend wrapping of Core
- [desktop-frontend.md](contracts/desktop-frontend.md) - Wails bindings and RPC surface
- [event-catalog.md](contracts/event-catalog.md) - Complete event type reference
- [conductor-tools.md](contracts/conductor-tools.md) - Conductor tool surface (delegate, declare_plan, execute_plan, reflect, cancel_delegation, goal-mode tools)

### decisions/

- [001-single-module.md](decisions/001-single-module.md) - Single Go module design
- [002-sp4rk-isolation.md](decisions/002-sp4rk-isolation.md) - sp4rk imports confined to core → Superseded by ADR-008
- [003-cgo-free-sqlite.md](decisions/003-cgo-free-sqlite.md) - CGO-free SQLite choice
- [004-external-binary-dependencies.md](decisions/004-external-binary-dependencies.md) - git (conditional) and rg as external binary deps → Superseded by ADR-010 (ripgrep; git now conditional)
- [005-bleve-rrf-hybrid-search.md](decisions/005-bleve-rrf-hybrid-search.md) - Bleve BM25 + Reciprocal Rank Fusion hybrid search → Superseded by ADR-013
- [006-skills-mcp-layer.md](decisions/006-skills-mcp-layer.md) - Skills integration with MCP tool layer → Superseded (no successor ADR; reversed by code drift — skills/MCP moved to sp4rk)
- [007-shell-parser-dependency.md](decisions/007-shell-parser-dependency.md) - mvdan.cc/sh shell parser for symlink detection
- [008-backend-sp4rk-direct-import.md](decisions/008-backend-sp4rk-direct-import.md) - Backend allowed to import sp4rk directly → Supersedes ADR-002
- [009-backend-domain-logic-extraction.md](decisions/009-backend-domain-logic-extraction.md) - Extraction of domain logic from App/UI layer → Superseded by ADR-011
- [010-tool-manager.md](decisions/010-tool-manager.md) - Tool manager for external binary dependencies (rg, uv, markitdown) → Supersedes ADR-004 (ripgrep; git now conditional); amended by ADR-032 (point 8 replaced)
- [011-sp4rk-to-core-extraction.md](decisions/011-sp4rk-to-core-extraction.md) - Move vector index and proxy from sp4rk to Core → Supersedes ADR-009
- [012-conductor-orchestration-pipeline.md](decisions/012-conductor-orchestration-pipeline.md) - Conductor-driven ReAct pipeline replacing system-driven plan-execute-reflect
- [013-rrf-pre-fusion-score-thresholds.md](decisions/013-rrf-pre-fusion-score-thresholds.md) - Pre-fusion score thresholds and configurable RRF parameters for hybrid search → Supersedes ADR-005
- [014-sp4rk-separate-module.md](decisions/014-sp4rk-separate-module.md) - sp4rk as a separate Go module → Superseded by ADR-015
- [015-sp4rk-external-module-dependency.md](decisions/015-sp4rk-external-module-dependency.md) - sp4rk as an external module dependency → Supersedes ADR-014
- [016-aiignore.md](decisions/016-aiignore.md) - .gitignore + .aiignore as the ignore source of truth (removes workspace.* config ignores and hardcoded defaults)
- [017-macos-wake-reload.md](decisions/017-macos-wake-reload.md) - Reload frontend on macOS power-state wake → Superseded by ADR-018
- [018-macos-webview-recovery.md](decisions/018-macos-webview-recovery.md) - macOS webview recovery: native process-death hook + deferred wake reload → Supersedes ADR-017 (017-macos-wake-reload)
- [019-goal-mode.md](decisions/019-goal-mode.md) - Goal mode: self-agent + evidence-mandate, derive-then-confirm UX, persist + pause/resume, anti-spin auto-pause
- [020-multi-source-agents-md-threat-model.md](decisions/020-multi-source-agents-md-threat-model.md) - Threat model for global/c0wrk/project AGENTS.md sources (all untrusted advisory; tool-policy pipeline is the hard boundary)
- [021-subagents.md](decisions/021-subagents.md) - Subagent Profiles: `.agents/agents` persona/budget profiles applied at delegation time, `#agent-name` mention routing (parallels skills)
- [022-small-llm-profile.md](decisions/022-small-llm-profile.md) - Model Profiles: manual master toggle + five independently sub-toggled variants (essential tools, system-prompt Lite, sampling, loop hardening, context management) for tuning c0wrk to local / mid-size models → Inline-knob config storage superseded by ADR-041; variant architecture stands
- [023-auto-update.md](decisions/023-auto-update.md) - Self-update: single-binary re-exec, SHA256-only fail-closed verification, unsigned GitHub-Releases trust anchor, `.old` rollback; threat model for the supply-chain delivery vector (ASI04)
- [024-group-policies.md](decisions/024-group-policies.md) - Tool-capability group policies: 8 declared groups (incl. reserved `system`), `security.groups.<group>.{policy,blacklist?}`, hard/soft judge severities, legacy per-tool schema removed
- [025-dual-repo-dev-flow.md](decisions/025-dual-repo-dev-flow.md) - Dual-repo dev flow: mid-cycle c0wrk builds via the parent-dir `go.work` while the `go.mod` sp4rk pin lags unpublished APIs; commit-sp4rk-then-repin is the mandatory release step → Superseded by ADR-031
- [026-smart-approve-unified-funnel.md](decisions/026-smart-approve-unified-funnel.md) - Smart Approve unified confirmation funnel: every escalation routes through one strict judge (hard-bias), with a deterministic backstop that forces confirmation for canonical destructive hard reasons (blacklist, SSRF, symlink escape) even on a strict ALLOW
- [027-linux-arm64-build.md](decisions/027-linux-arm64-build.md) - Linux arm64 release support: platform registry + CI/release matrix + updater asset
- [028-session-pinned-judge.md](decisions/028-session-pinned-judge.md) - Session-pinned strict judge: each session's judge binds to its own router and re-binds only on the session's own model switch; global default changes never re-bind a live session; per-message selector cluster locks while the task runs
- [029-terminal-env-conventions.md](decisions/029-terminal-env-conventions.md) - Embedded terminal env conventions: TERM_PROGRAM=c0wrk marks the in-app terminal so rc files can skip tmux auto-attach; terminal.env config adds user env overrides (win over defaults, ${VAR} expanded at startup)
- [030-session-context-restore.md](decisions/030-session-context-restore.md) - Session context restore: saved-first (selection-time persistence), effective activity (newest event, read-side), archived never auto-selected, restart restores the exact last mode/project/session via app_state
- [031-gowork-repo-root.md](decisions/031-gowork-repo-root.md) - Dual-repo dev flow via repo-root gitignored `go.work` (`use . ../sp4rk`): workspace scoped to the c0wrk subtree instead of leaking into sibling projects → Supersedes ADR-025
- [032-offline-first-tool-reconciliation.md](decisions/032-offline-first-tool-reconciliation.md) - Offline-first tool reconciliation: two-pass startup (local-only sync pass + background network pass), per-tool failure isolation, no destructive ops without secured replacement bytes → Amends ADR-010
- [033-git-subprocess-hardening.md](decisions/033-git-subprocess-hardening.md) - Git subprocess hardening for untrusted repositories: global `-c` baseline on every git process, fresh per-repo config re-scan + neutralization (fail-closed), `project:git_config_risk` intake warning, agent-side `.git` write gate; hooks strip-and-warn, LFS distrusted unconditionally (ASI04/ASI05) → Superseded by ADR-034 (trust/harden opt-out)
- [034-git-trust-opt-out.md](decisions/034-git-trust-opt-out.md) - Git config trust opt-out: trust = raw-git opt-out (snapshot-bound fingerprint + recheck-with-diff + fail-closed eviction), explicit harden list (mutually exclusive with trust), legacy-entry migration
- [035-remove-small-llm-tool-budget.md](decisions/035-remove-small-llm-tool-budget.md) - Removes the Model Profiles Essential Tools `max_tools` slot budget, router tool matching, and the `tools_assigned` / over-budget diagnostics; every other ADR-022 decision stands → Partially supersedes ADR-022
- [036-vector-index-embedding-optimization-policy.md](decisions/036-vector-index-embedding-optimization-policy.md) - Vector-index embedding optimization integration policy: confirmed optimizations on by default (accumulator, embedding cache, content filter, persistent fixed-shape sessions), gated SDK opt-ins (length buckets, batch pipeline) and NO-GO paths (persistence pipelining, parallel sessions) stay off; generic ONNX logic in sp4rk, persistence/cache in c0wrk; default flips require a new benchmark decision
- [037-agent-profile-skills.md](decisions/037-agent-profile-skills.md) - Subagent Profile required skills: optional `skills:` frontmatter validated fail-closed at parse (sp4rk `agents`) and at resolution (c0wrk), activated for the subagent via the existing `ActiveSkills`/`## Active Skills` path (verbatim bodies) + a per-subagent `read_skill_resource` decorator, inherited first on redelegation; no trust-boundary change (skills grant no permissions) → Extends ADR-021
- [038-validated-command-substitution.md](decisions/038-validated-command-substitution.md) - Validated command substitution: an assignment-form `VAR=$(...)` whose inner command is fully assessable (same recursive pipeline) no longer escalates, its name stays assessable-but-not-resolvable, and its inner literal paths are still surfaced; fail-closed union over all bindings, bare `$(...)` in argument position and unbound refs (e.g. `$HOME`) stay suspicious; PowerShell static `$NAME = <literal RHS>` parity (D1–D6); implemented in sp4rk, consumed via repo-root `go.work` (ADR-031); accepted residual risk — substitution stdout is not analyzed
- [039-e2s-explicit-execution-state.md](decisions/039-e2s-explicit-execution-state.md) - E2S explicit execution state: a per-message alternative loop where the model's only memory is a patched external state Σ (fresh one-shot [system, user] per turn, O(1) context; arXiv 2608.26263) — native `e2s_step` meta-tool (GroupSystem; dispatch carries the policy), hybrid fixed-core + add-only-extension schema with fingerprint-guarded persistence, delegate-via-injection with inert plan state, Execution State panel replacing the plan view, experimental gate fail-closed on both sides → add-only extension keys, plain observation truncation, the schema-less tool catalog, and the step-limit terminal mapping superseded by ADR-040
- [040-e2s-stabilization.md](decisions/040-e2s-stabilization.md) - E2S stabilization after the first production post-mortem: mutable extension keys (value writes replace in place, `null` stays the tombstone), cache-on-truncate observations with the shared fragmentation nudge (`tool_result_read` recovery), full input schemas in the Available Tools catalog + pre-dispatch structural validation (`sdktools.ValidateToolInput`), real `batch` execution through the registry path (nested batch/envelope targets rejected fail-closed), `[turn N of M]` budget header with wrap-up directive, semantic anti-spin fingerprint (tool + target anchor args, ignoring line ranges/limits; batch fingerprints per sub-call); step-limit exhaustion is a resumable checkpoint (Σ preserved, fresh turn budget) and `e2s_state` carries run-local `turn` + cumulative `total_turns` → Supersedes those mechanics of ADR-039
- [041-slm-profiles.md](decisions/041-slm-profiles.md) - Model Profiles catalog: research-backed read-only predefined profiles ∪ operator custom profiles (`~/.c0wrk/model-profiles.yaml`) replace the inline `small_llm.*` knobs; `config.yaml` persists only `model_profiles.enabled` + `model_profiles.active_profile` (generic soft fallback, sanctioned reset migration); five profile-scoped RPCs + the `SetModelProfilesEnabled` master-toggle RPC (persisted toggle; enabling gate-checked, closing the experimental gate resets it) + suggested-profile hint (never auto-applied); `agent_metrics` carries profile identity; Model Profiles nomenclature → Partially supersedes ADR-022 → The `SLM` nomenclature, the `slm.*` config keys, the `slm-profiles.yaml` store, the `slm` metrics key and the "Small LLM" tab label are superseded by ADR-043 → The experimental-gate coupling is partially superseded by ADR-044
- [042-goal-mode-slm-essential-tools-gate.md](decisions/042-goal-mode-slm-essential-tools-gate.md) - Goal mode is refused while the Model Profiles `essential_tools` narrowing is active (`goalBlocked = ModelProfiles.Enabled && EssentialTools.Enabled`, the effective values — master-on with the variant off does NOT block): the narrowing applies only to the non-goal Conductor path and the E2S branch (both after goal mode's early return), so it never narrows a goal run — if it did it would hide the goal-loop tooling and make the loop unrunnable — which is why the goal is refused before any side effect; enforced at three layers (frontend gate, session API, orchestrator `ErrGoalBlockedByModelProfiles`) and surfaced via `ConfigResponse.model_profiles`; the goal derivation/verification passes also opt into the profile's `system_prompt` Lite variant
- [043-model-profiles-rename.md](decisions/043-model-profiles-rename.md) - Rename "Small LLM" / `SLM` to **Model Profiles**, applied mechanically across every surface: user-facing (settings tab id `model-profiles`, was `small-llm`), config (`model_profiles.enabled` + `model_profiles.active_profile` and the `~/.c0wrk/model-profiles.yaml` custom-profile store, was `slm.*` / `slm-profiles.yaml`), wire (`agent_metrics.model_profiles`, the frontend guard/normalizer still accepting the legacy `slm`/`small_llm` keys), code (`ModelProfile*`/`ModelProfiles*`, `core/modelprofiles`, `model_profiles*.go`, the six `*ModelProfile*` RPCs) and docs (`specs/domains/model-profiles.md`); a clean break with no value migration (a legacy `slm:` section and an old `slm-profiles.yaml` are ignored, the active profile resolves to `generic`) → Partially supersedes ADR-041 (the `SLM` nomenclature and the "Small LLM" tab label; the catalog architecture stands) → The experimental-gate coupling carried through the rename is partially superseded by ADR-044
- [044-model-profiles-out-of-experimental.md](decisions/044-model-profiles-out-of-experimental.md) - Model Profiles graduated out of the experimental gate: the manual `model_profiles.enabled` master toggle is the feature's only switch (`experimental.enabled` now gates only the E2S execution mode), `ToBuilderConfig` carries the toggle through verbatim, `SetModelProfilesEnabled` allows both directions, and `UpdateExperimentalFeatures` no longer resets or pushes Model Profiles state → Partially supersedes ADR-041 and ADR-043 (the experimental-gate coupling only)
- [045-gpu-embedding-provider.md](decisions/045-gpu-embedding-provider.md) - GPU embedding execution provider: `execution_provider` auto/cpu/cuda with `device_id`, loud (never-silent) CPU fallback tracked at the sp4rk level, all ORT touches on a single locked thread, external `nvidia-smi` PID verification for GPU tests, cuda13-only opt-in packaging (`fetch-onnx-gpu`), flavor-aware version stamps
- [046-cuda-release-artifact.md](decisions/046-cuda-release-artifact.md) - CUDA release artifact: fifth release archive `c0wrk-desktop-linux-amd64-cuda13.tar.gz` (linux/amd64 only), always same-flavor in-app updates (flavor read from the install tree), flavor switches are manual reinstalls → Closes ADR-045's deferred packaging item, extends ADR-023's release matrix
- [047-plan-step-dependencies.md](decisions/047-plan-step-dependencies.md) - Plan step dependencies: a flat plan executes concurrently, so a step with no `depends_on` runs alongside its siblings — three non-blocking levers fix this without a gate. (A) explicit order by default: drop the "DAG of independent steps" framing and require `depends_on` whenever a step consumes another's output/artifacts/decisions (a missing edge is a correctness bug, not a harmless omission), across the Conductor guidance, the `declare_plan` description, and the router/conductor specs. (B) `declare_plan` echoes the Kahn-layered execution waves on every success (`Execution waves: 1=[step_1, step_3] · 2=[step_2]`); in `present` mode only, a multi-step plan that collapses to one wave gets a non-blocking single-wave hint (never for a one-step plan, suppressed in `await_approval`). (C) `SerializePlan` prints `Depends on: <ids>` / `Depends on: (none)` under each step heading in the approve-markdown, so the human reviewer sees the ordering. No plan is rejected or blocked for a missing edge — a legitimate flat plan still proceeds.
- [048-unified-recovery-ledger.md](decisions/048-unified-recovery-ledger.md) - Unified recovery ledger: one durable per-unit record (`core/units`, additive `task_units` table) with one writer (the `conductorLauncher`) and one Resume settlement funnel (`resumeUnits`: paused → relaunch seeded, not-started/running/**interrupted** → relaunch fresh, terminal → replay) plus one frontend read path (`GetSessionRuntimeStatus.work_units` + a `work_unit_settled` event → the `groupMessages` work-unit overlay). Fixes the three reported recovery defects — an interrupted delegate is relaunched instead of dropped and no longer stays a stale `running` block, a cooperative pause inside the goal verifier suspends the request instead of synthesizing a `not_met` rejection, and an errored goal turn (retried `goalTurnMaxErrorRetries` times) surfaces a resumable `ExecutionStatusFailed` with its cause instead of `blocked_idle`/`partial`.
- [049-vector-index-memory-reduction.md](decisions/049-vector-index-memory-reduction.md) - Vector-index memory-reduction program (rationale A+B+C+F): GC soft memory limit (`runtime.memory_soft_limit_mb`, tri-state, AUTO=clamp(50% RAM, 2..8 GiB), GOMEMLIMIT-precedence) + `FreeOSMemory` after full passes/evictions/migration; sidecar entry format v5 with the committed chunk-index set (`idxset`) enabling materialization-free enumeration and embedding-free Browse/RebuildLexical; content-less storage contract (ID+vector+4 position fields; best-effort content reconstruction from source files with a placeholder for missing files) with a one-time marker-gated background migration (`contentless_<collection>.done`, cancel-safe replay); park LRU bounded by estimated bytes (`vector_index.park_budget_mb`, `docs × (dims×4 B + 1 KiB)`, effective bound min(capacity, budget)); live RSS indicator in the status bar (`GetProcessMemory`, 5 s polling). D's metadata dedup delivered via F; E (quantization) and G (store replacement) rejected
- [050-per-provider-tls-pinning.md](decisions/050-per-provider-tls-pinning.md) - Per-provider TLS verification override via SPKI pinning: one config key, `tls_fingerprint` (base64 SHA-256 SPKI pin, Chromium/RFC 7469 style) on both compatible provider families — **the pin is the only switch** (non-empty = only the pinned key is accepted, empty = system CA verification; no toggle, no configured accept-any state). sp4rk `ProviderEntry.HTTPClient` per-entry client override keeps TLS policy in c0wrk; `core/llmtls` mechanics + credential-less Get-fingerprint RPC; all four dial paths (router, Fetch Models, lazy probe, draft) wrapped consistently; pointer-sentinel round-trip (`TLSFingerprint *string` at the API boundary only; nil = keep, `""` = clear) keeps debounced partial saves from dropping the pin → The pin-vs-proxy interaction is settled by ADR-051 (proxy wins)
- [051-proxy-wins-over-tls-pin.md](decisions/051-proxy-wins-over-tls-pin.md) - Proxy wins over the per-provider TLS pin (amends ADR-050): with an effective proxy (`enabled` AND a non-empty `url`, the `proxy.BuildTransport` rule) the pin is ignored on every dial path — `llmtls.ResolveProviderClient` returns the plain proxy client verbatim, router entries carry no per-entry client, and the Get-fingerprint RPC rejects with an actionable error; the Settings UI disables the whole TLS section with an explanatory comment while the proxy is active; `proxy.bypass_list` is the documented route for running both at once (bypassed host → direct dial → pin applies). Without a proxy, ADR-050 semantics are unchanged.
