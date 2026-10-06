# ADR-037: Subagent Profile Required Skills

## Status

Accepted — note [ADR-076](./076-unified-slash-mentions.md): `/` is now the single mention trigger for skills and subagents. This ADR's `/skill-name` references remain valid (a skill may also be named via the collision-qualified `/skill: name` form); the `skills:` field semantics are unchanged.

## Context

[ADR-021](021-subagents.md) introduced Subagent Profiles: `.agents/agents/<name>/AGENT.md`
files whose frontmatter declares a specialized persona, tool budget, step cap,
and model, applied at delegation time. A profile's body is the subagent's core
directive — and that body could *say* "follow the code-review skill" — but there
was no mechanism to make a profile **require** a skill. The skills a delegated
subagent ran with were not something a profile could declare or guarantee.

Skills ([ADR-006](006-skills-mcp-layer.md) lineage; `<workspace>/.agents/skills/<name>/SKILL.md`,
activated with `/skill-name`) are reusable instruction bundles. Activation
attaches an `ActiveSkills` carrier to the context, which (a) renders a
`## Active Skills` section with each skill's body **verbatim** into the system
prompt and (b) makes the skill addressable by `read_skill_resource`. But
activation was driven by the router or an explicit `/skill` mention for the
*main* Conductor — nothing let a *profile* mandate skills for the subagent it
launches. Three gaps followed:

1. **No declarative requirement.** A "code-reviewer" profile could describe its
   need in prose but could not guarantee that the `code-review` skill was active.
2. **Silent divergence.** A profile that depended on a skill could launch with
   none active, silently degrading the very behavior the persona promised.
3. **No inheritance rule.** When a subagent redelegates (ADR-021
   `allow-redelegate`), there was no defined semantics for whether the parent's
   active skills carry into the child.

### Forces

- **Reuse the delegation pipeline.** `delegate`, the Delegation Registry, DAG
  waves, and `buildSubAgentTask` already exist and work; skill requirements must
  ride on top of them, not replace them.
- **Reuse the skills carrier.** `ActiveSkills`, the `## Active Skills` renderer
  (`formatActiveSkills`), and `read_skill_resource` already exist. A second
  activation mechanism would drift from the main-Conductor behavior.
- **Keep the profile parser self-contained and fail-closed.** Frontmatter
  validation lives in the sp4rk `agents` package (ADR-021); it mirrors the strict
  `tools:` / `max-steps` translation APIs and must reject malformed input rather
  than silently drop it.
- **Do not widen the trust boundary.** A skill is guidance text plus a
  resolvable resource path. Per [ADR-024](024-group-policies.md), skill tool
  grants were removed — activating a skill must never expand a tool budget or the
  policy applied to a tool call.

## Decision

Add an optional `skills` frontmatter field to Subagent Profiles and activate the
named skills for the delegated subagent.

### 1. Field and validation (sp4rk `agents` package)

`AGENT.md` gains optional `skills: <comma-separated skill names>`.
`Agent.RequiredSkills() ([]string, error)` splits/trims the value and validates
it **fail-closed**: an empty item, a duplicate, or an invalid skill name is a
`ParseError` surfaced at parse time (`validateAgent` → `validateSkillsField`), and
an invalid profile is skipped from the catalog with a Warn (identical to the
existing malformed-AGENT.md handling). An absent or empty value (surrounding
whitespace tolerated) returns `(nil, nil)`, so existing profiles are unaffected.
Names use the same shape as skill directory names.

### 2. Resolution and activation (`core/conductor.go`)

At delegation time `buildSubAgentTask` resolves the profile (ADR-021) and then:

- `profile.RequiredSkills()` → `resolveProfileSkills` resolves each name against
  the skill catalog via `conductorDeps.skillResolver`
  (`func(name string) (*skills.Skill, bool)`, built from `o.skillManager.Get`).
- Resolution is **fail-closed**: a nil resolver with a non-empty requirement
  list, or a required name with no matching skill, returns an error naming both
  the skill **and** the profile — no subagent launches.
- Resolved skills are merged with any skills already active in the context
  (`ActiveSkillsFromContext`) by `mergeActiveSkills`: inherited skills keep their
  position and come first, later duplicates are dropped by name (first
  occurrence wins), and nil entries are skipped. The merged set is attached with
  `WithActiveSkills`.

### 3. Prompt and tool surface

The merged set renders exactly like any other active skill: `formatActiveSkills`
emits the `## Active Skills` section, one `### Skill: <name>` block per skill,
with the **full body emitted verbatim** — bodies are never truncated, because
truncating guidance silently degrades plan/execution fidelity. The same set must
also reach the subagent's tool execution so `read_skill_resource` can address
those skills.

