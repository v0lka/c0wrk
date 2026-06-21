# Specs Index

## Navigation by Task

| If your task involves...                 | Read these specs                                                         |
| ---------------------------------------- | ------------------------------------------------------------------------ |
| Layer boundaries, import rules           | [architecture/layers.md](architecture/layers.md)                         |
| Request lifecycle end-to-end             | [architecture/data-flow.md](architecture/data-flow.md)                   |
| Tool policies, confirmations, judge      | [architecture/security-model.md](architecture/security-model.md)         |
| Prompt injection defense, content wrapping | [architecture/security-model.md](architecture/security-model.md)         |
| Routing, complexity classification       | [domains/orchestration/router.md](domains/orchestration/router.md)       |
| Plan generation, DAG, replan             | [domains/orchestration/planner.md](domains/orchestration/planner.md)     |
| Agent loop, step limits, circuit breaker | [domains/orchestration/executor.md](domains/orchestration/executor.md)   |
| Orchestration overview (full cycle)      | [domains/orchestration/README.md](domains/orchestration/README.md)       |
| Adding/modifying built-in tools          | [domains/tool-system/builtins.md](domains/tool-system/builtins.md)       |
| MCP servers, dynamic tools               | [domains/tool-system/mcp-gateway.md](domains/tool-system/mcp-gateway.md) |
| Tool registry, execution pipeline        | [domains/tool-system/README.md](domains/tool-system/README.md)           |
| Context window, compaction               | [domains/memory/compaction.md](domains/memory/compaction.md)             |
| Blackboard, facts, persistence           | [domains/memory/blackboard.md](domains/memory/blackboard.md)             |
| LLM providers, model registry, tokens    | [domains/llm-providers.md](domains/llm-providers.md)                     |
| Session create/resume/persist            | [domains/session-lifecycle.md](domains/session-lifecycle.md)             |
| Plan review, user approval, replan with feedback | [domains/session-lifecycle.md](domains/session-lifecycle.md) + [domains/orchestration/planner.md](domains/orchestration/planner.md) |
| File tree, vector index, workspace       | [domains/workspace.md](domains/workspace.md)                             |
| Frontend stores, state management        | [domains/frontend/stores.md](domains/frontend/stores.md)                 |
| Frontend events, streaming               | [domains/frontend/events.md](domains/frontend/events.md)                 |
| Message rendering, display items         | [domains/frontend/rendering.md](domains/frontend/rendering.md)           |
| Core-SDK interface boundary              | [contracts/core-sdk.md](contracts/core-sdk.md)                           |
| Backend-Core wiring                      | [contracts/backend-core.md](contracts/backend-core.md)                   |
| Wails bindings, frontend RPC             | [contracts/desktop-frontend.md](contracts/desktop-frontend.md)           |
| Event types, payloads, protocol          | [contracts/event-catalog.md](contracts/event-catalog.md)                 |
| "Why was X designed this way?"           | [decisions/](decisions/)                                                 |

## Domain Dependency Graph

```
                ┌──────────┐
                │ frontend │
                └────┬─────┘
                     │ Wails events + RPC
                     ▼
┌──────────┐    ┌──────────┐    ┌──────────┐    ┌─────┐
│ desktop  │───▶│ backend  │───▶│   core   │───▶│ sdk │
└──────────┘    └──────────┘    └──────────┘    └─────┘
```

Import rule: each arrow is one-way. `backend` imports `core` AND `sdk`. `core` remains the primary sdk consumer. See ADR-008.

## Spec Workflow and Format Reference

See [META.md](META.md) for document templates, naming rules, and update protocol.

## Directory Listing

### architecture/

- [layers.md](architecture/layers.md) - Layer hierarchy, import rules, responsibilities
- [data-flow.md](architecture/data-flow.md) - Request lifecycle, event flow, config flow
- [security-model.md](architecture/security-model.md) - Tool policies, judge, confirmations

### domains/orchestration/

- [README.md](domains/orchestration/README.md) - Orchestration cycle overview
- [router.md](domains/orchestration/router.md) - Request classification and skill matching
- [planner.md](domains/orchestration/planner.md) - DAG plan generation and replan
- [executor.md](domains/orchestration/executor.md) - ReAct loop, circuit breakers, step config

### domains/tool-system/

- [README.md](domains/tool-system/README.md) - Tool registry and execution pipeline
- [builtins.md](domains/tool-system/builtins.md) - Built-in tool catalog and extension guide
- [mcp-gateway.md](domains/tool-system/mcp-gateway.md) - MCP server lifecycle and dynamic tools

### domains/memory/

- [README.md](domains/memory/README.md) - Context management overview
- [compaction.md](domains/memory/compaction.md) - Compaction strategies and thresholds
- [blackboard.md](domains/memory/blackboard.md) - Shared state, facts, persistence

### domains/ (single-file)

- [llm-providers.md](domains/llm-providers.md) - Provider abstraction, routing, token counting
- [session-lifecycle.md](domains/session-lifecycle.md) - Session and task lifecycle
- [workspace.md](domains/workspace.md) - File tree, vector index, workspace watcher

### domains/frontend/

- [README.md](domains/frontend/README.md) - Frontend architecture overview
- [stores.md](domains/frontend/stores.md) - Zustand store catalog
- [events.md](domains/frontend/events.md) - Event subscription and handling
- [rendering.md](domains/frontend/rendering.md) - Message grouping and display pipeline

### contracts/

- [core-sdk.md](contracts/core-sdk.md) - Core layer's consumption of SDK interfaces
- [backend-core.md](contracts/backend-core.md) - Backend wrapping of Core
- [desktop-frontend.md](contracts/desktop-frontend.md) - Wails bindings and RPC surface
- [event-catalog.md](contracts/event-catalog.md) - Complete event type reference

### decisions/

- [001-single-module.md](decisions/001-single-module.md) - Single Go module design
- [002-sdk-isolation.md](decisions/002-sdk-isolation.md) - SDK imports confined to core → Superseded by ADR-008
- [003-cgo-free-sqlite.md](decisions/003-cgo-free-sqlite.md) - CGO-free SQLite choice
- [004-external-binary-dependencies.md](decisions/004-external-binary-dependencies.md) - git and rg as hard runtime dependencies
- [005-bleve-rrf-hybrid-search.md](decisions/005-bleve-rrf-hybrid-search.md) - Bleve BM25 + Reciprocal Rank Fusion hybrid search
- [006-skills-mcp-layer.md](decisions/006-skills-mcp-layer.md) - Skills integration with MCP tool layer
- [007-shell-parser-dependency.md](decisions/007-shell-parser-dependency.md) - mvdan.cc/sh shell parser for symlink detection
- [008-backend-sdk-direct-import.md](decisions/008-backend-sdk-direct-import.md) - Backend allowed to import sdk directly
- [009-backend-domain-logic-extraction.md](decisions/009-backend-domain-logic-extraction.md) - Extraction of domain logic from App/UI layer
- [010-tool-manager.md](decisions/010-tool-manager.md) - Tool manager for external binary dependencies (rg, rtk, uv, markitdown)
