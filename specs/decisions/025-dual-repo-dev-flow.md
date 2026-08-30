# ADR-025: Dual-Repo Development Flow with Unpublished sp4rk APIs

## Status

Superseded by [ADR-031](./031-gowork-repo-root.md)

> **Note:** only the workspace-file location changed (shared parent directory → gitignored repo root). The mid-cycle pin-lag flow and release checklist below still describe the sanctioned process and are carried forward by ADR-031.

## Context

Cross-cutting features (e.g. verify-on-edit, `llm.CallPurpose`) are developed simultaneously in `github.com/v0lka/sp4rk` and c0wrk: the SDK gains new APIs while c0wrk's working tree consumes them. During such a cycle:

- sp4rk carries **uncommitted** changes (owner's choice: both diffs stay open for holistic review).
- c0wrk's `go.mod` still pins the last **published** sp4rk commit, per [ADR-015](015-sp4rk-external-module-dependency.md).
- Consequently `GOWORK=off go build ./...` in c0wrk fails against the pinned version (undefined symbols), and CI — which checks out c0wrk alone — cannot build a mid-cycle push.

Local development bridges the gap with a `go.work` in the repositories' **shared parent directory** (`Repositories/go.work`, outside both repos), which ADR-015 permits in spirit: the workspace file is a local-development tool that is not published and stays out of the repository.

## Decision

Approved by the repository owner (2026-08-17): during cross-repo development cycles, the supported build mode for c0wrk is the parent-directory `go.work` (both checkouts present side by side). The `go.mod` pin lags the sp4rk working tree **by design** until the release step; a `// mid-cycle` note at the top of `go.mod` marks the state.

Publishing checklist (restores single-module buildability and CI):

1. Commit and push the sp4rk changes (`github.com/v0lka/sp4rk`).
2. In c0wrk: `GOWORK=off go get github.com/v0lka/sp4rk@main && go mod tidy` (resolves the new pseudo-version; drop the `go.mod` note).
3. Verify `GOWORK=off go build ./...`, then run the pre-PR gates (`make build` / `make lint` / `make test`).
4. Commit and push c0wrk.

CI on c0wrk `main` is green only at published-pin points; pushing c0wrk mid-cycle (before step 1–2) is expected to fail `go test`/`golangci-lint` with undefined sp4rk symbols and must be avoided.

## Consequences

**Positive:**

- sp4rk and c0wrk diffs stay uncommitted as long as needed for a single holistic review; no forced per-cycle commits.
- No `replace` directive and no in-repo `go.work` — ADR-015's repository-content rules remain intact.

**Negative:**

- c0wrk is not buildable from `go.mod` alone mid-cycle (`GOWORK=off` fails); contributors without a sibling sp4rk checkout cannot build such states.
- c0wrk CI is red for mid-cycle pushes; the pin advance is a mandatory release step, not an option.

## Alternatives Considered

**Commit + repin every cycle**: commit sp4rk as soon as c0wrk consumes new APIs, then bump the pin. Rejected by the owner — it fragments the review into per-cycle commits and forces commit granularity the current workflow does not want.

**`replace github.com/v0lka/sp4rk => ../sp4rk` in `go.mod`**: rejected — ADR-015/ADR-001 forbid it; it breaks CI and any checkout without the sibling directory.

**Vendoring sp4rk**: rejected — ADR-015 explicitly keeps the repository free of a vendored framework copy.

## Related

- Extends [ADR-015](015-sp4rk-external-module-dependency.md): the repository-content rules (no in-repo `go.work`, no `replace`, pinned `require`) are unchanged; this ADR documents the sanctioned mid-cycle dev state between pin advances.