Because the context handed to `agent.RunSubAgent` cannot be mutated by
`buildSubAgentTask` (and a parallel wave shares one context across subagents),
the profile skills travel on a per-subagent `agent.ToolExecutor` decorator
(`activeSkillsToolExec`) that injects `ActiveSkills` into every tool call's
context. This keeps each subagent's skill set correct even when a single wave
carries several profiles with different requirements. The decorator is installed
only when the profile contributes at least one skill.

### 4. Inheritance on redelegation

A redelegating subagent's `delegate` / `cancel_delegation` calls run through its
decorator, so the parent subagent's active skills are present in the context that
the child `buildSubAgentTask` reads. The child therefore **inherits** the parent's
active skills; `mergeActiveSkills` places the inherited skills first, then the
child profile's own requirements (deduplicated). Inheritance is intentional: a
delegated specialty that itself delegates should carry the same mandated skills
into its children unless a child profile adds more.

### 5. Trust boundary unchanged

Skills contribute guidance text and a resolvable resource path — nothing more. A
skill's frontmatter `allowed-tools` field grants nothing (ADR-024 removed skill
tool grants), and a profile's `skills` never widens the tool budget or the policy
applied to any tool call. Activating a skill cannot escalate capability.

### 6. No-op when unused

Profiles without `skills:` and projects without skills behave exactly as before
(ADR-021): an empty requirement list leaves the exit path untouched and installs
no decorator.

## Consequences

**Positive:**

- A profile can now *guarantee* the skills its persona depends on, and a missing
  or unknown skill fails the delegation instead of silently degrading.
- Reuses the existing `ActiveSkills` carrier and `## Active Skills` renderer — no
  second activation mechanism, so main-Conductor skills and profile skills render
  identically.
- Inheritance on redelegation is explicit and stable (inherited-first, deduped by
  name).
- Fail-closed at two layers: parse-time validation (sp4rk `agents`) and
  resolution-time validation (c0wrk `resolveProfileSkills`). An invalid or
  unknown skill can never launch a subagent that is missing a skill the profile
  mandates.

**Negative / trade-offs:**

- **Prompt bloat.** Every required skill contributes its full body verbatim to
  the subagent's system prompt, and redelegation accumulates inherited bodies. A
  profile pairing many large skills enlarges the context (bounded by the normal
  context/compaction budget, but deliberately not trimmed at render time).
- **Workspace skill-surface amplification.** A profile pulls a *different* on-disk
  subtree (`.agents/skills`) into a delegated loop's prompt — a wider surface than
  the profile body alone. This does **not** expand the trust boundary: skills
  still grant no permissions, and every tool call still passes the policy/judge
  pipeline.
- **Coupling to the skill catalog.** A profile's validity now depends on the
  skills present in the workspace: renaming or removing a skill turns a
  previously valid profile's delegation into a hard failure. This is intentional
  (fail-closed), but it means profiles are not portable across workspaces without
  their skills.

## Alternatives Considered

- **Include-syntax in the profile body** (e.g. author `@skill:code-review` or an
  inline include directive resolved at prompt-build time). Rejected: it invents a
  fourth reference syntax and a second resolution path, bypasses the fail-closed
  frontmatter validation (a typo becomes an inert literal in the body instead of
  an error), and does not make the skill addressable to `read_skill_resource`. The
  declarative field reuses the existing `RequiredSkills` validation and the
  existing `ActiveSkills` pipeline.
- **`delegate(skills: [...])` runtime parameter** (activate skills per delegation
  call rather than per profile). Rejected: skills are a property of the *persona's*
  contract, not of an individual call — a profile should not be launchable without
  its mandated skills, and a task-time parameter lets the caller omit them. It
  also duplicates `RequiredSkills` and does not compose with plan-step targeting
  (which selects a profile, not a skill list). Keeping the requirement on the
  profile places it where the persona is defined.
- **Inherit only, with no per-profile requirement** (rely solely on the parent
  context's active skills flowing to the child). Rejected: it cannot express "this
  profile must run with skill X" when the parent has no reason to activate X, and
  it gives no fail-closed signal when the skill is absent.

## Related Specs

- [decisions/021-subagents.md](021-subagents.md) — Subagent Profiles, the
  frontmatter format this field extends; the delegation-time application this
  builds on. Immutable.
- [domains/orchestration/delegation.md](../domains/orchestration/delegation.md) —
  delegation pipeline, `tasks[].agent` profile targeting, subagent prompt/tool
  assembly.
- [decisions/006-skills-mcp-layer.md](006-skills-mcp-layer.md) — skills origin
  (`/skill-name`, `.agents/skills`, the `## Active Skills` section).
- [decisions/024-group-policies.md](024-group-policies.md) — capability-group tool
  policies; skills grant no permissions.
