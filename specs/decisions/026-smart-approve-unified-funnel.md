# ADR-026: Smart-approve unified confirmation funnel with hard-bias and deterministic backstop

## Status

Accepted

## Context

The tool-policy pipeline resolves every tool call to an effective
capability-group policy (`allow` / `user_confirm` / `deny`, see
[ADR-024](024-group-policies.md)) in
[`core/builder.go`](../../core/builder.go) `applySecurityPolicies`, then
classifies any tool-local safety signal in
[`core/tools/registry.go`](../../core/tools/registry.go) `Execute` into a
**hard** or **soft** severity. Hard signals are fired security controls
(a command-blacklist pattern, an SSRF/private-address escape, a symlink escape
out of the session roots) and unassessable inputs (an undeterminable URL/path,
degraded SSRF protection); soft signals are scope questions (path containment).

Before this change, the Smart Approve strict LLM judge treated the two policies
asymmetrically:

- Effective `user_confirm` calls → consulted the strict judge.
- `allow`-policy calls with a **soft** reason → consulted the strict judge
  (Smart Approve could auto-allow).
- `allow`-policy calls with a **hard** reason → went straight to manual
  confirmation with `DisableJudge=true`; the strict judge was never consulted.
- `deny` → hard block, never judged.

The asymmetry meant the strict judge — the most capable discriminator of whether
a fired control actually applies to the concrete call — was bypassed precisely
on the highest-stakes calls. The same hard reason was judged on a `user_confirm`
tool but not on an `allow` tool. Conversely, where the strict judge *was*
consulted, its ALLOW verdict was trusted to auto-approve destructive calls; there
was no deterministic guarantee that a fired security control could not be waived
by an advisory LLM.

This ADR records the decision that resolves both gaps: every escalation funnels
through a single strict judge, hard reasons always consult it, and a
deterministic backstop keeps the user as the final authority over canonical
destructive calls.

## Decision

The confirmation pipeline is unified around a single escalation entry point,
`smartApproveOrConfirm` in
[`core/tools/registry.go`](../../core/tools/registry.go). Three properties
define the invariant.

### 1. Single funnel

Every escalation that survives the deterministic gates — regardless of whether
the effective policy is `allow` or `user_confirm`, and regardless of whether the
surfaced reason is hard or soft — routes through
`smartApproveOrConfirm` → the strict judge (when Smart Approve is enabled) →
the deterministic backstop → either an automatic execute or a manual
confirmation. There is no separate bypass path for hard reasons on `allow`
groups.

The gates that still short-circuit before the funnel are unchanged:
tool-not-found, No-Project-disabled, `GroupSystem` (the reserved orchestration
group that bypasses policy and judge by design, ADR-024), the extra shell
blacklist, a group-policy `deny`, and the workspace auto-approval path (which
fires only when `hardReason == ""`).

### 2. Hard-bias

A hard reason (a fired security control) is always evaluated by the strict
judge, **including for `allow`-policy tools**. Previously an `allow` tool with a
hard reason skipped the strict judge; now it enters `smartApproveOrConfirm` with
severity `Hard`. `deny` remains unjudged and always blocked.

### 3. Deterministic backstop

A **canonical** hard reason is never auto-approved by the strict judge. When
the strict judge returns ALLOW but `isCanonicalHardReason(code)` is true, the
verdict is deterministically overridden to CONFIRM, so the user always decides.

Canonicality is keyed off the **typed reason code** — `JudgeOutcome.ReasonCode`
from sp4rk (`tools/safety.go`), a stable cross-repository contract — never off
the human-readable prose, which sp4rk may reword freely. Matching prose would
silently stop backstopping a control when its wording drifts; a code match
fails loudly at compile/contract level instead. The canonical codes are:

- `command_blacklist` — a shell command matched a blacklist pattern (a fired
  control),
- `ssrf_private_address` — the fetch target resolves to a private/reserved
  address (a fired control),
- `symlink_escape` — the input traverses symlinks resolving outside the
  session roots (a fired control),
- `ssrf_protection_degraded` — the SSRF check could not initialize, so the
  call's posture is unassessable,
- `unassessable_url` — the target URL could not be determined from the input,
- `unassessable_path` — the target path could not be determined from the input.

The last three are deliberate: an unassessable call is one whose safety the
strict judge is **structurally unable** to evaluate — the judge sees only the
prose, not the DNS resolution or filesystem state the deterministic control
lacked — so a strict ALLOW there would be a guess, not an assessment. A fired
control and an unmeasurable input both end at the user.

Non-canonical hard reasons — `unresolvable_path_token`, `symlink_suspicious`
(scope/pattern questions where the judge can positively resolve the concrete
call), an empty/unclassified code, or a soft `outside_session_roots` reason —
are **not** backstopped: a strict ALLOW may auto-approve them. The strict judge
verdict is the last automatic gate for these.

The contract is guarded by
[`core/tools/registry_canonical_reasons_test.go`](../../core/tools/registry_canonical_reasons_test.go),
which drives the **real** sp4rk builtin judges (not prose copies in mocks) and
fails when a judge stops attaching a code or a code's classification changes.

When Smart Approve is disabled the strict judge is not consulted:
`smartApproveOrConfirm` sends the escalation straight to confirmation, with
`DisableJudge=true` for a Hard reason (so the advisory "Ask Agent" action cannot
weaken a fired control) and the normally-judged confirmation for a soft reason.

## Consequences

- **Security posture.** A fired security control is never un-judged
  (hard-bias) and, when canonical, can never be auto-approved (backstop). The
  strict judge is now the single evaluation point for every escalation, so its
  reasoning and severity (`JudgeReasoning`, `JudgeSeverity`) are populated
  throughout and reach the confirmation envelope.
- **Behavioral change.** `allow`-policy tools that previously executed
  immediately on a hard reason may now surface a confirmation (when the backstop
  fires) or be auto-approved (when the judge positively clears a non-canonical
  reason). This is a stricter posture by default.
- **Fail-safe.** Every path that does not positively establish an ALLOW ends in
  `confirmAndExecuteWithOptions(DisableJudge=true)`; the user remains the final
  authority. Judge errors, timeouts, and unparseable responses default to
  CONFIRM.
- **Cost.** A hard reason on an `allow` tool now costs a strict-judge
  round-trip (when Smart Approve is enabled) before it can be auto-approved or
  confirmed. `deny` and `GroupSystem` are unchanged, so the added cost is
  bounded to escalated calls.

## Alternatives Considered

- **Keep hard reasons unjudged on `allow` groups (status quo ante).** Rejected:
  it inverted the risk profile — the strict judge, the most capable
  discriminator, was bypassed exactly on high-stakes calls, and `allow` tools
  could execute destructive actions with no human gate.
- **Trust the strict judge unconditionally, including for canonical destructive
  calls.** Rejected: an advisory LLM should not be able to waive a deterministic
  blacklist/SSRF/symlink control without a human in the loop. The backstop keeps
  the user authoritative (ASI02, ASI05).
- **Backstop all hard reasons, not only canonical ones.** Rejected: non-canonical
  hard reasons are scope/pattern questions the strict judge can legitimately
  resolve; backstopping all of them would force confirmations for provably safe
  calls, adding friction without a security gain.
