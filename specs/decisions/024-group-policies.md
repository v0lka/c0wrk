# ADR-024: Tool-Capability Group Policies

## Status

Accepted

> **Drift note (2026-08-25, vibespec-check):**
> - §1's "Group() is a required method of the sp4rk Tool interface" — Group() is exposed through the OPTIONAL GroupProvider interface (sp4rk/tools/tool.go), read via ToolGroupOf, fail-closed to "" when unimplemented; BaseTool.ToolGroup remains the declaration point and the fail-closed invariant still holds.
> - §3/§4's "hard reasons never pass Smart Approve (DisableJudge=true)" — superseded by ADR-026: allow-policy hard reasons now route through the strict judge, with a deterministic backstop that auto-approval is denied for canonical hard reason codes only.

## Context

Until now, security policy was configured **per tool name**. `config.yaml` carried
`security.tool_policies` — a map from tool names (`bash_exec`, `write_file`,
`web_fetch`, …) to `{policy, blacklist?}` — plus a `security.default_policy`
fallback. The registry resolved a call's effective policy through a four-step
chain (per-tool config → skill policy override → registry default → the tool's
own `DefaultPolicy()`), and a hand-maintained `internalTools` name-set decided
which tools bypassed policy entirely.

This had four structural problems:

1. **Fragile, name-coupled configuration.** Policy keys are tool *names*, so
   every rename, platform split (`bash_exec` vs `posh_exec`), or newly added
   tool silently fell to the default policy until someone remembered to add a
   config entry. Windows users configured `posh_exec`; the example config
   documented `bash_exec`; neither inherited the other's blacklist.
2. **Hidden privilege class.** `internalTools` was a duplicated string set in
   the registry — out of band from the tools themselves. A new infrastructure
   tool that nobody added to the set was policy-gated (annoying); a dangerous
   tool someone *did* add silently bypassed every gate. Membership was
   invisible at the tool definition site.
3. **Policy layers that overlapped confusingly.** Skills could inject policy
   overrides (`SetSkillPolicyOverrides`), the config could override per tool,
   and the registry had per-tool runtime overrides (`SetPolicyOverride`s) —
   three resolution layers with subtle precedence, no single place that said
   what a call's policy actually was.
4. **The semantics the policy names encoded were capability semantics.** In
   practice every per-tool entry expressed *what kind of capability* the tool
   grants: "execute shell", "write local files", "read remote data". The
   per-tool map was a capability map written out longhand, once per tool.

