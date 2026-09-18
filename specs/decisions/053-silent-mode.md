# ADR-053: Silent Mode — Opt-In Unattended Operation with an Auditable Receipt Chain

## Status

Accepted — amended in place (pre-release): the original decision was folded into the **autonomy axis** before any release shipped it. This revision records five decisions made during that fold: the axis itself (D1), the revisited canonical-backstop scope — the strict judge now holds final authority over canonical hard reasons in silent `judge` mode (D4) — and the assisted posture's `VerdictDeny` / assisted-DENY terminal / canonical-ALLOW card (D5). No released build ever implemented the earlier D4 ("the backstop holds in every mode").

## Context

c0wrk gates risky work behind **blocking, human-answered prompts**: `tool_confirm`
(a confirmation-gated tool call), `step_limit` (a step-budget / circuit-breaker
boundary), `ask_user` (the question tool), and `review_prompt` (the post-task
code-review prompt). That is the right default for interactive use — ASI09's
"confirmation blocks until the user responds (no timeout)" is intentional — but
it makes a whole class of legitimate workflows impossible: an unattended run
(nightly maintenance, a long refactor, a headless/scheduled session) simply
stops at the first prompt and waits forever for a human who is not there.

Two forces pull against each other here:

1. **Autonomy needs a way past the prompts.** Without one, "unattended" is not a
   feature c0wrk can offer.
2. **Autonomy must not become a silent weakening of the floor.** The
   confirmation funnel sits on top of a deterministic security floor (group
   policy `deny`, path containment, symlink-escape detection, the flowsh shell
   analysis) and, interactively, a canonical hard-reason backstop that overrides
   even a strict-judge ALLOW. Any "skip the prompt" mechanism risks being read as
   "skip the gate" unless the boundary is made explicit and enforced.

A third requirement follows from the first two: **auditability**. ASI10 requires
"the trajectory must be reconstructable". A run that auto-denied a call or
auto-resumed past a step limit without a human present must leave a durable,
inspectable record of *what was decided and why* — otherwise unattended autonomy
is exactly the "rogue agent" failure mode the ASI10 rule exists to prevent.

The mechanism first landed as two independent toggles — `security.smart_approve`
(the strict judge auto-resolving escalated calls, ADR-026) and a
`security.silent_mode.enabled` master switch — which made the posture space
awkward: two booleans encode three real postures, and the unattended posture
silently implied the judge posture. This ADR records the **whole decision**: the
unified autonomy axis, the assisted and silent terminals, the revisited scope of
the canonical backstop, and the audit receipt chain that makes unattended
operation safe to operate.

## Decision

### D1. The autonomy axis: `security.autonomy_mode` = `standard` | `assisted` | `silent`

The two legacy booleans are replaced by a single enum, `security.autonomy_mode`
(default **`standard`**, fail-safe):

- **`standard`** — the interactive posture: no automatic gate; every
  confirmation-gated call opens a user card (hard reasons with the advisory
  judge disabled).
- **`assisted`** (the former Smart Approve) — the strict judge evaluates every
  escalated call; see D5.
- **`silent`** — the unattended posture: no card can open; the `security.
  silent_mode` sub-policies (D3) resolve every gated call to a terminal
  execute-or-deny. The former `silent_mode.enabled` master switch **no longer
  exists** — the enum value *is* the switch that makes the sub-policies live;
  in `standard`/`assisted` they are inert.

**Why an axis, not two booleans.** The postures are mutually exclusive stations
on one line — how much latitude the operator delegates — not independent
features. Two booleans could express "smart approve ON + silent ON" only by
implicit precedence (silent wins), could not express "strict judge denied
outright" as a distinct terminal (D5), and made the UI lie: a silent master
toggle next to an unrelated judge checkbox. One enum makes the delegation
explicit, enumerable, and validatable.

