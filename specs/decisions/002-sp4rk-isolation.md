# ADR-002: sp4rk Isolation (Confined to Core)

## Status

Superseded by [ADR-008](./008-backend-sp4rk-direct-import.md)

## Context

The sp4rk layer (the `sdk/` directory) contains reusable agent execution primitives (executor, LLM providers, memory, tools). The backend layer (`backend/`) is the application ViewModel. If backend imports sp4rk directly, it creates a coupling web where changes to low-level sp4rk types ripple through the application layer, and sp4rk effectively becomes non-reusable outside this project.

## Decision

All sp4rk imports are confined to the `core/` layer. Backend wraps core without importing sp4rk. Core re-exports sp4rk types via aliases in `core/types.go` so backend can reference them.

## Consequences

**Positive:**

- Backend is insulated from sp4rk internals — sp4rk refactoring doesn't touch backend code
- Clear single point of integration (`core/builder.go` wires all sp4rk components)
- sp4rk remains theoretically reusable (no c0wrk-specific imports)
- Type aliases provide a controlled API surface for backend
- Easier to reason about: "if I'm in backend, I only need to understand core's API"

**Negative:**

- Type aliases in `core/types.go` require maintenance — each new sp4rk type backend needs must be aliased
- Adapter code in core (emitterEventsAdapter, plannerSp4rkAdapter) adds indirection
- IDE "go to definition" on aliased types lands in core/types.go, then requires another jump to sp4rk

## Alternatives Considered

**Allow backend to import sp4rk directly**: Simpler initially, but creates a flat dependency graph where backend couples to both core and sp4rk. Rejected because it removes the layering benefit and makes sp4rk changes riskier.

**Use interfaces at the core-backend boundary instead of type aliases**: Cleaner separation, but much more boilerplate for what are effectively data transfer types (Step, Plan, etc.). Rejected as over-engineering for value types.
