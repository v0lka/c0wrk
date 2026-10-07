# PART D — End-to-end cross-repo verification evidence

Date: 2026-10-07 (MSK). Scope: verify all fixes across c0wrk **and** sp4rk, run the
acceptance scenarios, and confirm no sleep/timeout debt was added.

## Environment

- c0wrk: `/home/vkochetkov/Repositories/c0wrk`, HEAD `477a136f` — the fix commit
  (`1627a5f0` + parts A–E).
- sp4rk: `/home/vkochetkov/Repositories/sp4rk`, HEAD `c8cd0dd` — the fix commit
  (`0b35afe` + the bounded tool-call / glob-walk changes).
- `go.work` present: `use ( . ../sp4rk )`. `go list -m github.com/v0lka/sp4rk`
  resolves to `/home/vkochetkov/Repositories/sp4rk` (local HEAD `c8cd0dd`). With
  `GOWORK=off` it resolves to the module cache pin
  `v0.15.1-0.20261006200117-0b35afe097b3` (commit `0b35afe`, the **pre-fix**
  pin), so `go.mod` still lags sp4rk HEAD by the fix commit and the tree does
  **not** build from the pin alone (`undefined: agent.ErrToolTimeout`,
  `builtins.GlobLimits`, `builtins.NewGlobToolWithLimits`). This is the
  sanctioned ADR-031 cross-repo mid-cycle state; it is closed by publishing
  sp4rk `c8cd0dd` and running `make bump` (which re-pins `go.mod` under
  `GOWORK=off`).
- Toolchain: Go 1.27.x, wails v2.15.0, Node 20.20.2.

## Commands

| # | Command | Result |
|---|---------|--------|
| 1 | sp4rk `go test ./...` | **ok** — 23 pkgs ok, 1 no-test-files, 0 FAIL |
| 2 | c0wrk `make build` | **ok** — `Built build/bin/c0wrk-desktop in 23.0s`; fetch-onnx + fetch-embedding-model OK; exit 0 |
| 3 | c0wrk `make lint` | **ok** — fmt-check silent-pass, `golangci-lint run` → `0 issues.`, `eslint .` clean; exit 0 |
| 4 | c0wrk `make test` | **ok after remediation** — see Regression below. Go: 35 ok / 0 FAIL / 4 no-test-files, exit 0. Frontend: 350 files / 5225 tests passed, exit 0 |
| 5 | c0wrk `make vulncheck` | **ok** — `No vulnerabilities found.` exit 0 |
| 6 | `go test -count=1 -v ./internal/testtiming` | **ok** — timing-debt guard green; `internal/testtiming/` tree unchanged (no sleep/timeout debt added) |

No data races, panics, or unexpected diagnostics in the run logs. The only
diagnostic is pre-existing/environmental: `npm warn cli npm v12.0.2 does not
support Node.js v20.20.2`.

## Regression found and remediated (the PART D finding)

**Symptom.** The first `make test` run was **red**:
`TestShutdown_MidPlan_RestartResumeCompletesAllStepsTerminal` (package
`backend/session`) failed **deterministically (5/5)** with
`persisted trajectory after shutdown is empty (<nil>) — the checkpoint must
survive a restart`.

**Isolation** (non-destructive, using `git archive HEAD` into a scratch tree plus
scratch `go.work`-redirected copies of sp4rk):

- HEAD c0wrk + committed sp4rk → **PASS**
- HEAD c0wrk + sp4rk working tree → **FAIL** ⇒ the regression is caused by
  sp4rk's fix changes (now `c8cd0dd`), not c0wrk's.
- Reverting sp4rk `agent/executor*.go` + `orchestration/conductor.go` → **PASS**;
  keeping the agent changes and reverting only `orchestration/conductor.go` →
  **FAIL** ⇒ culprit is the new per-tool-call watchdog
  (`agent/executor_run.go` + new `agent/tool_watchdog.go`).

