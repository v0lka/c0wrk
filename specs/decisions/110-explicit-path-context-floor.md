# ADR-110: Explicit-Path Context Floor

## Status

Accepted

## Context

The planner sizes the context on two paths. Under fit, `planTargetContext` held
the RAM tier to the `-fitc` floor (`DefaultFitMinContext`, 65536 — ADR-067):
fit sizes the offload, never the context, and a fit run that cannot reach its own
minimum aborts rather than degrading. On the explicit path — a pinned offload, a
device list or split mode (ADR-107), or an explicit fit-off — the plan serves the
raw RAM tier: a 32 GiB host tiers at 32768 and got `-c 32768`.

The combined memory gate prices every machine at a different figure.
`gateContext` takes the operator's exact override or the `-fitc` floor — never
the tier — because the gate answers "can this machine serve the model at all",
and the smallest context c0wrk is willing to serve IS the floor. So on the
explicit path the gate and the planner disagreed by construction: the gate
admitted a 32 GiB machine at 65536 and the plan then served 32768. The gate's
verdict was not a promise the launch kept.

The under-delivery is not a cosmetic shortfall — it is the pinned model's
most-reported failure in its purest form (empty and truncated answers, the
documented workaround for which is `-c 65536`), concentrated on exactly the
machines an operator pin targets: small hosts beside one fast card. ADR-111's
measured CUDA tuple is what makes the fix affordable there — the floor prices
against an honest budget (the 5060 Ti's 14402 MiB holds the 65536/f16 shape at
11836 MiB), so holding the pin to the floor serves a real launch instead of
trading a silent failure for a loud one on the machines that motivated the pin.

## Decision

**D1: both planner-sized paths hold the RAM ladder to the same 65536-token
floor.** `planTargetContext` returns `max(RAM tier, floor)` on the fit path (the
resolved `-fitc` value: the override, else the default) and on the explicit path
(always `DefaultFitMinContext` — the `-fitc` flag is not emitted when fit is
off, so its override has no launch meaning there). An exact `Context` pin wins
unchanged on both paths, including below the floor: the operator asked for that
window by name. `shrinkTarget` reads the same figure, so the rung-2 relaxation
compares against the plan's true target (behaviour-neutral: the context rungs
fire exactly when the tier exceeds the floor, as before), and
`insufficientForPlan`'s existing floor clamp now prices every explicit-path
refusal at the same figure the gate admitted. When the floor lifts the tier, the
plan's `Notes` say so — one operator-facing sentence naming the tier, the floor
and why a smaller window is refused. The `gateContext` comment states the
promise two-sided: a machine the gate admitted at 65536 is never planned beneath
it.

**Alignment with ADR-107.** The floor changes what a pinned plan TARGETS and
what its refusal PRICES — not who may relax a pinned shape. The KV-precision
escalation stays allowed on the explicit path (it serves the pinned residency:
a pinned 12 GiB card escalates to q8_0 or q4_0 and loads); the degradation
ladder stays out of reach for pinned operators (an 8 GiB card refuses with the
typed both-pools `InsufficientMemoryError` at the floor — it is not answered
with a smaller-tier shape the operator did not pin). The pinned 5060 Ti serves
65536 at f16 (11836 ≤ 14402); the 12 GiB-class cards stay green (q8_0 at 10128
against 10752; a 11520 MiB reading lands on q4_0 at 9104 against 9984); the 8
GiB card refuses honestly at 65536.

## Consequences

- Every explicit-shape plan on a machine whose tier sat below 65536 (RAM ≤ 35
  GiB: tiers 8192/16384/32768) now serves 65536 — a larger KV cache on the
  accelerator (or a larger CPU-only footprint) and a larger compaction and
  prompt-cache working set host-side. No gate verdict moves: the gate already
  priced exactly this figure, so the plans catch up to what the gate promised
  rather than the machine becoming harder to admit.
- Fit-path plans are unchanged. The RAM-only fallback (`ramOnlyPlan`) is
  deliberately unchanged — it has no measured budget to serve against and keeps
  the raw tier as its documented estimate; its gate still prices the floor, so
  the gate/plan disagreement survives there by that estimate posture. Aligning
  it is a separate decision if the estimate ever becomes launch-authoritative.
- An operator `-fitc` override beside an explicit shape remains dead config, as
  before — the explicit path floors at the default; an operator who wants a
  different window pins `ContextExact` (honoured verbatim, below the floor
  included).
- The `Notes` trail grows one entry on pinned small-RAM plans (the applied
  floor); tiers already at or above 65536 gain no note and no behaviour change.
- A below-floor refusal now names the floor's arithmetic (`insufficientForPlan`
  prices the floored target), so a "closest shape" report describes a launch
  c0wrk would actually serve.

## Alternatives Considered

- **Floor the explicit path with the resolved `FitMinContext` override too.**
  Rejected: `-fitc` is a fit-pass flag with no launch meaning when fit is off;
  honouring it would give a fit-only knob silent authority over explicit shapes
  and would let an override below 65536 reintroduce the failure the floor exists
  to prevent.
- **Serve the tier and require an explicit `ContextExact` for more.** Rejected:
  the default must be usable without a manual rescue, and the failure is silent
  — a server that loads, answers and truncates reports nothing. That is exactly
  the failure mode `DefaultFitMinContext` exists to prevent.
- **Extend the floor to `ramOnlyPlan`.** Deferred: the fallback's whole posture
  is "a worse estimate than a measured budget but better than nothing", and it
  has no budget to verify the floored shape against. Revisit only if the
  estimate becomes launch-authoritative.
- **Raise the auto target on measured device headroom instead.** Already
  rejected in ADR-111 D4: headroom buys KV precision, not context reach.

## Related

- [Embedded LLM](../domains/embedded-llm.md) — the domain spec ([Memory plan](../domains/embedded-llm.md#memory-plan))
- [ADR-107](./107-operator-selected-offload-devices.md) — the operator device pin whose plans this floor completes
- [ADR-111](./111-measured-backend-memory-profiles.md) — the measured CUDA/Blackwell budgets that make the floor affordable on the measured tuple
- [ADR-067](./067-memory-aware-embedded-llm-provisioning.md) — the fit-path floor, the exclusivity rule and the gate this decision extends
