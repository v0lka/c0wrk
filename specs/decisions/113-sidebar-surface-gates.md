# ADR-113: Sidebar Surface Gates (Research / Specs)

## Status

Superseded by [ADR-114](./114-sidebar-pack-gates-reconcile-and-serialization.md) (the launch-side behavior is deepened — launch now reconciles a disabled surface's leftover copies, and the toggle applies serialize against the launch seed on `researchSeedMu` — with every decision below restated and carried forward there). Historical record below.

## Context

The RESEARCH subsystem and the Specs subsystem are always on: their sidebar tabs render for every real project and their skill/agent packs seed globally at every launch ([ADR-056](./056-research-always-on-global-seeding.md), [ADR-105](./105-vendor-spec-skillset.md)). Both surfaces are workspace-methodology toolkits a user may not need in every project; their seeded skills also occupy the router catalog and the `/`-completion lists unconditionally, with no way to opt out short of hand-deleting the seeded directories — which the next launch reseeds.

Settings → General already carries per-feature master gates with a common shape (pointer-bool config keys, nil = on; a persist-and-apply-in-one-beat RPC with rollback; the surface reads GetConfig and re-hydrates on `config:updated`) — `prompt_suggestions.enabled` ([ADR-088](./088-prompt-suggestions.md)) and `model_profiles.enabled` are the precedents.

## Decision

Each sidebar surface gains a config gate persisted in config.yaml and exposed as a Settings → General toggle placed under Prompt Suggestions:

- `research.enabled` gates the Research surface — the workspace panel's Research tab AND its globally-seeded packs (the seven `research-*` skills, the `study-paper` skill, the research Subagent Profile).
- `specs.enabled` gates the Specs surface — the Specs tab AND its packs (the six `spec-*` skills, the specs Subagent Profile).

Defaults are ON (pointer bools; nil = enabled), so existing configs behave exactly as before. `seedGlobalPacks` consults the gates at launch: a disabled surface's packs are not seeded. The toggle RPCs (`SetResearchEnabled`/`SetSpecsEnabled`) persist AND apply in one beat: enabling seeds the surface's packs, disabling unseeds them. Unseeding is non-destructive by content and mirrors the seeding classification (`core/seedpack.UnseedTarget`): a copy byte-identical to the current pack (any marker state) or a pack-marked truncated subset (an interrupted write) is removed; a marker-less diverging copy (user-owned) and a pack-marked diverging copy (a local edit) are preserved, the modified ones named in a visible warning log. The tab strip follows the gates live through `sidebarPacksStore` (the `indexingSettingsStore` hydration shape), and a remembered research/specs workspace tab falls back to the layout's primary tab when its gate is off — the exact contract the indexing gate established for the Search tab.

The gates hide the UI surface and remove the seeded pack copies only. The research root (`<workspace>/.research`), the specs root (`<workspace>/specs`), and the backend RPCs stay untouched: disabling is a frontend-visibility plus pack-on-disk concern, never a data deletion, and no persisted research/specs artifact is removed by a toggle.

## Consequences

- A disabled surface costs nothing at launch (no seed writes) and stays disabled across restarts; an enabled one reseeds idempotently on re-enable and at the next launch.
- User-authored and locally edited copies survive disable cycles; their skill entries stay discoverable until the user removes them (the warning names every kept, edited copy).
- An in-flight task using a seeded skill when the toggle flips loses the skill's files mid-task — the same exposure as hand-deleting; the orchestrator rediscovers the catalog at the next task entry.
- This ADR amends ADR-056/ADR-105's "always seeded" invariant to "seeded while the surface gate is on" — the always-on roots, RPC surfaces, and discovery precedence are unchanged.

## Alternatives Considered

- Per-project toggles — rejected: the packs are global and the surfaces are workspace-methodology features; per-project state would fork the pack-location model ADR-056 just consolidated.
- Hiding the tabs without unseeding — rejected: removing the skills from disk (and thus from the router catalog and the `/`-completion lists) is the feature; leaving the catalog entries would keep the router bias and the completion noise.
- Deleting every marked copy including locally edited ones — rejected: it silently destroys user edits; preserve-plus-warning keeps the non-destructive seeding invariant intact across enable/disable cycles.
