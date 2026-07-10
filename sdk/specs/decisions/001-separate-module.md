# ADR-001: sp4rk as a Separate Go Module

## Status

Accepted

## Context

sp4rk is a reusable agent-execution framework: the ReAct loop, LLM providers, tool registry, memory compaction, orchestration engine, skills, and the MCP gateway. It was designed to have no knowledge of any particular embedding application — it imports no host code and carries no application-specific concepts.

As long as sp4rk remained a package within a host application's single Go module, external consumers who imported it pulled the entire host module graph — including GUI toolkits, application-level persistence, search indexes, terminal emulation, and other dependencies that sp4rk never imports. While Go's module-graph pruning prevents these from compiling in a consumer's binary, they pollute the module graph, prevent independent versioning, and make sp4rk impractical to consume as a standalone framework.

The prior architectural work ensured sp4rk had zero imports of any host-application layer and was fully self-contained: it was therefore ready to become an independent module.

## Decision

sp4rk is a standalone Go module with the path `github.com/v0lka/sp4rk`.

- sp4rk's `go.mod` declares `module github.com/v0lka/sp4rk` with only the dependencies sp4rk actually imports: `go-anthropic`, `openai-go`, `mcp-go`, `tiktoken-go`, `tokenizer`, `chromem-go`, `onnxruntime_go`, `html-to-markdown`, `go-readability`, `doublestar`, `sh/v3`, `golang.org/x/net`, `yaml.v3`.
- A host application depends on sp4rk via its module path. For local co-development where sp4rk lives in a subdirectory of the host repo, the host module uses a `replace` directive:
  ```
  require github.com/v0lka/sp4rk v0.0.0
  replace github.com/v0lka/sp4rk => ./sdk
  ```
- No `go.work` file. The host module uses the `replace` directive for local development; external consumers import `github.com/v0lka/sp4rk` directly without any replace.
- The host application's build/test/lint tooling runs in both modules.

## Consequences

**Positive:**

- External consumers import `github.com/v0lka/sp4rk` and get a clean dependency graph containing only framework-relevant packages — no GUI, persistence, search, or other application-level dependencies.
- sp4rk can be versioned independently of any host application (tagged as `vX.Y.Z`).
- sp4rk's dependency surface is explicit and auditable via its own `go.mod`.
- The layer boundary is now enforced at the module level: sp4rk cannot import host-application packages because they live in a different module. A compile error, not a convention, catches violations.

**Negative:**

- Two `go.mod` files to maintain; `go mod tidy` must be run in sp4rk and in the host module.
- Dependency version drift risk: sp4rk and the host module may pin different versions of shared dependencies (e.g. `mcp-go`). Mitigated by pinning sp4rk's versions to match the host.
- `go test ./...` from the host module root no longer covers sp4rk — the build tooling must run tests in both modules.

## Alternatives Considered

**Keep sp4rk inside the host's single module.** Simpler dependency management, but sp4rk remains impractical for external consumption due to the polluted module graph and lack of independent versioning.

**Multi-module with `go.work`.** Rejected: `go.work` is a local-development tool, not a publishing mechanism, and adds complexity without solving the external-consumer problem. The `replace` directive achieves the same local-development ergonomics while remaining invisible to external consumers.

**Extract `sp4rk/embedding` into a third module.** The embedding subsystem (`onnxruntime_go`, `chromem-go`) is the heaviest sp4rk dependency. It is a leaf package not imported by the framework entry point. A future ADR may extract it behind a build tag or separate module if the native ONNX dependency proves burdensome for consumers that do not need vector search.

## Related

- [004-application-concept-extraction.md](004-application-concept-extraction.md) — the extraction of application-specific concepts is what made sp4rk module-independent and made this split possible.
- [002-skills-mcp-in-sdk.md](002-skills-mcp-in-sdk.md) — skills and the MCP gateway live in sp4rk and are wired by the host.
- [../contracts/agent-execution.md](../contracts/agent-execution.md), [../contracts/llm-providers.md](../contracts/llm-providers.md), [../contracts/tools.md](../contracts/tools.md) — the public boundaries an embedder consumes across this module boundary.
