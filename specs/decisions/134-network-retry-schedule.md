# ADR-134: Growing network auto-resend schedule and provider-signposted Retry-After windows

## Status

Accepted

## Context

[ADR-128](128-auto-resend-network-errors.md) (commitment 3) gave the network class one global interval: `llm.network_auto_retry_seconds`, default 90, where a positive value IS the wait and `0` is the kill-switch. The value is consulted fresh at every stamp, so the cadence is flat: the first re-send of a fresh failure pays the full 90 s even when the failure is a seconds-scale blip (a self-hosted gateway restarting), and a healing outage gets no fast probe before the full ceiling. Separately, the rate-limit class ([ADR-065](065-auto-resend-retryable-errors.md)) always waits the operator's configured per-provider interval — even when the provider explicitly signposts the window in the RFC 7231 `Retry-After` header of its 429 response, which is authoritative information the configured guess can only approximate.

The signpost is reachable from c0wrk without touching sp4rk: the openai-compatible transport is the only one whose error type carries the HTTP response (openai-go's `*Error` keeps a `*http.Response`, and sp4rk wraps SDK errors with `%w`, so `errors.As` reaches the header from the session seam), while the anthropic and google transports carry no response on their error types and simply report absent. The questions this ADR answers: how should the task-level auto-resend time its re-sends — keep both flat cadences, or grow the network one and honor the server's window — with no new config key, no UI change, and without editing the committed ADR-128?

## Decision

**The network class waits a code-owned growing schedule, the rate-limit class honors the provider's Retry-After, and everything else about the auto-resend contract stands.** Three commitments, amending ADR-128's commitment 3 only — ADR-128 is **amended, not superseded, and not edited** (a committed accepted record; this ADR carries the change):

1. **The network class waits `schedule[min(n, cap)]`: 15 s → 30 s → 60 s, capped at 90 s, where n is the task-failure episode's prior stamped re-sends.** The schedule is code-owned (`networkAutoRetryScheduleSeconds` in `backend/session/manager_auto_retry.go`) — policy, not tuning, exactly like the class taxonomy ADR-065/ADR-128 already froze into code. The knob `llm.network_auto_retry_seconds` retires to the switch: any positive value arms THIS schedule, `0` stays the kill-switch, and the default 90 keeps materializing, persisting and validating against the same inclusive 3600 bound — the value simply no longer sets the wait. The cap is the ADR-128 cadence: the schedule front-loads faster probes (15 s, 30 s, 60 s) and then holds the longest wait the class ever needed, so a sustained outage is re-sent no faster than before while a healing one is probed early. The re-arms stay unlimited (ADR-065's contract verbatim). A flip of the knob still applies to the next surfaced deadline without a restart (same atomic snapshot).
2. **One re-send counter per task-failure episode.** Keyed by session + task, live-only in memory, it advances only when a deadline is actually stamped — by either class, so n counts the episode's prior auto re-sends regardless of which class produced them — and resets when the episode ends: the resumable banner resolved with any decision but `resumed` (a cancel, or a mode-takeover abandonment), the session's task completing successfully, or an app restart. A `resumed` resolution deliberately keeps the ladder: the UI's auto fire and a manual resume are indistinguishable at the `resumeTask` seam, and the ladder reflects the episode's accumulated transient-failure pressure, not one attempt's.
3. **The rate-limit class honors a present `Retry-After` header, clamped, with the configured interval as the fallback.** The window is extracted from the error chain (`*openai.Error.Response`), in both RFC 7231 forms — delay-seconds, or an HTTP-date rounded up to whole seconds — and clamped to `config.MaxAutoRetrySeconds` (3600, the same inclusive bound `validate()` enforces). An absent, empty, unparseable, or non-positive value (a past date included — "retry now" is not a stampable window) falls back to the configured per-provider interval, which is also what the anthropic and google transports always answer: their errors carry no response. The class's own `0 = off` stays authoritative: a header refines the window of an ARMED class, it never arms a disabled one.

What does not change: the `task_failed_resumable` payload shape (`auto_retry_at`, unchanged), the ownership split (backend classifies and stamps, UI counts down and fires), the live-only deadline, the stop-on-any-manual-click lifecycle, the ADR-071 budget-death exclusion, and the config schema — no new key.

## Consequences

Positive:

