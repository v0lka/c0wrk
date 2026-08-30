# ADR-031: Repo-Root go.work for Dual-Repo Development

## Status

Accepted

## Context

[ADR-025](025-dual-repo-dev-flow.md) established the dual-repo development flow (c0wrk consumes unpublished sp4rk APIs mid-cycle while `go.mod` pins the last published commit) and placed the bridging workspace file in the repositories' **shared parent directory** (`Repositories/go.work`).

That placement has a harmful side effect: a `go.work` applies to the entire directory tree beneath it, so **every sibling project** under `Repositories/` (dozens of unrelated Go checkouts) silently built inside the c0wrk/sp4rk workspace — wrong module resolution and surprising `go` command behavior outside this project.

The forces from ADR-025 are unchanged: no committed `go.work` (ADR-015), no `replace` directive, pinned `require` in `go.mod`.

## Decision

Approved by the repository owner (2026-08-30): during cross-repo development cycles, the local workspace file lives at the **c0wrk repository root** (`c0wrk/go.work`, with `go.work.sum` beside it). Both files are covered by the existing `.gitignore` entries (`go.work`, `go.work.sum`), so the workspace remains a local-development tool that is never committed — ADR-015's repository-content rules stay intact.

```go
go 1.26.3

use (
	.
	../sp4rk
)
```

Everything else from ADR-025 carries over unchanged:

- The `go.mod` pin lags the sp4rk working tree **by design** until the release step; a `// mid-cycle` note at the top of `go.mod` marks the state.
- Publishing checklist: commit+push sp4rk → `GOWORK=off go get github.com/v0lka/sp4rk@main && go mod tidy` → verify `GOWORK=off go build ./...` → pre-PR gates (`make build` / `make lint` / `make test`) → commit+push c0wrk.
- CI on c0wrk `main` is green only at published-pin points; mid-cycle pushes are expected to fail and must be avoided.

Scope note: `go` commands run inside the sp4rk checkout no longer see the workspace — sp4rk builds there as a standalone module. This is safe (sp4rk has no dependency on c0wrk) and arguably cleaner. If a combined view is ever needed from the sp4rk side, set `GOWORK=<path-to-c0wrk>/go.work` explicitly.

## Consequences

**Positive:**

- Workspace effect is scoped to the c0wrk subtree; sibling projects under `Repositories/` are no longer affected.
- Editors/gopls opened at the c0wrk root pick up the workspace automatically (a `go.work` at the opened root is the best-supported layout).
- No `replace` directive and no committed `go.work` — ADR-015's repository-content rules remain intact.
- sp4rk's own checkout behaves as a plain standalone module again.

**Negative:**

- Running `go` commands from the sp4rk checkout loses the combined-workspace view previously provided by the parent `go.work`; mitigated by an explicit `GOWORK` when needed.
- (Carried over from ADR-025) c0wrk is not buildable from `go.mod` alone mid-cycle; CI is red for mid-cycle pushes — the pin advance remains a mandatory release step.

## Alternatives Considered

**Keep the parent-directory `go.work`**: rejected — it leaks the workspace into every sibling project under `Repositories/`, which is the problem this ADR fixes.

**`GOWORK` environment variable pointing at a workspace file stored elsewhere**: rejected as the default — global mutable env state that is easy to forget and diverges per shell/editor. A file at the repo root is self-describing and works with plain `go` commands and editors.

**`replace github.com/v0lka/sp4rk => ../sp4rk` in `go.mod`**: rejected — ADR-015/ADR-001 forbid it; it breaks CI and any checkout without the sibling directory.

## Related

- Supersedes [ADR-025](025-dual-repo-dev-flow.md) (introduced the dual-repo flow with a parent-directory `go.work`); only the workspace-file location changes.
- [ADR-015](015-sp4rk-external-module-dependency.md): repository-content rules (no committed `go.work`, no `replace`, pinned `require`) — unchanged.
