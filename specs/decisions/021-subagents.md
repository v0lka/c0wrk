# ADR-021: Subagent Profiles (`.agents/agents` + `#agent-name` mentions)

## Status

Accepted

## Context

c0wrk's Conductor can already delegate coherent units of work to subagents via
the `delegate` tool (see [contracts/conductor-tools.md](../contracts/conductor-tools.md),
ADR-012). Each subagent runs an isolated ReAct loop and reports a summary back.
Until now, **every delegation used the generic orchestrator system prompt, the
full mutating toolset, and the default ReAct step cap** — there was no way to
give a subagent a specialized persona, a restricted tool budget, or a per-agent
model.

Three concrete gaps motivated this work:

1. **No persona specialization.** A "code review" delegation and a "research"
   delegation were indistinguishable to the subagent — both ran the same generic
   orchestrator directive. Users wanting a strict reviewer or a read-only
   investigator had no way to express that intent.
2. **No discovery surface.** The Conductor had no roster of *available*
   specialties to delegate to; delegation was always ad-hoc prose ("delegate the
   code review to a subagent"), with no stable names an autocomplete or a plan
   step could reference.
3. **No explicit routing.** A user who wanted a *specific* specialty had to hope
   the Conductor would infer it from the task text — there was no first-class
   "route this to agent X" affordance analogous to `/skill`.

Skills ([decisions/006-skills-mcp-layer.md](006-skills-mcp-layer.md)) already
solve a parallel problem — *activating* a reusable capability for the main
Conductor — but they change the toolset/prompt of the *current* loop, not the
persona of a *delegated* loop. We needed the delegation equivalent: a named,
on-disk profile that, when targeted, replaces the subagent's system prompt and
tunes its tool budget.

### Forces

- **Reuse the delegation pipeline.** `delegate`, the DelegationRegistry, DAG
  waves, and `buildSubAgentTask` already exist and work. A specialization layer
  must ride on top of them, not replace them.
- **No sp4rk changes.** sp4rk is a separate external module
  ([ADR-015](015-sp4rk-external-module-dependency.md)). The `PlanStep.Agent`
  field lives in sp4rk's `orchestration` package; a copy-omission bug there
  (`blackboard.copyPlan` dropped `Agent`) was the one cross-module fix needed,
  but the feature itself is c0wrk-local wiring.
- **Symmetry with skills.** Skills live at `.agents/skills/<name>/SKILL.md` and
  are mentioned with `/name`. Agents should feel familiar: a parallel layout and
  a parallel mention syntax keep the mental model uniform.
- **Disambiguation with file refs.** `@path` already denotes a file reference
  (`@x.go#L20`); `#L20` is a GitHub-style line anchor *on* a file. A new mention
  syntax must never collide with that.

## Decision

Introduce **Subagent Profiles**: markdown files at
`.agents/agents/<name>/AGENT.md` whose YAML frontmatter declares a specialized
subagent persona and budget, and whose body is the agent's core directive. The
profile is applied at delegation time when a user or plan step targets it by
name.

### 1. Profile location and discovery

- Profiles live at `<workspace>/.agents/agents/<name>/AGENT.md`
  (`core/pathsegments.go` `AgentsRelativePath = ".agents/agents"`, mirroring
  `SkillsRelativePath`).
- Base/shared profiles are discovered from `~/.agents/agents` and
  `~/.c0wrk/.agents/agents` (`backend/application.go` default dirs); the
  project-local `<workspace>/.agents/agents` is always highest priority
  (`core/builder.go` `GetAgentDescriptors`).
- Discovery is performed by `github.com/v0lka/sp4rk/agents.AgentManager` (a self-contained package
  importing only the stdlib + `gopkg.in/yaml.v3`), which mirrors
  `skills.SkillManager`: directories scanned in priority order, first/highest
  occurrence wins, invalid `AGENT.md` files logged at Debug and skipped.

### 2. AGENT.md format (v1)

```markdown
---
name: code-reviewer          # required, must match dir name; lowercase alnum + hyphens
description: Reviews Go code. # required; drives #-autocomplete + "Available Subagents"
tools: read-only             # optional: "all"(default) | "read-only" | comma-list of mutating tools
max-steps: 25                # optional: ReAct iteration cap; 0 = derive from complexity
model: claude-haiku-4        # optional: per-agent model override
allow-redelegate: false      # optional: permit nested delegation (default false)
hidden: false                # optional: hide from #-autocomplete (default false)
color: "#e06c75"             # optional: UI accent color for the badge
---

You are a meticulous code reviewer. Read the full diff before commenting.
```

Unknown frontmatter keys (e.g. `temperature`, `mode`) are silently ignored
(unknown-field-ignore), so not-yet-supported fields do not break parsing.

### 3. Why `#` (not `@`)

The explicit mention syntax is `#agent-name`, paralleling `/skill-name`:

- **`@` is taken.** `@path` already means a file reference with an optional
  line anchor (`@x.go#L20`, `@x.go#L5-L10`). Reusing `@` for agents would
  collide with file refs and with the `@file#anchor` form.
- **`#` parallels `/`.** Skills use `/`; agents use `#`. Both are "trigger a
  capability by name" affordances, distinct from the file-attachment `@`.
- **Line-anchor safety.** A `#` glued to a file token (`@x.go#L20`) has no
  preceding whitespace and is not a known agent name, so it is never matched as
  an agent mention — in both the frontend parser
  (`lib/parseReferences.ts` `AGENT_REF_PATTERN`) and the backend preprocessor
  (`core/message_preprocess.go`). `#review`, `/review`, and `@review` are three
  distinct references handled by independent preprocessors.

### 4. Two delegation modes

- **Implicit (discovery).** When the discovered catalog is non-empty, the
  Conductor system prompt gains a `## Available Subagents` roster (non-hidden
  agents only). The Conductor may delegate to a fitting specialty via
  `delegate(agent: "name")` at its discretion.
- **Explicit (request).** A user `#mention` is stripped from the message text
  (`PreprocessMessageText`) and threaded as `HandleOptions.UserAgents` →
  `WithUserAgents`. The prompt then gains a `## Requested Subagents` directive
  instructing the Conductor to **MUST** delegate the corresponding work to each
  named agent. A requested agent absent from the catalog is still listed (without
  a description) to surface the mismatch rather than silently dropping it.

Both sections are **Conductor-only**: specialized runs (e.g. goal derivation via
`buildSpecializedSystemPrompt`) define their own delegation semantics and never
emit these sections.

### 5. Profile application at delegation time

When `delegate(agent: "name")` or a plan step carrying `agent: "name"` executes,
`buildSubAgentTask` (`core/conductor.go`) resolves the profile via the context's
`AgentResolver` and applies:

- **System prompt.** The profile body replaces the generic `OrchestratorSystem`
  core directive via `buildSpecializedSystemPrompt` (the shared project context —
  workspace, AGENTS.md, env — is preserved).
- **Tools.** `Agent.ToolPreference()` → `normalizeToolPreference` →
  `DelegationTask.Tools` (`nil` = all; `"read-only"` = base only; comma-list =
  named mutating tools on top of the read-only base).
- **Max-steps.** A profile `max-steps` > 0 overrides both the task field and the
  complexity-derived default.
- **Model.** A profile `model` wraps the caller via
  `agent.NewModelOverrideCaller` so every LLM call forces that model.
- **Redelegation.** `resolveAgentAllowRedelegate` upgrades (never downgrades):
  a profile with `allow-redelegate: true` wins over a false task flag; a profile
  with `false` never downgrades an explicitly-granted task flag.

An unknown agent name, or a requested agent with no resolver configured, **fails
fast** (`buildSubAgentTask` returns an error) rather than launching a
profile-less subagent.

### 6. Plan-step targeting

`declare_plan` accepts an `agent` field on each step
(`core/tools/delegate.go`). `conductorPublisher.Publish` copies
`PlanTaskInput.Agent` onto the `PlanStep`; `defaultPlanStepWave` threads it into
the `DelegationTask`. The field survives JSON persistence/restore
(`PlanStep.Agent` has a JSON tag) and the blackboard `copyPlan` fix
(`sp4rk/orchestration/blackboard.go`), so agent-targeted steps round-trip across
task continuation.

## Consequences

**Positive:**

- Subagents gain first-class specialization (persona, tools, step cap, model)
  without any change to the delegation pipeline's structure.
- The `#agent-name` mention gives users an explicit routing affordance
  (autocomplete, highlight, badge) symmetric with `/skill-name`.
- Discovery (`Available Subagents`) lets the Conductor proactively delegate to
  known specialties instead of inferring intent from prose.
- The whole feature is c0wrk-local: only one cross-module fix to sp4rk
  (`copyPlan`), which is a pure bug fix independent of this feature.
- No regression for projects without `.agents/agents`: nil `agentManager` and
  empty `UserAgents` leave the context untouched, and both roster sections are
  omitted from the prompt.

**Negative / trade-offs:**

- A new on-disk convention (`.agents/agents`) and mention syntax (`#`) to learn,
  alongside skills. Mitigated by symmetry with the skills layout and syntax.
- Profiles are advisory persona/budget declarations — they do not change the
  security boundary. A `tools: edit_file` profile still passes every tool call
  through the policy pipeline (policy → judge → confirmation); the tool budget
  only narrows the *available* tools, never the *policy* applied to them.
- `allow-redelegate` can deepen the delegation tree up to
  `config.MaxRedelegationDepth`; profiles only upgrade this, capped by config.

## Alternatives Considered

- **Reuse `@` for agent mentions.** Rejected: `@` is the file-reference trigger,
  and `@file#anchor` would collide. Keeping `@` for files and `#` for agents
  keeps the three reference kinds (`/` skill, `#` agent, `@` file) unambiguous.
- **Store agent profiles in the database instead of on disk.** Rejected: it
  breaks symmetry with skills (on-disk markdown), prevents version-controlling
  profiles alongside the project, and adds a persistence layer with no benefit —
  the discovery/parse model already works for skills on disk.
- **Make agent specialization an sp4rk engine feature.** Rejected: sp4rk stays a
  reusable engine with no knowledge of c0wrk's filesystem conventions or persona
  format (consistent with ADR-006 / ADR-015). The profile parser
  (`github.com/v0lka/sp4rk/agents`) is c0wrk-local; sp4rk only owns the generic
  `PlanStep.Agent` field and the delegation primitives.
- **Drive specialization purely through the tool budget (no persona body).**
  Rejected: the system-prompt directive is the primary value (a "code reviewer"
  persona behaves differently from a generic orchestrator even with the same
  tools). The body-replaces-prompt behavior is the core of the feature.
- **Treat `#mention` as a hard router input (skip the Conductor's discretion).**
  Rejected: the Conductor remains the single decision-maker. The directive
  instructs it to delegate ("you MUST"), but the Conductor can still surface
  impossibilities (e.g. the named agent does not exist) to the user rather than
  failing opaquely.

## End-to-end scenario

1. **Author.** A user drops
   `<workspace>/.agents/agents/code-reviewer/AGENT.md` with a reviewer persona
   and `tools: read-only`.
2. **Mention / implicit delegate.** The user types "review my PR
   `#code-reviewer`". The frontend extracts `["code-reviewer"]`
   (`extractAgentRefs`) and sends it as `activeAgents` (`sendMessage` arg 4 → Go
   `SendMessage` arg 4 → `HandleOptions.UserAgents`).
3. **Preprocess.** `PreprocessMessageText` strips the `#code-reviewer` ref from
   the message text (so the Conductor doesn't see the raw mention).
4. **Enrich.** `enrichAgentContext` attaches the discovered catalog
   (`WithAvailableAgents`) and the explicit request (`WithUserAgents`) to the
   context.
5. **Prompt.** `buildSystemPrompt` renders `## Available Subagents` (discovered)
   and `## Requested Subagents` (`#code-reviewer`, with its resolved
   description and a `delegate(agent: "code-reviewer")` directive).
6. **Execute.** The Conductor delegates; `buildSubAgentTask` resolves the
   profile, swaps in the reviewer body as the core directive, applies the
   read-only tool budget and any max-steps/model override, and runs the subagent
   ReAct loop with that specialized persona.

## Related Specs

- [contracts/conductor-tools.md](../contracts/conductor-tools.md) —
  `delegate`/`declare_plan`/`reflect`, the `agent` field on delegation and plan
  steps.
- [domains/orchestration/delegation.md](../domains/orchestration/delegation.md) —
  delegation registry, DAG waves, subagent execution.
- [domains/orchestration/conductor.md](../domains/orchestration/conductor.md) —
  Conductor system prompt assembly and the Available/Requested Subagents
  sections.
- [decisions/006-skills-mcp-layer.md](006-skills-mcp-layer.md) — skills (the
  parallel `/skill-name` + `.agents/skills` design this mirrors).
- [decisions/012-conductor-orchestration-pipeline.md](012-conductor-orchestration-pipeline.md)
  — the Conductor delegation pipeline this layer specializes.
- [decisions/020-multi-source-agents-md-threat-model.md](020-multi-source-agents-md-threat-model.md)
  — note: profiles are a *different* `.agents` subtree (`.agents/agents`, not
  `AGENTS.md`); the threat model there covers instruction files, not persona
  profiles. Profile tool budgets do not bypass the tool-policy pipeline.