- A fresh failure's first re-send probes at 15 s instead of 90 s: brief blips (gateway restarts, flappy DNS) now self-heal within seconds-scale waits instead of always paying the ceiling.
- A sustained outage is never re-sent faster than the ADR-128 cadence — the cap IS the old interval — so the schedule spends only the first three probes buying faster recovery.
- Providers that signpost their 429 windows get the authoritative wait instead of the operator's approximation; the operator's interval remains meaningful exactly where no signpost exists.
- Zero new surface: same payload key, same countdown hook (untouched), same knob, no schema change, no sp4rk change.

Negative / residual:

- An operator who tuned `network_auto_retry_seconds` to a non-default value loses that tuning: every positive value now behaves identically (on). The knob's numeric semantics are retired to a switch — documented in the domain spec and `config.example.yaml` — and a value above the 90 s cap can no longer express "wait longer".
- The re-send counter is per-manager in-memory state: entries are cleared on episode end and restart, but a task that simply stops failing (never cancelled, never succeeded) leaves its entry until one of those happens — a bounded map of small entries, the same live-only trade-off as the deadline itself.
- `Retry-After` is honored only where the transport exposes the response (openai-compatible, including the ChatGPT subscription path); the anthropic and google transports fall back to the configured interval. A transport limitation, not a class change — the fallback is the documented behavior, not an error.
- The two interval knobs now read asymmetrically: the per-provider value sets its interval, the global value only arms a schedule. The domain spec states both contracts.

## Alternatives Considered

- **Keep the flat 90 s cadence (status quo).** Rejected: every outage pays the full ceiling on its first probe even when the blip heals in seconds, and the rate-limit class keeps ignoring an authoritative server signpost in favor of a configured guess.
- **Cap the schedule at the configured value (`min(step, knob)`).** Rejected: it re-introduces "the value sets the wait" through the back door while keeping the schedule — two coupled meanings for one knob — and a knob set below 15 s would gut the ladder's probe-then-hold shape; a value above 90 s would silently do nothing.
- **A configurable schedule** (base/multiplier/step-list keys). Rejected: a schedule is policy, not tuning — the same price ADR-065/ADR-128 already accepted for the class taxonomy — and a new config key would grow the Settings surface for a cadence no operator asked to control.
- **Honor `Retry-After` even when the per-provider interval is 0.** Rejected: ADR-065's `0 = off` is the operator's explicit "never auto-resend" for that provider; a server hint must not override the operator's kill switch, and "kill-switch semantics unchanged" is the amendment's own constraint.
- **Unbounded exponential growth** (multipliers beyond 90 s). Rejected: these re-sends are UI-fired resumes of a whole task, not in-flight request retries — an unbounded ladder would park a healable failure behind multi-minute waits; 90 s is the longest wait the class ever needed, so growing past it only trades recovery for patience.
- **Plumb the header onto sp4rk's `llm.Error`.** Rejected: the error chain already carries the response for the only transport that has one, so the seam reads it with a one-package `errors.As`; a new cross-repo field would freeze a transport detail into sp4rk's error type for a single consumer — and the anthropic SDK surfaces rate-limit headers on its response wrapper, not its error, so the field could not even be filled uniformly without reworking sp4rk's transport error paths.
- **Bounded network retries (max N with backoff).** Rejected for the same reason ADR-065 and ADR-128 rejected it: a cap gives up during exactly the long outages the feature targets; Cancel remains the one-click opt-out.

## Related

- [ADR-128](128-auto-resend-network-errors.md) — amended by this record (commitment 3's interval semantics become schedule + switch); the committed record stands untouched — extended-not-edited, like ADR-065 before it.
- [ADR-065](065-auto-resend-retryable-errors.md) — the ownership split, live-only deadline, `0 = off, no bool`, and unlimited re-arm all stand verbatim; the rate-limit knob's window is now refined by the header when present.
- [ADR-127](127-fixed-provider-auto-resend.md) — the per-provider knob the Retry-After fallback reads; unchanged.
- [../domains/llm-providers.md](../domains/llm-providers.md) § Automatic Resend — the domain spec carrying the schedule, the counter lifecycle, and the header rules.
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — `task_failed_resumable.auto_retry_at`; the payload shape is unchanged.
- `backend/session/manager_auto_retry.go` — the schedule constant, the `retryAfterSeconds` extraction, and the re-send counter.
- `backend/session/manager_execution.go` — the stamp call site and the episode-end clearing (banner resolution, successful completion).
- openai-go `internal/apierror` (`openai.Error`) — the response-carrying error type whose `Retry-After` header this seam reads.