**Migration.** Legacy keys are honored at load and dropped at the next save
(`migrateLegacyAutonomyMode`, the same load-time pattern as the blocklist
migration): an explicit `autonomy_mode` wins (an unknown value fails safe to
`standard` with a warning — never a hard load error); otherwise
`silent_mode.enabled: true` maps to `silent` (an explicit unattended posture
beats everything), else `smart_approve: true` maps to `assisted`, else
`standard`. Every legacy-key observation is reported through the load-warnings
channel the UI displays. `security.smart_approve` and `security.silent_mode.
enabled` survive in documentation **only** as these legacy migration keys.

The posture is a **plain value** validated at load and on every Settings save,
pushed atomically to the shared builder registry and every per-session clone
(`ToolRegistry.ApplySecurityState`), so a runtime change from Settings →
Security reaches live sessions with **no restart**.

### D2. Silent mode sits *inside* the confirmation funnel, never above it

Silent mode is reached only from `smartApproveOrConfirm` — the unified
confirmation funnel — i.e. **after** every deterministic gate. Group-policy
`deny`, the `allow`-policy Judge gate ordering, workspace/temp auto-approval
priority, path containment, symlink-escape detection, and the flowsh shell
analysis all run first and are untouched. Silent mode replaces the **human
answer** to a prompt; it never removes a gate.

### D3. The four sub-policies and their terminals

While the mode is `silent`, four sub-policies (`security.silent_mode`) resolve
the four prompts:

| Sub-policy | Modes | Effect |
| --- | --- | --- |
| `tool_confirm` | `judge` (default) / `allow` / `deny` | How a confirmation-gated call is resolved without a human. |
| `step_limit` | `auto` (default) / `allow_once` / `allow_more` / `allow_always` / `deny` / `stop` | `auto` lets the strict loop judge decide; a fixed value pins that response without the judge; `stop` keeps the blocking card (opts this gate out of silent mode). |
| `ask_user` | `disable` (default) / `enable` | `disable` registers the tool in a disabled form that reports it as unavailable. |
| `review_prompt` | `suppress` (default) / `allow` | `suppress` does not emit the post-task review prompt. |

The posture is validated at load and on every Settings save
(`ValidateSilentMode`), and the sub-policy defaults are seeded by
`ApplySilentModeDefaults` so a partially-specified block is still unambiguous.
The `tool_confirm` terminals:

- `deny` — every gated call is auto-denied, without consulting the judge.
- `allow` — a call with **no** hard safety reason runs unattended; a call
  carrying a **hard** reason (canonical or not) is **escalated to the strict
  judge**, which decides — and its ALLOW executes, canonical included (D4). A
  fired control is never auto-executed by the permissive mode *alone*.
- `judge` — the strict judge decides, with **final authority** (D4): a strict
  ALLOW executes — canonical hard reasons included — and every other outcome
  (a deliberate DENY, CONFIRM, a missing judge, an error/timeout, an
  unparseable verdict) auto-denies carrying the reasoning, with the
  `Justification` prefixes distinguishing a judge DENY from the fail-closed
  causes so the audit trail says WHO refused the call.

### D4. Revisited — the canonical backstop is scoped to the interactive paths; the silent `judge` terminal delegates final authority over canonical reasons to the judge

