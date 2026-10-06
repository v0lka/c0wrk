# Local-change code review: c0wrk and sp4rk

## Summary and scope

**1 MUST FIX, 4 SHOULD FIX findings** in the current uncommitted local changes. No fixes were applied.

Reviewed all staged, unstaged, and untracked local changes in both repositories at the current working-tree state:

- **c0wrk — 15 modified tracked files, 0 staged:** `backend/application.go`, `backend/frontend_api.go`, `backend/session/{background.go,background_test.go,manager.go,manager_execution.go,manager_ignore_cache_test.go,shutdown_resumable_test.go}`, `core/{builder.go,builder_mcp.go,builder_mcp_test.go}`, `desktop/startup.go`, `frontend/src/index.css`, `frontend/src/lib/cmChatAutocomplete.{ts,test.ts}`.
- **c0wrk — 4 untracked files:** `frontend/src/components/chat/MentionInfoTooltip.tsx`, `frontend/src/lib/cmMentionTooltip.{ts,test.ts}`, `frontend/src/lib/cmChatAutocompleteConfig.test.ts`.
- **sp4rk — 4 modified tracked files, 0 staged:** `ignore/ignore.go`, `ignore/ignore_test.go`, `tools/mcp/server.go`, `tools/mcp/server_test.go`.

The change set is a shutdown-hardening pass (per-step teardown timing logs, cooperative cancellation of the ignore-resolver walk, attributed/named background-goroutine tracking with a straggler goroutine dump, a shared shutdown wait budget, a bounded MCP-gateway startup handoff, and a bounded MCP server close in sp4rk) plus a chat-input `/`-mention UX rework (popup opens upwards, a hover markdown description pane, and its CSS chrome).

The prior content of this file described a **different, already-committed** change set (model-profiles / embedded-LLM) and has been replaced; that review remains in git history (it was last touched by commit `dbab6b1c`).

Verification performed: `go build ./backend/... ./core/...` (c0wrk) and `go build ./ignore/... ./tools/mcp/...` (sp4rk) pass; `gofmt -l` is clean on every changed Go file; the affected Go tests pass (`backend/session` new tests, `core` new MCP tests, `sp4rk/ignore`, `sp4rk/tools/mcp` Close tests); the affected vitest suites pass (`cmMentionTooltip.test.ts`, `cmChatAutocompleteConfig.test.ts`, `cmChatAutocomplete.test.ts`, `zoomViewportInvariant.test.ts`). One required gate fails — see finding 1.

## Finding index

| # | Severity | Summary |
|---|---|---|
| 1 | MUST FIX | New `time.Sleep` in `manager_ignore_cache_test.go` breaks `TestNoNewTimingDebt` |
| 2 | SHOULD FIX | Hover description resolved by DOM row position, wrong when the completion list is virtualized |
| 3 | SHOULD FIX | Early-quit MCP startup handoff can return before the gateway (and its stdio children) is stopped |
| 4 | SHOULD FIX | `ReconfigureMCP`'s shutdown guard is checked only at entry, so a gateway can still be published (and leaked) after `StopGateway` |
| 5 | SHOULD FIX | Bounded MCP stdio close kills only the direct child, orphaning a launcher-spawned server (e.g. under `npx`) that ignores stdin EOF |

## Findings

### 1. MUST FIX — New `time.Sleep` in `manager_ignore_cache_test.go` fails the timing-debt guard

