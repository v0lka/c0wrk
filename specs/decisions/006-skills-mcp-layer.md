# ADR-006: Skills and MCP Remain in Core Layer

## Status

**Superseded** (2026-07-03). Skills and the MCP gateway have since moved to sp4rk (the `sdk/` directory) — see `github.com/v0lka/sp4rk/skills` and `github.com/v0lka/sp4rk/tools/mcp`. The concerns about orchestration coupling were resolved via interface indirection — sp4rk's skills package uses context values for per-session activation, and sp4rk's tools/mcp registers through the standard sp4rk tool registry. `core/` now only *wires* skills and MCP into the orchestration cycle (see `core/builder.go`, `core/builder_mcp.go`). No superseding ADR was written for this move; this status update records the drift.

> **Related:** the canonical, sp4rk-native version of this decision now lives in [sp4rk: specs/decisions/002-skills-mcp-in-sdk.md](https://github.com/v0lka/sp4rk/blob/main/specs/decisions/002-skills-mcp-in-sdk.md). This c0wrk ADR is retained as historical decision history.

## Context

The `skills/` and `tools/mcp/` packages currently reside under `core/`, which sits between sp4rk (the `sdk/` directory, generic engine) and `backend/` (app-specific) in the import hierarchy. The question arose (review suggestion S-16) whether these packages would be more reusable if moved to sp4rk.

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

1. **Skills** require orchestration-level context (per-session activation, router skill matching, workspace-relative path resolution) that doesn't exist in sp4rk. Moving them would either pull orchestration concerns down into sp4rk or require complex interface indirection.

2. **MCP Gateway** enforces policy via the core ToolRegistry (which wraps the sp4rk ToolRegistry with policy enforcement). The gateway registers tools with policy metadata that sp4rk's simpler registry doesn't model.

3. **sp4rk remains a reusable engine**: it should not know about filesystem conventions, config formats, or orchestration lifecycle. Skills and MCP are application-level integrations wired by the builder.

## Consequences

- sp4rk stays lean and independently testable
- Adding a new MCP feature or skill capability touches `core/` files
- If a future consumer needs "skills-like" functionality from sp4rk alone, an adapter layer would be required
- The `depguard` linter (ADR-002 enforcement) continues to prevent accidental sp4rk→core imports
