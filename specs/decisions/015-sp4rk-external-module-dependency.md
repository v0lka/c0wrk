# ADR-015: sp4rk as an External Module Dependency

## Status

Accepted

## Context

ADR-014 split sp4rk into a separate Go module held in the in-repo `sdk/` directory, with the root module depending on it through `require github.com/v0lka/sp4rk` + `replace github.com/v0lka/sp4rk => ./sdk`.

sp4rk has since been extracted to its own repository (`github.com/v0lka/sp4rk`) and published. The `sdk/` directory and the `replace` directive no longer exist in the c0wrk repository. The root module now depends on sp4rk through a normal `require` of the published version:

```
require github.com/v0lka/sp4rk v0.0.0-20260710205611-b7dbcbbad959
```

There is no `sdk/` directory, no `replace` directive, and no `go.work` file. `go.mod` lives only at the repository root.

## Decision

c0wrk is a single Go module (`github.com/v0lka/c0wrk`) with one `go.mod` at the repository root. sp4rk is consumed as a normal external Go module dependency via `require github.com/v0lka/sp4rk <version>`, versioned by the pseudo-version pinned in `go.mod`.

- `make test` and `make lint` run against the single root module; CI (`.github/workflows/ci.yml`) runs `go test ./...` and `golangci-lint run` on the same single module.
- No `sdk/` directory, no `replace` directive, no `go.work`.
- The `sdk-no-core` depguard rule was removed — sp4rk isolation is now enforced by the repository boundary, not a linter rule. The only remaining depguard rule is `core-no-backend`.
- sp4rk's own source, `go.mod`, and specs live in the `github.com/v0lka/sp4rk` repository, not in this repository.

## Consequences

**Positive:**

- One `go.mod` / one `go.sum` to maintain; `go mod tidy` and `go test ./...` operate on the whole c0wrk codebase from the root.
- No local `replace` plumbing; sp4rk is versioned and consumed like any other dependency.
- Cleaner repository surface — no vendored copy of the framework.

**Negative:**

- Changes to sp4rk require a new published version and a `go.mod` bump in c0wrk; sp4rk cannot be edited in-place inside this repository.
- sp4rk engine behavior is no longer documented at a local path (`sdk/specs/`); its specs reside in the `github.com/v0lka/sp4rk` repository.

## Alternatives Considered

**Keep in-repo separate module (ADR-014 status quo)**: Retain `sdk/` + `replace github.com/v0lka/sp4rk => ./sdk`. Rejected — sp4rk is now independently versioned and published externally, so the in-repo split no longer applies.

**`go.work` for local cross-repository development**: Rejected per the reasoning in ADR-001 — `go.work` is a local-development tool that is not published. The single root module depending on a published sp4rk version needs no workspace file.

## Related

- Supersedes [ADR-014](014-sp4rk-separate-module.md) — the in-repo `sdk/` separate-module mechanism is replaced by consumption of the published external module.
- Restates the single-module approach of [ADR-001](001-single-module.md) for the c0wrk root module; ADR-001's broader claim (sp4rk internal to the project) no longer holds because sp4rk is external.