**Repository / location:** c0wrk, [backend/session/manager_ignore_cache_test.go:206](backend/session/manager_ignore_cache_test.go#L206) (inside the new `TestStartIgnoreBuild_MidWalkShutdownJoinIsPrompt`).

The new test introduces `time.Sleep(30 * time.Millisecond)`. The project's timing guard [`internal/testtiming/timing_test.go`](internal/testtiming/timing_test.go#L192-L215) walks every `*_test.go` and rejects any wall-clock `time.Sleep` expression that is not in the reviewed baseline [`internal/testtiming/timing-debt.json`](internal/testtiming/timing-debt.json). The baseline contains **no** entry for `backend/session/manager_ignore_cache_test.go`, so the new occurrence is reported as new debt.

**Trigger and impact:** `go test ./internal/testtiming/` (part of `make test` / `make test-go`, a mandatory pre-PR gate) fails deterministically:

```
--- FAIL: TestNoNewTimingDebt (0.12s)
    timing_test.go:202: new forbidden timing debt (1 > 0): backend/session/manager_ignore_cache_test.go | sleep: time.Sleep(30 * time.Millisecond); use virtual time or lifecycle barriers
FAIL
```

This is a build-gate failure, not a flake: it reproduces on every run because the source expression is fixed.

**Why it is a problem:** `AGENTS.md` states `timing-debt.json` is a visible legacy backlog, **not permission to add sleeps**, and that it may be updated "only as an explicitly reviewed migration operation, never automatically in CI". A green local `make test` is claimed by the task's verification expectations, but this check is red.

**Suggested fix:**
a) Remove the wall-clock sleep and make the cancellation contract deterministic without a timer: `startIgnoreBuild` spawns its named goroutine synchronously, so the important, observable property is that a cancel-then-join drains promptly. Assert that directly (e.g. cancel and then require `closeAndWait` to return `true` within the test's bounded watchdog), or drop the strict "mid-walk" requirement and rely on the existing `TestStartIgnoreBuild_ShutdownCancelAbortsBuildAndCleansSentinel` plus the sp4rk-level `TestNewResolverContext_CancelDuringWalkAborts`.
b) If the "walk genuinely in flight when cancel fires" property must be kept, introduce an explicit barrier instead of a sleep — a test-visible hook (e.g. a package-level `onWalkEntry`/wrapper passed into `startIgnoreBuild`) or a coordinated fake walk — so the test blocks on a channel, not on elapsed time.
c) Only as an explicitly reviewed migration, regenerate the baseline with `go test ./internal/testtiming/ -run TestNoNewTimingDebt -update-timing-baseline` and record the new entry in the commit message. This is the option the project documentation explicitly discourages for convenience sleeps; prefer (a) or (b).

### 2. SHOULD FIX — Hover description is resolved by DOM row position and is wrong when CodeMirror virtualizes the list

**Repository / location:** c0wrk, [frontend/src/lib/cmMentionTooltip.ts:128-142](frontend/src/lib/cmMentionTooltip.ts#L128-L142) (`rowIndex` / `markdownFor`), consumed at [cmMentionTooltip.ts:244-250](frontend/src/lib/cmMentionTooltip.ts#L244-L250).

`rowIndex(li)` counts the row's `li[id]` siblings from zero and the result is used as an index into `currentCompletions(state)`. That is only correct while the rendered window is the whole list. CodeMirror's completion tooltip **virtualizes**: [`@codemirror/autocomplete` `createListBox`](frontend/node_modules/@codemirror/autocomplete/dist/index.js#L719-L722) sets `li.id = id + "-" + i` with the **absolute** option index `i`, and renders only the window `rangeAroundSelected(options.length, selected, maxRenderedOptions)` (default `maxRenderedOptions` is 100, line 383; the window is recomputed whenever the selection leaves it — lines 521, 598-599). `range.from` is therefore non-zero once the list exceeds 100 entries and the selection moves past the first window. CodeMirror's own row-click handler avoids exactly this trap by recovering the absolute index from the id (`/-(\d+)$/.exec(dom.id)`, line 535).

**Trigger and impact:** Type `/` in the chat input with more than 100 subagent/MCP/skill entries, then arrow down past index 100 so the rendered window starts at `range.from > 0`. Hovering a row: `rowIndex` returns the window-relative position, so `currentCompletions(state)[index]` is off by `range.from` and the pane shows the description of a **different** completion than the hovered row. With ≤100 entries (`range.from === 0`) the mapping is correct, which is why the current tests pass.

**Why it is a problem:** The pane is a user-facing description surface; showing the wrong capability's description for a hovered row is misleading. The defect is latent today (it needs a large catalog) but the mapping is fundamentally wrong and diverges from how CodeMirror itself identifies rows.

**Suggested fix:**
a) Derive the index from the row id, mirroring CodeMirror's own handler: `const m = /-(\d+)$/.exec(li.id); const index = m ? Number(m[1]) : -1;` then `currentCompletions(state)[index]`. This is correct in both the un-virtualized and virtualized cases and removes `rowIndex` entirely.
b) If an id parse is unwanted, expose the tooltip's `range.from` (or use `completionStatus`/the open state) and offset `rowIndex` by it — more coupling to CodeMirror internals than (a).

### 3. SHOULD FIX — Early-quit MCP startup handoff returns before the not-yet-published gateway is stopped

