# ADR-011: Move Vector Index and Proxy from SDK to Core

## Status

Accepted

## Context

ADR-009 placed `vectorindex/` in `sdk/` as part of the backend domain logic extraction. Subsequent architectural review identified that both `sdk/vectorindex/` and `sdk/proxy/` violate two core SDK constraints:

1. **They are NOT used by any other SDK package** — they are leaf packages consumed only by `core/`, `backend/`, and `desktop/`.
2. **They are NOT about general agent building** — `vectorindex/` is project-aware, git-aware, and workspace-aware (c0wrk-specific infrastructure). `proxy/` is HTTP proxy configuration used only for c0wrk's LLM API clients (not a generic agent SDK primitive).

Additionally, c0wrk-specific tool concepts embedded in `sdk/tools/` — `noProjectKey`/`WithNoProject`/`IsNoProject` (CHAT-mode context key) and `AskUser*` types (c0wrk-UI-specific question/answer protocol) — were extracted to `core/tools/`. The `ask_user` tool implementation followed.

## Decision

Move the following packages from `sdk/` to `core/`:

| Source | Destination |
|--------|-------------|
| `sdk/vectorindex/` | `core/vectorindex/` |
| `sdk/vectorindex/lexical/` | `core/vectorindex/lexical/` |
| `sdk/proxy/` | `core/proxy/` |

Extract c0wrk-specific concepts from `sdk/tools/` to `core/tools/`:

| Concept | New Location |
|---------|-------------|
| `noProjectKey`, `WithNoProject`, `IsNoProject` | `core/tools/mode.go` |
| `AskUserOption`, `AskUserQuestion`, `AskUserRequest`, `AskUserAnswer`, `AskUserResponse`, `AskUserFunc` | `core/tools/askuser_types.go` |
| `AskUserTool` (implementation) | `core/tools/askuser.go` |

Refactor `FormatFullEnvBlock`/`FormatCompactEnvBlock` to accept explicit `EnvFormatOptions` instead of reading c0wrk's `IsNoProject` from context, decoupling the SDK formatters from the application's mode concept.

## Consequences

**Positive:**

- `sdk/` is now a clean reusable agent framework with zero c0wrk-specific concepts
- Import graph: `core/vectorindex/` and `core/proxy/` are imported from `core/`, `backend/`, and `desktop/` — all layers above sdk
- `sdk/tools/` no longer contains UI-specific or mode-specific types
- 5 specs and AGENTS.md updated to reflect new package locations

**Negative:**

- ~6,000 lines moved (files deleted from sdk/ and recreated in core/ with updated imports)
- Historical references in ADR-009 are now outdated (describes intermediate `sdk/vectorindex/` placement; superseded by this ADR)

## Related

- Refines [ADR-009](009-backend-domain-logic-extraction.md) — vectorindex moved from `sdk/` to its final `core/` home
- Follows [ADR-008](008-backend-sdk-direct-import.md) — all layers import source packages directly
- Aligned with [specs/contracts/core-sdk.md](../contracts/core-sdk.md) — updated interface tables
