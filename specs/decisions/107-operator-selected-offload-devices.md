# ADR-107: Operator-Selected Offload Devices

## Status

Accepted

## Context

On a heterogeneous multi-GPU machine (the motivating pair: an RTX 5060 Ti 16 GiB beside an RTX 2060 6 GiB), llama-server's default layer split uses EVERY device it finds. A layer split buys KV capacity but never token speed — layers execute sequentially at batch 1, so the slower card gates every token's latency and the fast card idles behind it. The operator had no way to say "run on the fast card only": the planner's `Tuning` vocabulary already carried `Devices` (`-dev`) and `SplitMode` (`-sm`) as typed, argv-validated fields (fit-exclusivity, `validateDeviceName`, the closed split set, mtplx refusal — all pre-existing), but `backend/config` deliberately did not expose them, because naming a device the machine does not have yields an unlaunchable server rather than a slower one.

Two further facts forced a design decision rather than a flag flip:

1. **The planner's budgets were built for the full inventory.** `devicePoolMiB` sums every discrete card's total; `normalizeTopology` and `gateBudgetsFor` never intersected the topology with the pin. Pinning one card through the planner vocabulary today would budget the context against 22 GiB of two GPUs while the launch can only draw 16 GiB — an at-load OOM wearing a successful plan.
2. **The load-time refinement is fail-soft by contract.** `Server.effectivePlan` launches the recorded plan whenever a re-plan fails, because "a load must not fail because a driver query wedged". A pin the fresh inventory cannot verify would be silently dropped by that fallback — the launch would run exactly the shape the operator overruled.

## Decision

Expose the device pin as an operator surface, end to end, with three properties:

1. **Typed keys, not environment.** `embedded_llm.tuning.devices` (the `-dev` list) and `embedded_llm.tuning.split_mode` (`-sm`) join the persisted tuning surface, the tuning RPC (DTO, set/reset, fingerprint — fourteen knobs), and the Advanced-tuning UI (a checklist of the measured inventory plus a split-mode selector, hidden under the mtplx engine). No `CUDA_VISIBLE_DEVICES`-style env mechanism: it is CUDA-only, it hides the device from the probe (making the Settings inventory lie), and it puts the choice outside the typed, argv-validated vocabulary the subsystem's security posture is built on.

2. **A truthful pool.** `Plan` filters the topology to the named devices before any budget is derived (`filterTopology`), re-deriving the device budget over only the named pool with the same margin and unified cap. A plan built for `CUDA0` on a two-card machine is byte-for-byte the plan the CUDA0-only machine would get. `SplitMode` alone does not narrow a pool — every card still participates in a split.

3. **Fail-closed verification, with one carve-out in the fail-soft contract.** A pin must verify against a measured inventory: `filterTopology` refuses an unknown or stale name with the typed `UnknownDeviceError` (device, measured inventory, probe stamp), and `effectivePlan` propagates that refusal as a failed load instead of falling back — the recorded plan predates the pin, so the fallback would launch the shape the operator overruled. The carve-out keeps the old contract alive where it is still honest: a wedged or unwired probe falls back to the recorded plan ONLY when that plan itself carries the same pin (a shape a previous load already launched on this machine). Everything else about a wedged probe keeps the old behavior — a load still never fails because a driver query wedged unless an unverified device pin is in force.

## Consequences

- A single fast card can be pinned for the best per-token speed; leaving the keys unset keeps the full multi-card pool for the largest possible context. Both are first-class choices, documented in `config.example.yaml`.
- Pinning devices forces `-fit off` (the pre-existing exclusivity rule), so the planner — not the runtime — sizes the context and layer count for the pinned pool. A 16 GiB card may simply not fit the model at a large context; the refusal is the existing typed both-pools `InsufficientMemoryError` with its arithmetic.
- Device names follow PCI enumeration (`CUDA0`/`CUDA1`); a driver update or hardware swap can rename them. The refusal names the stale device and the probe stamp, and re-probing (install/load probe) refreshes the inventory the UI shows.
- The reset vocabulary grows from twelve to fourteen knobs; the tuning fingerprint covers the new fields through the translated vocabulary, so a whitespace respelling of the same list never raises `reload_required`.
- `Server.effectivePlan`'s "it NEVER fails" contract is now "it fails only for an unverifiable device pin" — the comment states the exception where the code states it.

## Alternatives Considered

- **`CUDA_VISIBLE_DEVICES` on the spawned process.** One env line; the entire planner/gate/fit pipeline stays untouched AND automatically truthful, because the probe no longer sees the hidden device. Rejected: CUDA-only (ROCm/HIP, Vulkan and Metal each have different or no spellings — the subsystem serves all five backends), the device disappears from every probe and support bundle (an opaque machine), and it moves the choice outside the typed tuning vocabulary into spawn-env plumbing.
- **Fail-soft on unknown devices.** Warn in the plan notes and launch all devices. Rejected: the launch would differ from what the operator asked for with no user action behind it — exactly the "naming the wrong device is an unlaunchable server" posture the config layer had already recorded.
- **`-ts`/`--tensor-split` ratios.** Bias weight distribution across a kept split (e.g. 80/20 toward the fast card). Deferred: useful only beside a split the operator chose to keep, and it adds a per-card ratio surface the memory model does not measure. Revisit after real heterogeneous-GPU usage data.
- **Implicit split from the checklist** (1 device ⇒ `none`, 2+ ⇒ `layer`, no separate selector). Rejected in favor of the explicit selector: `row`/`tensor` are real choices, and "one checked device" already pins the offload through `-dev` alone without pinning `-sm`.

## Related

- [Embedded LLM](../domains/embedded-llm.md) — the domain spec (tuning surface, memory plan, launch supervision)
- [ADR-066](./066-embedded-llm-runtime.md) — the runtime and its supervision
- [ADR-067](./067-memory-aware-embedded-llm-provisioning.md) — the memory-aware launch shape and the fit-exclusivity rule this decision extends
