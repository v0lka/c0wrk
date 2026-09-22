# ADR-063: MCP Tool Timeouts — Bounded Handshake and Per-Call Calls

## Status

Accepted

## Context

MCP servers are external processes (stdio) or remote endpoints (http) that c0wrk does not control. Before this decision, an MCP operation had **no timeout at all**:

- A server that accepted a connection but never answered the MCP `initialize` or `tools/list` exchange stalled the handshake — and, because the gateway handshake runs during startup and during every `Reconfigure`, it could stall startup or a reconfigure indefinitely.
- A server that accepted a `tools/call` but never replied blocked the agent loop forever, with no way for the caller to distinguish a hung server from a slow one.

The gateway also had no way to tell a persistently unresponsive server from a merely slow one, so callers (and the MCP management UI) could not warn about or deprioritize a dead-but-connected server.

The timeout has to live where the connection and the call actually happen. The c0wrk layer only wires config into the engine; the connection, the `tools/call` execution, and the per-server status all live in sp4rk's `tools/mcp` package (`Server`), so the enforcement belongs there.

## Decision

Bound every MCP operation at the **sp4rk gateway layer** with two per-server durations, resolved once and applied via `context.WithTimeoutCause`, and expose sustained timeouts as an advisory status flag.

1. **Gateway-layer wrap.** `Server.Connect` resolves `ServerConfig.Timeout` / `ServerConfig.CallTimeout` into non-zero `Server.timeout` / `Server.callTimeout` (never zero at any use site), then every lifecycle call derives a child context via `context.WithTimeoutCause` carrying a typed `*TimeoutError` as the deadline cause:
   - `initialize` — bounded by `timeout` (op `initialize`);
   - `tools/list` — bounded by `timeout` (op `list_tools`; discovery is part of the handshake, so a server that answers `initialize` but hangs on `tools/list` cannot stall startup or a reconnect);
   - `tools/call` — bounded by `callTimeout` (op `call_tool`, with the tool name).

2. **Never wrap `client.Start`.** The transport start call is deliberately left unbounded. For stdio the child process is already spawned by the transport constructor with `context.Background()`, and `client.Start` only attaches to it; deriving `Start`'s context from the handshake deadline would **couple the child process's lifetime to the handshake deadline**, so a server merely slow to finish the handshake would have its process killed and the connection torn down, instead of the handshake being aborted and retried. `Start` also returns as soon as the transport is up, so it is not the blocking step a timeout needs to bound. Only the `initialize` exchange onward is time-bounded.

3. **Single 60s default.** `defaultMCPTimeout = 60 * time.Second`. A `Timeout` that is zero or negative resolves to it, so there is one default across the handshake and the (default) per-call bound.

4. **`call_timeout` override.** A `CallTimeout` that is zero or negative inherits the resolved `timeout` (which itself already includes the default), so a **per-call wire timeout is always in effect** while a server that needs a longer individual call can set `call_timeout` independently of its handshake bound.

5. **Unhealthy after 3 consecutive timeouts.** `unhealthyTimeoutThreshold = 3`. A genuine `tools/call` timeout increments a back-to-back counter; on reaching 3 the server is marked `ServerStatus.Unhealthy` and a streak description is recorded as the server's last error. Only **our** timer firing counts — a caller cancellation is surfaced as the parent context error and is never misattributed to the server; the classification lives in `timeoutErrorFor` (our timer fired while the caller's context is still live). Any clean call resets the streak, clears the unhealthy mark, and drops the stale streak error.

6. **Mark, not kill.** A timeout never closes the connection or kills the process. `Unhealthy` is an advisory status flag (surfaced through `ServerStatus.Unhealthy`, non-omitempty) that lets callers and the UI deprioritize or warn about a persistently unresponsive server while it stays connected. The timeout itself surfaces to the agent loop as a typed `*TimeoutError` (with server/op/tool/bound attribution; it unwraps to `context.DeadlineExceeded`), never swallowed.

