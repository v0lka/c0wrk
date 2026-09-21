# Custom Skills and Subagents

This guide is the complete, user-facing reference for extending c0wrk with your
own **Agent Skills** and **Subagent Profiles**. It covers, end to end:

- what each mechanism is and how the two differ;
- the on-disk format of each (every frontmatter field, the body, bundled
  resources);
- **every** location a skill or profile can be placed, and their precedence;
- how each becomes active inside the agent loop (automatic matching, explicit
  mentions, delegation);
- every way to *mention* or *target* them from chat and from plans;
- the tunables, events, and failure modes; and
- worked, copy-pasteable examples.

c0wrk stores both mechanisms as plain Markdown on disk. There is **no in-app
editor** for them — you author files with your own editor (and can version them
in the project repository), and c0wrk discovers them automatically, including
live while the app is running.

---

## 1. The two extension points at a glance

Both mechanisms are "markdown file with YAML frontmatter in a `.agents/`
directory", but they do different jobs:

| | **Agent Skill** | **Subagent Profile** |
| --- | --- | --- |
| File | `<dir>/<name>/SKILL.md` | `<dir>/<name>/AGENT.md` |
| Job | Activates *reusable instructions + bundled resources* for the agent that runs with them | Declares a *specialized persona + tool budget + limits* for a **delegated** subagent |
| Applied to | The current (main) Conductor loop, or a subagent that requires it | A subagent launched under that profile |
| Effect on the prompt | Adds a `## Active Skills` section (body emitted **verbatim**) | **Replaces** the orchestrator core directive with the profile body |
| Trigger | Automatic router match **or** explicit `/skill-name` | Explicit `#agent-name`, plan-step `agent:`, or `delegate(agent: "name")` |
| Mention syntax | `/skill-name` | `#agent-name` |
| Bundled files | Yes (`references/`, `scripts/`, `assets/`, …) read via `read_skill_resource` | No — a profile is a single directive file |
| Tool permissions | None — a skill grants and revokes nothing | Narrows/closes the subagent's *available* tool set (never the policy applied) |
| Spec lineage | [agentskills.io](https://agentskills.io) open spec | c0wrk-specific (ADR-021) |

The mental model: a **skill** is a reusable capability you switch on; a
**subagent profile** is a named specialist you hand work to. They compose — a
profile can *require* skills (see [§3.7](#37-requiring-skills-profile-skills)).

---

## 2. Agent Skills

### 2.1 What a skill is

A skill follows the open **agentskills.io** specification: a directory
containing a `SKILL.md` file whose YAML frontmatter carries metadata and whose
Markdown body carries the instructions. When a skill is *activated*, its body is
injected **verbatim** into the agent's system prompt under a `## Active Skills`
section, and any files bundled in the skill directory become readable by the
agent through the `read_skill_resource` tool.

A skill is therefore the right tool for "a procedure, convention, checklist, or
domain playbook I want the agent to follow" — including interactive procedures
that ask questions or require tools, because the agent runs inside a full tool
loop.

### 2.2 Where skills live (discovery and precedence)

c0wrk scans several directories for skill subdirectories. The complete chain,
**highest priority first**:

| Priority | Directory | Notes |
| --- | --- | --- |
| 1 (highest) | `<workspace>/.agents/skills/` | Project-local. **Always scanned automatically** — never needs to be listed in config. Version it with the project if you like. |
| 2 | `~/.c0wrk/.agents/skills/` | c0wrk-global (your machine). This is where c0wrk seeds its own built-in skills. |
| 3 | `~/.agents/skills/` | Portable, user-wide — the conventional cross-tool location for Agents Skills. |

Only `<workspace>/.agents/skills` is always included; the other two are the
defaults of the `skills.dirs` config key (see [§4.3](#43-configuration-keys)).
If you override `skills.dirs`, the project-local directory is still prepended
automatically.

**How precedence resolves conflicts.** Discovery walks the directories in
reverse priority and the **first (highest-priority) occurrence of a skill *name*
wins**. So a project-local `code-review` skill shadows a c0wrk-global one, which
shadows a `~/.agents` one. This is why c0wrk's own seeded skills live in the
c0wrk-global directory (rank 2): they beat a stale same-named copy in
`~/.agents`, but you can still override any of them with a project-local copy.

Discovery details:

- Each **immediate subdirectory** of a skills directory that contains a valid
  `SKILL.md` becomes a skill. Subdirectories are the unit — a loose `*.md` file
  in the skills root is ignored.
- Symlinked directories **are followed**.
- A directory whose `SKILL.md` fails validation is **skipped** (logged at Debug)
  — it never aborts the scan and never breaks other skills.
- Changes in the c0wrk-global directories are surfaced via the `skills:changed`
  event; changes inside the workspace (project-local skills) come through the
  normal `workspace:tree_changed` event. Both refresh the chat-input `/`
  autocomplete live (see [§2.7](#27-live-reload)).

### 2.3 The `SKILL.md` format

```
<skills-dir>/<name>/SKILL.md
```

The file starts with a `---` YAML frontmatter block, then a Markdown body.

#### Frontmatter fields

| Field | Required | Rules |
| --- | --- | --- |
| `name` | **Yes** | 1–64 chars; lowercase letters, digits, and hyphens; no leading/trailing hyphen. **Must equal the parent directory name.** |
| `description` | **Yes** | Max 1024 chars. This is what the router and the `/` autocomplete see, so make it a precise, complete sentence describing *when* to use the skill. |
| `license` | No | Free text (portability metadata). |
| `compatibility` | No | Max 500 chars. |
| `allowed-tools` | No | Space-separated tool names. **Experimental and inert in c0wrk** — see [§2.10](#210-trust-and-safety-notes). |
| `metadata` | No | A free-form map of arbitrary key/value pairs (e.g. `domain:`, `methodology:`). Preserved but not otherwise interpreted by c0wrk. |

Unknown keys are ignored. A missing/invalid `name` or `description`, a `name`
that does not match its directory, or an over-long value makes the skill invalid
and the whole skill is skipped.

#### The body

Everything after the closing `---` is the skill body. It is injected verbatim,
so write it as instructions to the agent — procedures, rules, checklists,
examples. The body is **never truncated** at render time (truncating guidance
silently degrades behavior), so keep it focused; very large bodies cost context
on every turn the skill is active.

#### Bundled resources

Any other files you place in the skill directory travel with it and are readable
by the agent through `read_skill_resource`. The convention (not a hard
requirement) is:

| Path | Purpose |
| --- | --- |
| `references/*.md` | Deep reference material the body points at ("see `references/api.md`"). |
| `scripts/*` | Executable helpers (e.g. a Python script) the skill instructs the agent to run. |
| `assets/*` | Templates, diagrams, and other static files. |

Paths that escape the skill directory are rejected, so a skill cannot be used to
read arbitrary files.

### 2.4 How skills become active

A skill is activated through one of two independent paths, and the two sets are
**merged (deduplicated) into the active set** for the task:

1. **Automatic (router matching).** On each new request the router classifies the
   message and may select skills from the catalog. The router sees only each
   skill's **name + description** — this is why a good `description` matters. The
   skills it selects (`MatchedSkills`) are activated automatically.
2. **Explicit (`/skill-name` mention).** If you type `/my-skill` in the message,
   that skill is activated directly and **bypasses the router**. To keep routing
   quality high, the router message is rebuilt with the skill's name and
   description restored, so classification still reflects the task.

When the router auto-selects, c0wrk emits a `skills_activated` event; the
frontend reflects the active set.

If a `/skill-name` you typed does not correspond to a discovered skill, it is
left as-is in the message (no error) — nothing is activated.

#### The `## Active Skills` prompt section

Active skills are rendered into the system prompt as:

```
## Active Skills
The following skills have been activated for this task. Follow their instructions carefully.

### Skill: <name>
Description: <description>

<body, verbatim>
```

One `### Skill:` block per active skill. Because this section is
session-invariant for a task, it sits in the cacheable prefix of the prompt — no
action is needed on your side to benefit from prompt caching.

### 2.5 Reading bundled resources: `read_skill_resource`

The agent has a `read_skill_resource` tool (always-allowed, read-only). It takes
a skill name and a path relative to that skill's directory and returns the file
contents. Only **currently active** skills are addressable — a skill that is not
active on this request cannot have its resources read. This is how a body like
"see `references/forms.md`" becomes actionable.

### 2.6 Validation and failure modes

- Invalid `SKILL.md` → the skill is skipped (Debug log); everything else keeps
  working.
- `name` ≠ directory name → invalid.
- Missing `description` → invalid.
- A `/skill-name` with no match → ignored (no activation, text preserved).
- A `read_skill_resource` call for a non-active skill or an out-of-tree path →
  an error result to the agent.

### 2.7 Live reload

c0wrk watches the skill directories. Editing a skill's `SKILL.md` (or adding/
removing a skill directory) refreshes the catalog — and the `/` autocomplete —
without restarting the app. One caveat: a skills directory that does **not
exist at startup** is not watched (its later creation is only noticed after a
restart); for directories that do exist, each immediate subdirectory is also
watched, so edits to an existing skill are detected even on platforms without
recursive watching.

### 2.8 Built-in skills shipped with c0wrk

c0wrk seeds its own skills into `~/.c0wrk/.agents/skills/` once per launch
(idempotent, non-destructive — a directory you authored is never clobbered):

| Skill(s) | What they provide |
| --- | --- |
| `research-init`, `research-hypothesis`, `research-prior-art`, `research-experiment`, `research-decision`, `research-status`, `research-synthesis` | The seven-step **Iterative Engineering Research Methodology** used by RESEARCH: briefs, hypothesis cards, prior art, experiments, decisions, status, and synthesis. |
| `study-paper` | Studying/appraising/reproducing research papers, with bundled `references/`, `assets/`, and a `scripts/literature.py` helper. |

Because they are seeded into the c0wrk-global directory (rank 2), they
outrank a same-named skill in `~/.agents`, but a project-local copy still wins if
you want to override one.

### 2.9 A complete example skill

```
<workspace>/.agents/skills/sql-migrations/
├── SKILL.md
└── references/
    └── conventions.md
```

`SKILL.md`:

```markdown
---
name: sql-migrations
description: >-
  Author, review, and apply database migrations safely. Use when adding,
  changing, or rolling back a schema migration, or when the user asks about
  the migration workflow, naming, or rollback strategy.
metadata:
  domain: engineering
---

# SQL Migrations

Follow the project's migration conventions in
[references/conventions.md](references/conventions.md).

## Rules

1. **One logical change per migration.** Never mix unrelated schema edits.
2. **Always write a rollback.** If a rollback is impossible, say so explicitly
   in the migration header.
3. **Never edit an applied migration.** Add a new one instead.
4. **Check the naming convention** before creating the file.

## Procedure

1. Inspect the existing migrations directory and the most recent migration.
2. Draft the migration following the naming convention.
3. Draft the matching rollback.
4. State the exact commands to apply and to roll back.
```

Activate it either by asking something the router matches (the `description`
mentions migrations) or explicitly with `/sql-migrations`.

### 2.10 Trust and safety notes

- **Skills grant no permissions.** Per ADR-024 the former skill-level tool-
  permission layer was removed: a skill neither expands nor restricts the tool
  set, and the `allowed-tools` frontmatter field is carried but **grants
  nothing**. What the agent may *do* is governed entirely by capability-group
  policies (`security.groups`), independent of skills.
- **Skill bodies are instructions, not authority.** They shape behavior, but they
  do not override c0wrk's security policies or your explicit request.
- Bundled resources are read through a path-traversal-safe resolver restricted to
  the skill directory.

---

## 3. Subagent Profiles

### 3.1 What a subagent profile is

The Conductor can delegate coherent units of work to **subagents**, each running
an isolated ReAct loop in its own context window and reporting a summary back. By
default every subagent uses the generic orchestrator prompt and the full toolset.
A **Subagent Profile** lets you define a *named specialist*: a persona (the
profile body), a tool budget, a step cap, an optional model, and optional
required skills. When a subagent is launched under a profile, that profile's
body **replaces** the orchestrator core directive.

Think of it as the delegation-time counterpart to a skill: a skill specializes
*the current loop*; a profile specializes *a delegated loop*.

### 3.2 Where profiles live (discovery and precedence)

Exactly parallel to skills:

| Priority | Directory | Notes |
| --- | --- | --- |
| 1 (highest) | `<workspace>/.agents/agents/` | Project-local. **Always scanned automatically.** |
| 2 | `~/.c0wrk/.agents/agents/` | c0wrk-global. Where the built-in `research` profile is seeded. |
| 3 | `~/.agents/agents/` | Portable, user-wide. |

The precedence rule is identical: the first/highest occurrence of a profile
*name* wins. Only `<workspace>/.agents/agents` is unconditionally included; the
other two are the defaults of the `agents.dirs` config key (see
[§4.3](#43-configuration-keys)).

Discovery mechanics mirror skills: immediate subdirectories containing a valid
`AGENT.md`; symlinked directories followed; a `AGENT.md` that exists but fails
validation is skipped with a **Warn** log (so a broken profile is visible);
a directory without an `AGENT.md` is skipped silently. A profile is valid only
if its `name` matches its directory name and it has a `description`.

### 3.3 The `AGENT.md` format

```
<agents-dir>/<name>/AGENT.md
```

YAML frontmatter, then the body. Unknown frontmatter keys are ignored (so
not-yet-supported fields do not break parsing).

#### Frontmatter fields

| Field | Required | Rules / meaning |
| --- | --- | --- |
| `name` | **Yes** | Lowercase alphanumeric + hyphens, no leading/trailing hyphen. **Must equal the parent directory name.** |
| `description` | **Yes** | Drives the `#` autocomplete and the `## Available Subagents` roster. Describe the specialty and when to use it. |
| `tools` | No | The subagent's tool budget. `all` (default) · `read-only` · a comma-separated list of **capability-group tokens** (see [§3.6](#36-tool-budgets-capability-groups)). A typo fails the profile. |
| `max-steps` | No | ReAct iteration cap for the subagent. `0`/absent ⇒ derived from complexity (see [§3.8](#38-delegation-settings-and-limits)). |
| `model` | No | Per-agent model override — every LLM call the subagent makes is forced to this model. |
| `allow-redelegate` | No | `true` lets this subagent itself delegate deeper (capped by config). Default `false`. |
| `hidden` | No | `true` hides the profile from the `#` autocomplete and the `Available Subagents` roster (see [§3.10](#310-hidden-profiles)). Default `false`. |
| `color` | No | UI accent color for the agent badge (e.g. `"#e06c75"`). |
| `skills` | No | A comma-separated list of **skill names this profile requires** (see [§3.7](#37-requiring-skills-profile-skills)). |

#### The body = the agent's core directive

Everything after the frontmatter is the profile body: the subagent's system
prompt. At delegation time it **replaces** the generic orchestrator directive,
while the *shared project context* (workspace, `AGENTS.md`, environment, active
skills, …) is preserved. A good body states the role, the constraints, the
method, and what "done" means for this specialist.

### 3.4 How a profile is selected (targeting)

There are four ways a profile is chosen — two driven by you, two by the
Conductor:

1. **Explicit `#agent-name` mention (you).** Type `#code-reviewer` in your
   message. The frontend extracts the mention, validates it against the
   user-visible catalog, **strips it from the message text**, and threads it to
   the backend. The Conductor's prompt then gains a `## Requested Subagents`
   directive telling it that it **MUST** delegate the corresponding work to each
   named agent. This is a directive, not a hint.
2. **Implicit discovery (the Conductor).** When the catalog is non-empty, the
   Conductor's prompt gains a `## Available Subagents` roster (non-hidden
   profiles only). The Conductor may choose to delegate to a fitting specialist
   at its discretion via `delegate(agent: "name")`.
3. **The `delegate` tool.** The Conductor passes `agent: "<name>"` on a
   delegation task. Any `agent` name that is not found **fails fast** (the whole
   delegate call is rejected before any subagent launches) — a requested profile
   is never silently ignored.
4. **Plan steps.** A plan step can carry an `agent: "<name>"` field
   (`declare_plan`), so a step's work runs with that profile's persona and
   budget. This survives pause/resume and JSON round-trips.

> **`#` vs `/` vs `@`.** `#agent-name` targets a subagent, `/skill-name`
> activates a skill, and `@path` references a file. A `#` that is glued to a file
> token (the GitHub-style line anchor in `@x.go#L20`) is never mistaken for an
> agent mention. A mention only counts when it is at the start of the text or
> preceded by whitespace.

#### The two Conductor-only prompt sections

Both sections are emitted only for the **main Conductor** — never inside a
subagent (a generic subagent must not inherit the Conductor's roster or its
`MUST delegate` directive).

`## Available Subagents` introduces the roster with:

> The following subagents are available for delegation. Delegate coherent units
> of work to them when doing so keeps your context lean or enables parallelism.
> Each runs in its own isolated ReAct loop and reports back a summary. Specify an
> agent by name via `delegate(agent: "name")` when a subagent's specialty fits
> the unit of work.

`## Requested Subagents` (from your `#mentions`) is a directive:

> The user explicitly requested delegation to the following subagents. You MUST
> delegate the corresponding units of work to each named agent via
> `delegate(agent: "name")` rather than handling them inline, unless delegation
> is genuinely impossible (e.g. the named agent does not exist — in which case
> surface the problem to the user).

A requested name that is not in the catalog is still listed (without a
description) so the mismatch is surfaced rather than silently dropped. In
practice the frontend already validates `#mentions`, so only names in the
user-visible catalog are threaded from chat (see [§3.10](#310-hidden-profiles)
for the hidden-profile nuance).

### 3.5 What the profile controls at delegation time

When a subagent launches under a profile, `buildSubAgentTask` applies:

- **System prompt** — the profile body replaces the orchestrator core directive
  (shared project context preserved).
- **Tools** — the profile's `tools` preference (see [§3.6](#36-tool-budgets-capability-groups)).
- **Max steps** — a profile `max-steps > 0` overrides both the task field and the
  complexity-derived default.
- **Model** — a profile `model` forces that model on every LLM call.
- **Redelegation** — a profile `allow-redelegate: true` grants the deeper-
  delegation tools.
- **Required skills** — a profile `skills:` list is activated for the subagent
  (see [§3.7](#37-requiring-skills-profile-skills)).

An unknown agent name, or a requirements list that cannot be resolved, **fails
the delegation** rather than launching a mis-specialized subagent.

### 3.6 Tool budgets (capability groups)

Every tool in c0wrk declares a **capability group**, and a profile's `tools`
field is expressed in group tokens (kebab-case; underscore spelling is also
accepted). Groups:

| Token | Meaning |
| --- | --- |
| `execute` | Shell/command execution. |
| `local-read` | Read files in the workspace. |
| `local-write` | Write/edit files in the workspace. |
| `remote-read` | Read from remote sources (web, etc.). |
| `remote-write` | Write to remote sources. |
| `local-mcp` / `remote-mcp` | Local / remote MCP-backed tools. |
| `system` | Agent-infrastructure/meta tools (checklist, facts, `read_skill_resource`, …). **Always included** on top of whatever you grant. |

The `tools` field accepts:

- `all` (or omitting the field) → the full toolset (everything except
  Conductor-only tools).
- `read-only` → a shortcut for `system ∪ local-read ∪ remote-read` (read-only
  exploration, **no MCP**).
- A comma list of group tokens, e.g. `local-read,execute` → the `system` group
  **plus exactly those groups**.

Validation is fail-closed: an unknown token, `all`/`read-only` mixed with other
items, or a duplicate group makes the profile invalid (it is skipped), so a typo
can never silently widen or narrow the budget. An empty list is rejected — pass
`["system"]`/`system` if you deliberately want only the system group.

**The budget only narrows *availability*; it never changes *policy*.** Every
tool call a subagent makes — including one whose group you granted — still passes
the full policy → judge → confirmation pipeline. A profile is an advisory
persona/budget declaration, not a security boundary change.

### 3.7 Requiring skills (`profile skills:`)

A profile can *guarantee* the skills its persona depends on by listing them in
`skills:`:

```yaml
skills: code-review,git-conventions
```

At delegation time:

- Each name is resolved against the skill catalog **fail-closed**: a name that
  does not match a discovered skill (or a missing skill catalog) **fails the
  delegation** — the subagent never launches missing a skill it mandates.
- The resolved skills are **merged** with any skills already active in the
  inherited context (inherited first, then the profile's own, deduplicated by
  name), and rendered exactly like any active skill: a `## Active Skills` section
  with **full verbatim bodies**, and addressable via `read_skill_resource`.
- Names are validated at parse time too (empty item, duplicate, or invalid name
  shape invalidates the whole profile).

**Inheritance on redelegation.** A redelegating subagent carries its active
skills into its children. The child inherits the parent's skills *first*, then
adds its own profile's requirements (deduplicated).

Trade-off: a profile's required skills each add their full body to the
subagent's prompt, and inheritance accumulates bodies — so pair a profile with
focused skills. Also, because resolution is fail-closed, a profile becomes
non-portable across workspaces unless its required skills travel with it.

### 3.8 Delegation settings and limits

These are Conductor/plumbing settings rather than profile fields, but they bound
how profiles run:

- **Concurrency** — `agents.max_parallel_subagents` (default **4**) caps how many
  subagents run at once, shared across the `delegate` tool (blocking batches and
  async launches) and plan waves.
- **Batch size** — a single `delegate` call accepts at most **16** tasks; larger
  work must be split across calls.
- **Default step budget** — when neither the profile nor the task sets
  `max-steps`, a subagent's budget derives from routing **complexity × 30**.
- **`max_steps`** on a delegation task overrides the default; a profile's
  `max-steps > 0` overrides both.
- **Redelegation cap** — `orchestration.maxRedelegationDepth` (default **2**)
  bounds recursive delegation when `allow-redelegate` is in play.

### 3.9 Redelegation

By default a subagent **cannot** delegate further (flat). Setting
`allow-redelegate: true` grants the subagent the `delegate` and
`cancel_delegation` tools, letting it spawn its own subagents up to
`maxRedelegationDepth`. The interaction between the task flag and the profile
field is **upgrade-only**: a profile with `allow-redelegate: true` wins over a
task that did not request it, but a profile with `false` never downgrades an
explicitly-granted task flag. (`declare_plan` and `reflect` remain
Conductor-only.)

### 3.10 Hidden profiles

`hidden: true` keeps a profile out of the discovery surfaces:

- It is excluded from the `#` autocomplete list (the frontend roster).
- It is excluded from the `## Available Subagents` roster the Conductor sees.

A hidden profile remains **resolvable by name** when targeted explicitly (a
`delegate` call or a plan step naming it). Note the consequence of the
autocomplete exclusion: because chat-input `#mentions` are validated against the
*visible* catalog, a hidden profile's `#name` typed in chat will not be threaded
as a delegation directive. Treat `hidden` as "not offered for discovery", not as
a secret with special privileges.

### 3.11 Live reload

c0wrk watches the profile directories. Adding or editing an `AGENT.md` refreshes
the `#` autocomplete live; changes outside the workspace emit the
`agents:changed` event, changes inside the workspace arrive via
`workspace:tree_changed`. As with skills, a profile directory that did not exist
at startup is not watched (a restart picks it up); existing directories also
watch each immediate subdirectory.

### 3.12 A complete example profile

```
<workspace>/.agents/agents/code-reviewer/
└── AGENT.md
```

```markdown
---
name: code-reviewer
description: Reviews code changes for correctness, clarity, and convention adherence. Use when the user asks for a code review or a diff walkthrough.
tools: read-only
max-steps: 25
color: "#e06c75"
---

You are a meticulous code reviewer.

- Read the full diff (and the surrounding files) before commenting.
- Cite file and line for every finding.
- Rank findings by severity; separate correctness bugs from style nits.
- Do not modify any file — produce a review, not an edit.
- If the change is correct, say so plainly; do not invent issues.
- End with a short verdict: approve / request changes, and why.
```

Use it by asking "review my changes `#code-reviewer`" (explicit), or simply by
asking for a code review and letting the Conductor pick it from the
`Available Subagents` roster (implicit). To make it depend on a skill, add
`skills: code-review` and ship the matching skill.

### 3.13 Trust and safety notes

- A profile is an **advisory** persona/budget declaration. It does **not** change
  the security boundary: the `tools` field only narrows the *available* tool set,
  and every call still passes the policy pipeline.
- Delegated subagents run in an isolated context; they receive a task brief, not
  your full chat history.
- Subagent output is treated as **untrusted** (a subagent may have read external
  content), so it is framed accordingly when returned to the Conductor.

---

## 4. Reference

### 4.1 Mention and reference syntax

| Syntax | Refers to | Extracted/validated by |
| --- | --- | --- |
| `/skill-name` | An Agent Skill to activate | Frontend `extractSkillRefs`; backed by the discovered skill catalog |
| `#agent-name` | A Subagent Profile to delegate to | Frontend `extractAgentRefs` + catalog filter |
| `@path` | A file reference (with optional `#L20` / `#L5-L10` line anchors) | Frontend; converted to a `fileref://` URI for the agent |
| `/goal …` | Switches the first message of a task into goal mode | Backend goal-mode detection |

Rules that keep the three reference kinds unambiguous:

- A reference is only recognized at the **start of the text or after
  whitespace** — mid-word slashes and hashes are not references (so
  `http://...` is safe).
- A `#` glued to a file token (the line anchor in `@x.go#L20`) is **never** an
  agent mention.
- `#review`, `/review`, and `@review` are three distinct references.
- `#mentions` are validated against the user-visible profile catalog before being
  threaded; unknown ones (e.g. an issue number `#42`) are left in the message
  text and never become a delegation directive.

### 4.2 File layout at a glance

```
~/.c0wrk/                       (c0wrk app dir; also agent dir)
├── config.yaml
└── .agents/
    ├── skills/                 c0wrk-global skills   (rank 2)
    │   ├── research-init/SKILL.md
    │   ├── … (research-*, study-paper — seeded)
    │   └── <your-skill>/SKILL.md
    └── agents/                 c0wrk-global profiles (rank 2)
        ├── research/AGENT.md   (seeded)
        └── <your-agent>/AGENT.md

~/.agents/                      portable, user-wide     (rank 3)
├── skills/<name>/SKILL.md
└── agents/<name>/AGENT.md

<workspace>/                    an open project         (rank 1 — highest)
└── .agents/
    ├── skills/<name>/SKILL.md
    └── agents/<name>/AGENT.md
```

### 4.3 Configuration keys

In `~/.c0wrk/config.yaml`:

| Key | Default | Meaning |
| --- | --- | --- |
| `skills.dirs` | `["~/.c0wrk/.agents/skills", "~/.agents/skills"]` | Skill discovery directories, highest priority first. Omitting the key keeps the default; an explicit list replaces it. The current project's `.agents/skills` is always added at highest priority. |
| `agents.dirs` | `["~/.c0wrk/.agents/agents", "~/.agents/agents"]` | The same, for Subagent Profiles. |
| `agents.max_parallel_subagents` | `4` | Max concurrently running subagents (all dispatch paths). `0` ⇒ 4. |
| `orchestration.maxRedelegationDepth` | `2` | Max recursive delegation depth when `allow-redelegate` is used. |

Paths may be absolute or relative to the agent directory (`~/.c0wrk`), and
support `${VAR}` environment-variable expansion. If you customize `skills.dirs`/
`agents.dirs` and omit the c0wrk-global directory, the seeded built-in packs
become undiscoverable (c0wrk warns at startup).

### 4.4 Events

| Event | Emitted when |
| --- | --- |
| `skills:changed` | A skill directory **outside** the workspace (e.g. `~/.c0wrk/.agents/skills`) changed. |
| `agents:changed` | A profile directory **outside** the workspace changed. |
| `workspace:tree_changed` | A workspace change, including project-local `.agents/skills` and `.agents/agents` edits. |

These drive the live refresh of the `/` and `#` autocompletes.

### 4.5 Related tools and RPCs

| Name | Kind | Purpose |
| --- | --- | --- |
| `read_skill_resource` | Tool (system group) | Read a bundled file from an active skill. |
| `delegate` | Tool (Conductor) | Launch subagents, optionally under a profile (`agent:`). |
| `declare_plan` | Tool (Conductor) | Declare a plan; each step can carry an `agent:`. |
| `cancel_delegation` | Tool (Conductor, or a redelegating subagent) | Cancel a running delegation. |
| `ListSkills` | RPC | Read-only: the discovered skill descriptors (feeds the `/` autocomplete). |
| `ListAgents` | RPC | Read-only: the discovered, non-hidden profile descriptors (feeds the `#` autocomplete). |

There is no create/update/delete RPC and no in-app editor: skills and profiles
are files on disk, managed with your own tools.

---

## 5. Combining skills and subagents

The two mechanisms compose naturally. Common patterns:

- **A specialist that ships its playbook.** Put the method in a skill and
  reference it from a profile with `skills:`. The profile gets the persona and
  budget; the skill carries the detailed, reusable procedure (and any bundled
  resources). Resolution is fail-closed, so the pair must travel together.
- **A persona vs. a capability.** Use a skill when you want *the main agent* to
  follow a procedure right now; use a profile when you want a *delegated
  specialist* whose whole context is devoted to the task.
- **Explicit routing.** Mention both: "review the API change `#code-reviewer`"
  (profile) while the review skill it requires is activated automatically for
  that subagent via `skills:`.
- **Parallelism.** Several independent profiles can be delegated in one batch to
  run concurrently (bounded by `max_parallel_subagents`), each with its own tool
  budget and persona.

---

## 6. Troubleshooting

| Symptom | Things to check |
| --- | --- |
| Skill never activates automatically | Its `description` must clearly state when to use it (the router only sees name + description). Or activate it explicitly with `/skill-name`. |
| `/my-skill` does nothing | The skill is not discovered: wrong directory, `name` ≠ folder name, missing/invalid `SKILL.md`, or it is shadowed by a same-named higher-priority skill. |
| `#my-agent` is ignored | The profile is not in the user-visible catalog (invalid `AGENT.md`, or `hidden: true`), or the mention was not at a boundary (needs whitespace/start). |
| Delegation fails with an unknown-agent error | The `agent:` name doesn't match a discovered profile name exactly. |
| Delegation fails mentioning a skill | The profile's `skills:` names a skill that isn't discovered — resolution is fail-closed by design. |
| Profile's tools don't apply | Check `tools` for unknown tokens or mixed `all`/`read-only` + list (invalid ⇒ profile skipped, Warn logged). |
| Nothing refreshed after editing a file | Ensure the edit landed in a watched directory; workspace edits come via `workspace:tree_changed`, global ones via `skills:changed`/`agents:changed`. Restart as a fallback. |
| A seeded built-in skill vanished | A custom `skills.dirs`/`agents.dirs` list that omits `~/.c0wrk/.agents/...` orphans the seeded packs. |

---

## Further reading (in-repo specs)

- `specs/decisions/021-subagents.md` — Subagent Profiles, `#agent-name`, plan-step targeting.
- `specs/decisions/037-agent-profile-skills.md` — profile-required skills and inheritance.
- `specs/decisions/006-skills-mcp-layer.md` — skills (and MCP) live in sp4rk.
- `specs/decisions/024-group-policies.md` — capability-group policies; skills grant no permissions.
- `specs/domains/orchestration/delegation.md` and `…/conductor.md` — the delegation pipeline and prompt assembly.
- `specs/domains/research.md` — the built-in research methodology and its seeded pack.
