# ADR-072: Silent-mode `user_confirm` — a fail-closed-CONFIRM terminal policy

## Status

Accepted — amends [ADR-053](./053-silent-mode.md) D3 (a fourth silent-mode sub-policy) and D4 (the silent `judge` terminal's delegation now optionally covers the fail-closed CONFIRM outcome).

## Context

[ADR-053](./053-silent-mode.md) established silent mode (`security.autonomy_mode: silent`) and its `security.silent_mode` sub-policies. Its `tool_confirm: judge` terminal resolves a confirmation-gated call through the strict judge with **final authority**: the judge's ALLOW executes (canonical hard reasons included, ADR-053 D4), and — because no human is available — every other outcome (a deliberate DENY, a spoken CONFIRM, a missing judge, an error/timeout, an unparseable verdict) auto-denies fail-closed.

That auto-denial is the safe default, but it is the *only* terminal for the whole **fail-closed CONFIRM family** (a spoken CONFIRM and the four infrastructure failures: missing judge, error, timeout, unparseable verdict). Two problems follow:

- With `tool_confirm: judge` and no strict-judge provider configured at all, *every* confirmation-gated call auto-denies, so an unattended task cannot proceed past its first gate.
- The only way to keep such a run moving is `tool_confirm: allow`, which is much broader than the problem: it changes how the *whole* terminal is decided, not just what happens when the judge is inconclusive.

Operators asked for two additional terminals for that one family — one that defers to a human (restore the card), one that accepts the risk and runs unattended — without touching the deliberate-DENY and ALLOW terminals, which are not fail-closed and must not become governable.

Three constraints bound the design:

1. **The refinement must be scoped narrowly** — only the fail-closed CONFIRM outcome — or it would silently govern a deliberate danger verdict.
2. **It must be opt-in and loudly documented**, because the permissive value removes the fail-closed floor for exactly the calls the judge could not clear, including canonical hard reasons and including a judge outage.
3. **It must not weaken anything at or below the confirmation funnel** (ADR-053 D2) — `deny` groups and every deterministic pre-funnel gate stay untouched.

## Decision

**D1 — A fourth silent-mode sub-policy, `security.silent_mode.user_confirm`.** It carries one mode from `confirm` | `deny` | `escalate`, defaulting to `deny`. It refines the `tool_confirm` terminal and is live only while `security.autonomy_mode: silent`, inert elsewhere — the same scope as the other three sub-policies (ADR-053 D1/D3). The "three sub-policies" count recorded in ADR-053 D3 — and in [ADR-060](./060-remove-post-task-review-prompt.md) item 3, which had dropped D3's set by one — is thereby amended from three to four.

**D2 — Scope: the fail-closed CONFIRM outcome only.** The sub-policy governs exactly the outcomes the judge terminal produces when it did **not** return ALLOW and did **not** positively reject: a spoken CONFIRM, a missing judge, a judge error/timeout, or an unparseable verdict. It never governs a deliberate judge DENY (which always takes the non-negotiable fail-closed terminal) nor a judge ALLOW (which always executes). This narrows and extends ADR-053 D3: the "every other outcome auto-denies fail-closed" sentence there is now precisely the `user_confirm: deny` **default**; the deliberate-DENY outcome is unaffected by this ADR.

**D3 — The three terminals.**

- `deny` (default) — auto-denies the CONFIRM outcome with its reasoning, byte-for-byte the behavior before this ADR; any empty or unrecognized value ranks as this default.
- `escalate` — falls back to the blocking confirmation card (`confirmAndExecuteWithOptions`), so a human answers. The advisory judge action is disabled for a **hard** reason (`DisableJudge=true`) so a fired control is never weakened by an advisory judgment. No `autonomy_decision` is emitted: a human, not the registry, decided. It is the least permissive value — the only one that can *block* an unattended run.
- `confirm` — executes the call unattended (D4) and audits it as an allow (D5).

**D4 — The permissive value is unsafe, opt-in, canonical-inclusive, and judge-outage-fail-open.** `confirm` executes the CONFIRM outcome **including when it carries a canonical hard reason** — a fired control or an unassessable input — because the refinement sits below the interactive canonical backstop (ADR-053 D4). And because a missing/failing judge is itself one of the CONFIRM causes, a **judge outage becomes a fail-open** for exactly the calls the operator delegated. It is therefore the **unsafe**, most-permissive value: it removes the fail-closed floor for the calls the strict judge could not clear. It is opt-in twice over (the operator must select `autonomy_mode: silent` **and** the `confirm` value), gated in the Settings UI behind a danger modal, and flagged by a persistent warning while stored. This extends ADR-053 D4: there the delegation covered the judge's ALLOW; here the operator may additionally delegate the judge's *failure to clear* a call.

**D5 — Every `confirm` execution is audited.** An executed CONFIRM outcome emits the same persisted, non-blocking `autonomy_decision` event as the other silent terminals (ADR-053 D6) with `kind: tool_confirm`, `mode: silent`, `policy: user_confirm` (the `autonomyDecisionPolicyUserConfirm` constant), `verdict: allow`, and a justification that names the policy and carries the waived fail-closed reasoning — so a post-hoc review reconstructs what executed and why (ASI10). `escalate` emits nothing (a human decided); `deny` keeps the existing fail-closed denial event.

**D6 — Direction-asymmetric delivery (tightening).** The refinement takes part in the per-task pinning/tightening rule (ADR-053 D1): `silentUserConfirmPermissiveness` ranks the values `confirm` > `deny` > `escalate` (unknown/empty as the `deny` default), and `silentModeAtLeastAsStrict` admits a same-mode sub-policy change to a running clone only when it is no more permissive. A Settings save that loosens the value is ignored on a task already running; a tightening (`confirm` → `deny`/`escalate`, or `deny` → `escalate`) reaches it at once. The judge memo (ADR-053 D6) is deliberately **not** keyed on the refinement: a replayed fail-closed CONFIRM resolves through the *current* policy, so a `confirm` → `deny` tightening turns a memoized CONFIRM from an unattended execution into a fail-closed denial.

**D7 — Config surface.** `backend/config` owns the enum (`SilentUserConfirmConfirm` / `SilentUserConfirmDeny` / `SilentUserConfirmEscalate`), validates it in `ValidateSilentMode` (an invalid value errors naming `security.silent_mode.user_confirm.mode`), and seeds the `deny` default in `SilentModeDefaults` / `ApplySilentModeDefaults`; `core/tools` mirrors the enum and implements the terminal in `silentConfirmTerminal`. The builder adapter (`backend/configadapter.go`) forwards the value verbatim, and the runtime Settings RPC (`UpdateSecuritySettings` → `silentModeToResponse` / `responseToSilentMode`) defaults-and-validates a partial payload. A config that predates this ADR carries no `user_confirm` key and loads at the `deny` default — the pre-ADR behavior is preserved with no migration.

## Consequences

**Positive**

- An unattended run with `tool_confirm: judge` and no judge provider can now proceed (`confirm`) or hand the decision to a human (`escalate`) instead of auto-denying every gated call.
- The fail-closed default is unchanged (D2/D3): a config without the key, and the empty/unrecognized value, behave byte-for-byte as before.
- The deliberate-DENY terminal stays non-negotiable (D2) — no `user_confirm` value can auto-approve a positive danger verdict.
- Every permissive execution is audited (D5), and a tightening reaches a running task at once (D6).

**Negative / risks**

- `confirm` executes a canonical hard reason unattended (D4) and fails open on a judge outage — the accepted cost of the twice-opt-in delegation. Mitigations: the default is `deny`, the value is gated by a danger modal and a persistent warning, the deterministic floor below the funnel is untouched (a `deny` group is never judged; the pre-funnel gates still fire), and every execution is audited (D5); see `SECURITY.md` "Known Risks & Accepted Trade-offs".
- A configuration that sets `confirm` and then has the judge provider removed silently loses its fail-closed floor for the delegated calls; the audited executions are the after-the-fact signal.

## Alternatives Considered

- **Fold the CONFIRM terminals into the `tool_confirm` enum.** Rejected: `tool_confirm`'s values (`judge`/`allow`/`deny`) describe *how the whole terminal is decided*, while the CONFIRM refinement only fires *inside* the `judge` terminal — folding them would conflate "who decides" with "what happens when the decider is inconclusive" and explode the enum.
- **A single boolean ("run fail-closed CONFIRMs unattended").** Rejected: it cannot express `escalate` (restore the card), which is the safest way to keep an unattended run progressing without accepting the risk.
- **Let the refinement govern the deliberate DENY too (auto-approve on `confirm`).** Rejected (D2): a positive danger assessment is not an inconclusive verdict; auto-approving it would invert the judge's explicit "no".
- **Emit no audit event for `confirm` (treat it like a clean allow).** Rejected (D5): it is precisely the case ASI10 must capture — an autonomous override of a fail-closed verdict — so it must be recorded with the policy and the waived reasoning.
- **Key the judge memo on the refinement.** Rejected (D6): it would freeze a `confirm` execution against a later tightening, so revoking the unsafe value would not reach a running task.

## References

- [ADR-053](./053-silent-mode.md) — silent mode and the autonomy axis (D1), the sub-policies (D3), the canonical-backstop scope (D4), the audit event (D6); this ADR amends D3 (adds the fourth sub-policy) and D4 (extends the delegation to the fail-closed CONFIRM outcome)
- [ADR-026](./026-smart-approve-unified-funnel.md) — the unified confirmation funnel silent mode sits inside, and the interactive-scope backstop this refinement deliberately sits below
- [specs/architecture/security-model.md](../architecture/security-model.md#silent-mode-unattended-operation) — the Silent Mode section
- [specs/contracts/event-catalog.md](../contracts/event-catalog.md) — the `autonomy_decision` event and its `policy` field
- [SECURITY.md](../../SECURITY.md) — the Autonomy section, ASI09, ASI10, and the Known Risks row for this policy