**Config surface (c0wrk wiring).** `mcp.servers.<name>.timeout` / `.call_timeout` (`MCPServerConfig`) parse through `parseMCPDuration` into `BuilderMCPServer` and are forwarded into `mcp.ServerEntry.Timeout` / `.CallTimeout`. An omitted/empty key resolves to 0 (→ the sp4rk 60s default); an unparseable or non-positive value **fails soft** to 0 with a load warning — surfaced through the production load path (`config.ResolveAndLoad` → `LoadWithResult` → `LoadResult.LoadErrors`, produced by `normalizeMCPTimeouts`, into the UI's `configLoadErrors`, plus a WARN log) so a bad value never prevents a server from starting — while the UI save path (`validateMCPServerConfig`) rejects it up front. Because the bounds are captured at connect time, the gateway's `configChanged` treats a timeout-only edit as reconnect-worthy so the new bound re-applies immediately.

## Consequences

**Positive**

- An unbounded handshake or `tools/call` can no longer hang startup, a reconfigure, or the agent loop.
- Per-server granularity: each server tunes its own handshake bound and per-call bound independently.
- A persistently unresponsive server is visible (`ServerStatus.Unhealthy`) without being torn down, and a genuine timeout is attributable to a specific server/operation/tool via the typed error.
- A caller cancellation is never blamed on the server (`timeoutErrorFor`), so cooperative cancellation stays clean.

**Negative / accepted costs**

- A legitimately long-running tool call must set a larger `call_timeout` (or it will time out at the inherited bound). Accepted: the 60s default is generous for typical MCP tools, and the override exists precisely for the exceptions.
- An aggressive bound can mark a healthy-but-slow server unhealthy. Accepted: the mark is advisory (no connection is lost) and self-clears on the next clean call.
- A timeout-only config edit forces a reconnect of that server (the bounds are captured at connect time). Accepted: it makes the new bound take effect immediately rather than at the next incidental reconnect.

**Security impact:** none. This bounds operations that already ran under the gateway's existing policy/untrusted gates; it changes only how long a hung external server may block and how its unresponsiveness is reported.

## Alternatives Considered

- **Enforce the timeout in c0wrk (backend) instead of sp4rk.** Rejected: the connection and the `tools/call` execution live in the sp4rk `Server`; c0wrk only wires config. Wrapping the c0wrk side would leave the handshake and the per-server status in the engine unbounded, and would duplicate connection ownership.
- **Wrap `client.Start` with the handshake timeout too.** Rejected: for stdio it couples the child process's lifetime to the handshake deadline, so a merely-slow handshake would kill (and tear down) the process instead of aborting and retrying the handshake. `Start` is also non-blocking once the transport is up.
- **Kill/close the connection on the Nth consecutive timeout.** Rejected: mark-not-kill. A transient streak should not tear down an otherwise-working server, and the decision to deprioritize or drop a server belongs to the caller, not the transport layer.
- **One global (not per-server) timeout.** Rejected: MCP servers differ widely in how long a call legitimately takes; per-server is the natural granularity, and handshake vs. per-call are distinct concerns (hence the two keys).
- **Return a bare `context.DeadlineExceeded` instead of a typed error.** Rejected: callers need to attribute a timeout to a server/operation/tool and inspect the bound that elapsed; a bare context error carries none of that (the typed error still unwraps to the sentinel for callers that branch on it).
- **Count a caller cancellation toward the unhealthy streak.** Rejected: a cancellation is the caller's doing, not the server's; misattributing it would falsely mark a healthy server unhealthy (`timeoutErrorFor` distinguishes our timer from the parent context).

## References

- `sp4rk/tools/mcp/server.go` — `defaultMCPTimeout`, `unhealthyTimeoutThreshold`, `ServerConfig.Timeout`/`CallTimeout`, `Server` (resolved `timeout`/`callTimeout`, `consecutiveTimeouts`, `unhealthy`, `initializeClient`, `DiscoverTools`, `CallTool`, `Status`), `TimeoutError`, `timeoutErrorFor`
- `sp4rk/tools/mcp/gateway.go` — `ServerEntry.Timeout`/`CallTimeout`, `ServerStatus.Unhealthy`, `serverConfigFromEntry`, `configChanged`
- `config.example.yaml` — `mcp.servers.<name>.timeout` / `call_timeout`
- `backend/config/config.go` — `MCPServerConfig.Timeout` / `CallTimeout`
- `backend/configadapter.go` — `parseMCPDuration` (fail-soft to the default)
- `backend/config/mcp_timeout.go` — `ParseMCPDuration` (shared duration rule) and `normalizeMCPTimeouts` (load warning, so a bad value is reported on the production load path)
- [specs/domains/tool-system/mcp-gateway.md](../domains/tool-system/mcp-gateway.md) — c0wrk MCP wiring (timeouts + invariants)
- [sp4rk mcp-gateway spec](https://github.com/v0lka/sp4rk/blob/main/specs/domains/tool-system/mcp-gateway.md) — canonical engine behavior
