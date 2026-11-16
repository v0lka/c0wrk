# ADR-129: Match SDD framework skill names exactly (no `spec-` alias)

## Status

Accepted

## Context

[ADR-126](./126-sdd-framework-dsl.md) decision 3 made recognition depend on
installed skills and bridged two spellings with a symmetric alias: an installed
`spec-check` satisfied a declared `vibespec-check`, and the reverse. The alias
existed because of a rename inside c0wrk: [ADR-105](./105-vendor-spec-skillset.md)
vendored the upstream skillset as `spec-*`, while upstream publishes the same six
skills as `vibespec-*`. ADR-126 §7 then withdrew the vendored pack, so the alias
outlived its purpose and remained the only trace of that rename in the code.

It was implemented as a hard-coded prefix table — `skillAliasPrefixes` plus
`canonicalSkillName` in `core/sdd/detect.go`, both sides folded before every
comparison — and exported as `SkillNameMatches` for the action-skill gate
(`resolveActionSkill` in `backend/frontend_api_sdd.go`, which scanned the callable
catalog in sorted order to report the installed spelling).

Three costs outweigh the compatibility it buys:

1. **Prefix rewriting carries no semantics.** `vibespec-<X>` folds onto
   `spec-<X>` for *any* `X`, so the detector cannot distinguish "the same skill,
   renamed" from "a different skill whose name happens to start with the other
   prefix". The folds stayed harmless only because the declaration lists every
   name explicitly; the mechanism itself has no notion of identity.
2. **It puts a second spelling into a name-exact contract.** The action gate
   could dispatch a name the DSL never declared, and `resolveActionSkill` needed
   a sorted scan and an exported predicate solely to hide the difference from
   the panel. ADR-126 §1's "no privileged in-code representation" principle is
   contradicted by a privileged spelling pair that no operator DSL document can
   declare or extend.
3. **Nothing reconciles the installation the alias serves.** ADR-126 §7 removed
   the pack lifecycle, so a machine carrying pre-rename `spec-*` copies is
   exactly the state c0wrk no longer manages. Recognising it half-way — a panel
   whose gestures dispatch those old skills — prefers the retired spelling over
   the one upstream actually publishes.

## Decision

**Skill names match exactly, and no alias machinery remains.** Three commitments:

1. **Exact comparison.** A framework document's `detect.skills.all_of` /
   `any_of` entry is satisfied only by an installed skill directory whose name is
   that entry, compared case-sensitively after trimming surrounding whitespace.
2. **The alias is deleted, not redirected.** `skillAliasPrefixes`,
   `canonicalSkillName` and the exported `SkillNameMatches` are removed from
   `core/sdd/detect.go`; `installedIndex` is keyed by the exact skill name, so two
   similarly named skills are two entries. `resolveActionSkill` becomes a direct
   catalog lookup that reports the declared name and marks it not-callable when
   the catalog does not carry it.
3. **Several accepted spellings are the DSL's business, not the detector's.** An
   operator who wants alternatives lists them in `any_of` — the existing
   mechanism — and the DSL declares no alias vocabulary.

The embedded VibeSpec document declares the six upstream `vibespec-*` names and
only those recognise it. A leftover `spec-*` copy is ordinary agent skill data:
it neither recognises the framework nor satisfies a declared action.

This **amends ADR-126 decision 3** (the visibility detection contract). Every
other decision of ADR-126 — the DSL, the merge rules, the panel, the gate, the
parsing contract, the session-following tree — is unchanged.

## Consequences

- One spelling per skill. The name a document declares is the name dispatch
  invokes and the name detection requires, so the panel can no longer report a
  resolved spelling that no declaration mentions.
- The detection contract loses an exported predicate and the resolver loses its
  alias scan (`maps`/`slices` usage disappears from `backend/frontend_api_sdd.go`)
  — less surface to keep honest.
- A machine carrying **only** pre-rename `spec-*` copies loses VibeSpec
  recognition: the Specs tab disappears until the upstream `vibespec-*` skills
  are installed. There is no migration, by design — c0wrk ships no skillset and
  does not rewrite an operator's agent skill directories; the leftover copies
  stay on disk as inert data.
- Detection tests pin both directions: each of the six `vibespec-*` names
  recognises the framework on its own, while look-alikes (`vibespec-checker`,
  `my-spec-check`, …) and every retired `spec-*` name recognise nothing. The
  action gate has its own regression case for a declared-but-differently-spelled
  skill, and the backend pins that a `spec-*`-only workspace reports no
  detection at all.

## Alternatives Considered

- **Reverse the alias direction (canonical `vibespec-*`, alias `spec-*`).**
  Rejected: it is the same prefix-rewriting mechanism with the same semantics
  and the same second spelling at dispatch; only the fold direction changes.
- **Keep the alias internal and stop exporting `SkillNameMatches`.** Rejected:
  encapsulating it removes API surface, not the semantics — the detector still
  equates names it cannot prove are the same skill.
- **Declare aliases in the DSL (`detect.skills.aliases`).** Rejected: a new
  grammar surface added for a rename artifact. `any_of` already expresses
  "several acceptable names", and it needs no privileged built-in pair.
- **Rename leftover `spec-*` directories during detection or activation.**
  Rejected: that resurrects the pack lifecycle ADR-126 §7 deleted, writes into
  the operator's agent directories, and makes detection mutate its own evidence.

## Related

- [ADR-126](./126-sdd-framework-dsl.md) — decision 3 is amended here; the rest of
  the DSL/panel decision stands.
- [ADR-105](./105-vendor-spec-skillset.md) — the `vibespec-*` → `spec-*` rename
  the alias bridged; superseded in part by ADR-126.
- `specs/domains/sdd-dsl.md` — the detection and DSL contract.
- `specs/domains/specs.md` — the Specs subsystem.
- `docs/custom-specs-frameworks.md` — the operator-facing recognition rules.