**Mechanism.** The new `Executor.executeToolCall` returns `ctx.Err()` when the
context is **already** cancelled at dispatch, abandoning an in-flight tool's
already-available result. In this shutdown scenario the gated LLM call returns
its response *after* ctx cancellation, so the subagent then dispatches
`bash_exec` under a cancelled ctx; the watchdog's `ctx.Done()` arm aborts before
the tool step is recorded. That subagent executor **shares the task-level
`trajectoryHolder`** (inherited through the context) and syncs its own
*still-empty* step list at the top of its first loop iteration, replacing the
parent conductor's already-synced trajectory. Instrumented sync sequence:
`0, 1, 0` (final **empty**) vs the pre-change `0, 1, 0, 1, 2, 2` (final
non-empty). `Flush()` then persists the empty trajectory.

**Remediation** (`core/conductor.go`, c0wrk): enforce the holder's own documented
invariant — *"the persisted trajectory never goes backwards"* — by making a Sync
**monotonic**: any sync shorter than the trajectory already held is ignored (an
empty sync is just the degenerate case). A subagent shares the holder and its own
list starts empty and grows to one or two steps, so its first non-empty sync is
still *shorter* than the parent conductor's completed trajectory — an empty-only
guard would let that shorter list replace the parent's checkpoint. The composite
store also snapshots the guarded in-memory trajectory so the DB write follows the
same rule:

```go
func (h *trajectoryHolder) Sync(steps []agent.Step) {
	h.mu.Lock()
	defer h.mu.Unlock()
	// The trajectory never shrinks: a subagent's own (short) sync must not
	// replace the parent conductor's longer, completed trajectory.
	if len(steps) < len(h.steps) {
		return
	}
	h.steps = make([]agent.Step, len(steps))
	copy(h.steps, steps)
}
```

```go
// compositeTrajectoryStore.Sync: snapshot the *guarded* memory, not the raw
// caller steps, so a stale empty upsert cannot race ahead of the final Flush.
effective := c.memory.Steps()
snapshot := make([]agent.Step, len(effective))
copy(snapshot, effective)
```

**Re-verified after the fix:** the failing test passes 5/5; the **full c0wrk Go
suite is 35 ok / 0 FAIL**; `make build` and `make lint` remain green; the `core`
package passes; no races/panics.

## Acceptance scenarios

**(a) Symlink-loop repro — PASS.**
- sp4rk unit: `TestGlobTool_SymlinkLoopTerminates` (creates `root/self -> .` and
  `root2/link -> /`, globs `**/*`) and `TestGlobTool_ContextCancelMidWalk` →
  PASS.
- Literal end-to-end harness: created a temp dir with `root/self -> .`,
  `root2/link -> /` and `root/keep.txt`, then ran the real `glob` tool with
  `path=<temp> pattern=**/* type=all`. It returned in **0 s** with results
  (`root`, `root2`, `root/keep.txt`, `root/self`, `root2/link`) and did **not**
  follow the symlinks — no hang.

**(b) Long glob + Pause — PASS.**
- sp4rk `TestExecutor_Run_ToolCallWatchdog_PauseWhileToolBlocked` → `Run` returns
  `ErrPaused` with a non-nil checkpoint while the tool is still blocked (the
  pause is observed **mid-tool-call**, not only at a step boundary).
- c0wrk plumbing present: `ConductorConfig.PauseChecker` (main executor) and
  `conductorLauncher.configureExecutor` (subagents) both install the checker; the
  in-flight poll interval is 250 ms.

**(c) Stuck tool + quit within `shutdown.hardDeadline` — PASS (unit level).**
- desktop: `TestShutdownWatchdog_FiresOnExpiry`, `_StopDisarms`,
  `_DeadlineFallback`, `TestApp_ShutdownDeadline`, `TestShouldPreventClose*`
  (incl. `_PayloadCarriesHungFlag`) → PASS.
- session: `TestCancelTask_TimeoutForcesTerminalState`,
  `TestCancelTask_FastStopDoesNotForceTerminalState`,
  `TestActiveSessions_FlagsHungSessions`,
  `TestStopBackground_DrainLoopBoundedByDeadline` → PASS.

## Caveats

- Scenario (c)'s interactive GUI path (reproduce a hung tool, observe the quit
  modal, confirm the window closes) is **not runnable in this headless
  environment**. Its parts are covered by the unit tests at the exact injected
  seams: `ShouldPreventClose` (modal) and the `shutdownWatchdog.onExpiry`
  (`crashlog.ForceExit`) seam for the bounded close.
- The remediation touches a shared trajectory holder; it is validated by the
  complete c0wrk Go suite (35 pkgs) and the sp4rk suite.