At the same time, c0wrk already had a second, unrelated name-based mechanism:
subagent tool budgets (`AGENT.md` `tools:` and `delegate`'s `tools` parameter)
enumerated tool *names* to grant — which drifted out of sync the same way.

sp4rk (the engine) defined `ToolPolicy`, `ToolJudger`, and the registry, but had
no notion of a tool's capability class.

## Decision

Replace all name-based policy and toolset mechanisms with **tool-capability
groups**. Every tool declares exactly one group; policy, tool budgets, and the
internal-tool bypass all key off that group.

### 1. The group model (sp4rk `tools/group.go`)

Eight declared groups — there is **no `unknown` value**; every tool must declare
one (an undeclared group matches no allow-list — fail-closed):

| Group          | Capability                                                 | Members (sp4rk + c0wrk builtins)                                                |
| -------------- | ---------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `system`       | Agent-internal orchestration/state (no FS/shell/network side effects) | `finish`, `ask_user`, `delegate`, `declare_plan`, `execute_plan`, `propose_goal`, `declare_goal_status`, `declare_verification`, `declare_step_complete`, `cancel_delegation`, `reflect`, `store_fact`, `search_facts`, `update_checklist`, `batch`, `read_step_output`, `list_step_outputs`, `read_final_result`, `read_attachment`, `read_skill_resource`, `tool_result_read`, `semantic_search` |
| `local_read`   | Read local filesystem state                                | `read_file`, `list_directory`, `glob`, `ripgrep`                                  |
| `local_write`  | Mutate local filesystem state                              | `write_file`, `edit_file`, `create_directory`, `delete_file`, `delete_directory`  |
| `execute`      | Execute arbitrary shell commands                           | `bash_exec`, `posh_exec`                                                          |
| `remote_read`  | Read data from remote/network sources                      | `web_fetch`, `web_search`                                                         |
| `remote_write` | Write to remote resources (e.g. remote MCP mutations)      | — (currently no builtin; MCP servers via `ToolGroupOverride`)                     |
| `local_mcp`    | Tools of a stdio (local process) MCP server                | MCP tools, transport-derived                                                      |
| `remote_mcp`   | Tools of an http (network) MCP server                      | MCP tools, transport-derived                                                      |

- `Tool.Group() ToolGroup` is a **required method** of the sp4rk `Tool`
  interface; `BaseTool.ToolGroup` is the declaration point. The zero value
  ("not declared") matches no group allow-list — an untagged tool fails closed
  everywhere (registry filtering, subagent budgets, verifier sets).
- `system` is **reserved**: it cannot be configured and never appears in
  `security.groups`. It replaces the `internalTools` name-set as the bypass
  class.
- MCP tools derive their group from the server transport (stdio → `local_mcp`,
  http → `remote_mcp`); a server may pin itself with
  `ServerConfig.ToolGroupOverride` (validated; invalid overrides are ignored).
- `ToolDescriptor.Group` carries the group into planning/verifier surfaces
  (`ListFiltered` populates it from the live tool).

### 2. Configuration: `security.groups.<group>.{policy, blacklist?}`

```yaml
security:
  groups:
    local_read:   { policy: allow }        # read-only local state
    remote_read:  { policy: allow }
    execute:                                  # shell — the only group with a blacklist
      policy: user_confirm
      blacklist: ["rm\\s+-rf\\s+/", "sudo\\s+", ...]
    local_write:  { policy: user_confirm }
    local_mcp:    { policy: user_confirm }
    remote_mcp:   { policy: user_confirm }
    remote_write: { policy: user_confirm }
```

- Short enum: **`allow` / `user_confirm` / `deny`** (mapped at the registry to
  sp4rk's `PolicyAlwaysAllow` / `PolicyUserConfirm` / `PolicyAlwaysDeny`).
- Defaults (single source of truth `defaultToolGroupPolicies`): read-only
  groups `allow`; every mutating group `user_confirm`.
- A regex **blacklist is valid only on `execute`** (config validation rejects
  it elsewhere). The default execute blacklist is the dedup union of the bash
  and PowerShell pattern lists, **restricted to cross-dialect-safe patterns**:
  exactly one shell tool is registered per host (bash_exec on Unix, posh_exec
  on Windows), so the unified list always carries the other dialect's
  patterns, and a pattern may only hard-confirm command text that is dangerous
  under whichever shell reads it. The PowerShell Remove-Item **alias**
  patterns (`rm`, `del`, `erase`, `ri`, `rd`, `rmdir` — `rm -r -f <dir>` is
  the routine Unix delete spelling) cannot satisfy that invariant and are
  therefore NOT in
  the unified list; they are enforced as a Windows-only platform supplement at
  shell-tool construction (`core/tools/shelltool_windows.go`), restoring full
  PowerShell coverage without Unix false positives. The supplement is an
  engine-level floor: it applies on top of whatever the configurable group
  blacklist contains and is not user-removable.
- Unknown group names, `system` in config, blacklist outside `execute`, and
  invalid policy enums are load errors.
- An unconfigured group at runtime resolves fail-safe to `user_confirm` (never
  a zero-value `allow`).

### 3. Registry execution pipeline (gate order)

`core/tools/registry.go` `Execute` enforces, in order:

```
1. Required-field validation (JSON-Schema "required", defense-in-depth)
2. Disabled-tools check (No Project mode) — applies to ALL tools incl. system
3. group == system → execute immediately (bypasses everything below)
4. [PostExecuteHook deferred]
5. Extra shell blacklist (No Project) — hard block, reason names the pattern
6. PreExecuteHook (e.g. index-ready gate)
7. Group policy deny → hard block
8. Safety signals gathered once:
     tool Judge outcome (hard: blacklist / SSRF; soft: path containment)
     + symlink analysis (escape / unresolvable = hard; in-roots = not a concern)
9. Branch on effective group policy:
   allow        → hard reason ⇒ confirm with DisableJudge=true (never passes
                  Smart Approve); soft reason ⇒ Smart Approve may allow, else
                  confirm; clean ⇒ execute
   user_confirm → local_write + auto_approve_workspace_writes + Judge.Allow
                  ⇒ execute; hard reason ⇒ confirm (DisableJudge=true);
                  otherwise Smart Approve (ALLOW ⇒ execute, else confirm);
                  Smart Approve off ⇒ plain confirm
   deny         → blocked at step 7
```

Registry API: `SetGroupPolicies` / `GroupPolicies`
(`map[sdktools.ToolGroup]sdktools.ToolPolicy`).

### 4. Hard vs soft safety reasons

`ToolJudger.Judge` returns a `JudgeOutcome{Allow, Reason, Severity}` with
severity `hard` or `soft`:

- **Hard** — a fired security control: blacklist pattern match, SSRF (private/
  reserved addresses, degraded SSRF protection, unjudged URL), symlink escape
  out of session roots (or unresolvable/suspicious path). A hard reason forces
  a confirmation that **Smart Approve can never pass** and whose advisory
  "Ask Agent" action is disabled (`DisableJudge=true`).
- **Soft** — a scope question: path containment (file tools, out-of-root shell
  paths). Soft reasons are what Smart Approve exists to weigh; without Smart
  Approve they fall back to a plain confirmation.

Severity is per-source: the SDK file judge's path containment is a soft scope
question by design (sp4rk's safety model reasons about the symlink-resolved
path), while c0wrk's registry-level symlink analysis classifies an escape or
unresolvable traversal as a fired control — hard — so `splitSafetyReasons`
can never let it be auto-approved away.

A symlink whose resolution stays inside the session roots is explicitly **not**
a reason (containment reasons about resolved paths; OS-level symlinks like
`/tmp → /private/tmp` remain exempt via `IsOSLevelSymlink`).

### 5. Mechanisms removed ("killed")

- Registry per-tool machinery: `SetPolicyOverrides`, `SetDefaultPolicy`,
  `SetSkillPolicyOverrides`, `resolvePolicy`, and their backing fields.
- Skill-derived policy overrides end-to-end: `buildSkillPolicyOverrides`
  (orchestrator) and its call in `orchestrator_handle.go`. Skills may still
  narrow the *available* toolset (descriptors), never the *policy*.
- `IsInternalTool` / the `internalTools` name-set — replaced by
  `tool.Group() == GroupSystem`. (A brief `IsSystemToolName` name-bridge
  served transitional per-tool validation; it was removed together with the
  dead per-tool validation loop.)
- Config `security.tool_policies` and `security.default_policy` — replaced by
  `security.groups` (see "Legacy schema" below).
- Name-based subagent/verifier toolsets: `mandatorySubagentTools`,
  `subagentReadOnlyToolNames`, `parseToolNames`, `filterToolsByName`,
  `unionToolDescriptors`, `normalizeToolPreference`.

### 6. Group-based tool budgets (AGENT.md, delegate, verifier)

`AGENT.md` frontmatter `tools:` and `delegate`'s `tools` parameter now accept:

- `all` (default) — full toolset minus Conductor-only tools
- `read-only` — `system ∪ local_read ∪ remote_read`, **no MCP**
- a list of kebab-case group tokens (`local-read`, `execute`, …; underscore
  spellings normalize) — grants are **on top of the always-included `system`
  group**

Unknown tokens fail closed at three layers: the profile parser (`ParseError`),
delegate-task validation (the delegation never launches), and the runtime
resolver. The sp4rk agents package exposes `ToolGroupTokens()` /
`NormalizeToolGroupToken()`; a cross-check test pins the token set to
`tools.AllToolGroups()`.

The goal-mode verifier sets switched from name lists to groups: include
`system + local_read + remote_read + execute + local_mcp + remote_mcp` (+
`delegate` in `re_derivation`), hard-exclude groups `local_write` /
`remote_write` plus the goal-control names (`declare_goal_status`,
`execute_plan`, `propose_goal`, `reflect`, `cancel_delegation`, …).
`execute_plan` moved from an implicit to an explicit exclusion (it launches
subagents with full toolsets). The classic mutating builtin names
(`write_file`, `edit_file`, `delete_file`, `delete_directory`,
`create_directory`) stay name-excluded as a mis-tagging backstop: a mutating
builtin accidentally tagged into an included group passes both group checks
but is still stripped. Two non-mutating meta-tools (`batch`,
`read_attachment`) are now included via the system group — previously they
fell out of the name list by accident.

### 7. Legacy schema

`security.tool_policies` and `security.default_policy` are **gone**: the
`SecurityConfig` struct carries no fields for them, nothing back-fills them,
and no migration path exists. Policy is configured exclusively via
`security.groups`.

The YAML decoder ignores unknown keys, so a config file that still carries
stale `tool_policies`/`default_policy` entries loads with those entries having
**no effect** — groups the file does not configure resolve to their defaults
(reads `allow`, mutations `user_confirm`). There is **no automatic migration
and no backup**: a pre-groups installation upgrading across this change
converts its config by hand — `tool_policies.<tool>.blacklist` entries from
either platform list (`bash_exec` and `posh_exec` unify) merge into
`groups.execute.blacklist`, and `tool_policies.<tool>.policy` maps to the
policy of the tool's owning group (see the group table above). A legacy file
that is not hand-converted silently drops those settings on the next Save
(verified by test: legacy keys load inert and are wiped).

## Consequences

**Positive:**

- Policy is set once per *capability*, not once per tool: new tools inherit
  their group's policy automatically; renames and platform splits can't
  silently drop coverage; the bash/posh blacklist split is unified.
- The bypass class is declared where the tool is defined (`ToolGroup:
  GroupSystem`), not maintained as an out-of-band string set — and it is
  explicitly unconfigurable.
- One resolution rule replaces four (group policy, full stop); `deny` is a hard
  block no mechanism can weaken; hard reasons are unbypassable by Smart
  Approve by construction.
- Subagent budgets, verifier sets, and policy share one vocabulary — the same
  eight groups — and all fail closed on unknowns.

**Negative / trade-offs:**

- Per-tool exceptions are no longer possible ("allow `read_file` but confirm
  `ripgrep`" — both are `local_read`). This is deliberate: the per-tool layer
  was where the model was weakest; targeted exceptions should become a new
  group or a `ToolJudger` heuristic, not a config name-map.
- One mis-tagged tool gets its group's whole policy. Mitigations: the verifier
  hard-excludes whole mutating groups as a backstop, tests pin every builtin's
  group (`TestBuiltinToolGroups_ExactMapping`), and the undeclared-group
  fail-closed rule makes omissions loud.
- `remote_write` has no builtin members today — it exists for MCP servers that
  pin it and future tools; the UI shows it as an empty group.

## Alternatives Considered

- **Keep per-tool config, add groups as sugar.** Rejected: two vocabularies
  with precedence rules re-creates the exact resolution-chain confusion this
  ADR removes.
- **Groups with an `unknown` fallback group.** Rejected: an `unknown` bucket
  has a policy by definition, which either silently gates or silently allows
  untagged tools. Fail-closed-on-undeclared is explicit and loud.
- **Keep skill-policy overrides as a layer over groups.** Rejected: skills
  already narrow the *available* toolset; letting them also relax *policy*
  made the effective policy depend on routing history. Policy is now
  user-config-only.
- **Migrate configs lazily (read both schemas forever).** Rejected: dual-read
  means dual-source-of-truth and untestable precedence. The schema switch is
  a clean break — `security.groups` is the only policy surface.
- **Name-based token lists for `tools:` (status quo).** Rejected: name lists
  drift on every rename; group tokens are closed-vocabulary, validatable, and
  shared with policy.

## Related

- [../architecture/security-model.md](../architecture/security-model.md) — the group-policy enforcement layer, gate order, and invariants
- [../domains/tool-system/README.md](../domains/tool-system/README.md) — registry pipeline and configuration
- [../domains/tool-system/builtins.md](../domains/tool-system/builtins.md) — tool catalog with group column
- [021-subagents.md](021-subagents.md) — `AGENT.md` `tools:` group format
- [../domains/goal-mode.md](../domains/goal-mode.md) — group-based verifier toolsets
- sp4rk `tools/group.go`, `tools/safety.go` (`JudgeOutcome`), `agents` package — engine-side primitives
