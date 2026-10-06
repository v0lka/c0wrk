# ADR-077: Explicit MCP Server Mentions (per-server `auto` / `manual` / `disabled` + `/`-mention gating)

## Status

Accepted

## Context

Before this decision, **every** configured MCP server was unconditionally
connected when the gateway started or reconfigured, and **every** discovered
MCP tool was advertised to every agent, delegated subagent, and verifier —
subject only to capability-group policy ([ADR-024](./024-group-policies.md)).
Two affordances were missing:

- keeping a server **configured** (transport, credentials, timeouts in place)
  but **out of the default toolset**, and
- pulling one in **explicitly for a single task**.

[ADR-076](./076-unified-slash-mentions.md) had just unified the "trigger a
capability by name" affordances — skills and subagent profiles behind one `/`
trigger, one completion list, collision-qualified `/agent:` / `/skill:` forms.
Issue [#111](https://github.com/v0lka/c0wrk/issues/111) extends the same
mechanism to MCP servers: a third `/mcp:` namespace, a third labelled
completion section, and a per-server activation mode. Granularity is
**servers, never individual tools**.

(Numbering note: issue #111 drafted this ADR as 073 and #110's as 072; both
were taken by the time of writing — #110's decision is
[ADR-076](./076-unified-slash-mentions.md), so this is 077.)

## Decision

**A per-server activation mode in config, a task-scoped mention gate in core,
and a soft prompt directive.**

1. **Per-server mode** — `mcp.servers.<name>.mode`, a closed enum:
   `auto` (the default; omitted/empty resolves to `auto`, so existing configs
   are byte-for-byte backward compatible) | `manual` | `disabled`.
   - `auto` — connected whenever the gateway starts or reconfigures; its
     tools are advertised to every agent, subagent, and verifier, exactly as
     before the mode existed. Still mentionable — a mention only adds the
     soft directive (below).
   - `manual` — **still connected** whenever the gateway starts or
     reconfigures (so its tools are warm the moment they are mentioned), but
     its tools are **not advertised** unless the server is explicitly
     `/`-mentioned; a mention makes them available **task-wide**.
   - `disabled` — **never dialed**: `configToGatewayConfig` omits the server
     from `mcp.GatewayConfig` entirely, so no process is spawned and no
     connection is opened — neither at startup nor on reconfigure. The
     settings UI renders it neutrally (never as an error), and it is hidden
     from the completion list.
   - The mode is validated like the timeout keys: the UI save path rejects an
     unrecognized value up front; the load path **fails soft** — an
     unrecognized value is reset to `auto` with a load warning and, because
     the enum is closed, the canonical value is **rewritten in place**, so
     every consumer downstream of the load pipeline sees a canonical enum.

2. **Mention-gated availability** — a task's allowed MCP set is
   `auto ∪ (manual ∩ mentioned)`; `disabled` is never allowed, mention or
   not. The set is carried through the task context as the **gated
   complement** (manual-without-mention ∪ disabled) rather than an allowlist
   snapshot, so a server absent from the mode map — a gateway-only
   registration, or one added by a config change while the task runs — stays
   **allowed** instead of being silently dropped.

3. **Task-wide, durable, monotonic mentions** — mentions accumulate per
   **task**: each message's mentions are unioned with the task's persisted
   mention set, so a follow-up message **cannot retract** a manual server an
   earlier message enabled. The mention set is part of durable task state,
   keyed by task ID; an in-memory copy is a cache, not the source of truth.
   Accepted mentions are persisted before execution uses them. The set
   survives pause→resume, continuation, and app restart, and is restored
   **before any resumed execution**, including the system-driven auto-resume
   wave that relaunches plan steps and delegated subagents. A fresh task
   starts with its own mention set; tasks persisted without this state have
   an empty set.
   `task_mcp_mentions` stores `(task_id, server_name)` pairs with a composite
   primary key and a task FK `ON DELETE CASCADE`. `TaskPersistence` exposes
   context-aware, error-returning `PersistMCPMentions` / `LoadMCPMentions`;
   `TaskStoreAdapter` forwards them to `TaskStore.SaveMCPMentions` /
   `LoadMCPMentions`. Writes transactionally union names (duplicate pairs
   alone are ignored); reads return the ordered union. Fork copies these
   rows under independently remapped task IDs in the fork transaction;
   deleting a task, including through session deletion, removes its rows.
   The shared `prepareTaskMCP` helper uses the blackboard's actual task ID,
   loads intent, unions accepted additions, and reloads the committed union
   before execution. Legacy absence is an empty successful load; load,
   union-write or reload failures return `ErrMCPAuthorizationState` and stop
   task work before routing, a resume wave or an LLM turn. Backend
   continuation-to-fresh fallback excludes this error, preserving the same
   task for retry. Persistent tasks never downgrade to memory or silently
   treat failed reads as empty; explicitly nonpersistent orchestrators alone
   retain ephemeral task-local intent.
   Persisted state contains server names, not a snapshot of tool descriptors
   or effective permissions. At each new-task, continuation or resume entry,
   availability is computed from the task's mentions and **one current mode
   snapshot**. The builder initializes a copied immutable mode map from
   accepted config and publishes a replacement on accepted `ReconfigureMCP`
   before fallible gateway readiness/network work. Every builder-created
   orchestrator receives `MCPServerModesResolver`; preparation reads it once
   after durable intent is ready (direct constructors without a resolver use
   the static `MCPServerModes` fallback). Catalogs, delegates, E2S, verifier
   and dispatch retain that entry's context gate, so in-flight contexts stay
   unchanged and accepted settings changes apply on the next entry even if
   gateway reconciliation fails. The permission rule remains
   `auto ∪ (manual ∩ mentioned)`; a server now `disabled` stays gated even
   when its name is persisted. Restart is an execution boundary, not a
   revocation of the user's task-scoped selection.
   Mirroring the existing mention semantics: a mention sent while a task is
   **running** is rejected outright (the `liveSendRejectionLocked` guard
   shared with skill/agent refs), and a mention nudged into a **paused** task
   is not threaded into Resume (the nudge-resume path never carried
   `UserAgents` either).
   (Issue #111's draft described the scope as "per message, repeat each
   time"; task-wide availability takes precedence — see Alternatives.)

4. **Soft directive** — when one or more servers are mentioned, the system
   prompt gains a `## Requested MCP Servers` section (Conductor prompt and
   E2S system prompt alike; absent otherwise): the user explicitly selected
   these servers, their tools are in the toolset, prefer them where they
   fit — **"a preference, not an obligation."** Unlike `## Requested
   Subagents` (a MUST-delegate directive, [ADR-021](./021-subagents.md)), a
   server is a capability, not a delegation target; hard wording would
   invite tool-call theater. Mentioning an `auto` server adds only the
   directive (its tools were already available).

5. **Where the gate bites** — four layers, all keying on the tool's MCP
   source tag (the server name), never on tool names:
   - the orchestrator's fresh and resume paths compute and attach the gate
     **before any task execution**, including the system-driven auto-resume
     wave, and filter the advertised descriptor list before the
     E2S/goal/conductor branches. The same gate propagates to resumed plan
     steps and delegates, the Conductor, the E2S tool catalog, and the
     verifier (whose toolset is built from the already-filtered list);
   - the delegation launcher strips gated sources from the **raw registry
     list** across every grant branch (`all`, `read-only`, group lists,
     profile lists) — a gated server cannot ride an "all tools" or
     `local_mcp`/`remote_mcp` group grant back into a subagent;
   - the registry's `Execute` gains a dispatch-time gate (Gate 2b,
     defense-in-depth): a call to a tool whose server is gated — including a
     hallucinated or stale-name call — is rejected with an actionable error
     naming the server, before the system-group bypass;
   - both the mention set and the gated set are replaced at each execution
     entry, including explicit empty values that clear caller-supplied stale
     context. Everything derived from that context (executor, subagents,
     verifier, E2S loop) inherits the same boundary. E2S reads prepared names
     outside model-controlled Σ, including for its restored soft directive.

6. **Mention syntax** — the third namespace of the ADR-076 grammar. The
   plain form `/server-name` is partitioned at send time against the three
   catalogs (agents / skills / MCP servers); a name present in more than one
   is an **ambiguous no-op** (nothing threaded, text preserved, hint shown).
   The collision-qualified form `/mcp: <id>` (canonical spaced; `/mcp:<id>`
   accepted) is **always valid input** — no catalog check applies, mirroring
   `/agent:` / `/skill:`. Threading: frontend `sendMessage` threads
   `activeMCPServers` (positional arg 5, after `activeAgents`) → Go
   `SendMessage` → `HandleOptions.UserMCPServers` → the orchestrator records
   the mentions and attaches them to the context;
   `PreprocessMessageText` strips plain and qualified MCP refs from the
   message text, catalog-gated and fail-closed like the other kinds. The `/`
   completion list gains a labelled **MCP Servers** section between
   Subagents and Skills (Subagents → MCP Servers → Skills), listing `auto`
   and `manual` servers only (`disabled` hidden; manual entries carry a
   "mention to enable" detail). A new secret-free RPC,
   `GetMCPMentionableServers`, returns the `{name, mode}` pairs that feed
   the completion and the send-path partition; the auto+manual filter is
   applied frontend-side, and the payload carries no transport details or
   credentials by construction.

7. **A mention cannot override the mode** — a hand-typed qualified mention of
   a `disabled` server threads and strips like any qualified ref (rule 6),
   but the server stays gated: the gate keys on the **config mode**, not on
   the mention set, so `disabled` means never — even when mentioned.

Capability-group policy (ADR-024) is untouched and still applies **on top**
of the server gate: an allowed manual server's tools remain subject to
`local_mcp`/`remote_mcp` group policies and tool budgets exactly as before.

## Consequences

**Positive:**

- Servers can stay configured but out of the default toolset — smaller tool
  catalogs, less prompt noise, fewer irrelevant `user_confirm` cards for MCP
  tools the task never needed — and any task can pull one in with a mention.
- Task-scoped selections survive interruption and app restart, so resumed
  work keeps the manual servers the user selected without repeating mentions.
  Current server modes and capability-group policies remain authoritative.
- With no `manual`/`disabled` servers the gated set is empty and descriptor
  filtering is a no-op. Preparation still loads durable intent and overwrites
  mention/gate context, including empty values, so stale caller context cannot
  cross task boundaries.
- Fail-closed in depth: an unmentioned manual or disabled server is absent
  from every advertised catalog (main agent, subagents, verifier, E2S) AND
  rejected at dispatch if called anyway.
- Mode handling reuses the timeout-key contract (save-path reject, fail-soft
  load with a visible warning), so a hand-edited typo can never prevent a
  server from starting.

**Negative / trade-offs:**

- A `manual` server is still dialed at gateway startup — the process runs
  and the connection is open even when no task ever mentions it (chosen so a
  mention pays no cold-connect latency; `disabled` is the "don't even dial"
  mode).
- The task store persists the mention set alongside resumable task state;
  recording mentions and restoring a task require persistence plumbing.
  Legacy tasks without a persisted mention set resume with an empty set.
  Authorization-state storage failures block execution until a retry succeeds,
  rather than degrading to an in-memory selection or starting a fresh task.
- Mentions are monotonic within a task — there is no syntax to retract a
  manual server mid-task.
- The send path consults a third catalog (a cached `GetMCPMentionableServers`
  read, only for messages containing `/`-refs).
- Per-tool granularity remains out of scope: a server is enabled or gated as
  a whole.

## Alternatives Considered

- **Per-message mention scope** (issue #111's draft: "repeat the mention on
  each message; not carried to continuation/resume"). Rejected: it
  contradicts the same issue's task-wide availability requirement — a
  follow-up message omitting the mention would drop a manual server's tools
  mid-task and strand work already built on them. The union is monotonic
  and task-scoped instead.
- **In-memory-only mentions, reset on app restart.** Rejected: the lifetime
  of the process does not define the lifetime of a resumable task. Losing
  the user's selection on restart strands work for the same reason as
  per-message scope; a process restart is not a task-scoped reason to revoke
  that selection. Persisting names preserves it while recomputing
  availability keeps `disabled` servers gated and capability-group policies
  authoritative.
- **Carry the allowed set as an allowlist snapshot.** Rejected: a server
  absent from the mode map (gateway-only registration, or one added by a
  config change mid-task) would be silently dropped; the gated complement
  keeps unmapped servers allowed.
- **Lazy dial: connect a `manual` server on first mention.** Rejected: the
  mention would pay a cold process spawn + handshake before any tool is
  usable; the config is kept live so mentioned tools work instantly
  (`disabled` covers the "don't run it" need).
- **Per-tool granularity** (enable/disable individual MCP tools). Rejected as
  out of scope (#111): server-level only; per-tool gating would need
  name-keyed rules, which ADR-024 rules out for policy surfaces.
- **Gate via the existing disabled-tool-name set** (`disabledToolNames`).
  Rejected: it is keyed by tool *names*, which change with every server
  rename and tool-list refresh; the server name source tag is the stable
  key, and ADR-024 already forbids name-keyed policy.
- **Hard directive wording (MUST use these servers).** Rejected: a server is
  a capability, not a delegation target; obligation wording invites
  tool-call theater. The section says "preference, not an obligation"
  (contrast `## Requested Subagents`).
- **An ambient `## Available MCP Servers` roster for `auto` servers.**
  Rejected (out of scope per #111): only explicit mentions produce a
  directive; ambient rosters would re-grow the prompt noise the mode exists
  to remove.

## Related

- [ADR-076](./076-unified-slash-mentions.md) — the unified `/`-mention
  grammar this builds on (one trigger, three sections, qualified forms,
  ambiguity no-op).
- [ADR-024](./024-group-policies.md) — capability-group policy, unchanged
  and still applied on top of the server gate.
- [ADR-021](./021-subagents.md) — the `## Requested Subagents` MUST
  directive this ADR deliberately contrasts with.
- Issue [#111](https://github.com/v0lka/c0wrk/issues/111) (this feature),
  issue [#110](https://github.com/v0lka/c0wrk/issues/110) (ADR-076).
- [../domains/tool-system/mcp-gateway.md](../domains/tool-system/mcp-gateway.md)
  — per-server mode and mention-gating semantics.
- [../../docs/custom-skills-and-subagents.md](../../docs/custom-skills-and-subagents.md)
  §4.1 — the user-facing mention/reference syntax table.
