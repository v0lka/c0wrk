# ADR-114: Sidebar Pack Gates — Launch Reconcile and Toggle Serialization

## Status

Accepted

## Context

ADR-113 introduced the `research.enabled` / `specs.enabled` sidebar surface gates with launch-as-skip semantics: `seedGlobalPacks` "consults the gates at launch: a disabled surface's packs are not seeded", and a disabled surface "costs nothing at launch (no seed writes)".

Two gaps emerged against that wording:

1. **Drift never converges.** The toggle-time unseed is fail-soft at two granularities — per entry (a plain file squatting a pack name, a locked directory) and per pack — and a launch or a toggle can be interrupted mid-pass. With launch as a pure skip, leftover copies of a disabled surface stay on disk (and in the router catalog and the `/`-completion lists) until someone hand-deletes them: the config says off, the disk disagrees, indefinitely.
2. **Unserialized concurrent pack writers.** The toggle RPCs can complete while `Init`'s launch seed is still running (`seedPublished` is raised before `Init` calls `seedGlobalPacks`, so an early RPC genuinely races the seed). Without serialization, a launch seed carrying a pre-toggle gate snapshot can resurrect packs a completed toggle just unseeded — or unseed packs a completed enable just seeded.

The fix also needed a lock discipline that never inverts the established `configMu` nesting: the RPCs persist under `configMu` and apply inside the same call.

## Decision

This ADR supersedes [ADR-113](./113-sidebar-surface-gates.md) and restates its decisions unchanged, then deepens the launch-side behavior and adds the serialization contract.

Restated from ADR-113:

- The config gates `research.enabled` / `specs.enabled` (pointer bools, nil = on) gate each surface's sidebar tab AND its globally-seeded packs (`research-*` + `study-paper` + the research Subagent Profile; `spec-*` + the specs Subagent Profile), as Settings → General toggles under Prompt Suggestions.
- `SetResearchEnabled` / `SetSpecsEnabled` persist AND apply in one beat: a same-value request is a no-op, a failed config write rolls the in-memory value back, success runs the pack side-effect and announces `config:updated`.
- Unseeding is the non-destructive mirror of the seeding classification (`core/seedpack.UnseedTarget`): a copy byte-identical to the current pack (any marker state) or a pack-marked truncated subset is removed; a marker-less diverging copy (user-owned) and a pack-marked diverging copy (a local edit) are preserved, the modified ones named in a visible warning.
- The tab strip follows the gates live through `sidebarPacksStore`, with the remembered-tab fallback the indexing gate established; the research root, the specs root, and every read RPC stay untouched.

New in this ADR:

- **Launch reconcile.** `seedGlobalPacks` (once per launch, from `Init`, before the skill/agent watchers start) reconciles every surface with its persisted gate: an enabled surface seeds or repairs its packs; a disabled surface is not seeded AND its leftover pack copies are unseeded under the same classification — converging the disk to the config at every launch. In steady state the disabled pass is read-only (every target classifies `Missing`); writes happen only for actual drift. The reconcile is idempotent and preserves user-owned and locally modified copies like every other pass.
- **Serialization.** Every pack pass — the two toggle applies and the launch seed — runs under `researchSeedMu`. The RPCs hold `configMu` and take `researchSeedMu` inside the apply, fixing the nesting `configMu → researchSeedMu` everywhere; the launch seed reads the gates under `configMu` BEFORE taking `researchSeedMu`, so the order never inverts, and nothing inside a seed-lock critical section takes another lock. Each apply writes a gate mirror (`packGateResearch` / `packGateSpecs`) under `researchSeedMu` BEFORE its disk pass, and the launch seed re-reads that mirror INSIDE its critical section, letting it override the pre-lock config snapshot (a nil mirror — no toggle has run this session — leaves the snapshot authoritative). Whatever the interleaving, once the racing calls complete the disk matches the persisted gates: an apply that already persisted and mirrored can never be undone by a launch seed that snapshotted the older config.
- **Fail-soft stays.** Per-entry failures land in the result's `Failed` bucket and are joined into the pack error; per-pack failures are logged; neither is fatal to the toggle RPC or the launch. The persisted gate stays authoritative, and the next reconcile (a toggle cycle or the launch seed) retries failed entries.

## Consequences

- The disk converges to the persisted gates at every launch and after every completed toggle; the only divergence windows are a failed entry (logged, named) and an in-flight pass, both bounded by the next pass.
- A disabled surface's launch cost is a stat-plus-hash scan of its pack targets — no writes in steady state — refining ADR-113's "no seed writes" consequence to "no seed writes, a read-only reconcile".
- A failed unseed entry (a squatter file, a locked directory) delays but never blocks convergence; user-owned and locally edited copies survive every reconcile, the edited ones named in a warning.
- The residual exposures ADR-113 recorded carry forward: an in-flight task using a seeded skill when the toggle flips loses the skill's files mid-task (the same exposure as hand-deleting), and the research/specs roots plus read RPCs are never touched by a toggle.
- ADR-113's amendment of [ADR-056](./056-research-always-on-global-seeding.md) / [ADR-105](./105-vendor-spec-skillset.md)'s always-seeded invariant ("seeded while the surface gate is on") carries forward unchanged.

## Alternatives Considered

- Launch as a pure skip (ADR-113's wording) — rejected: with fail-soft removals and interruptible passes, drift becomes permanent; a config that says off must mean off on disk.
- Failing the toggle RPC on any unseed error (retry at the RPC) — rejected: a hostile or squatted entry must not wedge the toggle; the persisted gate is authoritative and the launch reconcile retries.
- A wider Init-wide pack mutex, or taking `researchSeedMu` before `configMu` — rejected: the RPC shape fixes the nesting (persist under `configMu`, then apply); inverting it risks deadlock, and the gate mirror already makes the launch seed's pre-lock snapshot harmless without any extra lock.
