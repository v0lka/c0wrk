# ADR-004: sp4rk Sheds Application-Specific Concepts

## Status

Accepted

## Context

For sp4rk to be a genuinely reusable agent-execution framework, it must contain only concepts that belong to *general agent building*. During its evolution, several application-specific concerns had crept into the framework layer. Architectural review identified that they violated two framework constraints:

1. **They were not used by any other framework package** — they were leaf packages consumed only by the host application.
2. **They were not about general agent building** — they encoded a specific embedding application's infrastructure, UI protocol, or operating mode.

The offending concerns were:

- **Vector index** — a project-aware, git-aware, workspace-aware search index. This is application infrastructure, not a generic agent primitive.
- **HTTP proxy** — proxy configuration used only for a host's LLM API clients; not a generic agent-building concern.
- **No-project mode** — a context key (`noProjectKey` and its helpers) encoding a host-specific "chat without a project" operating mode.
- **ask-user UI types** — a host-UI-specific question/answer protocol embedded in the tools package.

Their presence meant sp4rk carried assumptions about a particular kind of host, blocking clean reuse and independent versioning.

## Decision

Remove all application-specific concepts from sp4rk. An **embedding application owns these concerns** and supplies them where needed.

- **Vector index** and **HTTP proxy** are removed from the framework. They live in the host application (which imports sp4rk directly) and are wired into the layers that need them.
- **No-project mode** (`noProjectKey`, `WithNoProject`, `IsNoProject`) is removed from the framework tools package. It is a host operating-mode concept and belongs in the host.
- **ask-user UI types** (`AskUserQuestion`, `AskUserRequest`, `AskUserResponse`, `AskUserAnswer`, `AskUserOption`, `AskUserFunc`) and the `ask_user` tool implementation are removed from the framework tools package. They describe a host-specific UI protocol; an embedding application defines its own interaction types.
- The framework's environment-formatting helpers are decoupled from any host mode concept: they accept explicit `EnvFormatOptions` instead of reading a host-specific "no project" flag from context.

The framework retains only general agent-building primitives: the executor, LLM providers/router/registry, tool registry, memory/compaction, orchestration engine, skills, MCP gateway, planner, reflector, prompt building, embeddings, path utilities, and security.

## Consequences

**Positive:**

- sp4rk is a clean reusable agent framework with zero application-specific concepts. It makes no assumptions about whether a host has a "project," a UI, a vector index, or a proxy.
- The framework's import graph is self-contained: it imports no host layer. This is the precondition for [001-separate-module.md](001-separate-module.md).
- The framework tools package no longer contains UI-specific or mode-specific types, so consumers are not forced to depend on a particular interaction protocol.
- An embedding application is free to define its own ask-user interaction, its own operating modes, and its own search/proxy infrastructure without fighting framework assumptions.

**Negative:**

- An embedding application must implement its own vector search, HTTP proxy, ask-user interaction, and operating-mode plumbing. The framework provides the seams (e.g. the tool registry, the `Events`/`HITLHandler` interfaces) but not the application-specific behavior.
- Historical documentation describing an intermediate placement of these concerns is now outdated.

## Alternatives Considered

**Keep the vector index and proxy in the framework as optional packages.** Rejected: they are leaf packages consumed only by the host, and their workspace/git/project awareness is fundamentally application-specific. Keeping them pollutes the module graph and signals false framework boundaries.

**Keep ask-user types in the framework as a "standard" interaction protocol.** Rejected: the question/answer payload, option shape, and confirmation flow are inherently UI- and host-specific. Forcing one protocol on all consumers contradicts the goal of a host-agnostic engine.

**Abstract no-project mode behind a framework interface.** Rejected: "no project" is one specific operating mode among many a host might define. Modeling it in the framework privileges that mode; an explicit `EnvFormatOptions` parameter keeps the formatter mode-agnostic.

## Related

- [001-separate-module.md](001-separate-module.md) — this extraction is what made sp4rk module-independent.
- [002-skills-mcp-in-sdk.md](002-skills-mcp-in-sdk.md) — contrasts: skills and MCP *do* belong in the framework (general agent building), unlike the concerns removed here.
- [../contracts/agent-execution.md](../contracts/agent-execution.md), [../contracts/tools.md](../contracts/tools.md) — the host-implemented interfaces (`Events`, `HITLHandler`, `ConfirmFunc`) through which an embedding application supplies its own interaction and policy behavior.
