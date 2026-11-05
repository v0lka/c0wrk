# ADR-113: Research Root Rename (`.research` → `research`)

## Status

Accepted

## Context

The research root has lived at `<workspace>/.research` since its introduction — pinned there by [ADR-050](./050-papers-library.md), restated by [ADR-056](./056-research-always-on-global-seeding.md), and kept by [ADR-099](./099-research-root-follows-active-session.md) while the root learned to follow the active session's execution workspace. The dot-prefix was inherited from the tool's own hidden-infrastructure conventions (`.agents/`, `.git/`) — but research artifacts are not infrastructure: they are the project's richest working context (briefs, hypothesis cards, prior art, reports, the paper library).

A dot-prefixed directory is invisible to exactly the surfaces that should see it. The agent's `glob` skips hidden entries by default, `ripgrep` skips hidden paths unless forced with explicit flags, and the vector index's ignore-aware walker does not embed dot-directories — so every research artifact was unreachable through the discovery tools an agent actually uses, and any interaction with research data had to know the magic path in advance. The same hiding applies to the human: file trees, finders, and editors fold dot-directories away, so the research workspace was invisible to the user who owns it.

Renaming the segment fixes all consumers at once, because every root derivation funnels through `config.ProjectResearchPath` / `config.ResearchRootSegment` (`backend/config/paths.go`) — but every workspace written by a pre-rename build carries its data at the old path.

## Decision

Rename the canonical research-root segment to the visible `research`, with a manual, panel-driven migration for pre-rename data:

1. **The visible root.** `config.ResearchRootSegment = "research"` (`ProjectResearchPath(ws)` = `<ws>/research`). Everything else about the root is unchanged: it stays unconditional and always on for real projects, never persisted per project, with no user override ([ADR-056](./056-research-always-on-global-seeding.md)), and it still follows the ACTIVE SESSION's execution workspace ([ADR-099](./099-research-root-follows-active-session.md)). Only the directory name changes.

2. **Detection, never derivation.** Pre-rename `<workspace>/.research` roots are detection/migration-only data: `ResearchStatusDTO.legacy_research_roots` (always non-nil) lists the detected ones — the checkout's and the active-session workspace's, deduplicated, checkout first, any entry type counts (a symlink or stray file is surfaced so the user sees it). `config.LegacyProjectResearchPath` exists solely for this; a live research root is never derived from the legacy path.

3. **Manual migration RPC.** `MigrateResearchRoots(projectID)` renames every detected legacy root into its workspace's canonical `research`: each rename runs under the per-root mutation mutex keyed by its TARGET (the same family the hypothesis/pin mutations serialize on), an EMPTY existing target directory is removed first, a non-empty or non-directory target is refused with an actionable error, and a symlinked legacy root is refused outright (never followed or silently moved). On success the research trees are re-registered with the live CODE watcher (the canonical tree may only have come into existence with the rename), `research:changed` (action=`root_migrated`) and `papers:changed` are emitted, and the fresh status is returned. A no-op (no legacy roots anywhere) is a plain success that creates nothing on disk and emits no event.

4. **The panel is the migration surface.** While `legacy_research_roots` is non-empty, the Research panel replaces its `[Dashboard | Papers]` segments with a migration notice (`frontend/src/components/research/ResearchLegacyMigration.tsx`) carrying a Migrate button (busy state while the RPC runs, inline error with retry on failure). On success the returned fresh status is loaded directly — its empty legacy list unmounts the notice and restores the panel content without waiting for the `research:changed` event.

Migration never runs automatically: nothing moves until the user clicks.

## Consequences

- Research artifacts become discoverable end to end: `glob`, `ripgrep`, the vector index, file trees, and every external tool see `research/` because it is an ordinary directory.
- A pre-rename project pays one notice and one click. That friction is deliberate: a multi-directory rename of user data happens on an explicit action, and the refusal rules (non-empty target, symlink) surface as actionable errors on that click instead of as surprise failures on project open.
- Before migration the two roots coexist harmlessly: the app reads and writes only the canonical `research`; the legacy tree sits untouched (a user who never opens the panel keeps it forever, undamaged).
- Pack content changed with the rename, so seed versions bumped: the research skill-pack `CurrentSeedVersion` 2→3 (`core/research/skillpack.go`) and the agent-pack `AgentSeedVersion` 1→2 (`core/research/agentpack.go` — its AGENT.md names the tree), the papers pack `CurrentSeedVersion` 5→6 (`core/papers/skillpack.go` — its SKILL.md pins the root). Without the bumps the content-hash classifier would treat seeded copies as user-modified and never refresh them.
- The CODE watcher gains one re-registration step (`rewatchResearchTreesAfterMigration`): a tree admitted as merely missing at setup (marked recursive) only becomes watchable when the rename materializes it.
- `specs/domains/research.md`, `specs/domains/papers.md`, `specs/domains/workspace.md`, `specs/domains/session-worktrees.md`, and the desktop-frontend/event contracts document the canonical `research` root and the migration surface; ADR-050/056/099 remain historically accurate with their `.research` spelling.

## Alternatives Considered

- **Auto-migration on project open** (rename detected legacy roots silently). Rejected: a silent rename of user data on a mere project switch is too destructive to be implicit — a non-empty target or a symlink would turn every open into a failure or an unwanted merge decision, and "my directories moved" must never be a side effect of looking at a project. The panel notice delivers the same one-time cost as an informed choice, and the detection field alone (one status entry) is all the always-on price the feature pays.
- **Keep `.research`, un-hide it through ignore exceptions** (ship `.gitignore`/`.aiignore` negations, force hidden-inclusion flags on every consumer). Rejected: it patches consumers one by one and only the ones c0wrk controls — external editors, finders, and hand-invoked `rg`/`glob` still skip dot-directories by default, and each new tool would need the exception re-argued. The dot-prefix itself is the defect; the rename fixes every consumer at the single funnel (`ProjectResearchPath`) they already share.

## Related

- [Research](../domains/research.md) — the domain spec (root invariant, detection/migration invariant, panel flow)
- [Papers](../domains/papers.md) — the paper library living under the same root
- [ADR-056](./056-research-always-on-global-seeding.md) — the always-on root this rename re-spells (decision otherwise unchanged)
- [ADR-099](./099-research-root-follows-active-session.md) — the session-following resolution the rename preserves verbatim
- [contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — `MigrateResearchRoots` + `legacy_research_roots` boundary
- [contracts/event-catalog.md](../contracts/event-catalog.md) — `research:changed` action `root_migrated`
