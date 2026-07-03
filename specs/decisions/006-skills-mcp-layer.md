# ADR-006: Skills and MCP Remain in Core Layer

## Status

**Superseded** (2026-07-03). Skills (`sdk/skills/`) and MCP gateway (`sdk/tools/mcp/`) have since moved to the `sdk/` layer. The concerns about orchestration coupling were resolved via interface indirection — `sdk/skills` uses context values for per-session activation, and `sdk/tools/mcp` registers through the standard SDK tool registry. `core/` now only *wires* skills and MCP into the orchestration cycle (see `core/builder.go`, `core/builder_mcp.go`). No superseding ADR was written for this move; this status update records the drift.

## Context

The `skills/` and `tools/mcp/` packages currently reside under `core/`, which sits between `sdk/` (generic engine) and `backend/` (app-specific) in the import hierarchy. The question arose (review suggestion S-16) whether these packages would be more reusable if moved to `sdk/`.

Skills depend on:
- Filesystem access (reading skill directories, resolving paths)
- Orchestration context (active skill state per session, router matching)
- Tool registry (registering skill-derived tools at runtime)

MCP Gateway depends on:
- Tool registry (policy-aware registration)
- Config expansion (`ExpandEnvVars`)
- HTTP client with proxy (from builder)
- Schema sanitization (strips auto-injected params)

## Decision

Skills and MCP remain in `core/`:

1. **Skills** require orchestration-level context (per-session activation, router skill matching, workspace-relative path resolution) that doesn't exist in `sdk/`. Moving them would either pull orchestration concerns down into SDK or require complex interface indirection.

2. **MCP Gateway** enforces policy via the core ToolRegistry (which wraps the SDK ToolRegistry with policy enforcement). The gateway registers tools with policy metadata that SDK's simpler registry doesn't model.

3. **SDK remains a reusable engine**: it should not know about filesystem conventions, config formats, or orchestration lifecycle. Skills and MCP are application-level integrations wired by the builder.

## Consequences

- SDK stays lean and independently testable
- Adding a new MCP feature or skill capability touches `core/` files
- If a future consumer needs "skills-like" functionality from SDK alone, an adapter layer would be required
- The `depguard` linter (ADR-002 enforcement) continues to prevent accidental SDK→core imports
