# ADR-065: Auto-Resend on Retryable Provider Errors

## Status

Accepted

## Context

sp4rk's LLM router already retries **inside** a single in-flight LLM call — backoff against classified `*llm.Error` values, bounded per request. But the router's retry budget is finite and the failure can outlive it: against a self-hosted gateway (LM Studio, vLLM, an aggressive proxy) hammered with rate limits, the whole executor loop dies on a 429/529, the task lands in the `failed`/resumable state, and the session ends at the manual resume banner. The user's only recourse is to watch the chat and click Resume — for a local endpoint the user owns, that babysitting is exactly what a machine should do.

The requirement that shaped this ADR: after a task fails on a retryable provider error (rate limit / overload), wait a configurable interval and re-send it automatically — without touching the engine's own in-flight retry, without a new enable/disable bool, and only for endpoints the user actually owns (custom compatible providers).

A previous attempt implemented the timer on the **backend** (branch `llm/auto-resume`): a `time.AfterFunc` in the session manager, eight disarm points woven through `SendMessage`/`ResumeTask`/`CancelUnfinishedTask`/`DeleteSession`/`ArchiveSession`/`CompactSessionContext`/`Shutdown`, deadline stripping on history load, and a persisted `auto_retry_at` that had to be reconciled against process-local timers after every restart. The cross-cutting disarm matrix produced a large diff and a trail of races and stranded-banner bugs. This ADR records the deliberate reversal of that ownership.

## Decision

**The backend only classifies the failure and stamps a deadline into the banner payload; the UI owns the countdown and performs the auto-resend.**

### Ownership: backend classifies, UI fires

- **Backend** (`backend/session/manager_auto_retry.go`, ~100 lines, no timers): when a terminal execution error reaches `emitResumableIfUnfinished` with a `cause`, `maybeAutoRetryAt` classifies it (`isAutoRetryableCause`: an `*llm.Error` whose `ErrKind` is `rate_limit` or `overloaded`, or whose HTTP `StatusCode` is 429/529) and resolves the provider's interval (`SetAutoRetryResolver`, wired to an immutable atomic snapshot of the live config — see below). When both qualify, the deadline `now + interval` travels in the `task_failed_resumable` payload as `auto_retry_at` (unix seconds, `omitempty`). Nothing is scheduled; no state is held; nothing needs disarming. Two transports carry the cause into that call: (a) the synchronous error path — the orchestrator returns the error and the manager forwards it verbatim; (b) the **degraded-completion path** — a best-effort result with a nil returned error (the goal loop's errored-turn halt collapses its typed turn error into `GoalState.LastError`) preserves the error VALUE on `HandleResult.Err` (set by `goalLoopResult` from `GoalState.LastErrorTyped`, process-local and never persisted), and `emitTaskComplete` forwards `result.Err` as the cause. Without (b) the longest rate-limit storm — the one that outlives the goal loop's bounded turn retries and surfaces as a degraded completion — would never arm the countdown.
- **Frontend** (`frontend/src/components/chat/useAutoRetryCountdown.ts`): the live event handler copies the deadline into the banner message metadata together with `auto_retry_live: true`. The panel's 1-second ticker renders `Resume (Ns)` on the button; on zero it calls `resumeTask(sessionId)` — the ordinary guarded backend resume path — exactly once per deadline (one-shot fire guard).

### Live-only deadline (restored sessions never count down)

The `auto_retry_live` discriminator exists **only in memory**: the event persister stores the raw event payload (which never contains the flag), so a banner restored from the DB — after an app restart, or when switching into the session — carries at most a stale `auto_retry_at` with no live flag and always renders the plain manual banner. This is deliberate: the countdown serves an **open session in the absence of the user** (the task failed while the user was watching, the panel is mounted, the window may be minimized but the panel is not unmounted). After a restart nobody is watching; the user decides manually. No backend stripping, no persisted-deadline reconciliation — the failure mode that motivated `StripStaleAutoRetryDeadline` in the backend-timer design cannot exist.

### Any manual click stops the countdown

While the ticker runs, the seconds tick on the Resume button itself. **Any** manual click — Resume or Cancel — stops the countdown optimistically (`stop()`, before the RPC round-trip): a manual Resume IS the resume the timer would have performed; a Cancel is the user's final discard. In both cases the auto fire is disarmed and never happens. After the deadline hits zero the Resume button disables as `Auto-resend…` until the `task_resumed` event resolves the banner, and **Cancel disables with it**: the auto fire already dispatched `resumeTask`, so a click in that window would race the just-fired resume (optimistically mark the banner cancelled while the task starts running, or cancel the resumed task) — the window is seconds at most. If the auto fire's `resumeTask` rejects (busy session, archived, backend refusal), the hook strips the live keys from the banner metadata so the panel falls back to the plain manual banner instead of a permanently disabled dead end — and a REJECTED MANUAL RESUME degrades identically: its revert-to-`originalMetadata` strips the live keys too, because the optimistic `resumed` marking unmounted the banner (destroying the instance-scoped one-shot disarm), so reverting the raw live metadata would remount the countdown and fire at the unchanged deadline anyway.

### Compaction

A manual compaction needs an idle window; an auto fire landing inside it would call `resumeTask` into the compacting guard and strand the banner. The `compaction_started` handler therefore strips the live keys from the latest unresolved banner (`stripAutoRetryFromBanner`) — the sole external stop for a running countdown.

### `0` = off; no bool

The interval is a plain int, `auto_retry_seconds`, with `omitempty` — 0/omitted means "never auto-resend; surface the failure and ask the user". There is no separate `enabled` bool: two knobs for one behavior is a state space with no additional expressiveness.

