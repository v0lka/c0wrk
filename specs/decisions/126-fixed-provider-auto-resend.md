# ADR-126: Auto-Resend for Fixed LLM Providers

## Status

Accepted

## Context

[ADR-065](065-auto-resend-retryable-errors.md) restricted the session-layer auto-resend interval to compatible providers. Fixed Anthropic, ChatGPT and Kimi requests can exhaust the same in-flight retry budget on rate-limit or overload failures. Endpoint ownership and authentication mode do not determine whether the operator wants a whole-task resend after that failure.

## Decision

`auto_retry_seconds` applies to fixed `llm.anthropic`, `llm.chatgpt` and `llm.kimi` entries as well as named OpenAI-compatible and Anthropic-compatible entries. API-key and OAuth modes share the same interval. The previous default remains unchanged: omitted or `0` disables auto-resend; there is no enabled flag. The inclusive range remains `0..3600`, enforced at config load and RPC save and published as `llm.auto_retry_max_seconds`.

This supersedes ADR-065's compatible-only scope. All other ADR-065 decisions remain in force: backend classification and advisory deadline only, immutable atomic resolver snapshot, UI-owned live-only countdown, ordinary guarded resume, manual-click and compaction cancellation, manual fallback on resume failure, and no change to engine in-flight retry.

`GetAllProviderConfigs` supplies the resolver snapshot. A legacy compatible entry named `kimi` keeps precedence over the hidden fixed Kimi entry, including its interval. Unknown providers resolve to `0`. Settings updates preserve the pointer sentinel: omitted keeps the persisted value, explicit `0` disables; rejected or rolled-back saves preserve the previous config and effective snapshot.

The retry input is independent of Base URL, TLS pin and auth controls. Fixed provider forms expose the interval without exposing endpoint/TLS overrides. The existing server bound and presets remain authoritative. Backend-owned embedded provider records remain outside the editable settings draft.

Classification is unchanged: HTTP 429/529 or `ErrKindRateLimit`/`ErrKindOverloaded` qualifies. `Retryable` alone does not. Anthropic typed failures and Responses SSE `rate_limit_exceeded`, `server_busy`, `overloaded_error` retain the logical provider name; generic server errors, timeout, service-unavailable and unknown codes do not gain a session-level resend merely because the engine can retry them in-flight.

## Consequences

- Operators can opt in to fixed-provider auto-resend without changing transport or credentials.
- Existing installations keep auto-resend disabled unless they explicitly configure an interval.
- A vendor outage can cause repeated task resumes while the live banner remains mounted; the operator can stop it with the existing manual controls.
- Tests cover fixed-provider config/default/bounds/round-trip, save sentinel and rollback, legacy Kimi precedence, API-key/OAuth form/save behavior, and classification boundaries.
- Dependency delta: the sp4rk bump in this change (`f261d9b35563` → `b3a00a021cb3`) also carries an embedder query-session gate for single-text queries after runtime degradation and an `appendcodec.ErrLocked` wording change — both unrelated to auto-resend, verified read-only as safe for c0wrk (no `ErrLocked` consumer exists; the embedder path is closed by the vector-embedder supervisor). The bump itself is not required by this feature's classification, which exists on the previous pin; it lands here so the SDK-side SSE classification matrix (`provider_auto_resend_test.go`) is covered by the pinned commit's CI.

## Alternatives Considered

- Retain compatible-only scope: prevents the same recovery workflow for vendor rate limits without a transport-based reason.
- Enable fixed-provider retries by default: changes quota-spending behavior for existing installations; rejected in favor of explicit opt-in.
- Broaden auto-resend to every engine-retryable failure: conflates request-level transport recovery with whole-task replay; rejected.
- Introduce a backend scheduler or per-auth-mode knobs: adds lifecycle/state combinations without changing the required recovery behavior; rejected.

## Related

- [LLM Providers](../domains/llm-providers.md)
- [Desktop/Frontend Contract](../contracts/desktop-frontend.md)
- [Event Catalog](../contracts/event-catalog.md)
