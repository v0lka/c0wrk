# ADR-014: sp4rk as a Separate Go Module

## Status

Accepted

> **Related:** the canonical, sp4rk-native version of this decision now lives in [sdk/specs/decisions/001-separate-module.md](../../sdk/specs/decisions/001-separate-module.md). This c0wrk ADR is retained as historical decision history.

## Context

ADR-001 established a single Go module (`github.com/v0lka/c0wrk`) for the entire project, including sp4rk (then held in the `sdk/` directory). At the time, sp4rk was internal to c0wrk and multi-module overhead was not justified.

ADR-011 completed the extraction of all c0wrk-specific concepts from sp4rk, leaving it a clean reusable agent framework with zero imports from `core/`, `backend/`, or `desktop/`. sp4rk is now architecturally independent and suitable for external consumption.

However, as long as the sp4rk code remained inside the single root module (in the `sdk/` directory), external consumers who import `github.com/v0lka/sp4rk` pulled the entire c0wrk module graph — including GUI (`wailsapp/wails`), SQLite (`modernc.org/sqlite`), Bleve, PTY, and other application-level dependencies that sp4rk never imports. While Go's graph pruning prevents these from compiling in a consumer's binary, they pollute the module graph, prevent independent versioning, and make sp4rk impractical as a standalone framework.

## Decision

Split sp4rk (the `sdk/` directory) into a separate Go module with the path `github.com/v0lka/sp4rk`.

- The sp4rk `go.mod` (at `sdk/go.mod`) declares `module github.com/v0lka/sp4rk` with only the dependencies sp4rk actually imports: `go-anthropic`, `openai-go`, `mcp-go`, `tiktoken-go`, `tokenizer`, `chromem-go`, `onnxruntime_go`, `html-to-markdown`, `go-readability`, `doublestar`, `sh/v3`, `golang.org/x/net`, `yaml.v3`.
- The root module (`github.com/v0lka/c0wrk`) depends on sp4rk via a `require` + `replace` directive pointing to `./sdk` for local development:
  ```
  require github.com/v0lka/sp4rk v0.0.0
  replace github.com/v0lka/sp4rk => ./sdk
  ```
- No `go.work` file. The root module uses the `replace` directive for local development; external consumers import `github.com/v0lka/sp4rk` directly without any replace.
- `make test` and `make lint` run in both modules.

## Consequences

**Positive:**

- External consumers import `github.com/v0lka/sp4rk` and get a clean dependency graph containing only sp4rk-relevant packages — no Wails, SQLite, Bleve, or other application-level dependencies.
- sp4rk can be versioned independently of c0wrk (tagged as `vX.Y.Z` in its own repository `github.com/v0lka/sp4rk`).
- sp4rk's dependency surface is explicit and auditable via `sdk/go.mod`.
- Layer boundary is now enforced at the module level: sp4rk cannot import `core/`, `backend/`, or `desktop/` because they live in a different module.

**Negative:**

- Two `go.mod` files to maintain; `go mod tidy` must be run in both the `sdk/` directory and the root.
- Dependency version drift risk: sp4rk and the root module may pin different versions of shared dependencies (e.g., `mcp-go`). Mitigated by pinning sp4rk versions to match the root module.
- `go test ./...` from the root no longer covers sp4rk — the Makefile handles this by running tests in both modules.

## Alternatives Considered

**Keep single module (ADR-001 status quo)**: Simpler dependency management, but sp4rk remains impractical for external consumption due to the polluted module graph and lack of independent versioning.

**Multi-module with go.work**: Rejected per ADR-001's reasoning — `go.work` is a local-development tool, not a publishing mechanism, and adds complexity without solving the external-consumer problem. The `replace` directive achieves the same local-development ergonomics.

**Extract sp4rk/embedding into a third module**: The embedding subsystem (`onnxruntime_go`, `chromem-go`) is the heaviest sp4rk dependency. It is a leaf package not imported by the framework entry point. A future ADR may extract it behind a build tag or separate module if the native ONNX dependency proves burdensome for consumers that don't need vector search.

## Related

- Supersedes [ADR-001](001-single-module.md) — single-module decision is superseded for the sp4rk layer only; the root module remains a single module for `core/`, `backend/`, `desktop/`.
- Builds on [ADR-011](011-sp4rk-to-core-extraction.md) — the extraction of c0wrk-specific concepts made sp4rk module-independent.
- Canonical sp4rk decision: [sdk/specs/decisions/001-separate-module.md](../../sdk/specs/decisions/001-separate-module.md)
- Aligned with [specs/architecture/layers.md](../architecture/layers.md) — import rules now enforced at module level.
