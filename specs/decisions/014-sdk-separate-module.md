# ADR-014: SDK as a Separate Go Module

## Status

Accepted

## Context

ADR-001 established a single Go module (`github.com/v0lka/c0wrk`) for the entire project, including `sdk/`. At the time, the SDK was internal to c0wrk and multi-module overhead was not justified.

ADR-011 completed the extraction of all c0wrk-specific concepts from `sdk/`, leaving it a clean reusable agent framework with zero imports from `core/`, `backend/`, or `desktop/`. The SDK is now architecturally independent and suitable for external consumption.

However, as long as `sdk/` remains a package within the single root module, external consumers who import `github.com/v0lka/c0wrk/sdk` pull the entire c0wrk module graph — including GUI (`wailsapp/wails`), SQLite (`modernc.org/sqlite`), Bleve, PTY, and other application-level dependencies that the SDK never imports. While Go's graph pruning prevents these from compiling in a consumer's binary, they pollute the module graph, prevent independent versioning, and make the SDK impractical as a standalone framework.

## Decision

Split `sdk/` into a separate Go module with the path `github.com/v0lka/c0wrk/sdk`.

- `sdk/go.mod` declares `module github.com/v0lka/c0wrk/sdk` with only the dependencies the SDK actually imports: `go-anthropic`, `openai-go`, `mcp-go`, `tiktoken-go`, `tokenizer`, `chromem-go`, `onnxruntime_go`, `html-to-markdown`, `go-readability`, `doublestar`, `sh/v3`, `golang.org/x/net`, `yaml.v3`.
- The root module (`github.com/v0lka/c0wrk`) depends on the SDK via a `replace` directive pointing to `./sdk` for local development:
  ```
  require github.com/v0lka/c0wrk/sdk v0.0.0
  replace github.com/v0lka/c0wrk/sdk => ./sdk
  ```
- No `go.work` file. The root module uses the `replace` directive for local development; external consumers import `github.com/v0lka/c0wrk/sdk` directly without any replace.
- `make test` and `make lint` run in both modules.

## Consequences

**Positive:**

- External consumers import `github.com/v0lka/c0wrk/sdk` and get a clean dependency graph containing only SDK-relevant packages — no Wails, SQLite, Bleve, or other application-level dependencies.
- The SDK can be versioned independently of c0wrk (tagged as `sdk/vX.Y.Z`).
- The SDK's dependency surface is explicit and auditable via `sdk/go.mod`.
- Layer boundary is now enforced at the module level: `sdk/` cannot import `core/`, `backend/`, or `desktop/` because they live in a different module.

**Negative:**

- Two `go.mod` files to maintain; `go mod tidy` must be run in both `sdk/` and the root.
- Dependency version drift risk: the SDK and root module may pin different versions of shared dependencies (e.g., `mcp-go`). Mitigated by pinning SDK versions to match the root module.
- `go test ./...` from the root no longer covers `sdk/` — the Makefile handles this by running tests in both modules.

## Alternatives Considered

**Keep single module (ADR-001 status quo)**: Simpler dependency management, but the SDK remains impractical for external consumption due to the polluted module graph and lack of independent versioning.

**Multi-module with go.work**: Rejected per ADR-001's reasoning — `go.work` is a local-development tool, not a publishing mechanism, and adds complexity without solving the external-consumer problem. The `replace` directive achieves the same local-development ergonomics.

**Extract sdk/embedding into a third module**: The embedding subsystem (`onnxruntime_go`, `chromem-go`) is the heaviest SDK dependency. It is a leaf package not imported by the framework entry point. A future ADR may extract it behind a build tag or separate module if the native ONNX dependency proves burdensome for consumers that don't need vector search.

## Related

- Supersedes [ADR-001](001-single-module.md) — single-module decision is superseded for the SDK layer only; the root module remains a single module for `core/`, `backend/`, `desktop/`.
- Builds on [ADR-011](011-sdk-to-core-extraction.md) — the extraction of c0wrk-specific concepts made the SDK module-independent.
- Aligned with [specs/architecture/layers.md](../architecture/layers.md) — import rules now enforced at module level.