The inclusive upper bound (3600) lives in ONE place: `config.maxAutoRetrySeconds`, enforced by `validate()` and the `UpdateLLMConfig` RPC path, and published to the Settings UI as `llm.auto_retry_max_seconds` in the `GetConfig` response — the form clamps its interval input against that server-published value, so the UI can never propose a value the save RPC would reject. The frontend carries NO compiled-in fallback constant: frontend and backend ship in one Wails binary, so the field is REQUIRED in the payload — a missing or non-positive value fails the LLM-settings load loudly (logged error; the provider forms render a retry notice and stay gated) instead of silently clamping against a stale constant, which would mask exactly the drift the server-published bound exists to prevent. Presets offered in the dropdown are filtered against the bound at render time (`AUTO_RETRY_PRESETS.filter(p => p <= autoRetryMaxSeconds)`), so a future lower bound never surfaces a preset the save would reject.

### Compatible providers only

The key exists ONLY on `openai_compatible.<name>` and `anthropic_compatible.<name>` configs. The fixed providers (`anthropic`, `chatgpt`) have no `auto_retry_seconds` field, and the resolver walks the compatible providers only — a fixed provider name always resolves to 0. The resolver never reads the live config maps (Settings saves replace them under `configMu`; the session manager invokes the resolver without that lock — an unsynchronized read would be a fatal concurrent-map-access crash). Instead `Application` publishes an immutable provider→interval snapshot behind an `atomic.Pointer` (`publishAutoRetryIntervals`, seeded at construction), and `UpdateLLMConfig` republishes it from every committed candidate — interval changes apply to the next failure that surfaces a deadline, with no restart.

### Retryable class: a backend constant

A failure qualifies only when the terminal cause is a classified `*llm.Error` (sp4rk `llm/errors.go`) with `ErrKind == rate_limit` or `ErrKind == overloaded` — or the equivalent HTTP statuses 429/529 on status-carrying transports (the anthropic_compatible SDK parses the JSON error body into `APIError`, which carries no status; sp4rk derives ErrKind from the provider's own `rate_limit_error`/`overloaded_error` type fields). The list is a backend-local, unexported constant; extending it is a code change, not a config change. Network errors (StatusCode 0) and every non-listed status never surface a deadline. Engine-level `Retryable` semantics (500/502/503/504 are retryable in-flight) are deliberately NOT inherited: the auto-resend is for the two error classes a wait-and-retry actually cures.

## Consequences

**Positive:**

- The backend change is a pure function of the terminal error — no timers, no disarm matrix, no shutdown ordering, no persisted-deadline reconciliation. The entire failure surface of the previous attempt is gone by construction.
- The auto-resend uses the same guarded `resumeTask` entry point as a manual click — no parallel resume machinery.
- A restored session degrades gracefully to the manual banner with zero code dedicated to the transition (the live flag simply isn't there).
- One int knob, no bool; `omitempty` keeps disabled providers out of the written YAML.
- The live-config resolver makes Settings changes effective for the next surfaced deadline without a rebuild.

**Negative / trade-offs:**

- The countdown runs only while the banner is rendered. The panel stays mounted for an open session (a minimized window keeps ticking), but if the user closes the app or the session view mid-countdown, the auto-resend dies with it — accepted: the feature exists for the open-session-absent-user case, and everything else degrades to the manual banner.
- Unlimited auto-retries: a task that keeps failing on 429/529 re-arms after every failure; the only stop is the interval, a manual click, or closing the view. Accepted for v1 (bounded retries would give up during exactly the long rate-limit windows the feature targets); the banner keeps the Cancel affordance one click away.
- The retryable-class taxonomy (rate_limit/overloaded) is frozen in backend code — the deliberate price of not freezing an operational guess into a user-facing knob.
- When the frontend deadline ticker and the wall clock drift (suspended machine), the fire may land late — harmless, the deadline is advisory to the UI.

## Alternatives Considered

- **Backend timer in the session layer** (the `llm/auto-resume` branch) — the resume itself was correct, but the disarm matrix (eight entry points), the persisted-vs-process-local deadline reconciliation, and the shutdown/archive/compaction interactions produced a large, bug-prone diff. Rejected: the countdown's only true consumer is the open UI; moving it there collapses the entire coordination problem.
- **Engine-level retry escalation (sp4rk `RouterConfig` retry budget)** — would conflate "one request's transient retry" with "re-run the whole task"; the router cannot resume an executor loop, and the knob would leak into every provider including fixed vendors. Rejected as the wrong layer.
- **A dedicated `auto_retry_enabled` bool** — two knobs for one behavior; the disabled state must still remember or drop the interval. Rejected: `0 = off` covers it with less state.
- **Bounded retries (e.g. max 3 with backoff)** — gives up precisely during long rate-limit windows. Rejected for v1; a cap can be added later without schema changes.
- **Persisting the live flag / re-arming on reload** — rejected explicitly: after a restart there is no watcher, and re-arming would silently burn API quota against a possibly-changed world. The manual banner is the honest state.

## Related

- [../domains/llm-providers.md](../domains/llm-providers.md) § Automatic Resend — the domain spec (resolver, classification, Settings UI, frontend contract).
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — `task_failed_resumable` (`auto_retry_at` payload) and the UI countdown contract.
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — `UpdateLLMConfig` / `ProviderConfigRequest` pointer-sentinel fields, `GetConfig` per-provider response fields.
- [ADR-054](054-per-provider-tls-pinning.md) — the compatible-provider-only knob pattern (pin, sentinel, same UI gate).
