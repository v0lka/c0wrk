# ADR-001: Single Go Module

## Status

Accepted

> c0wrk is a single Go module (`github.com/v0lka/c0wrk`) with one `go.mod` at
> the repository root. sp4rk is consumed as a published external dependency
> (see [ADR-015](015-sp4rk-external-module-dependency.md)); the broader claim
> that "sp4rk is internal to this project" no longer holds, but the
> single-module structure for `core/`, `backend/`, `desktop/` remains in effect.
>
> History: this ADR was briefly superseded by [ADR-014](014-sp4rk-separate-module.md),
> which split sp4rk into an in-repo separate module. ADR-014 was itself
> superseded by [ADR-015](015-sp4rk-external-module-dependency.md), which moved
> sp4rk to its own external repository and restored the single-root-module
> approach for c0wrk.

## Context

The project could be structured as multiple Go modules (one per layer: sp4rk, core, backend, desktop) using `go.work`, or as a single module containing all packages. Multi-module setups provide stronger isolation but introduce complexity with inter-module versioning, replace directives, and CI/CD.

## Decision

Use a single Go module (`github.com/v0lka/c0wrk`) for the entire project. No `go.work` file. All layers are packages within the same module.

## Consequences

**Positive:**

- Simple dependency management — one `go.mod`, one `go.sum`
- Refactoring across layers is a single commit (no cross-module PRs)
- No `replace` directives needed for local development
- `go test ./...` covers the entire codebase
- IDE navigation works seamlessly across all packages

**Negative:**

- Layer boundary violations are only caught by code review and linting conventions (not by the Go module system)
- The module path (`github.com/v0lka/c0wrk`) doesn't match the binary name (`c0wrk-desktop`) — intentional, do not "fix"
- Cannot independently version sp4rk for external consumption (sp4rk is internal to this project)

## Alternatives Considered

**Multi-module with go.work**: Stronger compile-time isolation between layers. Rejected because the overhead of managing module versions and replace directives outweighs the benefit for a single-team project with consistent release cadence.

**Monorepo tool (e.g., Bazel)**: Overkill for the current project size. Go's native tooling is sufficient.
