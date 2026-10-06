# ADR-076: Unified Slash Mentions (one `/` trigger for skills and subagents)

## Status

Accepted

## Context

Until this decision, the chat input had **two trigger characters** for the same
conceptual affordance — "trigger a capability by name":

- `/skill-name` activated an **Agent Skill** (frontend `extractSkillRefs` →
  `activeSkills` → the `## Active Skills` prompt section, server-side
  resolution).
- `#agent-name` requested a **Subagent Profile** (frontend `extractAgentRefs` +
  `filterKnownAgentRefs` → `activeAgents` → `HandleOptions.UserAgents` → the
  `## Requested Subagents` directive; backend `PreprocessMessageText` stripped
  the ref). `#` was chosen in [ADR-021](./021-subagents.md) §3 because `@` was
  taken by file references.

The split had three costs:

1. **Two affordances to learn** for one mental model. Skills and subagent
   profiles are both "a named capability in a `.agents/` directory, mentioned by
   name"; the trigger char was the only UX difference.
2. **`#` is hostile prose territory.** GitHub-style text is full of `#42` issue
   refs, `#L100` line anchors, and `#refactor` hashtags. Extraction had to be
   permissive and then filtered against the catalog
   (`filterKnownAgentRefs`) to avoid stripping a misread mention from the
   user's message (data loss) and injecting a directive for a nonexistent agent
   (prompt noise).
3. **No way to express a collision.** A skill and a profile may carry the same
   name (they live in independent discovery roots: project-local → c0wrk-global
   → user). With two trigger chars the pair was at least distinguishable, but
   the moment both moved to one trigger, a plain `/name` would be ambiguous —
   the unified design needed an explicit answer.

Issue [#110](https://github.com/v0lka/c0wrk/issues/110) agreed the unified
design; this ADR records it. (The issue drafted the ADR number as 072; numbers
072–075 were taken by then, so this is 076.)

## Decision

**One `/` trigger, one completion list, `#` removed.**

1. **One trigger, one list.** Typing `/` opens a single completion list with
   two labelled sections: **Subagents first**, then **Skills** (CodeMirror
   `Completion.section` with `rank`; distinct option `type`s for styling).
2. **`#` is removed.** Typing `#` opens nothing; `#foo` is always plain text.
   The `#` in `@file#L20` line anchors belongs to the file ref and is
   untouched.
3. **Threading unchanged.** On send, every `/`-ref is extracted and
   **partitioned against the two catalogs**: agent names → `activeAgents`
   (unchanged `## Requested Subagents` directive), skill names →
   `activeSkills` (unchanged `## Active Skills`). The `sendMessage` positional
   args and the Go `SendMessage` signature are unchanged.
4. **Collision definition.** A collision is an **exact, case-sensitive** name
   present in **both** the public (non-hidden) skill catalog **and** the public
   agent catalog, across all precedence roots.
5. **Collision-qualified syntax.** `/agent: <id>` and `/skill: <id>`. Both
   `/agent: foo` and `/agent:foo` are accepted input; the **canonical
   emission** (what the dropdown inserts) is the spaced form. The qualified
   form is **always valid input**, even without a collision — a collision only
   *forces* its use in the dropdown.
6. **Ambiguity is a no-op.** A hand-typed plain `/name` under a real collision
   activates and requests nothing, the text is preserved verbatim, and a
   user-visible hint points to the qualified form. (Silent preference for one
   kind was rejected — a visible no-op beats a silent misactivation.)
7. **Chips.** Qualified refs render verbatim as a chip (`/agent:
   code-reviewer`) coloured by kind; a non-colliding plain `/name` renders as
   its kind's chip (kind resolved from the catalog at display time).
   Already-persisted `#agent-name` messages render as plain text.
8. **Preprocessing.** `core/message_preprocess.go`
   (`PreprocessMessageText`) strips plain **and** qualified refs for both
   kinds: plain `/name` when the name is in `activeSkills ∪ activeAgents`, and
   `/agent: id` / `/skill: id` (spaced or unspaced colon) when the id is in
   the marker's own catalog. Stripping stays **catalog-gated and fail-closed**
   — an unknown or unthreaded mention is preserved verbatim — and the former
   per-name regex loops (including the `#`-strip) are replaced by a single
   left-to-right scanner. `#name` is never stripped.
9. **`/goal` unaffected.** `goal` is a command name, not a catalog name, so a
   leading `/goal …` survives preprocessing and `DetectAndStripGoalMode`
   (which runs on the processed text) keeps working — including the case where
   a stripped ref ahead of it (`/explore /goal …`) exposes the command at the
   start of the processed text.

## Consequences

**Positive:**

- One mental model and one autocomplete for both extension kinds; `#`-prose
  (issue numbers, hashtags) can no longer be misread as mentions anywhere in
  the pipeline.
- Collisions between skill and profile names gain an explicit, self-documenting
  spelling instead of an unresolvable ambiguity.
- The backend directive paths (`## Active Skills`, `## Requested Subagents`)
  and all positional contracts are untouched — the change is syntax and
  frontend plumbing plus the preprocessor.
- `PreprocessMessageText` loses its per-name `regexp.MustCompile` loops for a
  single O(n) scanner with exact map lookups.

**Negative / trade-offs:**

- A plain `/name` under a real collision is a no-op until the user qualifies
  it — mitigated by the dropdown always offering qualified entries under a
  collision and the visible hint.
- The send path must consult **both** catalogs to partition refs and detect
  collisions (an extra `listSkills()` + `listAgents()` read for messages
  containing `/`-refs only; both are server-cached).
- Historical `#agent-name` messages render as plain text (no chip) — accepted;
  no migration is performed.
- A profile or skill literally named `goal` would still shadow the `/goal`
  command prefix by the catalog-gated strip (pre-existing semantics for
  skills, unchanged by this ADR).

## Alternatives Considered

- **Keep `#` for agents.** Rejected: preserves the two-affordance cost and the
  GitHub-prose false-positive surface that motivated `filterKnownAgentRefs`.
- **Reuse `@` or another character for agents.** Rejected: `@` is the
  file-reference trigger (`@file#L20`); any third char recreates the split the
  decision removes.
- **On collision, let plain `/name` prefer one kind** (e.g. agents, since they
  list first). Rejected: silent misactivation of the wrong capability is worse
  than a visible no-op with a hint.
- **Forbid skill/profile name collisions at discovery.** Rejected: the two
  catalogs come from independent precedence roots (project-local → c0wrk-global
  → user) across projects and machines; a global naming constraint is
  unenforceable and would break existing packs.
- **Keep per-name regex stripping in the preprocessor and merely swap `#` for
  `/`.** Rejected: plain `/name` matching would then be ambiguous against the
  qualified forms and still pay per-name pattern compilation; the single
  scanner parses qualified and plain forms in one pass with exact lookups.

## Related

- [ADR-021](./021-subagents.md) — Subagent Profiles (the `#`-mention rationale
  this ADR supersedes).
- [ADR-037](./037-agent-profile-skills.md) — profile-required skills
  (unaffected; its `/skill-name` references remain valid).
- [ADR-006](./006-skills-mcp-layer.md) — the skills layer whose `/skill-name`
  mention syntax is generalized here.
- `docs/custom-skills-and-subagents.md` §3.4 and §4.1 — the user-facing syntax
  reference.
