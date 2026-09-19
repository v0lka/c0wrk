# ADR-051: Research pack reconciliation — project-local `study-paper` seeding, no global seed

## Status

Superseded by [ADR-055](./055-research-always-on-global-seeding.md) (RESEARCH became always-on; all three decisions below — the project-local reconciliation, the removal of the global startup seed, and the project-local literature-helper resolution — were reversed by returning to a global seed and reordering discovery so the c0wrk global directory outranks `~/.agents`). Historical record below.

## Context

ADR-050 seeded the vendored `study-paper` skill globally (`~/.c0wrk/.agents/skills`) at startup so it would be "available in every project, independent of RESEARCH mode". Two assumptions behind that decision turned out to be wrong in practice:

1. **A global seed has no priority guarantee.** Skill discovery scans directories in priority order and resolves same-named skills first-wins: project `<workspace>/.agents/skills` → `~/.agents/skills` → `~/.c0wrk/.agents/skills`. The user-level `~/.agents` directory is a portable cross-tool skill library and sits ABOVE the c0wrk-seeded global dir. A user carrying a same-named `study-paper` there (an older portable copy without c0wrk's paper-library conventions) silently shadows the seeded copy in every project that lacks a project-local one. This shipped as a real failure: a RESEARCH-enabled project studied a paper through the stale `~/.agents` copy, the agent answered in chat, wrote nothing to `<research-root>/papers/`, and the Papers panel stayed empty.
2. **Reconciliation ran only at toggle time.** `EnableResearch` seeded the packs, but nothing revalidated them later: an app upgrade that bumped a pack version (or added a pack entry) never reached already-enabled projects until the user manually re-toggled RESEARCH.

Only the project-local `.agents/skills` directory reliably wins the discovery chain — it is prepended per-session with the highest priority.

## Decision

**1. One reconciliation path seeds every c0wrk-owned pack project-locally.** `FrontendAPI.reconcileResearchPacks(workspacePath)` materializes the research skill-pack (seven `research-*` skills), the papers skill-pack (`study-paper`), and the research agent-pack into `<workspace>/.agents/skills` and `<workspace>/.agents/agents`. The non-destructive classification contract is unchanged (content-hash against the embedded pack, `.seed-version` markers, staged temp-dir + atomic rename; user-owned marker-less directories are never clobbered). It runs from `EnableResearch` AND from `SwitchProject` whenever the target is a real project with a persisted research root — app-startup restoration rides along for free because the frontend replays the last active project through the same `SwitchProject` call. Concurrent reconciliations serialize on one seed mutex (`researchSeedMu`).

**2. The global startup seed is removed.** Nothing writes `study-paper` into `~/.c0wrk/.agents/skills` anymore; that directory keeps only whatever the user put there. The project-local copy is the single authoritative seeded location, because it is the only level that outranks a same-named `~/.agents` skill.

**3. The literature helper resolves project-locally.** `RunPaperLiterature` looks up `literature.py` under the REQUESTING project's `<workspace>/.agents/skills/study-paper/scripts/`; a project without the seeded copy degrades explicitly to the `no_script` status.

## Consequences

- The `study-paper` skill becomes RESEARCH-scoped: non-research projects and No-Project sessions no longer receive it from c0wrk (they fall back to whatever the user keeps in `~/.agents`, or nothing). Accepted: the skill's durable artifacts target the research-root library, and the dispatch surfaces that produce them are research-workflow surfaces. The paper LIBRARY itself stays hybrid (readable/watched regardless of the toggle) — only the skill seeding narrowed.
- Pack upgrades propagate on every project switch, so a `CurrentSeedVersion` bump reaches already-enabled projects without a manual re-toggle; missing entries (a pack that gained a skill) are seeded the same way.
- A user's own `~/.agents` copies are never modified — the project-local seed simply outranks them in discovery. A user-owned marker-less `study-paper` inside a project workspace is preserved untouched (and keeps winning discovery for that project).
- `EnableResearch`'s `SeedResult` DTO now merges the research and papers skill names into shared buckets; agent-pack outcomes remain log-only.

## Alternatives Considered

- **Reorder `config.defaultSkillDirs` to put `~/.c0wrk/.agents/skills` above `~/.agents/skills`.** Rejected: it inverts the "user owns their home-level library" principle for EVERY skill and agent (not just `study-paper`), and it still leaves no per-project copy — version pinning and reconciliation would remain unsolved.
- **Keep the global seed and add the project-local one.** Rejected (owner decision): two maintained copies of the same skill with the global one providing no priority guarantee (it stays shadowed by `~/.agents`) is pure duplication.
- **Seed into `~/.agents/skills` instead.** Rejected: c0wrk must not write outside its own directories (`~/.c0wrk`, the workspace) — `~/.agents` is user territory shared with other tools.

## Related

- Supersedes the seeding decision (point 2) of [ADR-050](./050-papers-library.md); ADR-050's vendoring and library-location decisions stand.
- [../domains/research.md](../domains/research.md) - the reconciliation flow and invariants
- [../domains/papers.md](../domains/papers.md) - the paper pack and the project-local seeding contract
