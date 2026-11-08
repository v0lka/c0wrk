# Capability Activation Modes (chat/code)

## Role

Defines the `capabilities:` config section that gives every mentionable capability kind — MCP servers, Subagents (agent profiles), and Skills — an **independent activation mode per session context**: a CHAT (No Project) session and a CODE (real project) session each get their own `auto` | `manual` | `disabled` value for every capability. The active slot is selected once per task entry from the session kind. Decision: [ADR-084](../decisions/084-per-capability-chat-code-activation-modes.md), which extends [ADR-077](../decisions/077-explicit-mcp-server-mentions.md)'s single per-server MCP mode to per-context pairs and to all three kinds (mention grammar, task-scoped mention durability, and the gated-complement rule carry over unchanged).

## Key Files

- `backend/config/config.go` — `CapabilitiesConfig` (`skills` / `agents` / `mcp` maps of `CapabilityModes{ChatMode, CodeMode}`, yaml/json `chat_mode`/`code_mode`, omitempty — empty = `auto`); `MCPServerConfig.Mode` retained only as the legacy YAML key (json `-`)
- `backend/config/mcp_mode.go` — legacy `mcp.servers.<name>.mode` migration (`migrateLegacyMCPServerModes`), load normalization (`normalizeCapabilities`), save-path gate (`ValidateCapabilities`)
- `backend/frontend_api_capabilities.go` — `GetCapabilityModes` / `UpdateCapabilityModes` RPCs over one configMu-coherent snapshot, their wire DTOs (`CapabilitiesInfo`, `CapabilityModesInfo`) and the section-id constants (`CapabilitySectionSkills` / `CapabilitySectionAgents` / `CapabilitySectionMCP`)
- `backend/frontend_api_mcp.go` — `GetMCPStatus` / `GetMCPMentionableServers` payloads carrying `chat_mode`/`code_mode` per server
- `backend/configadapter.go` — verbatim transfer of the modes: `capabilities.skills` / `capabilities.agents` into `BuilderConfig.Capabilities` (`capabilityModesToCore`), and `capabilities.mcp.<name>` onto each `BuilderMCPServer.ChatMode`/`CodeMode`; absent name = zero value (`auto`/`auto`)
- `core/builderconfig.go` — `core.CapabilityModes{ChatMode, CodeMode}` (string-compatible with the backend enum; core does not import backend/config), `BuilderConfig.Capabilities{Skills, Agents}`
- `core/builder_mcp.go` — gateway dial decision (`configToGatewayConfig`) and the MCP mode snapshot (`mcpServerModesFromConfig`)
- `core/builder_capabilities.go` — builder-level snapshots for all three sections: `capabilityModesFromConfig`, the cloning getters (`currentSkillModes` / `currentAgentModes`) the orchestrator resolvers read, and `UpdateCapabilities` (the whole-snapshot republish `UpdateCapabilityModes` performs on the live builder)
- `core/orchestrator_mcp.go` — session-kind resolution (`sessionCapabilityContext`), context projection (`capabilityModesForSession`), the per-context gated set
- `core/orchestrator_capabilities.go` — per-entry snapshot attachment (`attachCapabilitySnapshots`) and the mention filters (`mentionedSkillsForTask` / `mentionedAgentsForTask`)
- `frontend/src/lib/capabilityModes.ts` — mentionable predicates shared by `/`-completion and the send path (`isMentionableInContext`, `mentionableNamesInContext`, `sessionKindFromProjectId`)
- `frontend/src/lib/sessionKind.ts` — frontend session-kind resolution (`sessionKindForSession`, `activeSessionKind`)
- `backend/frontend_api_skills.go` / `backend/frontend_api_agents.go` — `ListSkills`/`ListAgents` catalog reads (a watcher/project-switch-invalidated cache) and the `RefreshSkills`/`RefreshAgents` manual re-discovery RPCs: a cache-generation bump + re-read, so every refresh call re-scans the skill/agent directories (watchers never see directories created after startup)
- `frontend/src/components/settings/CapabilitiesSettings.tsx` — the Settings → Capabilities surface (Subagents → MCP Servers → Skills); the Subagents/Skills headers carry MCP-style reload buttons wired to the refresh RPCs

## Behavior

### The three modes, per context

Every capability carries two independent slots — `chat_mode` and `code_mode`. A session reads **only** the slot of its context; the other slot is invisible to it.

| Mode | MCP server (in that context) | Subagent (in that context) | Skill (in that context) |
| ---- | ---------------------------- | -------------------------- | ----------------------- |
| `auto` (default; empty) | Dialed at gateway start/reconfigure; tools advertised to every agent, subagent, verifier; mention adds only the soft directive | In the implicit roster (`## Available Subagents`); delegable at the Conductor's discretion; mentionable | In the router catalog and restorable from persisted routing; mentionable |
| `manual` | Still **dialed** (warm on mention); tools advertised only after a `/`-mention; a mention enables it task-wide | Hidden from the implicit roster but **resolvable and delegable** via `/agent:` mention; mentionable | Hidden from the router catalog (a hallucinated match is dropped) but **activated** by an explicit mention; mentionable |
| `disabled` | Excluded from the active context's availability: mention discarded, tools gated. The **dial** decision is shared across contexts (below) | Removed from the roster; mention discarded; `buildSubAgentTask` refuses delegation fail-closed | Removed from the catalog; mention discarded; a persisted routing match does not reactivate it |

