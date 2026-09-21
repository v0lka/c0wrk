# ADR-056: RESEARCH always on — global pack seeding at startup and reordered discovery

## Status

Accepted

## Context

[ADR-051](./051-research-pack-reconciliation.md) solved the "seeded `study-paper` was shadowed by a same-named `~/.agents` copy" failure by moving the c0wrk packs INTO the project (`<workspace>/.agents/{skills,agents}`), reconciled on research enable and on every switch to a research-enabled project. That decision treated RESEARCH as an opt-in, project-local mode and deliberately refused to touch the global discovery order (its Alternatives considered and rejected the reorder).

The product direction has since changed on three fronts, and the ADR-051 model does not fit it:

1. **RESEARCH is no longer opt-in.** The feature is always available for every real project; there is no per-project toggle, no `ProjectInfo.ResearchRoot` persistence, and no `EnableResearch`/`DisableResearch` lifecycle. A per-project reconciliation has nothing to hang off — there is no "enable" moment and no switch-time revalidation trigger that a mere navigation provides.
2. **The packs are a general c0wrk capability, not a research-workflow artifact.** They must be discoverable in every project and session the moment the app is up, without a project having to be opened first.
3. **The shadowing problem ADR-051 worked around is better solved at its root.** ADR-051 concluded only the project-local copy outranks `~/.agents` (first-wins). The real defect was the *ordering* of `config.defaultSkillDirs`/`defaultAgentDirs`, which placed the user's portable `~/.agents` library above the c0wrk-managed `~/.c0wrk/.agents` directory. c0wrk-owned packs should win discovery over a stale user copy by default.

## Decision

**1. RESEARCH is always on for real projects; the research root is unconditionally `<workspace>/.research`.** There is no per-project toggle. `ProjectInfo.ResearchRoot` and `ProjectInfo.IsResearch` are removed (the legacy `research_root` column is retained physically — no read or write, and no destructive DROP migration is run); `GetResearchStatus` reports `enabled=true` with `research_root = config.ProjectResearchPath(workspacePath)` for a real project, and a neutral empty state (`enabled=false`, no root) for the No-Project pseudo-project, which has no workspace. Every root-deriving path (`GetResearchGraph`, `GetResearchNextStep`, the mutation guards, the papers RPCs, the watcher setup) computes the root from the workspace path unconditionally; the former "RESEARCH not enabled" empty branches are gone. `EnableResearch` and `DisableResearch` (and the toggle-only `ResearchSeedResultDTO` / `SeedResult` field) are removed from the RPC surface and the frontend.

**2. One global seed at startup.** `FrontendAPI.seedGlobalPacks()` seeds every c0wrk-owned pack into the global agent directories once per launch — the seven `research-*` skills and the `study-paper` skill into `config.SkillsDir(agentDir)` (`~/.c0wrk/.agents/skills`), and the research Subagent Profile into `config.AgentsDir(agentDir)` (`~/.c0wrk/.agents/agents`). It runs from `NewFrontendAPI` **before** the skill/agent directory watchers are created, so the freshly created directories are themselves watched on the same launch, and it serializes on `researchSeedMu`, invalidating the skill and agent caches afterwards. The non-destructive classification contract is unchanged (content-hash against the embedded pack, `.seed-version` markers, staged temp-dir + atomic rename; user-owned marker-less directories are never clobbered; failures are per-pack and logged, never fatal). `reconcileResearchPacks` is deleted and `SwitchProject` no longer seeds anything.

**3. Discovery is reordered so the c0wrk global directory outranks the user's `~/.agents`.** `config.defaultSkillDirs` and `config.defaultAgentDirs` now list `~/.c0wrk/.agents/{skills,agents}` ABOVE `~/.agents/{skills,agents}` (order = precedence). The untouched project-local `<workspace>/.agents/{skills,agents}` directory is still prepended per-session with the highest priority, so the chain is project-local → c0wrk global → user `~/.agents`.

**4. The literature helper resolves globally.** `RunPaperLiterature` looks up `literature.py` under `config.SkillsDir(agentDir)/study-paper/scripts/` — the same global seed location as every other pack — and degrades explicitly to the `no_script` status when it is absent.

## Consequences

- Every pack is present and discoverable in every project and No-Project session the instant the app is up; there is no seeding race with project opening and no per-project copy to version-pin, reconcile, or garbage-collect.
- The shadowing failure ADR-051 fixed project-locally is now fixed globally: a stale user-owned `study-paper` (or any same-named `research-*` skill / research agent) in `~/.agents` no longer outranks the c0wrk-managed copy. The cost is that c0wrk's global directory now takes precedence over the user's portable `~/.agents` library for EVERY skill and agent (not only the seeded pack): a user skill that collides by name with a c0wrk-seeded one is shadowed. This inverts ADR-051's "the user owns their home-level library" default; the user still has an escape hatch — a project-local `<workspace>/.agents` copy (or an explicitly configured higher-priority dir) still wins, and nothing in `~/.agents` is modified.
- The research root is a simple, unconditional function of the workspace (`<workspace>/.research`); custom per-project roots are no longer supported, so a project that had persisted a non-default root loses it (the legacy column is retained physically but is no longer read or written; no destructive DROP migration is run).
- `RunPaperLiterature`'s helper resolution no longer depends on which project is requesting it; a machine that predates the global seed and has only project-local copies reports `no_script`.
- RESEARCH remains independent of the `experimental.enabled` switch: that switch still gates only the E2S execution mode.
- Always-on RESEARCH has the intentional consequence of making the research **router hints unconditional for every real project**: the router context always carries the advisory `formatResearchRouterHints` mapping (and the `## Research Context` block when a snapshot is present), so every real project pays the extra prompt tokens and carries a mild classification bias toward the `research-*` skills — even with an empty research root. The alternative (gating the hints on evidence of research work) was declined in favour of the simple always-on rule; see `specs/domains/research.md`.

## Alternatives Considered

- **Keep ADR-051's project-local reconciliation and add always-on activation.** Rejected: reconciliation needs an activation trigger, and with no toggle the only remaining trigger (every project switch) writes a project-local copy per project — the duplication ADR-051 itself rejected, now multiplied. It also leaves the packs unavailable until a project is opened.
- **Keep the global seed but do NOT reorder discovery (leave `~/.agents` above `~/.c0wrk/.agents`).** Rejected: it reintroduces ADR-051's exact failure — a stale same-named `~/.agents` copy silently shadows the seeded pack.
- **Seed into `~/.agents/skills` directly.** Rejected (unchanged from ADR-051): c0wrk must not write outside its own directories (`~/.c0wrk`, the workspace).
- **Keep a per-project research root override.** Rejected: with RESEARCH always on there is no user-facing setting that would author one, and the single canonical root keeps the watcher, the containers, and the frontend loaders keyed off one path.

## Related

- Supersedes [ADR-051](./051-research-pack-reconciliation.md) in full (its project-local seeding decision, its removal of the global seed, and its project-local literature helper resolution are all reversed) — and therefore re-supersedes the global-seeding decision (decision 2) of [ADR-050](./050-papers-library.md), whose vendoring and library-location decisions still stand.
- [../domains/research.md](../domains/research.md) - RESEARCH always-on flow, startup global seeding, and the discovery-precedence invariant
- [../domains/papers.md](../domains/papers.md) - the paper pack and the global seeding contract