> This decision **reverses** the original D4 of this ADR ("no relaxation: the
> backstop holds in every mode — silent `judge` auto-denies a canonical
> ALLOW"), which was drafted before the axis work settled. No release ever
> shipped the original behavior.

On the **interactive** paths the ADR-026 deterministic backstop is unchanged
and absolute: a **canonical** hard reason (`isCanonicalHardReason`, keyed off
the typed `JudgeOutcome.ReasonCode` — a fired control such as the blocklist,
the flowsh criteria, SSRF, a symlink escape, or an unassessable input) is never
auto-approved. In `standard` a canonical escalation is always a card; in
`assisted` a strict ALLOW on a canonical reason is overridden to a
**confirmation** (D5) — the user remains the final authority (SECURITY.md
AI-agent rule 2, scoped to these paths).

In **silent `judge` mode there is deliberately NO canonical backstop**. The
operator selected unattended operation *and* left `tool_confirm` on its default
`judge` terminal: they explicitly delegated the final decision to the strict
judge, and no human is available to overrule it either way. Between the two
automatic outcomes the code makes the delegated one win: a strict ALLOW —
including on a canonical hard reason — **executes**, and the executed decision
is fully audited (the `autonomy_decision` event carries the judge's
justification for allowing a fired control, D6). Every other outcome still
auto-denies fail-closed.

**Why the reversal is sound:**

- The original D4's own rationale — "no human to confirm" — cuts both ways: if
  there is no human to confirm an ALLOW, there is no human to *review* a
  forced denial either. Overriding the judge's ALLOW to a deterministic denial
  means a canonical control's **false positive** collapses the whole unattended
  run at the first escalation, with the agent powerless to proceed — the
  operator's explicit delegation is silently vetoed by a deterministic layer
  the operator was told the judge now controls.
- The delegation is **explicit and opt-in twice over**: the operator must set
  `autonomy_mode: silent` AND leave `tool_confirm.mode` on `judge` (or pick
  `allow`, which still routes hard reasons through the judge). An operator who
  wants a hard floor without a human picks `tool_confirm.mode: deny` — every
  gated call denied outright, no judge consulted.
- The deterministic floor below the funnel is untouched (D2): `deny` groups,
  containment, symlink detection, and the flowsh criteria still fire and still
  escalate; what changes is only who answers the escalation. A canonical reason
  can never *silently* pass — it reaches the judge only as a flagged,
  hard-severity escalation with the fired control named, and the ALLOW that
  executes it is recorded with the judge's reasoning (D6).
- Accepting the judge's authority here is consistent with how the same
  operator's `assisted` posture already treats **non-canonical** hard reasons
  (ADR-026): the strict judge is the single evaluation point; SECURITY.md rule
  2 keeps only the canonical set human-locked **on interactive paths**, where a
  human actually exists to be the authority.

The split behavior is pinned by
`TestSilentMode_JudgeTerminalCanonicalAllowExecutes` (silent: the canonical
ALLOW executes; assisted: the same reason still forces a confirmation — rule 2
holds interactively), so neither path can drift unnoticed.

### D5. The assisted posture: `VerdictDeny`, the assisted-DENY terminal, and the canonical-ALLOW card

`assisted` is the former Smart Approve, folded into the axis — the strict judge
automatically evaluates **every escalated call** through the unified funnel
(ADR-026: hard-bias, single funnel, `deny` groups never judged). Three
decisions define its terminals:

1. **`VerdictDeny` is a first-class verdict.** The strict-judge contract (sp4rk
   `tools/judge.go`) distinguishes a **deliberate rejection** — the judge
   positively assessed the call as dangerous — from `CONFIRM` ("cannot decide,
   a human must review"). Denial-shaped tokens (`DENY`, `BLOCK`, `REJECT`,
   `DISALLOW`, `DISAPPROVE`…) parse to `VerdictDeny` and never to `ALLOW`, so a
   negated rejection can no longer masquerade as an allow. *Justification:* a
   positive danger assessment is actionable information, not uncertainty; and
   substring-negations ("DISALLOW") must never bypass the gate.
2. **A strict DENY terminates the call — the assisted-DENY terminal.** The tool
   is not executed, `ConfirmFunc` is never invoked (no card opens), and the
   `ToolResult` carries the judge's justification ("Denied by strict judge
   (assisted mode): …") so the agent can adapt. The decision rides the audit
   channel as an `autonomy_decision` event with `kind: assisted_deny` (D6).
   *Justification:* the judge positively assessed the call as dangerous, so
   there is nothing for a human to weigh in on — opening a card would be an
   approval-fatigue vector (ASI09), asking the user to second-guess a verdict
   that already says *no*; and silently swallowing the verdict would hide an
   autonomous decision (ASI10).
3. **A canonical ALLOW still becomes a card.** The deterministic backstop is
   preserved verbatim on the assisted path: a strict ALLOW on a **canonical**
   hard reason is overridden to a **user confirmation** with
   `DisableJudge=true` — the user remains the final authority over fired
   controls and unassessable inputs (SECURITY.md rule 2, interactive scope).
   Non-canonical hard reasons (e.g. the flowsh ⊤ limitation) may be positively
   cleared by a strict ALLOW, exactly as in ADR-026. *Justification:* a human
   IS present in assisted mode; rule 2 exists precisely for this path. The one
   deliberate exception is the silent `judge` terminal (D4), where the
   operator's delegation replaces the human.

`CONFIRM` — and a missing, failing, or unparseable judge — stays a user
confirmation with the advisory Ask Agent action disabled (`DisableJudge=true`):
the advisory judge must not re-decide what the strict judge already ran on.
Without a strict-judge provider configured, assisted degrades to cards
(standard behavior) — fail-safe, never fail-open.

### D6. Every automatic decision emits a persisted `autonomy_decision` event

An automatic decision is a gate a human would otherwise have answered. It MUST
leave an auditable, **non-blocking** trace (ASI10: "the trajectory must be
reconstructable"). A structured `autonomy_decision` session event carries
`kind` (`tool_confirm` | `assisted_deny` | `step_limit`), `mode` (the autonomy
posture that decided: `silent` | `assisted`), `policy` (the posture's
sub-policy), `verdict`, `tool`/`source`/`reason`, `justification`, and (for
`step_limit`) `category` + `current_step`/`max_steps`:

- **`tool_confirm` decisions** are emitted by the tool registry itself, through
  an `AutonomyDecisionObserver` wired once on the shared builder registry and
  inherited by every per-session clone (exactly like the existing
  `JudgeObserver`). It fires on *every* terminal — deny, unattended allow, and
  judge-decided allow/deny — so nothing is skipped. A canonical ALLOW executed
  by the silent `judge` terminal (D4) is recorded with the judge's
  justification, which is what makes that delegation auditable.
- **`assisted_deny` decisions** (the strict judge terminating a
  confirmation-gated call in the assisted posture before any card opened) ride
  the same observer and the same channel — the audit trail covers BOTH
  automatic postures.
- **`step_limit` decisions** are emitted by the backend's
  `ResolveSilentStepLimit`, which already owns that boundary.
- Both route through `Manager.EmitAutonomyDecision` to the session's live
  emitter (falling back to the raw pipeline), and the event is **persisted**
  (role `autonomy_decision`) unlike the transient judge-phase telemetry — the
  record must survive a reload.
- The frontend renders it as a non-blocking `status` service notice; the payload
  rides in metadata so `reconstructContent` rebuilds byte-identical text on
  reload. It is deliberately **not** a pending-action card: there is nothing to
  answer, and the ordinary HITL indicators and sound cues stay silent.

> **Rename note (pre-release).** The event was renamed from `silent_decision`
> to `autonomy_decision` — and extended with the `assisted_deny` kind — before
> any release shipped it, so no compatibility alias exists. Development
> databases created mid-cycle may still carry persisted rows with the old
> role `silent_decision`; the reloaded role map no longer recognizes them and
> such rows render as raw content. No migration is provided: no released
> build ever wrote those rows.

## Consequences

**Positive**

- Unattended operation becomes possible without weakening the deterministic
  floor below the funnel: `deny` groups and every pre-funnel gate are untouched
  (D2).
- The posture is explicit, opt-in, validated, documented, and hot-appliable to
  live sessions (D1) — it is never a hidden default, and one enum value
  replaces two historically-drifting booleans.
- The assisted posture gains a real negative terminal (D5): a judge that
  positively detects danger now *stops* the call instead of bouncing it to an
  approval-fatigued human.
- Every automatic decision is reconstructable after the fact (D6), satisfying
  ASI10 with a durable receipt chain rather than a transient status label —
  including the D4 canonical-ALLOW executions, which carry the judge's
  justification for allowing a fired control.
- The interactive canonical backstop (rule 2) is preserved verbatim on the
  standard and assisted paths (D4, D5) — where a human exists to be the
  authority — and pinned against drift by a real-judge test.

**Negative / risks**

- In silent `judge` mode, a wrong or manipulated strict-judge ALLOW can execute
  a call carrying a fired canonical control with no human in the loop (D4).
  This is the accepted cost of the explicit delegation; mitigations: the
  posture is opt-in and reversible at runtime, `tool_confirm.mode: deny`
  provides a judge-free hard floor for unattended runs, the deterministic
  pre-funnel gates still fire (a `deny` group or an out-of-funnel block is
  never judged), and every executed decision is audited with the judge's
  reasoning (D6) so a post-hoc review can reconstruct exactly what the judge
  waived.
- `tool_confirm.mode: allow` can execute a run of mutating calls unattended.
  Mitigation: hard reasons always escalate to the judge (D3), and every decision
  is recorded (D6).
- A canonical control's **false positive** no longer collapses an unattended
  `judge`-terminal run (the motivation for D4) — but the same false positive
  still can on the interactive paths, where it becomes a card. Accepted: there
  the human can simply click Allow.
- An unattended session can persist many `autonomy_decision` rows. Accepted: the
  rows are bounded by the step/loop caps, and the audit value outweighs the
  volume.

## Alternatives Considered

- **A global "no confirmation" kill switch.** Rejected: it removes the gates
  themselves, not just the prompts, and offers no per-prompt control or audit
  trail — the exact failure ASI09/ASI10 warn about.
- **Timeouts on confirmation.** Rejected: "confirmation blocks until the user
  responds (no timeout)" is an explicit ASI09 rule; a timeout would silently
  convert a pending human decision into an automatic one with no record.
- **Keep the canonical backstop in silent `judge` mode (the original D4).**
  Considered, drafted, and **rejected** in favor of the revisited D4: with no
  human present, overriding the judge's ALLOW to a deterministic denial
  silently vetoes the operator's explicit delegation and collapses unattended
  runs on canonical false positives, while protecting nothing the operator did
  not already agree to delegate (the floor below the funnel is untouched and
  `deny` mode exists for hard-floor unattended runs). The executed ALLOW is
  audited with the judge's justification, so the delegation stays
  reconstructable (ASI10).
- **Downgrade a strict DENY to a confirmation card (no assisted-DENY
  terminal).** Rejected: it converts a positive danger verdict into an
  approval-fatigue prompt (ASI09) and hides an automatic decision the audit
  chain should record (ASI10).
- **Treat DENY and CONFIRM identically in the parser (no `VerdictDeny`).**
  Rejected: uncertainty ("cannot decide") and a positive danger assessment are
  different facts with different correct terminals; and negated tokens
  ("DISALLOW") must never parse as ALLOW.
- **Keep two booleans (`smart_approve` + `silent_mode.enabled`).** Rejected for
  D1: two booleans cannot express three mutually exclusive postures without
  implicit precedence, invite a fourth undefined combination, and make both the
  UI and the docs lie about what is live.
- **Transient (unpersisted) `autonomy_decision` events.** Rejected: ASI10 requires
  the trajectory to be reconstructable; a live-only notice vanishes on reload and
  the audit trail with it.
- **Emitting from a backend wrapper instead of a registry observer.** Rejected:
  the `tool_confirm` decision happens deep in the registry on the executor
  goroutine, where the only host hook is an observer; wrapping would not see the
  `deny`/`allow` terminals.

## References

- [SECURITY.md](../../SECURITY.md) — ASI09/ASI10 rules and the Autonomy section
- [specs/architecture/security-model.md](../architecture/security-model.md#silent-mode-unattended-operation)
- [specs/contracts/event-catalog.md](../contracts/event-catalog.md) — `autonomy_decision`
- [ADR-026](./026-smart-approve-unified-funnel.md) — the unified confirmation funnel silent mode sits inside, and the interactive-scope backstop contract
- [ADR-052](./052-flowsh-command-analysis.md) — the deterministic floor and the canonical code contract
