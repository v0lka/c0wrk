# ADR-105: Vendor and seed the vibespec spec-\* skillset

## Status

Accepted

## Context

c0wrk maintains a rich, hand-written `specs/` tree (`META.md`, `INDEX.md`, `architecture/`, `contracts/`, `domains/`, `decisions/NNN-slug.md` + `_template.md`), and `AGENTS.md` directs agents at `specs/INDEX.md`. Keeping that tree aligned with the code is currently a manual discipline: nothing automates the spec lifecycle (bootstrap, create, update, drift-check, consult).

The external **vibespec** skillset (`/Users/vkochetkov/Repositories/skills/development/sdd`, MIT) implements exactly that lifecycle as six Agent Skills — `vibespec-init`, `vibespec-explore`, `vibespec-create`, `vibespec-update`, `vibespec-check`, `vibespec-consult` — agent-agnostic (they describe what to do, not which tools to call), validated against the Agent Skills spec v1. The `vibespec-init` skill carries templates in `assets/` (`meta-template.md`, `adr-template.md`, `workflow-template.md`).

The RESEARCH subsystem (issue [#70](https://github.com/v0lka/c0wrk/issues/70), [ADR-056](./056-research-always-on-global-seeding.md)) already establishes the reuse pattern: an embedded, versioned skill pack (`core/research/skillpack.go`, `core/papers/skillpack.go`) plus a bundled Subagent Profile (`core/research/agentpack.go`), seeded globally once per launch by `FrontendAPI.seedGlobalPacks` before the skill/agent watchers start, idempotent and non-destructive (content-hash classification, `.seed-version` markers, staged write + atomic rename).

Three decisions had to be made at design time:

1. **Skill count.** The source skillset has SIX skills, but the issue text and its acceptance criteria name FIVE (excluding `explore`). `vibespec-explore` is the on-ramp of the documented workflow (`explore → consult → implement → update`), the README and the other skills reference it, and the roadmap mechanism it provides (Spec footprint per task) is what keeps this very ADR-writing process honest. Dropping it would require scrubbing cross-references from the other five skills and the README.
2. **`WORKFLOW.md` divergence.** The upstream `spec-init` emits `specs/WORKFLOW.md`; c0wrk's tree has none — and `AGENTS.md` already owns the "how to use the spec system" pointer role (`specs/INDEX.md`, `specs/META.md`, `specs/decisions/`).
3. **Seed-staging duplication.** `core/research/seedstaging.go` is deliberately mirrored by `core/papers/seedstaging.go` ("fix bugs in both copies"). A third vendored pack would make it a third copy.

## Decision

**1. Vendor SIX skills into `core/specs/skills/spec-*/SKILL.md`, renamed to the c0wrk convention.** `vibespec-init` → `spec-init`, `vibespec-explore` → `spec-explore`, `vibespec-create` → `spec-create`, `vibespec-update` → `spec-update`, `vibespec-check` → `spec-check`, `vibespec-consult` → `spec-consult` (mirroring the `research-*` naming). Each `SKILL.md` frontmatter `name:` matches its directory. The vendored tree is adapted once and then **evolved independently of upstream** — there is no sync obligation; upstream is the origin, not a dependency. The upstream MIT license and attribution are preserved in `core/specs/LICENSE.md`. Skill bodies stay **tool-agnostic** (no c0wrk tool names), so they remain portable.

**2. Adapt to c0wrk's actual spec layout; do NOT introduce `specs/WORKFLOW.md`.** The adapted skills reference the real tree (`specs/META.md`, `specs/INDEX.md`, `specs/{architecture,domains,contracts,decisions}/`, ADR `NNN-slug.md`, `specs/decisions/_template.md`) and c0wrk's conventions (ADR numbering rules, INDEX registration). `spec-init` emits the c0wrk shape — no `WORKFLOW.md`; the "how to use the system" role stays with `AGENTS.md`, which `spec-init` updates with the specs pointer exactly as upstream does. The upstream `workflow-template.md` asset is therefore NOT vendored; `assets/` carries only `meta-template.md` and `adr-template.md`.

**3. Bundle one Subagent Profile `core/specs/agents/specs/AGENT.md`.** A spec-aware subagent (analog of the research profile) whose directive is "consult relevant specs before structural changes, update affected specs after them". Frontmatter: `tools: local-read, local-write` (least privilege — spec work reads and writes Markdown, never executes), `skills: spec-consult, spec-create, spec-update, spec-check` (the four continuous-skills; resolved fail-closed against the skill catalog at launch). Discoverable automatically once seeded; mentionable and delegatable via `delegate(agent: "specs")`.

**4. Seed globally, exactly like RESEARCH (ADR-056).** `FrontendAPI.seedGlobalPacks` additionally calls `specs.SeedSkills` (`→ ~/.c0wrk/.agents/skills/spec-*`) and `specs.SeedAgents` (`→ ~/.c0wrk/.agents/agents/specs/`) once per launch, before the watchers start, per-pack fail-soft, with the unchanged non-destructive classification contract. Always-on: no config toggle, no per-project step. Discovery precedence is inherited from ADR-056 (project-local → c0wrk global → `~/.agents`), so the seeded pack outranks a stale user copy with no catalog change.

**5. Consolidate the seed-staging core into `core/seedpack`.** The generic machinery (content-hash classification, staged write + atomic rename, marker handling, leftovers sweep) moves out of `core/research/seedstaging.go` into one shared package; `core/research`, `core/papers`, and `core/specs` become thin consumers (they keep their embed directives, version constants, and pack-specific logging). This supersedes the deliberate-mirror precedent of `core/papers/seedstaging.go` for every future pack.

## Consequences

- Every launch materializes the six `spec-*` skills and the `specs` profile machine-globally; agents and subagents get the spec lifecycle with no catalog change, and c0wrk dogfoods its own `specs/` tree through the same skills it ships.
- The vendored copy diverges from upstream over time by design; a future upstream improvement must be re-evaluated and ported by hand, not pulled.
- A user's marker-less `spec-*` copies in `~/.c0wrk/.agents` or `~/.agents` are never clobbered; a stale same-named `~/.agents` copy is shadowed by the seeded one (the ADR-056 precedence trade-off, now applying to the spec skills too).
- `core/seedpack` is one more package, and the research/papers refactor is a mechanical but non-zero diff; the payoff is a single tested implementation for three (and future) packs, and the "fix bugs in both copies" debt disappears.
- Excluding `spec-explore` would have been less to vendor but would have broken the documented workflow graph and required reference scrubbing; including it keeps the README, the skills, and the roadmap mechanism consistent.

## Alternatives Considered

- **Vendor exactly five skills (exclude `spec-explore`), as the issue's acceptance criteria literally state.** Rejected: `explore` is the documented on-ramp (`explore → consult → implement → update`), every other skill and the README reference it, and the Spec-footprint roadmap discipline it provides is actively used. The issue's AC is extended to six (owner-approved).
- **Keep `specs/WORKFLOW.md` as a real new deliverable (make `spec-init` emit it).** Rejected: it duplicates the role `AGENTS.md` already plays (pointer to `specs/INDEX.md` + `specs/META.md`), adding a file whose content would mirror AGENTS.md and drift from it.
- **Keep per-pack mirror copies of `seedstaging.go` (a third copy for `core/specs`).** Rejected: two copies already carry a "fix bugs in both copies" comment; a third triples the known debt. The consolidation is mechanical and guarded by the existing research/papers tests.
- **Seed into `~/.agents` or per-project directories.** Rejected for the same reasons as in ADR-056 (c0wrk must not write outside its own directories; per-project copies multiply reconciliation).

## Related

- [ADR-056](./056-research-always-on-global-seeding.md) — the global pack-seeding and discovery-precedence precedent this pack inherits.
- [ADR-106](./106-specs-sidebar-panel.md) — the human-facing Specs panel that consumes the same subsystem.
- `specs/domains/specs.md` — the domain spec for the pack, the seeding invariants, and the panel (created with the implementation sync; not yet present when this ADR was accepted).
