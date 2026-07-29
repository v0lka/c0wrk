# ADR-020: Multi-source AGENTS.md threat model

## Status

Accepted

## Context

c0wrk reads `AGENTS.md` project instructions and injects them into the system
prompt as advisory guidance. Historically the only source was the
workspace-root `AGENTS.md` (`<workspace>/AGENTS.md`), read exclusively in CODE
mode (where a workspace exists).

A recent change ([`backend/configadapter.go`](../../backend/configadapter.go)
`agentsMDSearchPaths`, [`core/orchestrator.go`](../../core/orchestrator.go)
`collectAgentsMDPaths`) broadened the source set to three files, concatenated
in priority order:

1. `~/.agents/AGENTS.md` — **global**, shared across all agents on the machine.
2. `~/.c0wrk/.agents/AGENTS.md` — **c0wrk-specific**.
3. `<workspace>/AGENTS.md` — **project-specific** (CODE mode only).

The first two are read on **every** request, including CHAT (No Project) mode
where previously no `AGENTS.md` was read at all. Both live outside the
workspace and outside the c0wrk data directory (`~/.c0wrk/`), so they are not
covered by the workspace-containment or session-root security invariants.

This raises a prompt-injection threat-surface question that must be documented
explicitly: who is expected to own and write these files, and how are they
treated by the security model?

### Forces

- **Utility.** A global instructions file lets users define cross-project
  conventions (coding style, preferred tools, "always run tests before
  finishing") without repeating them in every project. The c0wrk-specific file
  scopes the same idea to one agent.
- **Threat surface.** `~/.agents/AGENTS.md` is a machine-wide writable file.
  Any process or user on the host can create or modify it. Its content now
  feeds the system prompt on every request, including in CHAT mode where no
  workspace-local file was read before.
- **Existing defenses.** The content is already wrapped in
  `<untrusted-content source="AGENTS.md">` tags and framed as advisory /
  non-authoritative in [`core/systemprompt.go`](../../core/systemprompt.go)
  `formatAgentsMD`. When `security.injection_defense.enabled` is true
  (default), the injection-defense prompt section further instructs the LLM to
  treat delimited content as data, not instructions. The hard security boundary
  remains the tool-policy pipeline (policy → judge → confirmation), which is
  unaffected by prompt content.

## Decision

The multi-source `AGENTS.md` design is **accepted with an explicit threat
model**. The global and c0wrk-specific files are treated as **untrusted
advisory input**, exactly like the workspace-root file. No new enforcement is
added; the decision records the threat model and the mitigations already in
place so the security posture is explicit and auditable.

### Threat model

| Source                       | Location                          | Expected writer            | Trust level          |
| ---------------------------- | --------------------------------- | -------------------------- | -------------------- |
| Global                       | `~/.agents/AGENTS.md`             | The user (owner of `~/$HOME`) | Untrusted advisory   |
| c0wrk-specific               | `~/.c0wrk/.agents/AGENTS.md`      | The user (owner of `~/$HOME`) | Untrusted advisory   |
| Project-specific             | `<workspace>/AGENTS.md`           | The user / repo contributors | Untrusted advisory   |

**Owner expectation.** All three files are expected to be authored by the user
who owns the home directory. They are **not** expected to be written by
untrusted processes, package installers, or other agents. c0wrk does not create
or manage these files; it only reads them.

**Untrusted treatment.** Regardless of source, `AGENTS.md` content is:

- Wrapped in `<untrusted-content source="AGENTS.md">` tags by
  `formatAgentsMD` ([`core/systemprompt.go`](../../core/systemprompt.go)).
- Prefixed with an advisory framing that instructs the LLM to treat the content
  as guidance to consider, **not** as authoritative system instructions, and to
  surface contradictions via `ask_user` rather than resolving them silently.
- Subject to the full injection-defense prompt section when
  `security.injection_defense.enabled` is true (default).
- **Not** a bypass for the tool-policy pipeline. Any tool call the LLM
  initiates — even one suggested by `AGENTS.md` content — still passes through
  policy resolution, the `PolicyAlwaysAllow` Judge gate, symlink detection,
  workspace auto-approval, and user confirmation as appropriate. The policy
  layer is the hard security boundary; prompt-level framing is defense in
  depth.

### Residual risk

The reachable attack surface did grow: a machine-wide writable file
(`~/.agents/AGENTS.md`) now feeds every prompt where before only
workspace-local files did. The mitigations above reduce the *impact* of a
malicious file (the LLM is told to disregard embedded instructions, and tool
calls are still gated), but they do not prevent the file from being read. This
is an accepted trade-off for the utility of global instructions.

## Consequences

**Positive:**

- The threat model for multi-source `AGENTS.md` is now explicit and auditable,
  rather than implicit in the code.
- Users gain a cross-project and per-agent instructions channel without
  per-project duplication.
- The untrusted framing and tool-policy pipeline mean a malicious
  `AGENTS.md` cannot silently escalate tool execution — it can only attempt
  prompt-level influence, which the injection defense and advisory framing
  counter.

**Negative / trade-offs:**

- `~/.agents/AGENTS.md` is a machine-wide writable file read on every request.
  A compromised or hostile process on the host can attempt prompt injection via
  this channel. Mitigated by untrusted framing + tool-policy gating; not
  eliminated.
- The combined content of all sources is capped by
  `security.agents_md_max_bytes` (default 65536; see
  [domains/orchestration/README.md](../domains/orchestration/README.md)), so a
  single oversized source cannot unboundedly consume the context window — but
  the cap is on *size*, not *content*.
- CHAT (No Project) mode now reads `AGENTS.md` where it previously read none.
  This is intended (global/c0wrk instructions apply everywhere) but is a
  behavior change worth noting.

## Alternatives Considered

- **Restrict `~/.agents/AGENTS.md` to owner-only permissions at read time
  (warn/skip if group- or world-writable).** Rejected for now: it adds
  platform-specific permission checks for a file c0wrk does not own or manage,
  and the untrusted framing already treats the content as adversarial. A
  permission check would be defense in depth on top of defense in depth. Left
  as a future hardening option if real-world abuse is observed.
- **Make the global/c0wrk sources opt-in via config.** Rejected: it defeats the
  purpose of a zero-config global instructions channel and pushes configuration
  burden onto the user for a feature whose risk is already mitigated by the
  untrusted framing.
- **Read only the workspace-root `AGENTS.md` (revert the multi-source change).**
  Rejected: the multi-source design is a deliberate feature (global +
  c0wrk-specific instructions), and the threat is adequately addressed by
  treating all sources as untrusted advisory input.
- **Treat `AGENTS.md` as trusted system instructions (no untrusted wrapping).**
  Rejected: this would elevate workspace-controlled and machine-wide content to
  the authority of core directives, breaking the injection-defense model. All
  `AGENTS.md` sources are untrusted by construction.

## Related Specs

- [architecture/security-model.md](../architecture/security-model.md) —
  Indirect Prompt Injection Defense (content delimiting, tag-breakout
  protection, system-prompt instructions), tool-policy pipeline, session roots.
- [domains/orchestration/README.md](../domains/orchestration/README.md) —
  `AgentsMDSearchPaths`, `agents_md_max_bytes` configuration, multi-source
  concatenation order.
