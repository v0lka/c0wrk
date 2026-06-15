# ADR-008: Backend and Desktop Import SDK/Core Packages Directly

## Status

Accepted (amended)

## Context

ADR-002 confined all SDK imports to `core/`. To expose sdk types to `backend/`, `core/types.go` maintained ~40 type aliases (`type Step = agent.Step`, `type Plan = orchestration.Plan`, etc.). This created a double-hop chain (`sdk → core → backend`), mechanical maintenance burden, and obscured type origins in IDE navigation.

The original rationale (sdk reusability, backend insulation from sdk internals) proved theoretical — sdk IS the engine, not a swappable component. A developer already violated the rule (`backend/session/manager_execution.go` imported `sdk/orchestration` directly), confirming the rule was counterproductive.

## Decision

Backend and desktop may import `sdk/` and `core/` packages directly. Core's sdk type re-exports in `core/types.go` are removed. Genuine core types (`Emitter`, `HandleResult`, `RoutingDecision`, etc.) remain in core.

`backend/types.go` convenience re-exports were removed. The aliases provided no architectural value (they didn't isolate anything — the dependency was real, just obscured) and ~70% were dead code. All consumers now import the source packages directly (`core/tools`, `sdk/agent`).

## Consequences

**Positive:**

- Cleaner code navigation — IDE "go to definition" goes to the actual type in the source package
- ~40 fewer alias declarations to maintain in `core/types.go`
- `backend/types.go` (~180 lines of aliases) removed entirely — no more indirect type lookups
- Breaking Change Checklist shrinks — no more "add alias in core/types.go" step
- Backend and desktop's dependency on sdk/core is explicit and transparent
- All layers above sdk follow the same pattern: import directly from the source package

**Negative:**

- Desktop now has a direct import dependency on sdk and core packages (previously proxied via `backend/types.go`)
- No central registry of which sdk types are used by the UI layer — discoverability relies on IDE search

## Related

- Supersedes [ADR-002](002-sdk-isolation.md)