A `disabled`-in-one-context capability stays fully active in the other: the contexts never bleed. An absent config entry is `auto` in both contexts, so existing configs (and unlisted discovered capabilities) are unchanged.

### Session-kind context

- CHAT = a No Project session (`Orchestrator.isNoProject`); CODE = a real-project session (`sessionCapabilityContext`).
- The context is resolved **once per task entry** — in `prepareTaskMCP` and `attachCapabilitySnapshots`, on both the `HandleMessage` and `Resume` paths — and the entry's whole gate/snapshot set is derived from that one projection. In-flight executions keep their entry snapshot; a chat→code promotion takes effect at the next entry.
- `capabilityModesForSession` projects the two-slot map onto the flat name→mode map for the active context; entries that are empty in the selected slot are dropped (absent = `auto`), and a wholly empty map yields nil (every consumer treats a missing name as auto — zero regression with no `capabilities:` section).
- The frontend mirrors the same selection: `sessionKindFromProjectId` (empty/No-Project id → chat, real project id → code) and `sessionKindForSession` (a session resolves through its `project_id`; unknown falls back to the active project's context — the one a new session would be created in).

### MCP servers

- **Dial rule (shared, both contexts):** a server is omitted from `mcp.GatewayConfig` — never spawned, never connected — only when **both** slots are `disabled` (`configToGatewayConfig`). A server disabled in a single context stays dialed so the other context keeps instant access; its tools are hidden in the disabling context by the per-task gate. The dial decision is evaluated at gateway start and at `UpdateMCPServers`-triggered reconfigures; `UpdateCapabilityModes` deliberately does **not** reconfigure the gateway (the server set is unchanged) — a both-disabled flip stops the process at the next reconfigure or restart, while advertising respects the new mode from the next task entry.
- **Availability (per context):** the allowed set in the active context is `auto_ctx ∪ (manual_ctx ∩ mentioned)`, carried as the **gated complement** (manual-without-mention ∪ disabled, that context) — a name absent from the snapshot stays allowed. Mention durability (`task_mcp_mentions`), the gated-complement transport, the four gate layers, and `ErrMCPAuthorizationState` semantics are exactly the ADR-077 mechanics, now applied to the context-projected modes.
- **A mention never overrides the mode:** a qualified `/mcp:` mention of a server disabled **in the sending session's context** threads and strips like any ref, but the server stays gated. A mention always adds the soft `## Requested MCP Servers` directive.
- **Status surface:** `MCPServerStatusInfo` carries `chat_mode`/`code_mode` (normalized, gateway-only names default to auto). The neutral disabled render is keyed on **both**-disabled — the only combination that never dials; a partially-disabled server keeps its live status. `GetMCPMentionableServers` returns `{name, chat_mode, code_mode}` (secret-free, stale names filtered); the visibility filter is applied frontend-side and hides a server only when it is disabled in the **active** context (`isMentionableInContext`), with the "mention to enable" hint keyed on manual in that same context.
- **Wire shape:** `MCPServerConfig` no longer carries a `mode` field over JSON; add/edit/delete/reload stay on `UpdateMCPServers`, and mode changes go through `UpdateCapabilityModes("mcp", …)` with the **full replacement section**.

### Subagents

- The implicit roster is **auto-only** (`filterRosterAgents` in `enrichAgentContext`): a `manual` agent is hidden from `## Available Subagents` yet resolves and delegates when mentioned; a `disabled` agent is absent and its mention is discarded.
- Delegation is refused fail-closed for a disabled agent (`buildSubAgentTask`: "disabled by capabilities.agents and cannot be delegated to") — mirroring the MCP registry gate; a manual agent delegates normally, and an absent snapshot blocks nothing.
- Mentioned agents pass the `mentionedAgentsForTask` filter (auto and manual pass, disabled is dropped) before `UserAgents` reaches any execution path (Conductor, E2S, Goal).

### Skills

- The router catalog is **auto-only** (`implicitSkillDescriptors` in `routeAndActivateSkills` — the single catalog assembly point for the Conductor, Goal, and resume). A `manual` skill is absent from the catalog: a hallucinated router match of it is dropped (no `SkillsActivated`), while an explicit `UserSkills` mention activates it.
- Restored routing respects the same filter (`implicitSkillNames` in `reactivateSkills` and `collectActiveSkillDescriptors`): a disabled skill named by a persisted routing decision does not reactivate on resume.
- Mentioned skills pass the `mentionedSkillsForTask` filter (auto and manual pass, disabled is dropped).

### Config surface

```yaml
capabilities:
  mcp:
    filesystem:
      chat_mode: "manual"   # auto (default) | manual | disabled
      code_mode: "auto"
  agents:
    code-reviewer:
      code_mode: "manual"
  skills:
    research-deep:
      chat_mode: "disabled"
```

- **Legacy migration:** a `mcp.servers.<name>.mode` value folds into `capabilities.mcp.<name>` **both** contexts at load — but only when the target entry carries no explicit value of its own (an explicit entry wins, all-or-nothing). `Mode` is always erased and disappears from the YAML on the next save; the canonicalized fold survives round-trips.
- **Load normalization (fail-soft):** empty → `auto` silently; an unknown value emits a load warning naming the full key (`capabilities.<section>.<name>.<field>`) and is rewritten in place, so every downstream consumer sees a canonical enum; `capabilities.mcp` names outside `mcp.servers` are dropped silently; both-empty entries are dropped; warning order is deterministic (sections skills → agents → mcp, names sorted).
- **Save validation (strict):** `ValidateCapabilities` rejects any non-empty unknown value up front with the key path and the enum members; structural cleanup (stale names, both-empty entries) is load/normalize work, not a save error.

### Wire surface

- `GetCapabilityModes() CapabilitiesInfo` returns all three sections (`skills` / `agents` / `mcp`) normalized and non-nil — each a `map[string]CapabilityModesInfo` whose `chat_mode`/`code_mode` fields always carry a canonical enum string; `capabilities.mcp` names always resolve against `mcp.servers`.
- `UpdateCapabilityModes(kind ∈ {skills, agents, mcp}, modes map[string]CapabilityModesInfo)` **replaces the whole section** (never merges): strict enum validation, stale `mcp` names trimmed, both-empty entries dropped, persistence through the common atomic config pipeline (`config:updated`), then a republish to the live builder — still under `configMu`, so the pushed `BuilderConfig` is the one just persisted. `OrchestratorBuilder.UpdateCapabilities` rewrites all three immutable snapshots (`mcp` from `mcpServerModesFromConfig`, `skills` and `agents` from `capabilityModesFromConfig`) in one write-lock pass; a cleared section publishes nil and gates nothing. Orchestrators read those snapshots through the cloning resolver getters `Build()` wires (`MCPServerModesResolver` / `SkillModesResolver` / `AgentModesResolver`), so the saved modes reach the next task entry — new task, continuation or resume — with no orchestrator rebuild and no restart, while an in-flight execution keeps the context values its own entry attached. With no builder yet the push is a no-op: construction projects the persisted section. No gateway reconfigure (see the dial rule above).
- `GetMCPStatus` / `GetMCPMentionableServers` carry `chat_mode`/`code_mode` per server (see MCP above).

## Error Handling

- Load-path value errors are fail-soft: warning + in-place canonicalization; the capability never blocks startup or a server from dialing.
- Save-path value errors are fail-closed: the update is rejected with the offending key and the enum list, and the stored config is untouched.
- A disabled capability is never an error state: UI renders it neutrally, mentions are silently discarded (the model-side refusal names the gate — "disabled by capabilities.agents"), and gated MCP calls fail with an actionable error naming the server.

## Invariants

- Every capability mode is a per-context pair: a session reads exactly the slot of its context and never the other one
- The active context (CHAT = No Project, CODE = real project) is selected once per task entry — on sends and resumes alike — and the entry's snapshots stay immutable for the whole execution; in-flight tasks never observe a settings change mid-flight
- An absent capability entry is `auto` in both contexts, and a wholly empty capabilities section reproduces the pre-capabilities behavior exactly (zero regression)
- A server is never dialed only when both its slots are `disabled`; a single-context-disabled server stays dialed and is gated only in that context
- A `disabled`-in-context capability is invisible to implicit discovery in that context, and its mention is discarded — a mention can never activate it (fail-closed for subagent delegation and skill activation alike)
- A `manual`-in-context capability stays mentionable: MCP stays dialed, subagents stay resolvable by `/agent:`, skills stay activatable by explicit mention — each becomes available task-wide after the mention
- Mode changes apply at the next task entry without rebuilding orchestrators; a both-disabled dial flip catches up at the next gateway reconfigure or restart
- Every persisted mode value the load pipeline emits is a canonical enum member; unknown values cannot survive a load
- `UpdateCapabilityModes` replaces its section atomically and never merges with the stored one

## Related Specs

- [tool-system/mcp-gateway.md](tool-system/mcp-gateway.md) — MCP-specific wiring: dial/reconfigure lifecycle, status reporting, mention gating and its four gate layers
- [session-lifecycle.md](session-lifecycle.md) (§ Task-scoped MCP authorization) — durable mention state, per-entry preparation, `ErrMCPAuthorizationState`
- [frontend/README.md](frontend/README.md) (§ Settings → Capabilities) — the settings surface and the session-kind-aware mention filters
- [../decisions/084-per-capability-chat-code-activation-modes.md](../decisions/084-per-capability-chat-code-activation-modes.md) — the decision (supersedes the single-mode part of ADR-077)
- [../decisions/077-explicit-mcp-server-mentions.md](../decisions/077-explicit-mcp-server-mentions.md) — the superseded single-mode MCP decision; mention grammar and task-scoped durability carry over
- [../decisions/076-unified-slash-mentions.md](../decisions/076-unified-slash-mentions.md) — the unified `/`-mention grammar