**Repository / locations:** c0wrk, [core/builder_mcp.go:112-141](core/builder_mcp.go#L112-L141) (`StopGateway`), [core/builder.go:294-306](core/builder.go#L294-L306) and [core/builder.go:335-357](core/builder.go#L335-L357) (`runMCPInit` / `publishMCPGateway`), [core/builder.go:278](core/builder.go#L278) (`go b.runMCPInit(cfg)`).

On the timeout path `StopGateway` sets `b.mcpStopping` and returns without stopping anything, delegating the stop of the freshly built gateway to the still-running `runMCPInit` goroutine ("the startup goroutine keeps ownership … and stops it here — off the app-shutdown path"). That goroutine is neither tracked nor joined: it is launched as a bare `go b.runMCPInit(cfg)` and its context is an independent `context.WithTimeout(context.Background(), 30*time.Second)` ([builder.go:305](core/builder.go#L305)). `Application.Shutdown` calls `builder.StopGateway()` as its final step and then returns ([backend/application.go:533-536](backend/application.go#L533-L536)); nothing waits for `mcpDone` afterwards.

**Trigger and impact:** Quit (window close / Cmd+Q) within the first ~`mcpStopStartupGrace` (1 s) of startup while MCP servers are still being spawned. `StopGateway` returns after 1 s; the remaining shutdown steps are short; the process exits. The `runMCPInit` goroutine is terminated mid-`StartGateway`, so the ownership handoff it was supposed to perform never runs. Any stdio MCP child spawned so far is not stopped by the app. Most such children exit on stdin EOF when the parent's pipe closes, but a server that ignores stdin EOF — the exact case `tools/mcp`'s new bounded close was added to handle — survives as an orphaned process. Before this change, `StopGateway` waited out startup (up to its previous 30 s bound) and then stopped the published gateway, so the children were reaped.

**Why it is a problem:** The mitigation documented in `publishMCPGateway` ("this cannot stall app exit materially") only holds while the process is alive to run it. On a real early quit the process is gone, so the mitigation is void and the guarantee that MCP child processes are stopped before exit is lost. This is a resource-leak regression confined to the startup window and to servers that do not exit on stdin EOF.

**Suggested fix:**
a) Make the handoff joinable: have `App.Shutdown` wait (bounded) for the `runMCPInit` goroutine — e.g. expose a `WaitMCPStartup(ctx)`/`mcpDone` join and call it at the end of `Application.Shutdown`, or track `runMCPInit` in a `sync.WaitGroup`/`backgroundTracker` joined on shutdown — so the process does not exit until the not-yet-published gateway has been stopped (and, with the sp4rk bounded close, that stop cannot itself hang).
b) If no join is acceptable, make the process-level exit path stop the gateway synchronously: return the pending gateway (or a "stopping in flight" handle) from `StopGateway` and have the caller drive its `Stop()` with the same bounded budget, rather than returning nil and relying on a goroutine that may never run.

### 4. SHOULD FIX — `ReconfigureMCP`'s shutdown guard is checked only at entry, so a gateway can still be published (and leaked) after `StopGateway`

**Repository / location:** c0wrk, [core/builder_mcp.go:36-45](core/builder_mcp.go#L36-L45) (the up-front `stopping := b.mcpStopping` check) vs [core/builder_mcp.go:80-90](core/builder_mcp.go#L80-L90) (the "no gateway" branch that publishes `newGW`).

`ReconfigureMCP` samples `b.mcpStopping` exactly once, before `waitMCPReady` and before it acquires `reconfigureMu`, and returns early only if the flag is already set at that instant. Its own later publication — `b.gateway = newGW` on the branch taken when `waitMCPReady` returns and `b.gateway` is still nil — does NOT re-check the flag. The `gw != nil` branch is similarly unguarded.

**Trigger and impact:** `StopGateway` sets `mcpStopping = true` only when its bounded wait on `mcpDone` expires (startup still in flight past `mcpStopStartupGrace`); `runMCPInit`'s `publishMCPGateway` then suppresses the pending publication and stops the fresh gateway itself. A `ReconfigureMCP` call that started *before* `StopGateway` ran (so its initial check saw `false`) is still parked in `waitMCPReady(ctx)` — and its context is an independent `context.WithTimeout(context.Background(), mcpReconfigureTimeout)` ([backend/frontend_api_mcp.go:335-337](backend/frontend_api_mcp.go#L335-L337)), so shutdown does not cancel it. When `mcpDone` finally closes, this call proceeds, observes `b.gateway == nil` (the startup gateway was suppressed), dials a brand-new gateway and assigns it — after the stop decision, with nothing left to stop it. The freshly spawned MCP children are then orphaned at exit: the same process-leak class as finding 3, reached by a different path. In the complementary order (`StopGateway` took its `gw != nil` branch and stopped the published gateway) the unguarded `gw.Reconfigure(...)` is called on an already-stopped gateway; `Gateway.Stop` reset `g.servers` to an empty map, so `Reconfigure`'s add/change loop treats every configured server as new and re-dials it (see [sp4rk `Gateway.Reconfigure`](../sp4rk/tools/mcp/gateway.go)), resurrecting servers nobody will stop.

**Why it is a problem:** The `mcpStopping` flag exists specifically to prevent publication after the shutdown path declined to wait; guarding only the entry point leaves the exit unguarded, so the guarantee the flag is meant to provide is not actually enforced. The window is narrow — a user-triggered reconfigure has to overlap the ~1 s startup grace during an early quit — but the consequence is exactly the orphaned MCP process the rest of the change set is at pains to avoid.

**Suggested fix:**
a) Re-check `b.mcpStopping` under `b.mu` at the publish site: if it is set, do not assign `b.gateway`; stop `newGW` (mirroring `publishMCPGateway`) and return an error, e.g. `if b.mcpStopping { b.mu.Unlock(); _ = newGW.Stop(); return fmt.Errorf("mcp reconfigure rejected: builder is shutting down") }`.
b) Prefer routing this assignment through the existing `publishMCPGateway` helper (it already owns the `mcpStopping` check, the work-dir apply, and the unlock discipline), so a second call site cannot bypass the guard.
c) Additionally, take `b.reconfigureMu` (or an equivalent barrier) in `StopGateway` so no reconfigure can be in flight once the flag is set — the two mechanisms together make "no gateway is published after the stop decision" structural rather than a race that usually does not happen.

