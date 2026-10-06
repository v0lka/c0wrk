# MCP Gateway

## Role

c0wrk wires sp4rk's MCP (Model Context Protocol) gateway into the orchestration builder: it starts configured MCP servers at startup, discovers their tools, and registers them into the core tool registry. The `Gateway`, `Server`, and `mcp.Tool` lifecycle, transports, and schema sanitization are **sp4rk engine** primitives — see [the sp4rk mcp-gateway spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md).

## Key Files

- `core/builder.go` — `NewOrchestratorBuilder` starts the gateway in `runMCPInit()` (a dedicated goroutine, gated by `mcpDone`, decoupled from `runAsyncInit`) and registers discovered tools into the core registry
- `backend/frontend_api_mcp.go` — `GetMCPStatus` (live per-server status) / `UpdateMCPServers` (persist config + hot-reload, which calls `OrchestratorBuilder.ReconfigureMCP`) surface for the frontend MCP management UI
- `core/orchestrator_mcp_prepare.go` — synchronous durable intent preparation before send/resume execution
- `backend/session/persistence_mcp.go` — transactional monotonic name union and ordered load; adapter and fork preserve task ownership
- `core/tools/registry.go` — exposes the wrapped sp4rk `ToolRegistry` (via `r.ToolRegistry`) on which the sp4rk gateway registers MCP tools (see below)

Engine files (`github.com/v0lka/sp4rk/tools/mcp/gateway.go` `Gateway`, `server.go` `Server`, `mcptool.go` `mcp.Tool`) are documented in [the sp4rk mcp-gateway spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md).

## c0wrk Wiring

### Lifecycle

```
NewOrchestratorBuilder()
│
├─ runMCPInit() (dedicated goroutine, gated by mcpDone; decoupled from initDone):
│   └─ mcp.StartGateway(ctx, cfg, b.registry.ToolRegistry, …)  // Connect + DiscoverTools + RegisterTools (RegisterWithSourceCategory with SourceCategoryMCP; partial failure = non-fatal)
│
├─ Application running...
│   ├─ Tool execution: mcp.Tool.Execute() → server.CallTool(name, input)
│   └─ UpdateMCPServers() → persist + ReconfigureMCP → gateway.Reconfigure (atomic: unregister old, stop, start new)
│
└─ Shutdown:
    └─ StopGateway() → gateway.Stop() → close all server connections
```

`EventBackendReady` fires without waiting for the MCP gateway's async init; MCP tools become available asynchronously. The dedicated `EventMCPReady` (`mcp:ready`) event fires once the startup goroutine completes (success or failure).

`StopGateway` bounds its wait for an in-flight startup: it aborts the startup goroutine (cancelling its context), which stops the not-yet-published gateway it owns and joins within a short grace — so a quit during the first seconds of startup neither stalls the shutdown thread nor leaves MCP server processes orphaned. Once the stop decision is made, no gateway is published or reconfigured (`mcpStopping` guards both, and a stopped `mcp.Gateway` refuses `Reconfigure`).

### Registration into the Core Registry

MCP tools are wrapped in sp4rk `mcp.Tool` (implements the `Tool` interface) and registered by the sp4rk gateway via `RegisterWithSourceCategory(mcpTool, serverName, SourceCategoryMCP)` on the embedded sp4rk `ToolRegistry` (`b.registry.ToolRegistry`):

- `DefaultPolicy()` → `PolicyUserConfirm` (external tools, conservative default)
- `IsUntrusted()` → `true` (all MCP tool output is wrapped in `&lt;untrusted-content>` tags)
- Source tag: the MCP server's name (e.g. `filesystem`); source category `SourceCategoryMCP`

### Status Reporting (frontend UI)

`gateway.Status()` returns per-server `ServerStatus` (`Name`, `Transport`, `Connected`, `Unhealthy`, `Starting`, `ToolCount`, `Tools`, `Error`). The frontend MCP management UI consumes it via `GetMCPStatus`, whose payload wraps each entry in `MCPServerStatusInfo` — the gateway status plus the configured per-server activation `mode` (`auto` | `manual` | `disabled`; gateway-only names default to `auto`).

Every **configured** server is always visible in the settings UI, unavailable ones rendered with a red indicator:

- `Gateway.Status()` includes configured servers whose connection/discovery failed (`Connected: false` + last error; kept in the gateway's separate `failedServers` map, never in the live connection map).
- `FrontendAPI.GetMCPStatus` additionally merges names present in the stored config but absent from the gateway status (gateway missing or failed to start, config ahead of a failed reconfigure) as disconnected entries with error `unavailable`.
- A **disabled** server renders NEUTRALLY in both shapes: a configured-but-absent name is synthesized WITHOUT the `unavailable` error (absence from the gateway is exactly what "never dialed" produces), and a name a stale gateway still reports (a failed mid-flight reconfigure can leave the old connection behind) is neutralized — the config mode is authoritative, so `Connected`/`Unhealthy`/`Starting`/`Tools`/`Error` are cleared and the UI renders the entry by its mode instead of a live-looking or red state.
- While the gateway startup placeholder (`_gateway`, `Starting: true`) is active, the merge is suppressed — availability is unknown; the UI shows "Starting…" and refreshes on `mcp:ready`.

`GetMCPMentionableServers` complements the status surface with a secret-free `{name, mode}` listing of every configured server (name-sorted, modes normalized) for the chat-input `/`-completion and the send path — it carries no transport details and no credentials by construction. The auto+manual filter (disabled hidden) is applied frontend-side (`mentionableMCPNames`), the same single source the completion sections and the mention partition consume.

## Configuration

From `config.yaml`:

```yaml
mcp:
  servers:
    filesystem:
      transport: stdio
      command: "npx"
      args: ["-y", "@modelcontextprotocol/server-filesystem", "/path"]
      env:
        NODE_PATH: "/usr/local/lib/node_modules"
      mode: "auto"          # auto (default) | manual | disabled
      timeout: "60s"       # handshake + default per-call bound
      call_timeout: "30s"  # optional per-call override

    remote-api:
      transport: http
      url: "http://localhost:8080/mcp"
      headers:
        Authorization: "Bearer ${MCP_TOKEN}"
```

Env vars are expanded as `${VAR}`. Transport types (stdio/http), schema sanitization, and server connection behavior are engine concerns — see [the sp4rk mcp-gateway spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md).

### Per-server timeouts

Each server entry accepts two optional duration keys (Go duration strings — a unit suffix is required, e.g. `60s`, `2m`, `500ms`):

- `timeout` — bounds the server's **initialization handshake** (`initialize` + `tools/list`) and is the **default bound for every `tools/call`** on that server. Default **60s**.
- `call_timeout` — optional **per-call override** for a single `tools/call`; unset inherits `timeout`, so a per-call wire timeout is always in effect.

A key that is omitted, empty, unparseable, or non-positive resolves to its fallback: `timeout` to the built-in **60s** default, and `call_timeout` to the resolved `timeout` (so a bad `call_timeout` under `timeout: 5m` yields 5m, not 60s; 60s only when `timeout` is itself unset). The UI save path rejects an invalid value up front; on the load path a bad value **fails soft** to that fallback with a load warning — surfaced through the config load-warnings channel (`configLoadErrors`) and logged at WARN — so it never prevents the server from starting. The resolved bounds are captured at connect time, so editing a timeout re-applies it by reconnecting that server (a timeout-only change is reconnect-worthy). The bound mechanics and the unhealthy flag are engine concerns — see [the sp4rk mcp-gateway spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md).

### Per-server mode

Each server entry accepts an optional `mode` key — the activation enum `auto` (default) | `manual` | `disabled`:

- `auto` — connect whenever the gateway starts or reconfigures (the behavior of every server before the mode existed; the effective value for an omitted or empty key).
- `manual` — keep the configuration live and surface the server as **mentionable** (`GetMCPMentionableServers`) for the chat-input completion / send flow instead of treating it as always-on. The server is still **dialed** at gateway start/reconfigure (so its tools are warm the moment they are mentioned); the gating applies to which toolset is **advertised** (see [Mention gating](#mention-gating-manual-mode)).
- `disabled` — **never dialed**: the server is omitted from the gateway config entirely (`configToGatewayConfig`), so no process is spawned and no connection is opened, neither at startup nor on reconfigure. The settings UI renders it neutrally (never as an error — see [Status Reporting](#status-reporting-frontend-ui)).

The UI save path rejects an unrecognized value up front (`validateMCPServerConfig`); on the load path it **fails soft** — `normalizeMCPModes` resets it to `auto` and emits a load warning through the same channel as the timeout fallbacks — and, unlike the timeout keys (whose raw strings are left for the adapter to resolve), the canonicalized value is **rewritten in place**, so every consumer downstream of the load pipeline (the config→builder adapter, the status merge, the mentionable RPC, the frontend) sees a canonical enum value without re-normalizing. `manual` and `disabled` survive config round-trips unchanged; `BuilderMCPServer.Mode` carries the value verbatim and `core` skips only `disabled` when building the gateway config.

### Mention gating (`manual` mode)

Which servers' tools a task may see is decided by the **allowed set**
`auto ∪ (manual ∩ mentioned)` — `disabled` is never allowed, mention or not
([ADR-077](../../decisions/077-explicit-mcp-server-mentions.md)). The set is
computed per task (`core/orchestrator_mcp.go` `gatedMCPServerSet`) and carried
as the **gated complement** — manual-without-mention ∪ disabled — attached
at each task launch or resume boundary to the execution context (`core/tools/mcp_gate.go`
`WithGatedMCPServers`). Carrying the complement, not an allowlist, is
deliberate: a server absent from the mode map (a gateway-only registration,
or one added by a config change while the task runs) stays **allowed**
instead of being silently dropped by a snapshot.

The builder owns the current immutable mode map, initialized from accepted
configuration and replaced by `ReconfigureMCP` before gateway readiness/network
work. `Build()` gives every session orchestrator a live snapshot provider
(`OrchestratorConfig.MCPServerModesResolver`); direct constructors without a
provider retain their static `MCPServerModes` fallback. At each new-task,
continuation, or resume entry, `prepareTaskMCP` reads the provider exactly once
and derives the gated complement from that snapshot and the task's mentions.
The resulting context remains stable throughout that execution, including all
catalogs, delegation, verifier, and dispatch gates. An accepted auto→manual or
manual→auto settings change therefore affects the next entry in an existing
session without rebuilding its orchestrator or mutating a running task's gate;
a failed gateway reconciliation leaves the accepted mode policy authoritative.

**Mentions are task-scoped, monotonic, and durable.** The common
`prepareTaskMCP` helper uses the blackboard's actual task ID to load selected
server names and transactionally union accepted additions before execution,
then reload the committed union (including concurrent accepted additions).
`TaskPersistence.PersistMCPMentions` / `LoadMCPMentions` are required,
context-aware, error-returning methods; `TaskStoreAdapter` forwards them to
`TaskStore.SaveMCPMentions` / `LoadMCPMentions`. The synchronous preparation is
separate from best-effort blackboard write queues; in-memory state is not the
authority for a persistent task.
`task_mcp_mentions` stores one task/server pair per row, with task deletion
cascade (including session deletion); fork copies the rows in the fork
transaction under independent remapped task IDs. Empty legacy
state is a successful empty load, while persistence failures return
`ErrMCPAuthorizationState`, stop all execution, and preserve the task for retry
without continuation-to-fresh fallback. Explicitly nonpersistent orchestrators
retain an ephemeral task-local set; persistent tasks never downgrade to it.

Selections survive pause→resume, continuation and app restart. The builder
publishes immutable current mode snapshots on accepted reconfiguration before
fallible gateway reconciliation; every task entry snapshots modes once and
computes permissions from names, not persisted tool or permission snapshots.
Both mention and gate context values are overwritten, including empty values.
Resume prepares before its system-driven plan/delegate auto-resume wave and uses
the same context for the wave and main loop. E2S consumes the prepared names
outside model-controlled Σ. In-flight contexts remain unchanged.

Mirroring the other mention kinds: a mention sent while a task is
**running** is rejected (`liveSendRejectionLocked` — same guard as
skill/agent refs, "cannot be sent while a task is running"), and a mention
nudged into a **paused** task is not threaded into Resume (the nudge-resume
path never carried `UserAgents` either).

**Where the gate bites** — four layers, all keying on the tool's MCP source
tag (the server name), never on tool names:

1. the orchestrator's fresh and resume paths filter the advertised descriptor
   list and attach the task gate **before any execution**, including the
   auto-resume wave, so the filter propagates to resumed plan steps/delegates,
   the Conductor, the E2S tool catalog, and the verifier;
2. the delegation launcher (`conductor.go` `resolveTaskTools`) strips gated
   sources from the **raw registry list** across every grant branch (`all`,
   `read-only`, group lists, profile lists) — a gated server cannot ride an
   "all tools" or `local_mcp`/`remote_mcp` group grant back into a subagent;
3. the registry's `Execute` dispatch gate (Gate 2b, defense-in-depth) rejects
   a call whose server is gated — including a hallucinated or stale-name
   call — with an actionable error naming the server, before the
   system-group bypass;
4. the gated set rides the task context, so everything derived from it
   (executor, subagents, verifier, E2S loop) inherits the same boundary.

With no manual/disabled servers configured the descriptor filter is a no-op.
The context still carries explicit empty values so a caller-supplied stale gate
or mention set cannot cross task boundaries.

**Mention syntax and surfaces** (the third namespace of the unified `/`
grammar, [ADR-076](../../decisions/076-unified-slash-mentions.md)): the
plain form `/server-name` is partitioned at send time against the three
catalogs (agents / skills / MCP servers — a name in more than one is an
ambiguous no-op with a hint); the collision-qualified form `/mcp: <id>`
(spaced canonical, `/mcp:<id>` accepted) is always valid input. The frontend
threads mentions as `activeMCPServers` (`sendMessage` arg 5 → Go
`SendMessage` → `HandleOptions.UserMCPServers`), and
`PreprocessMessageText` strips plain and qualified MCP refs from the message
text, catalog-gated and fail-closed like the other kinds. The `/` completion
list renders an **MCP Servers** section between Subagents and Skills
(Subagents → MCP Servers → Skills), listing `auto` + `manual` servers only
(`disabled` hidden; manual entries carry a "mention to enable" detail).

**Soft directive.** When one or more servers are mentioned, the system
prompt gains a `## Requested MCP Servers` section (Conductor and E2S prompts
alike; absent otherwise): the user explicitly selected these servers, their
tools are in the toolset, "prefer them … a preference, not an obligation" —
deliberately soft wording, unlike the Requested-Subagents MUST. Mentioning
an `auto` server adds only the directive (its tools were already available).
A mention can never override the mode: a qualified mention of a `disabled`
server threads and strips like any qualified ref, but the gate keys on the
config mode, so `disabled` means never.

Capability-group policy ([ADR-024](../../decisions/024-group-policies.md)) is
unchanged and still applies **on top** of the server gate.

## Invariants

- MCP gateway failure is non-fatal (application starts without MCP tools)
- MCP tools always carry source category `SourceCategoryMCP` (source tag = the server name)
- MCP tools default to `PolicyUserConfirm` (never auto-execute untrusted external tools)
- All MCP tools are untrusted (`IsUntrusted()` returns `true`)
- `ReconfigureMCP()` is atomic: old servers stopped before new ones started
- Every MCP server has a bounded initialization handshake (`initialize` + `tools/list`) and a bounded per-call wire timeout; both default to 60s, and an unset `call_timeout` inherits `timeout` — a server never operates with an unbounded handshake or call
- A server is marked `Unhealthy` after **3 consecutive** `tools/call` timeouts; the mark is advisory and never disconnects the server (a timeout does not kill or close the connection), and a clean call clears it
- Timeout errors surface to the agent loop as a typed error (never swallowed); the server stays connected
- A `disabled` server is never dialed: it never appears in `mcp.GatewayConfig`, so the gateway neither spawns its process nor opens its connection — at startup and on every reconfigure alike
- An unrecognized `mcp.servers.<name>.mode` never prevents a server from starting: the load path resets it to `auto` with a warning, and the save path rejects it up front
- A task's allowed MCP set is `auto ∪ (manual ∩ mentioned)`, carried as the **gated complement** — a server absent from the mode map (gateway-only or added mid-task) stays allowed
- Every task entry in an existing session uses one live builder mode snapshot; all catalogs and dispatch gates retain that snapshot throughout execution, and accepted settings changes apply at the next entry even if gateway reconciliation fails
- A gated server's tools appear in **no** advertised catalog (main agent, delegated subagents, verifier, E2S) and a dispatched call to them is rejected at the registry gate with an error naming the server (fail-closed in depth)
- The gate keys on the tool's MCP source tag (the server name), never on tool names
- MCP mentions accumulate monotonically in durable task state and survive restart; current modes determine permissions at each entry, so a mentioned `disabled` server stays gated
- Durable intent preparation completes before every resume wave; a persistence failure runs no task work and remains a visible retryable authorization-state error

## Related Specs

- [sp4rk mcp-gateway](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md) — canonical Gateway/Server/mcp.Tool lifecycle, transports, schema sanitization
- [../../decisions/077-explicit-mcp-server-mentions.md](../../decisions/077-explicit-mcp-server-mentions.md) — the per-server mode + mention-gating decision (ADR-077)
- [../../decisions/076-unified-slash-mentions.md](../../decisions/076-unified-slash-mentions.md) — the unified `/`-mention grammar the MCP namespace joins
- [README.md](README.md) — tool system overview
- [../../architecture/security-model.md](../../architecture/security-model.md) — MCP tool policies
- [../../contracts/backend-core.md](../../contracts/backend-core.md) — ReconfigureMCP wiring
