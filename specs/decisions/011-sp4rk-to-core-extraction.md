# ADR-011: Move Vector Index and Proxy from sp4rk to Core

## Status

Accepted

> **Related:** the canonical, sp4rk-native version of this decision now lives in [sp4rk: specs/decisions/004-application-concept-extraction.md](https://github.com/v0lka/sp4rk/blob/main/specs/decisions/004-application-concept-extraction.md). This c0wrk ADR is retained as historical decision history.

## Context

ADR-009 placed `vectorindex/` in sp4rk (then the `sdk/` directory) as part of the backend domain logic extraction. Subsequent architectural review identified that both the `vectorindex/` and `proxy/` packages violate two core sp4rk constraints:

1. **They are NOT used by any other sp4rk package** — they are leaf packages consumed only by `core/`, `backend/`, and `desktop/`.
2. **They are NOT about general agent building** — `vectorindex/` is project-aware, git-aware, and workspace-aware (c0wrk-specific infrastructure). `proxy/` is HTTP proxy configuration used only for c0wrk's LLM API clients (not a generic agent-engine primitive).

Additionally, c0wrk-specific tool concepts embedded in sp4rk's `tools` package — `noProjectKey`/`WithNoProject`/`IsNoProject` (CHAT-mode context key) and `AskUser*` types (c0wrk-UI-specific question/answer protocol) — were extracted to `core/tools/`. The `ask_user` tool implementation followed.

## Decision

Move the following packages from sp4rk to `core/`:

| Package (moved from sp4rk) | Destination |
|--------|-------------|
| `vectorindex/` | `core/vectorindex/` |
| `vectorindex/lexical/` | `core/vectorindex/lexical/` |
| `proxy/` | `core/proxy/` |

Extract c0wrk-specific concepts from sp4rk's `tools` package to `core/tools/`:

| Concept | New Location |
|---------|-------------|
| `noProjectKey`, `WithNoProject`, `IsNoProject` | `core/tools/mode.go` |
| `AskUserOption`, `AskUserQuestion`, `AskUserRequest`, `AskUserAnswer`, `AskUserResponse`, `AskUserFunc` | `core/tools/askuser_types.go` |
| `AskUserTool` (implementation) | `core/tools/askuser.go` |

Refactor `FormatFullEnvBlock`/`FormatCompactEnvBlock` to accept explicit `EnvFormatOptions` instead of reading c0wrk's `IsNoProject` from context, decoupling the sp4rk formatters from the application's mode concept.

## Consequences

**Positive:**

- sp4rk is now a clean reusable agent framework with zero c0wrk-specific concepts
- Import graph: `core/vectorindex/` and `core/proxy/` are imported from `core/`, `backend/`, and `desktop/` — all layers above sp4rk
- sp4rk's `tools` package no longer contains UI-specific or mode-specific types
- 5 specs and AGENTS.md updated to reflect new package locations

**Negative:**

- ~6,000 lines moved (files deleted from the sp4rk module and recreated in `core/` with updated imports)
- Historical references in ADR-009 are now outdated (describes the intermediate `vectorindex/` placement in sp4rk; superseded by this ADR)

## Related

- Supersedes [ADR-009](009-backend-domain-logic-extraction.md) — vectorindex moved from sp4rk to its final `core/` home (ADR-009 placed it in sp4rk; this ADR reverses that placement)
- Follows [ADR-008](008-backend-sp4rk-direct-import.md) — all layers import source packages directly
- Canonical sp4rk decision: [sp4rk: specs/decisions/004-application-concept-extraction.md](https://github.com/v0lka/sp4rk/blob/main/specs/decisions/004-application-concept-extraction.md)
- Aligned with [specs/contracts/core-sp4rk.md](../contracts/core-sp4rk.md) — updated interface tables