### 5. SHOULD FIX — Bounded MCP stdio close kills only the direct child, orphaning a launcher-spawned server that ignores stdin EOF

**Repository / location:** sp4rk, [tools/mcp/server.go:906-922](../sp4rk/tools/mcp/server.go#L906-L922) (`closeClientBounded`, the `_ = cmd.Process.Kill()` at line 919), reached from `Server.Close` (line 892, `closeClientLocked`) and from `connectStdio`'s handshake-failure cleanup. The handle it kills is captured at [tools/mcp/server.go:371](../sp4rk/tools/mcp/server.go#L371): `cmd := exec.CommandContext(cmdCtx, command, args...)` — i.e. `cfg.Command` itself.

The new bounded close records the direct child and, on grace expiry, calls `cmd.Process.Kill()`. That terminates only the process it spawned (`command`). Nothing puts the child into its own process group or job object: [sysproc/sysproc_unix.go:10](../sp4rk/sysproc/sysproc_unix.go#L10) shows `HideConsole` is a **no-op** on non-Windows, and [sysproc/sysproc_windows.go:28](../sp4rk/sysproc/sysproc_windows.go#L28) only OR-s `CREATE_NO_WINDOW` into `CreationFlags`. The SDK ships a kill-on-close Job Object helper, [`sysproc.AssignKillOnCloseJob`](../sp4rk/sysproc/job_windows.go#L63), but the MCP stdio path never calls it. `command` is frequently a *launcher*, not the server: the canonical MCP config launches a server through a package runner — `command: "npx"`, `args: ["-y", "some-mcp"]` ([config.example.yaml:562-563](config.example.yaml#L562-L563)) — so the real server is a **grandchild** of `npx`.

This kill is the *only* reaper: mcp-go v0.45.0's stdio transport `Stdio.Close()` closes stdin, closes stderr, and then blocks in `cmd.Wait()` with no process kill of its own (`client/transport/stdio.go:221-255` in `github.com/mark3labs/mcp-go@v0.45.0`).

**Trigger and impact:** Configure a stdio MCP server as a launcher chain (`npx` / `pnpm dlx` / `bunx`) whose server refuses to exit on stdin EOF — the exact "draining / wedged" case this change was written to bound. On close, `cmd.Process.Kill()` kills the launcher; the grandchild server keeps running, holding its pipes, as an orphaned process. The previous code instead waited out the launcher chain in `cmd.Wait()` (a hang, not an orphan); the change converts that hang into a leaked child process for precisely the servers it targets.

**Why it is a problem:** The SDK already documents and solves this exact trap for spawned children: [tools/builtins/bash.go:184](../sp4rk/tools/builtins/bash.go#L184) sets `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` and [tools/builtins/bash.go:191](../sp4rk/tools/builtins/bash.go#L191) kills the whole group with `syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)`, with the comment "exec.CommandContext only kills the parent, leaving orphaned children that hold pipes open". The new MCP close reintroduces the trap that comment warns about. The window is narrow (an MCP server that both launches via a runner and ignores stdin EOF) but the consequence is a process leak at app exit — the same class of "orphaned MCP process" the rest of this change set exists to prevent (see findings 3 and 4).

**Suggested fix:**
a) Match the SDK's process-tree convention. On non-Windows, launch the MCP child in its own process group (`cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`) and, on grace expiry, kill the group with `syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)`; on Windows, call the existing `sysproc.AssignKillOnCloseJob(cmd)` (kill-on-close Job Object) and let the OS reap the tree. A single cross-platform helper (e.g. `sysproc.KillTree(cmd)`) shared with `bash` keeps one convention.
b) If the child must stay in the SDK's single-process model, make the command factory spawn the *real* server as the direct child (resolve the launcher), so the single-process kill is sufficient — less robust, and not always possible (e.g. `npx` resolution).
