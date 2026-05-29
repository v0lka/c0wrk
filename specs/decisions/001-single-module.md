# ADR-001: Single Go Module

## Status

Accepted

## Context

The project could be structured as multiple Go modules (one per layer: sdk, core, backend, desktop) using `go.work`, or as a single module containing all packages. Multi-module setups provide stronger isolation but introduce complexity with inter-module versioning, replace directives, and CI/CD.

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
- Cannot independently version sdk for external consumption (sdk is internal to this project)

## Alternatives Considered

**Multi-module with go.work**: Stronger compile-time isolation between layers. Rejected because the overhead of managing module versions and replace directives outweighs the benefit for a single-team project with consistent release cadence.

**Monorepo tool (e.g., Bazel)**: Overkill for the current project size. Go's native tooling is sufficient.
