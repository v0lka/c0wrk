# Specs Index

> **Engine behavior is canonical in sp4rk.** c0wrk's `specs/` cover c0wrk-only wiring (layering, lifecycle, session roots, the policy-enforcing registry wrapper, conductor/delegation tools, persistence, frontend). Engine primitives (Executor, Router, Planner, Reflector, Conductor, Blackboard, Tool/ToolRegistry, LLM Router, compaction, MCP gateway) are documented in [the sp4rk spec set](https://github.com/v0lka/sp4rk/blob/main/specs/INDEX.md) in the sp4rk repository. Engine-related c0wrk specs below cross-reference their canonical sp4rk counterparts.

## Navigation by Task

| If your task involves...                 | Read these specs                                                         |
| ---------------------------------------- | ------------------------------------------------------------------------ |
| Layer boundaries, import rules           | [architecture/layers.md](architecture/layers.md)                         |
| Request lifecycle end-to-end             | [architecture/data-flow.md](architecture/data-flow.md)                   |
| Tool policies, confirmations, judge      | [architecture/security-model.md](architecture/security-model.md)         |
| Prompt injection defense, content wrapping | [architecture/security-model.md](architecture/security-model.md)         |
| Orchestration overview (Conductor pipeline) | [domains/orchestration/README.md](domains/orchestration/README.md)    |
| Conductor (top-level ReAct loop)         | [domains/orchestration/conductor.md](domains/orchestration/conductor.md) |
| Subagent delegation, async, DAG          | [domains/orchestration/delegation.md](domains/orchestration/delegation.md) |
| Routing, complexity classification       | [domains/orchestration/router.md](domains/orchestration/router.md)       |
| ReAct loop, circuit breakers, step limits | [domains/orchestration/executor.md](domains/orchestration/executor.md)  |
| Conductor tool surface (delegate/declare_plan/execute_plan/reflect, goal-mode tools) | [contracts/conductor-tools.md](contracts/conductor-tools.md) |
| Adding/modifying built-in tools          | [domains/tool-system/builtins.md](domains/tool-system/builtins.md)       |
| MCP servers, dynamic tools               | [domains/tool-system/mcp-gateway.md](domains/tool-system/mcp-gateway.md) |
| Tool registry, execution pipeline        | [domains/tool-system/README.md](domains/tool-system/README.md)           |
| External binary deps (rg, uv, markitdown), tool-manager | [domains/tool-manager.md](domains/tool-manager.md)       |
| Supply-chain integrity, pinned tool versions, CVE review before bumping | [domains/tool-manager.md](domains/tool-manager.md) |
| In-app self-update (auto-update), release integrity, rollback | [decisions/023-auto-update.md](decisions/023-auto-update.md) |
| Context window, compaction               | [domains/memory/compaction.md](domains/memory/compaction.md)             |
| Blackboard, facts, persistence           | [domains/memory/blackboard.md](domains/memory/blackboard.md)             |
| LLM providers, model registry, tokens    | [domains/llm-providers.md](domains/llm-providers.md)                     |
| Session create/resume/persist/fork       | [domains/session-lifecycle.md](domains/session-lifecycle.md)             |
| Goal mode (multi-turn objective loop)    | [domains/goal-mode.md](domains/goal-mode.md), [decisions/019-goal-mode.md](decisions/019-goal-mode.md) |
| Small-LLM profile (tuning for small/local models) | [domains/small-llm.md](domains/small-llm.md), [decisions/022-small-llm-profile.md](decisions/022-small-llm-profile.md) |
| File & image attachments (pending → blackboard / content blocks)  | [domains/session-lifecycle.md](domains/session-lifecycle.md), [domains/memory/blackboard.md](domains/memory/blackboard.md) |
| File tree, vector index, workspace       | [domains/workspace.md](domains/workspace.md)                             |
| Auxiliary work directories               | [architecture/security-model.md](architecture/security-model.md), [contracts/desktop-frontend.md](contracts/desktop-frontend.md) (Work Directories section), [domains/frontend/stores.md](domains/frontend/stores.md) (`workDirsStore`) |
| Frontend stores, state management        | [domains/frontend/stores.md](domains/frontend/stores.md)                 |
| Frontend events, streaming               | [domains/frontend/events.md](domains/frontend/events.md)                 |
| Message rendering, display items         | [domains/frontend/rendering.md](domains/frontend/rendering.md)           |
| Code review feature                      | [domains/review.md](domains/review.md)                                   |
| Core-sp4rk interface boundary            | [contracts/core-sp4rk.md](contracts/core-sp4rk.md)                       |
| Backend-Core wiring                      | [contracts/backend-core.md](contracts/backend-core.md)                   |
| Wails bindings, frontend RPC             | [contracts/desktop-frontend.md](contracts/desktop-frontend.md)           |
| Event types, payloads, protocol          | [contracts/event-catalog.md](contracts/event-catalog.md)                 |
| Canonical engine behavior (sp4rk)        | [sp4rk/specs/INDEX.md (GitHub)](https://github.com/v0lka/sp4rk/blob/main/specs/INDEX.md) |
| macOS webview recovery / blank screen after sleep | [decisions/018-macos-webview-recovery.md](decisions/018-macos-webview-recovery.md) |
| "Why was X designed this way?"           | [decisions/](decisions/)                                                 |
| Multi-source AGENTS.md threat model     | [decisions/020-multi-source-agents-md-threat-model.md](decisions/020-multi-source-agents-md-threat-model.md) |
| Subagent Profiles (`.agents/agents`, `#agent-name` mentions) | [decisions/021-subagents.md](decisions/021-subagents.md) |

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
- [security-model.md](architecture/security-model.md) - c0wrk session-root/auto-approval/symlink layer (engine ToolPolicy/judge/untrusted primitives → canonical in [sp4rk: specs/architecture/security-model.md](https://github.com/v0lka/sp4rk/blob/main/specs/architecture/security-model.md))

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
- [session-lifecycle.md](domains/session-lifecycle.md) - Session and task lifecycle
- [goal-mode.md](domains/goal-mode.md) - Goal mode: multi-turn agent-driven loop over a user-approved success condition (derivation → approval → self-eval loop, budgets, anti-spin, pause/resume)
- [small-llm.md](domains/small-llm.md) - Small-LLM profile: master-toggle + four variants (essential-tools narrowing, system-prompt Lite swap, sampling override, loop hardening) for tuning c0wrk to small/local models
- [tool-manager.md](domains/tool-manager.md) - External binary dependency manager (rg/uv/markitdown): pinned-version reconciliation, SHA256 verification, no-auto-update supply-chain guarantee
- [workspace.md](domains/workspace.md) - File tree, vector index, workspace watcher
- [review.md](domains/review.md) - Code review feature (review sessions, diff parsing, hunk/file/general comments, clone-on-fork)

### domains/frontend/

- [README.md](domains/frontend/README.md) - Frontend architecture overview
- [stores.md](domains/frontend/stores.md) - Zustand store catalog
- [events.md](domains/frontend/events.md) - Event subscription and handling
- [rendering.md](domains/frontend/rendering.md) - Message grouping and display pipeline

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
- [010-tool-manager.md](decisions/010-tool-manager.md) - Tool manager for external binary dependencies (rg, uv, markitdown) → Supersedes ADR-004 (ripgrep; git now conditional)
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
- [022-small-llm-profile.md](decisions/022-small-llm-profile.md) - Small-LLM profile: manual master toggle + four independently sub-toggled variants (essential tools, system-prompt Lite, sampling, loop hardening) for tuning c0wrk to small/local models
- [023-auto-update.md](decisions/023-auto-update.md) - Self-update: single-binary re-exec, SHA256-only fail-closed verification, unsigned GitHub-Releases trust anchor, `.old` rollback; threat model for the supply-chain delivery vector (ASI04)
