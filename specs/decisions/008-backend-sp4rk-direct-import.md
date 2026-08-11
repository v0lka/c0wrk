# ADR-008: Backend and Desktop Import sp4rk/Core Packages Directly

## Status

Accepted

## Context

ADR-002 confined all sp4rk imports to `core/`. To expose sp4rk types to `backend/`, `core/types.go` maintained ~40 type aliases (`type Step = agent.Step`, `type Plan = orchestration.Plan`, etc.). This created a double-hop chain (sp4rk → core → backend), mechanical maintenance burden, and obscured type origins in IDE navigation.

The original rationale (sp4rk reusability, backend insulation from sp4rk internals) proved theoretical — sp4rk IS the engine, not a swappable component. A developer already violated the rule (`backend/session/manager_execution.go` imported `github.com/v0lka/sp4rk/orchestration` directly), confirming the rule was counterproductive.

## Decision

Backend and desktop may import sp4rk and `core/` packages directly. Core's sp4rk type re-exports in `core/types.go` are removed. Genuine core types (`Emitter`, `HandleResult`, `RoutingDecision`, etc.) remain in core.

`backend/types.go` convenience re-exports were removed. The aliases provided no architectural value (they didn't isolate anything — the dependency was real, just obscured) and ~70% were dead code. All consumers now import the source packages directly (`core/tools`, `github.com/v0lka/sp4rk/agent`).

## Consequences

**Positive:**

- Cleaner code navigation — IDE "go to definition" goes to the actual type in the source package
- ~40 fewer alias declarations to maintain in `core/types.go`
- `backend/types.go` (~180 lines of aliases) removed entirely — no more indirect type lookups
- Breaking Change Checklist shrinks — no more "add alias in core/types.go" step
- Backend and desktop's dependency on sp4rk/core is explicit and transparent
- All layers above sp4rk follow the same pattern: import directly from the source package

**Negative:**

- Desktop now has a direct import dependency on sp4rk and core packages (previously proxied via `backend/types.go`)
- No central registry of which sp4rk types are used by the UI layer — discoverability relies on IDE search

## Related

- Supersedes [ADR-002](002-sp4rk-isolation.md)
