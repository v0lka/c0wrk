# Code Review — stuck-tool / watchdog / glob-bound change set

Reviewer pass over **only** the two local change sets below. Findings are severity-tagged
(`MUST FIX` / `SHOULD FIX`), numbered, and each carries at least one concrete, letter-labelled
fix option. **No reviewed source file was modified** — this file is findings only.

## Reviewed revisions

| Repo | Revision under review | Composition |
|------|-----------------------|-------------|
| c0wrk | working tree at `HEAD`/`origin/main` = `1627a5f0` | `git diff origin/main`: 21 modified tracked files (+504/−35) plus 9 untracked files (`core/e2s/tool_watchdog.go`, `desktop/shutdown_watchdog.go`, 6 `*_test.go`, `docs/development/part-d-verification-evidence.md`) |
| sp4rk | single unpushed commit `c8cd0dd` (`origin/main` = `0b35afe`) | `agent/executor.go`, `agent/executor_run.go`, `agent/tool_watchdog.go` (+test), `orchestration/conductor.go`, `tools/builtins/glob.go` (+test), `tools/builtins/globfs.go`, `tools/builtins/limits.go` |

## Method

- Read the complete diff/commit and the full surrounding context of every changed file (the
  whole session manager cancel/resume paths, the conductor trajectory store, the E2S loop, the
  desktop `Shutdown` teardown, the doublestar `GlobWalk`/options source, and the sp4rk executor
  dispatch + watchdog).
- Built the affected packages (`go build ./backend/... ./core/... ./desktop/...` → exit 0) and
  ran the new/affected tests (`backend/config`, `backend/session`, `core`, `core/e2s`,
  `core/tools`, `desktop`, and sp4rk `agent/… tools/builtins/… orchestration/…`) — all pass.
  Passing tests do **not** clear the findings below; they are behavioural/design defects that
  the current tests do not cover.
- Applied `.golangci.yml` conventions, `SECURITY.md` threat model, and the project's testing /
  timer conventions from `AGENTS.md`.
- A second, deeper pass — dedicated read-only reviews of the sp4rk commit and of the c0wrk working
  tree — re-examined the same two revisions and added findings #12–#22 below; the first pass's
  findings #1–#11 are retained unchanged.
- A third, independent pass (a fresh read-only review of both change sets) added findings #23–#25
  below — a distinct sp4rk verify-on-edit flush site, a shutdown-watchdog `select` race, and the
  stale c0wrk behavioural specs. A fourth pass — two further fresh read-only reviews, one per change
  set — added #26–#29 (additional stale c0wrk/sp4rk specs and docs, the changed sp4rk `PauseChecker`
  invocation contract, a sp4rk lint-gate failure, and the shutdown watchdog's crashlog-marker
  bypass) and independently reproduced #1–#25. A fifth pass — two more fresh read-only reviews, run
  against the 29-finding file — rated it "complete for MUST FIX" with only residual SHOULD FIX items
  (the incomplete never-shrink guard, untested glob bounds, an inert test assertion, and a few more
  stale spec/doc anchors); those are recorded as #30–#32 with the extra anchors folded into #26/#27.
  Findings are numbered across all passes.

---

## Findings

### 1. MUST FIX — sp4rk tool watchdog: `select` is nondeterministic and can discard a tool result that is already available

`agent/tool_watchdog.go:91-108` (and the identical copy in `core/e2s/tool_watchdog.go:88-108`)
races the tool's completion against the context:

```go
for {
    select {
    case out := <-done:            // tool finished (result available)
        return out.result, out.err
    case <-ctx.Done():             // run cancelled / deadline
        return tools.ToolResult{}, ctx.Err()
    ...
    }
}
```

When a tool completes **and** the context is already done (or becomes done at the same moment),
Go's `select` picks a *uniformly random* ready case. So the exact same inputs can return either
the tool's real result or `ctx.Err()`. If `ctx.Done()` wins, a **completed tool's result is
thrown away**, and the caller treats the call as cancelled (`processSingleToolCall` returns the
infrastructure error; the E2S `executeSingle` turns it into an error observation) — this is
exactly the behaviour that loses a tool step when a run is cancelled, which the change-set's own
evidence document identifies as the cause of the `TestShutdown_MidPlan_…` regression (worked
around on the c0wrk side by making `trajectoryHolder.Sync` never shrink).

The same ready-case nondeterminism applies to the watchdog's other two arms — the new `timeoutCh`
arm (`:103-107`) and the `tickCh` pause arm (`:96-102`): a tool that completes exactly as the
ceiling fires can be reported as `ErrToolTimeout` with its result discarded, and a pause observed on
the same tick that the tool completes can win over the real result.

**Why it is a problem:** the outcome depends on a coin flip, so a run that is cancelled while a
tool has *already produced its output* may silently drop that output. The same nondeterminism
also makes the outcome of any future cancellation-concurrency test flaky. A pre-existing result
(the tool already returned) should never lose to a context that fired at the same instant.

**Suggested fix:**
a) Drain `done` first with a non-blocking `select` before entering the blocking loop, so a
   finished tool always wins:
   ```go
   select {
   case out := <-done:
       return out.result, out.err
   default:
   }
   for { select { /* done / ctx / tick / timeout */ } }
   ```
b) Or hold the tool result in a variable that `ctx.Done()`/pause/timeout paths also return when
   `done` has fired (re-check `done` in the `ctx.Done()` arm before returning `ctx.Err()`).

---

### 2. MUST FIX — c0wrk `forceTerminateStuckTask`: the settling stuck goroutine emits a **second** terminal event and overwrites the run metrics

`backend/session/manager_execution.go:2919` (`forceTerminateStuckTask`), called from `CancelTask`
at `:2896` after `stopTimeout` expires. Its doc comment claims: *"if it ever settles, the run is
already non-live and it emits no second terminal event."* That claim is false.

`forceTerminateStuckTask` deliberately does **not** deactivate the session, and the task
goroutine's epilogue is unguarded: when the task goroutine finally returns with
`ctx.Err() == context.Canceled`, it runs
`m.emitTaskCancelledUnlessShuttingDown(id)` (`:1094`) → a **second** `task_cancelled` event plus a
second `m.emitAgentMetrics(id, "cancelled")`. `EventEmitter.EmitAgentMetrics`
(`backend/session/emitter.go:1248`) emits unconditionally and resets the counters/window first, so
the second event carries **zeroed** step counters for a run that already reported its real ones —
clobbering the session's displayed/persisted metrics. A repeated Stop click mid-wait also re-runs
`forceTerminateStuckTask` and re-emits.

**Why it is a problem:** any tool slower than `stopTimeout` (10 s) — a long `bash_exec`, a slow
network tool, a large glob — is a routine trigger, not a pathological one. The user cancels, the
UI is told the task is cancelled, and then a duplicate terminal event + a metrics record with
zeroed counters arrives for the same run. A related asymmetry: `forceTerminateStuckTask` is **not**
gated by `m.shuttingDown` and persists the cancellation unconditionally (`PersistCancellation`),
whereas the goroutine's own terminal path uses `emitTaskCancelledUnlessShuttingDown` precisely so a
shutdown leaves the task `in_progress` and resumable — so a Stop that races app shutdown flips the
task to cancelled, defeating that documented contract. And because the forced path leaves
`session.active == true` while the UI's running state has already been cleared, the user's next
message is treated as a live interjection (`sendMessage` → `session.orchestrator.QueueLiveUserMessage`,
`backend/session/manager_execution.go:731-748`) and then thrown away by the settling goroutine's
`liveActionDiscard` — the message silently vanishes.

**Suggested fix:**
a) Mark the run terminal in `forceTerminateStuckTask` and have the epilogue consult that flag
   (e.g. an atomic per-run "terminal emitted" / generation counter on `Session`) so a settling
   goroutine skips its own `task_cancelled` + metrics emission.
b) Alternatively, deactivate the session in `forceTerminateStuckTask` (accepting the trade-off of
   no longer flagging it as hung) and make the epilogue's `ctx.Err() == context.Canceled` branch
   no-op when the session is no longer active/owning the run.

---

### 3. SHOULD FIX — c0wrk: the documented "0 disables" contract for the four new knobs is unreachable from `config.yaml`

`backend/config/defaults.go:342-345, 378-384` coerces zero to the default for
`toolLimits.globMaxEntries` (→500000), `toolLimits.globMaxResults` (→10000), `timeouts.globTimeout`
(→30) and `timeouts.toolCallTimeout` (→300). But every documented surface promises the opposite:
`config.example.yaml:1023` ("0 = no entry budget"), `:1068` ("0 = no timeout"),
`:1073` ("0 = disabled"), and the struct doc comments in `backend/config/config.go:2236-2240,
2258-2259` and `core/builderconfig.go`.

Because YAML-absent and YAML-`0` both land on the zero value, `ApplyDefaults` converts an explicit
`0` into the default too, so an operator can never disable the 5-minute tool-call ceiling or the
glob budgets from config.

**Why it is a problem:** the project already has the correct precedent — `llmRequestTimeout`
deliberately gets **no** 0→default coercion precisely because `0` is a meaningful "no opinion"
value (`defaults.go:398-405`, pinned by `config_test.go:287`). The new knobs break that contract
and advertise a switch that does not work.

**Suggested fix:**
a) Do not coerce these fields (drop the four `== 0` blocks) and let `0` mean "disabled/no budget",
   applying defaults only where the builder needs a non-zero value.
b) Or change the type to `*int` (mirroring `adaptive_budget.enabled`) / use a `-1` sentinel so
   "unset" is distinguishable from an explicit `0`, and document the sentinel in both
   `config.example.yaml` and the comments.

---

### 4. SHOULD FIX — c0wrk: the default shutdown hard deadline (20 s) is below the embedded-LLM stop ceiling, so quitting during a cold load can orphan `llama-server`

`desktop/shutdown_watchdog.go:14` (`defaultShutdownHardDeadline = 20 * time.Second`), armed in
`desktop/startup.go` `Shutdown`. `ShutdownConfig.HardDeadline` defaults to 20
(`backend/config/defaults.go:420`). The change-set itself documents (and `AGENTS.md` states) that
the embedded-server stop legitimately takes **up to ~46 s** when a quit races an in-flight cold
load holding the supervisor gate (`backend/config/config.go` `ShutdownConfig` comment;
`config.example.yaml:1124-1131`).

So with the default configuration, quitting while the embedded model is cold-loading forces
`os.Exit(0)` at 20 s — before the stop completes — leaving a detached `llama-server` holding
gigabytes of RAM/VRAM and a bound loopback port. This is a **new** failure mode: before this
change the teardown waited for the (bounded) embedded stop and ended cleanly.

**Why it is a problem:** the exact scenario the embedded teardown was hardened against (an
untracked `llama-server` surviving quit) is now reachable by default, and the failure is silent
(the process appears to have closed normally).

**Suggested fix:**
a) Raise the default (and the `config.example.yaml` documented default) above the embedded stop
   ceiling — e.g. 60 s — so the watchdog only fires on a genuinely wedged path.
b) Or make the watchdog defer while an embedded stop is in progress (skip/refresh the deadline
   when the embedded operation gate is held), keeping a short deadline for everything else.

---

### 5. SHOULD FIX — sp4rk glob: `WithFailOnIOErrors` escalates *any* I/O error into a total walk failure (behaviour change)

`tools/builtins/glob.go:174` now runs the walk with `doublestar.WithFailOnIOErrors()`. Previously
`doublestar.GlobWalk` was invoked with no options, so per-directory I/O errors were swallowed
(`forwardErrIfFailOnIOErrors` returns `nil` when the flag is off) and the walk continued. Now a
single unreadable entry — a permission-denied directory, an unreadable mount, a race that deletes
a directory mid-walk — aborts the **entire** glob and returns `glob error: …` instead of the
matches it had already collected.

The option is genuinely needed to surface the new `boundedFS` sentinels (`errGlobCanceled`,
`errGlobEntryBudget`), so it cannot simply be removed — but it also over-approximates by turning
benign, skippable errors into a hard failure.

**Why it is a problem:** an agent globbing a workspace that contains one unreadable directory now
gets no results at all, where before it got the readable ones — a usability regression triggered
by filesystem permissions rather than by the runaway this change targets.

**Suggested fix:**
a) Keep `WithFailOnIOErrors` only for the bounded-walk sentinels by wrapping the inner FS to
   re-signal them, and otherwise swallow `fs.ErrPermission` / per-entry errors as before.
b) Or filter in `walkErrorMessage`: when the walk error is a permission/transient error (not one
   of the sentinels), fall back to returning the partial `results` instead of an error result.

---

### 6. SHOULD FIX — sp4rk/c0wrk glob: a zero-value `GlobLimits` yields an unbounded walk, and the zero-value `BuiltinToolsConfig` path lost the previous default-limit safety net

`core/tools/builtin_registration.go:129` changed `registry.Register(builtins.NewGlobTool())` to
`registry.Register(builtins.NewGlobToolWithLimits(cfg.GlobLimits))`. Before the change the
registration always used `DefaultGlobLimits()` (30 s timeout + 500 000-entry / 10 000-result
budgets) regardless of the config struct. `NewGlobToolWithLimits` (`tools/builtins/glob.go:36`)
does **not** fall back to defaults, and `boundedFS.charge` treats a zero timeout/budget as
"disabled". So any caller that constructs `BuiltinToolsConfig{}` (several do:
`core/tools/tool_groups_guard_test.go:16`, `core/verify_on_edit_test.go:24`,
`core/tools/silent_mode_test.go:81,112`, `core/tools/shelltool_unix_test.go:73`,
`core/tools/symlink_test.go:23`) now registers a glob with **no timeout and no entry/result
budget** — the very runaway the change set exists to prevent (only `WithNoFollow` remains).

**Why it is a problem:** a new caller (or a future code path) that forgets to populate
`GlobLimits` silently registers an unbounded walk instead of failing safe to the defaults; the
guarantee now depends on the caller rather than on the tool.

**Suggested fix:**
a) In `NewGlobToolWithLimits`, fill any zero field from `DefaultGlobLimits()` before storing
   (mirrors the safety the old `NewGlobTool()` provided at the registration site).
b) Or have `RegisterBuiltinTools` substitute `builtins.DefaultGlobLimits()` when `cfg.GlobLimits`
   is the zero value.

---

### 7. SHOULD FIX — c0wrk E2S: a cancellation observed inside a single (non-batch) tool call is recorded as a spurious error step, contradicting the dispatch comment

`core/e2s/loop.go:895` `executeSingle` maps a plain error — including the `ctx.Err()` that
`executeToolCall` returns on cancellation — to a normal *error observation* (`case err != nil:
return fmt.Sprintf("tool execution error: %v", err), true, nil`). Only `ErrPaused` and
`ErrToolTimeout` become a `dispatchErr`. So on a context cancellation during a single tool call
the loop takes the success path, appends a step whose observation is
`tool execution error: context canceled`, and only then unwinds via the post-dispatch
`ctx.Err()` check (`loop.go:592`), returning a **canceled checkpoint that contains that bogus
step**. The comment at `loop.go:576-577` asserts cancellation "surfaces inside the tool call
(the watchdog returns ctx.Err())" as a `dispatchErr`, which is unreachable on the single path.

**Why it is a problem:** on resume the model sees a step claiming the tool genuinely errored,
which did not happen; the resumable checkpoint should not carry a fabricated failure observation
for a call that was merely interrupted by cancellation.

**Suggested fix:**
a) In `executeSingle`, add a `case errors.Is(err, context.Canceled) || errors.Is(err,
   context.DeadlineExceeded): return "", true, err` arm so cancellation propagates as a
   `dispatchErr` (matching the comment and the batch path's `isContextError` handling).
b) Or check `ctx.Err()` inside `dispatch` before building the step and return it as the
   `dispatchErr`, so no step is appended on a cancelled dispatch.

---

### 8. SHOULD FIX — sp4rk/c0wrk: after a timeout/pause/cancel the abandoned tool keeps running, so its side effects can land *after* the task is reported cancelled

`agent/tool_watchdog.go:64` (and `core/e2s/tool_watchdog.go:59`) explicitly do not cancel the
underlying tool — "the tool keeps running in its detached goroutine until it returns on its own;
only the WAIT is abandoned." On the c0wrk side a timed-out/paused action is then **not** appended
to the trajectory, and `forceTerminateStuckTask` / `persistCancellationIfUnfinished` mark the task
cancelled while the tool may still be mutating the workspace.

**Why it is a problem:** a `write_file`/`edit_file`/`bash_exec` that is reported as cancelled can
still complete a moment later, producing a cancelled task with a mutated file (or a running
child process) — a state the UI and the persisted task row both claim did not happen. The code
comment acknowledges "treat the tool's side effects as having possibly occurred", but no caller
acts on that. A second, resume-specific face of the same gap: on a **pause** the checkpoint is
`&ExecutorResult{Steps: state.allSteps, ...}` (sp4rk `agent/executor_run.go:1138-1139` single
path, `:1544` batch path), and the in-flight call's `Step` is appended to `state.allSteps` only
*after* `Execute` returns — so the checkpoint carries no record of the dispatched call. On resume
the model sees no evidence that the tool ran and may re-issue it, so a paused `write_file`/
`edit_file`/`bash_exec` can execute a **second** time.

**Suggested fix:**
a) Document the hazard at the *call sites* that mark work terminal (cancel/timeout) and surface a
   warning/pending-verification state to the user rather than an unqualified "cancelled".
b) Or bound the abandonment: after the watchdog fires, keep the tool's goroutine and re-attach
   its outcome (e.g. record a late result/observation) so the task can be reconciled instead of
   silently diverging.

---

### 9. SHOULD FIX — sp4rk: production timeout behaviour is driven by a mutable package-level variable

`agent/tool_watchdog.go:24` declares `var toolWatchdogInterval = defaultToolWatchdogInterval` and
`executeToolCall` reads it directly; the tests mutate it (`withToolWatchdogInterval`, and the
direct assignment in the E2S copy's `TestRun_PauseObservedWhileToolInFlight`). There is no
precedent for such a seam elsewhere in sp4rk (`agent/` and `tools/builtins/` have no other
`var … = <duration>` test seam).

**Why it is a problem:** a production code path reads mutable global state whose only purpose is
testing; a forgotten `t.Cleanup` restore, or any future parallelisation of the watchdog tests,
silently changes the real poll cadence (or races) for the whole package.

**Suggested fix:**
a) Make the interval a field on `Executor` (defaulted in `NewExecutor`, settable only via an
   unexported option used by tests) so each executor instance owns its cadence.
b) Or keep the var but gate it behind an internal testing hook that panics if mutated outside
   tests, and require the restore helper everywhere.

---

### 10. SHOULD FIX — c0wrk: the verification-evidence document misstates the reviewed sp4rk revision

`docs/development/part-d-verification-evidence.md` (untracked) "Environment" section states:
*"sp4rk: HEAD `0b35afe` + the uncommitted fix working tree"* and concludes *"with `GOWORK=off` it
resolves to the module cache pin `…-0b35afe097b3` — the same commit as sp4rk HEAD, so there is no
cross-repo drift."* The sp4rk changes are now **committed** as `c8cd0dd`, and sp4rk's working tree
is clean, so HEAD is `c8cd0dd` while `go.mod` still pins `0b35afe` — i.e. the document's revision
label and its "no cross-repo drift" conclusion are both inaccurate for the change set it claims
to verify.

**Why it is a problem:** the document exists precisely to pin *what* was verified; a stale
revision makes the evidence non-reproducible (a reader following it reproduces a different
revision than the one under review), and the drift claim is the opposite of the true mid-cycle
state.

**Suggested fix:**
a) Update the Environment/Commands section to name the reviewed revisions (`c0wrk 1627a5f0` +
   working tree; `sp4rk c8cd0dd`) and restate the cross-repo status (sp4rk HEAD ahead of the
   `go.mod` pin, expected for a mid-cycle cross-repo change).
b) Or, if the document is meant to describe only the pre-commit working-tree state, state that
   explicitly and note that the same content is now `c8cd0dd`.

---

### 11. SHOULD FIX — c0wrk: `TestStopBackground_DrainLoopBoundedByDeadline` does not create the scenario it documents, so the re-drain loop's real termination path is untested

`backend/session/stopbackground_deadline_test.go` tracks a single `&PersistentBlackboard{}` and
its comment states the blackboard "never reports stopped … so the loop cannot terminate by
draining to zero — exactly the straggler shape." That is not what happens: `stopBackground`
snapshots `m.blackboards` and sets it to `nil`, nothing re-registers, and the loop therefore
terminates on the `len(blackboards) == 0` check after a single iteration. The new
drain-deadline `WARN` fires only because `stopTimeout` is set to `0`, i.e. the shared deadline is
**already expired** before the first iteration — so the multi-iteration re-drain path the branch
was added to bound is never exercised.

**Why it is a problem:** the test gives false confidence that the new bound handles a straggler
that keeps re-registering (the actual failure the branch targets), while it only covers the
trivially-already-expired case. A future regression that drops the bounded check but keeps
terminating on the empty registry would still pass.

**Suggested fix:**
a) Make a `PersistentBlackboard` (or a stub) re-register itself via `m.trackBlackboard` from a
   goroutine during the drain, so the loop genuinely iterates while the registry stays non-empty,
   then assert it returns and logs the WARN once the deadline passes.
b) Or, if the single-iteration case is all that is intended, fix the comment to state that the
   deadline is pre-expired and the empty-registry check is what actually ends the loop.

---

### 12. MUST FIX — sp4rk: the tool watchdog moves `e.tools.Execute` onto a detached goroutine with no `recover`, voiding the documented subagent panic containment

Before this commit `Executor.processSingleToolCall` called `e.tools.Execute(...)` **inline on the
ReAct/Run goroutine** (`git show c8cd0dd^:agent/executor_run.go:1127`). `RunSubAgent`'s panic guard
was written for exactly that: its `defer func(){ if r := recover(); ... }()`
(`agent/subagent.go:49-56`) is installed on the goroutine that calls `executor.Run`, and its comment
states *"Execution runs on this goroutine, so the host cannot wrap it with its own recover — guard
here."* `ToolRegistry.Execute` (`tools/registry.go:254-322`, ending in `tool.Execute(ctx, input)` at
`:321`) adds no recovery of its own.

The new `(*Executor).executeToolCall` runs the call on a **fresh goroutine**
(`agent/tool_watchdog.go:66-69`):

```go
go func() {
    res, err := e.tools.Execute(ctx, name, input)
    done <- toolCallOutcome{result: res, err: err}
}()
```

and both dispatch sites now route through it (`agent/executor_run.go:1131` single, `:1537` batch).
A tool that panics now panics on an **unguarded** goroutine; the `recover` on the RunSubAgent
goroutine is on a different goroutine and cannot catch it, and an unrecovered panic in any goroutine
aborts the whole process.

**Why it is a problem:** the containment is a documented invariant, not a theoretical one. Any tool
(built-in, MCP-proxied, or future) that panics on malformed input/output now takes down the entire
c0wrk host, where before the change the panic was recovered and surfaced as a single failed
subagent step. Verified: a grep for `recover()` across `sp4rk/agent`, `sp4rk/tools` and
`sp4rk/orchestration` returns only `agent/subagent.go:56` (on the Run goroutine) plus an unrelated
blackboard worker — nothing on the new goroutine.

**Suggested fix:**
a) Add a `recover()` inside the watchdog goroutine and deliver the recovered value as the outcome's
   error, so containment is restored at the new call site:
   ```go
   go func() {
       defer func() {
           if r := recover(); r != nil {
               done <- toolCallOutcome{err: fmt.Errorf("tool %q panicked: %v", name, r)}
           }
       }()
       res, err := e.tools.Execute(ctx, name, input)
       done <- toolCallOutcome{result: res, err: err}
   }()
   ```
b) Or keep `e.tools.Execute` on the Run goroutine and bound it without moving the call off the
   guarded goroutine (e.g. run only an *observer* watchdog that abandons the wait, leaving the call
   itself on the goroutine `RunSubAgent` guards).

---

### 13. MUST FIX — sp4rk/c0wrk: the per-tool-call ceiling also kills legitimate long-running and human-interactive tool calls, failing the run at the 5-minute default

`executeToolCall`'s timeout arm (`agent/tool_watchdog.go:103-107`) arms one wall-clock `time.Timer`
around the *entire* `e.tools.Execute(...)` and returns `ErrToolTimeout` when it fires; it cannot
distinguish a wedged tool from one that is legitimately slow or waiting on a human. In c0wrk the
bound is **on by default at 300 s** (`backend/config/defaults.go:381-383` → `timeouts.toolCallTimeout`
in `backend/config/config.go`; threaded through `core/builder.go:697-699` →
`OrchestratorConfig.ToolCallTimeout` → `buildConductorDeps`' `conductorDeps.toolCallTimeout` →
`ConductorConfig.ToolCallTimeout`, installed by `orchestration/conductor.go:391` on the main executor
and, via `conductorLauncher.configureExecutor`, on every subagent executor; the E2S loop receives it
too, `core/orchestrator_e2s.go` → `e2s.Config.ToolCallTimeout`).

Several c0wrk tools block **inside `Execute`** for as long as the human (or the sub-work) needs:

- the registry's **user-confirmation gate** sits inside the timed call — `core/tools/registry.go`'s
  `confirmAndExecute` → the blocking `confirmFunc` (`desktop/startup_phases.go:687-694`) — and the
  mutating groups (`local_write`, `execute`, …) default to `user_confirm`, so a confirmation card
  the user answers slowly is bounded by the ceiling as well.
- `ask_user` waits on a channel for the user — `desktop/startup_phases.go:455-486`
  (`select { case resp := <-ch: ... case <-ctx.Done(): ... }`).
- `declare_plan` (`await_approval`) and `propose_goal` block on their approval callback.
- blocking `delegate` runs its subagents to completion before returning — `core/tools/delegate.go:188,260`.
- `execute_plan` runs the plan's steps synchronously — `core/tools/execute_plan.go:80`
  (`executor.Execute(ctx, input.Steps)`).

**Why it is a problem:** with the default config, a user who answers an `ask_user` prompt, approves a
plan, or clicks "Allow" on a confirmation card after 5 minutes gets `tool "…": tool call timed out`
and the run aborts; the pending prompt is orphaned because the watchdog abandons the wait **without**
cancelling the tool's context, so the late answer is dropped — silent loss of user input. Worse, on
the confirmation path the late "Allow" still reaches `tool.Execute` in the detached goroutine, so the
confirmed mutation (a `write_file`/`edit_file`/`bash_exec`) executes **after** the run has already
failed, with no owning run to record it. A plan or a blocking `delegate` that legitimately runs
longer than 5 minutes (routine for a multi-step task) also fails the whole run. The ceiling targets
the wrong dimension: it bounds elapsed time, not lack of progress.

**Suggested fix:**
a) Exempt interactive/long-running tools from the ceiling — skip the timer for `GroupSystem` tools
   and an allowlist (`ask_user`, `declare_plan`, `propose_goal`, `delegate`, `execute_plan`).
b) Make it a no-progress watchdog (reset the timer on observable progress) rather than a raw wall
   clock, so a tool that is working or awaiting a human is not killed.
c) When it does fire, cancel the tool's context so the pending prompt/sub-work is dismissed rather
   than orphaned; and/or raise the default and require explicit opt-in.

---

### 14. SHOULD FIX — sp4rk glob: every bound-abort path (MaxResults, entry budget, wall-clock timeout) discards the matches already collected and returns an error result

The walk callback returns `errGlobResultsLimited` as soon as `len(results) >= MaxResults`
(`tools/builtins/glob.go:164-166`), and `boundedFS.charge` returns `errGlobEntryBudget` /
`errGlobCanceled` once the entry budget is spent or the glob's own timeout/context fires
(`tools/builtins/globfs.go:16-22, 60-72`). All three propagate out of `GlobWalk` (the walk runs with
`WithFailOnIOErrors`, `glob.go:174`), so `Execute` takes the `walkErr != nil` branch and returns an
`IsError` result (`glob.go:177-178`) — **throwing away the `results` slice it had already built**.

**Why it is a problem:** the bounds fire on legitimate large trees, not only runaways. With the
defaults (10 000 results / 500 000 entries / 30 s), globbing a large monorepo — or any tree on a
slow or network filesystem where the walk exceeds 30 s — returns `glob matched 10000 or more
results; narrow the pattern or path`, `glob aborted after visiting 500000 filesystem entries…` or
`glob timed out after 30s…` and yields **zero** paths, even though thousands had already been
matched. This is both a behaviour change from the previous unbounded `GlobWalk` (which returned
everything it found) and a reversal of the tool's own documented test contract —
`tools/builtins/glob_test.go:159-167` asserts *"All matching files should be returned regardless of
max_results"* and that no `(results limited to` message appears, relying on the executor's central
result truncation instead.

**Suggested fix:**
a) On a budget/timeout sentinel, return the collected `results` (with a truncation/warning suffix,
   `IsError: false`) instead of an error result that discards them.
b) Or distinguish the three sentinels from real I/O errors: return partial results for the
   sentinels and keep the error result only for genuine I/O failures.

---

### 15. SHOULD FIX — sp4rk: the new batch pause/timeout/context early-returns skip the verify-on-edit flush, dropping pending verification across the checkpoint

`state.pendingVerifyEdit` is a `runState` field (`agent/executor_run.go:40`), **not** part of the
returned `Steps`. The verification note normally runs from `runVerifyOnEditHook` at the group's last
sub-call (`agent/executor_run.go:1586`), and the HITL-reject path was deliberately hardened to flush
it at a terminal boundary (`:1504`, comment *"flush verification pending from an earlier edit before
a terminal boundary can discard it"*). The three early-returns the commit adds to `processBatchTool`
— pause (`:1546-1548`), timeout (`:1549-1551`), context error (`:1552-1554`) — `return` **before**
that hook runs.

**Why it is a problem:** with `executor.verify_on_edit` enabled, a batch whose early sub-call is a
successful `write_file`/`edit_file` followed by a sub-call that pauses (or times out/cancels) never
runs the verification. Because a paused run is resumed with a fresh `runState`
(`pendingVerifyEdit=false`), the edit is never verified at all — exactly the silent loss the `:1504`
hardening exists to prevent.

**Suggested fix:**
a) Call `runVerifyOnEditHook` (flush pending verification) in each of the three batch early-returns
   before returning.
b) Or persist `pendingVerifyEdit` in the resumable checkpoint so a resumed run still verifies the
   edit.

---

### 16. SHOULD FIX — sp4rk glob: `walkErrorMessage` reports a wrong (often `0s`) duration for the cancel/deadline case

In the `errGlobCanceled` branch, `walkErrorMessage` tests the *passed* context and then formats the
glob's own configured timeout (`tools/builtins/glob.go:195-199`):

```go
case errors.Is(err, errGlobCanceled):
    if errors.Is(ctx.Err(), context.DeadlineExceeded) {
        return fmt.Sprintf("glob timed out after %s; narrow the pattern or path", t.limits.Timeout)
    }
    return "glob canceled"
```

When `GlobLimits.Timeout == 0` (a supported value — `globTimeout: 0` disables the glob's own timer,
so `walkCtx` is the caller's context) a caller/run deadline produces the literal `glob timed out
after 0s; narrow the pattern or path`. Symmetrically, when the run deadline is shorter than the
glob's 30 s, the message overstates it as `after 30s`.

**Why it is a problem:** the message is model-facing and is the only recovery signal the agent gets;
a wrong duration (`0s`, or a duration that did not actually fire) misdirects the retry.

**Suggested fix:**
a) Report it generically (`glob interrupted: context deadline exceeded`) without a specific
   duration.
b) Or print a duration only when the glob's own `context.WithTimeout` child actually fired (track
   which context produced `Done`).

---

### 17. SHOULD FIX — c0wrk: the new `hung` field on `app:exit_requested` silently drifts from the authoritative event contract and specs

`ActiveSessionInfo` is serialized verbatim into the `app:exit_requested` payload
(`desktop/exit_guard.go:25,89-92`), so the wire payload is now
`{sessions:[{id,name,compacting,hung}], update_pending}`
(`backend/session/manager_execution.go:2116-2118`; TS type `frontend/src/types/events.ts:641-645`).
But the authoritative contract and every spec that describes this payload were left unchanged:

- `specs/contracts/event-catalog.md:27` still documents
  `{sessions: [{id: string, name: string, compacting: boolean}], update_pending: boolean}`;
- the Go doc comment at `desktop/exit_guard.go:16` still enumerates `{id, name, compacting}`;
- `specs/domains/frontend/events.md`, `specs/domains/frontend/stores.md` and
  `specs/contracts/desktop-frontend.md` describe the payload/store without `hung`.

**Why it is a problem:** `AGENTS.md` and `specs/META.md` make the event catalog the authoritative
cross-boundary interface rule ("never silently drifting"). A consumer reading the contract cannot see
the new field or the "not responding" semantics the modal now renders, so any client built against
the contract is now wrong about the payload.

**Suggested fix:**
a) Update `specs/contracts/event-catalog.md:27` (and the `events.md`/`stores.md`/`desktop-frontend.md`
   references plus the `exit_guard.go:16` comment) to include `hung: boolean` and its semantics.
b) Or, if `hung` must not be part of the documented contract, keep it off the wire and derive the
   "not responding" label client-side from an already-documented signal.

---

### 18. SHOULD FIX — c0wrk: `Hung` false-positives a healthy session when a pause is pending during a long LLM call

`Hung` is computed purely from elapsed time since the request
(`backend/session/manager_execution.go:2148`: `!stopRequestedAt.IsZero() &&
time.Since(stopRequestedAt) >= hungSessionThreshold`, threshold `10s` at `:2098-2105`). But
`PauseSession` only flips a pause signal — it does **not** cancel the context (`:1793-1794`) — and the
executor checks the pause checker only at the **top of the next loop iteration, i.e. after the
in-flight LLM call returns** (sp4rk `agent/executor.go:1395-1400`); the new tool watchdog polls pause
only while a *tool* is in flight, not during the model request. The threshold's own justification
(*"a well-behaved pause is answered at the next step boundary — the per-tool watchdog keeps that
under a second"*) therefore does not cover LLM calls, which routinely exceed 10 s.

**Why it is a problem:** a user who clicks **Pause** while a long generation is in flight, then quits
within that window, sees the session labelled "not responding" / "Some sessions are not responding to
a stop request." although the goroutine is perfectly healthy and will pause imminently. `CancelTask`
(Stop) is unaffected because it cancels the context. The flag can therefore misrepresent a healthy
session at exactly the moment the user is deciding whether to force-quit.

**Suggested fix:**
a) Derive `Hung` from a signal that proves non-response — e.g. set it only after `CancelTask` has
   exhausted `stopTimeout` and called `forceTerminateStuckTask`, and/or require that no step boundary
   was crossed since the request (compare against an activity/step timestamp).
b) Or keep the heuristic but suppress it while the session is mid-LLM-request, and/or raise
   `hungSessionThreshold` above the largest expected request time and document it as a heuristic.

---

### 19. SHOULD FIX — c0wrk `stopBackground`: the new drain-deadline branch mislabels the *initial* persistence workers as "late-registered"

`deadline` is fixed **before** the background-goroutine join (`backend/session/manager.go:432`) and
reused for the blackboard stops (`m.bg.closeAndWait(m.stopTimeout)` at `:434`). If a manager-owned
goroutine ignores cancellation (the very case the code anticipates at `:440-448`), that join consumes
the whole `stopTimeout`; the drain loop then runs with an already-expired deadline, so the single,
normal batch of blackboards is stopped with a non-positive budget and the new branch fires
(`:477-481`) logging *"abandoning late-registered persistence workers"* — although these were the
first/only workers and nothing was "late-registered".

**Why it is a problem:** that log is the operator's only signal during a wedged shutdown, and it
misattributes the cause, sending a future debugger after a re-registration straggler that does not
exist. (The silent no-budget stop itself predates the change; only the message is new.)

**Suggested fix:**
a) Word the branch to match reality (e.g. "blackboard drain deadline exceeded; abandoning
   persistence workers"), or use the "late-registered" wording only on a second/re-drain pass.
b) Or give the blackboard drain its own fresh budget instead of reusing the deadline already spent by
   the background-goroutine join.

---

### 20. SHOULD FIX — sp4rk glob: `WithNoFollow` silently changes results for symlinked trees

The walk now runs with `doublestar.WithNoFollow()` (`tools/builtins/glob.go:171`); previously
`GlobWalk` was called with no options, so symlinked directories were traversed. Per doublestar
v4.10.0, `WithNoFollow` means a symlinked directory is no longer descended (e.g. a `**` pattern will
not walk through a symlinked dir) — a change not mandated by any c0wrk spec or ADR (a grep of
`specs/` for a non-following-glob rule found none).

**Why it is a problem:** workspaces that rely on symlinked directories (shared/monorepo layouts,
`node_modules`/`vendor` links, a linked sub-project) silently lose every match under those
directories, with no opt-in/opt-out and no note in the tool description. The change is intentional
per the commit message (it closes a symlink-loop hang), but the trade-off is undocumented to the
model and the operator.

**Suggested fix:**
a) Keep the non-follow default but document it in the glob tool description (and the builtins spec)
   so the behaviour change is discoverable.
b) Or make symlink following configurable (a `GlobLimits`/config flag), so a workspace that needs
   traversal can re-enable it while the entry/timeout budgets still bound the walk.

### 21. SHOULD FIX — sp4rk glob: `boundedFS` charges per fs *call*, not per entry, so a single huge `ReadDir` is neither interruptible nor memory-bounded

`boundedFS.charge` (`tools/builtins/globfs.go:64-75`) increments `seen` and checks the context once
per `Open`/`ReadDir`/`Stat` **call**, and `ReadDir` returns the inner `fs.ReadDir` slice whole
(`globfs.go:85-90`). doublestar reads each directory in one shot via `fs.ReadDir(fsys, dir)`
(`doublestar v4.10.0 globwalk.go:281,338`), which reaches `boundedFS.ReadDir`. So the entry budget
counts *directory accesses* (roughly one per directory), not entries, and a single directory holding
hundreds of thousands / millions of entries is read into one `[]fs.DirEntry` before any budget can
stop it; the 30 s timeout is likewise only observed at the *next* fs call, so the read itself is
uninterruptible.

**Why it is a problem:** the change advertises that a filesystem runaway "can neither hang the tool
nor exhaust memory", but a single pathological (or merely huge, flat) directory still allocates its
full entry slice and blocks uncancellably — the exact memory/DoS class the bound targets. The
`MaxEntries` default (500 000) does not bound a single directory that itself exceeds it.

**Suggested fix:**
a) Stream directories: open them as `fs.ReadDirFile` and read with `ReadDir(n)` in bounded batches,
   charging per entry, so the budget bounds one directory and the read stays interruptible.
b) Or count entries rather than calls and cap a single `ReadDir` (fail with `errGlobEntryBudget`
   once a directory yields more than the remaining budget).

### 22. SHOULD FIX — c0wrk: the working tree does not build from the pinned `go.mod` alone (`GOWORK=off`), so the release/CI gate fails until the sp4rk side is published

`go.mod` still pins `github.com/v0lka/sp4rk v0.15.1-0.20261006200117-0b35afe097b3` (commit `0b35afe`),
but this change consumes APIs that exist only in the unpushed sp4rk commit `c8cd0dd` —
`agent.ErrToolTimeout`, `builtins.GlobLimits`, `builtins.NewGlobToolWithLimits`,
`(*Executor).SetToolCallTimeout`, `ConductorConfig.ToolCallTimeout`. The tree builds only through the
gitignored `go.work` (`use ( . ../sp4rk )`).

**Why it is a problem:** `GOWORK=off go build ./core/...` fails (`undefined: agent.ErrToolTimeout`,
`undefined: builtins.GlobLimits`, `undefined: builtins.NewGlobToolWithLimits`), and so do CI, `make
bump` and `make vulncheck` until the sp4rk commit is pushed and the pin is bumped. This is the
ADR-031-sanctioned mid-cycle state (see #10, whose stale-revision claim follows from the same gap),
so it is expected — but it must be closed before the change set is mergeable.

**Suggested fix:**
a) Commit + push sp4rk `c8cd0dd`, run `make bump` (which updates the pin with `GOWORK=off`), and
   confirm a clean `GOWORK=off go build ./...` before merge.

---

### 23. SHOULD FIX — sp4rk: the new single-call pause/timeout early-returns also skip the verify-on-edit flush (the batch path is #15; the single path is not covered)

`agent/executor_run.go:1136-1143` (`processSingleToolCall`, added by the same `c8cd0dd`) returns the
resumable pause checkpoint and the timeout error **before** `runVerifyOnEditHook` runs at `:1198`:

```go
if isPauseError(err) {
    return &ExecutorResult{Steps: state.allSteps, Finished: false}, actionNone, ErrPaused
}
if isToolTimeoutError(err) {
    return nil, actionNone, fmt.Errorf("tool %q: %w", action.Name, ErrToolTimeout)
}
```

`state.pendingVerifyEdit` (declared `agent/executor_run.go:40`) is set when an earlier sibling
sub-call is a successful `write_file`/`edit_file` (`agent/verify_on_edit.go:133-134`) and is flushed
only at the group's **last** call (`:136-139`) or by `flushPendingVerifyOnEdit` on an implicit
finish (`:155-165`). A group whose final (or only) call blocks long enough to pause/time out returns
here, so the pending verification is neither run nor carried in the checkpoint — and a paused run
resumes with a fresh `runState` (`pendingVerifyEdit=false`), so the edit is never verified. This is
the same defect #15 records for `processBatchTool` (`:1546-1554`), but at the single-call dispatch
site, which #15 does not cover.

**Why it is a problem:** with `executor.verify_on_edit` enabled, a group `[write_file, slow_tool]`
whose `slow_tool` pauses silently drops the verification of the preceding edit — exactly the loss
the `:1504` HITL-reject hardening exists to prevent.

**Suggested fix:**
a) Call `e.flushPendingVerifyOnEdit(ctx, state)` (the existing last-resort flush, which runs the
   pending verification if any) in the pause early-return before returning the checkpoint.
b) Or persist `pendingVerifyEdit` (or the pending note) in the returned checkpoint so a resumed run
   still verifies the edit instead of relying on the in-memory `runState`.

---

### 24. SHOULD FIX — c0wrk: `shutdownWatchdog.run`'s `select` races the stop signal against the deadline, so a teardown that just completed can still be force-exited

`desktop/shutdown_watchdog.go:66-78`:

```go
select {
case <-w.done:   // teardown completed; the deferred w.stop() closed done
    return
case <-timer.C:  // hard deadline elapsed
    w.log.Error("shutdown exceeded hard deadline; forcing exit", ...)
    w.onExpiry() // production wiring: os.Exit(0)
}
```

When the deadline elapses at (nearly) the same instant `Shutdown` finishes and its deferred
`w.stop()` closes `done`, both cases are ready and Go picks one *uniformly at random*. If `timer.C`
wins, `onExpiry()` runs `os.Exit(0)` even though the teardown completed — skipping the remaining
deferred cleanup, including the "application shutdown: complete" record the watchdog comment itself
cites as the thing that must appear. This is the same nondeterministic-`select` class as #1,
surfaced in the new sibling file.

**Why it is a problem:** the forced-exit path can fire on a healthy, already-completed teardown, and
whether the completion log is written varies from run to run — the opposite of the "normal teardown
completes cleanly" behaviour the watchdog is meant to leave untouched.

**Suggested fix:**
a) Drain `done` first with a non-blocking `select { case <-w.done: return; default: }` before the
   blocking select, so a completed teardown always wins.
b) Or, in the `timer.C` arm, re-check `w.done` non-blockingly and return (without exiting) when it
   is already closed.

---

### 25. SHOULD FIX — c0wrk: the change set adds structural behaviour but updates no owning spec, leaving the E2S and session-lifecycle specs stale

The change set adds structural behaviour — the per-tool-call watchdog (sp4rk `agent` plus the new
`core/e2s/tool_watchdog.go`), the in-flight cooperative-pause observation (`stopRequestedAt`/`Hung`
in `backend/session/manager_execution.go`; the mid-tool pause in sp4rk), and the shutdown
hard-deadline watchdog (`desktop/shutdown_watchdog.go` + `ShutdownConfig`) — yet touches no spec.
The specs that own that behaviour are now stale:

- `specs/domains/e2s.md` enumerates the `core/e2s/*` files but not the new `core/e2s/tool_watchdog.go`,
  and its loop / `ErrPaused`-checkpoint description predates the in-flight pause and the
  `ErrToolTimeout` terminal path added to `core/e2s/loop.go`.
- `specs/domains/session-lifecycle.md` lists `backend/session/manager_execution.go` in its file
  inventory (its "Session Pause / Resume / Nudge" section) yet does not describe `stopRequestedAt`/
  `Hung`, `forceTerminateStuckTask`, the new `stopBackground` drain deadline, or a pause observed
  while a tool is in flight.

This is a distinct location from #17 (which covers the `app:exit_requested` payload / event-catalog
drift for the `hung` field): here it is the *behavioural* specs, not the wire contract.

**Why it is a problem:** `AGENTS.md` and `specs/META.md` require structural changes to be reflected
in the owning spec; a reader of `e2s.md`/`session-lifecycle.md` now gets a stale picture of how a
paused/timed-out tool call and the quit teardown behave.

**Suggested fix:**
a) Update `specs/domains/e2s.md` (add `tool_watchdog.go`; document the in-flight pause and the
   `ErrToolTimeout` terminal path) and `specs/domains/session-lifecycle.md` (add `stopRequestedAt`/
   `Hung`, `forceTerminateStuckTask`, the drain deadline, and the mid-tool pause) to match the
   change set.
b) Or record explicitly in each spec that these behaviours are deliberately out of scope for that
   document.

---

### 26. SHOULD FIX — c0wrk: #25's stale-spec enumeration is incomplete — the builtins and crash-logging specs are stale too

Beyond the two specs #25 names, the change set also leaves these owning c0wrk specs stale (verified:
`git diff origin/main --name-only` touches no `specs/` path at all):

- `specs/domains/tool-system/builtins.md:172-180` — the "Non-truncation tool limits" table lists
  `BashTimeouts` / shell blocklist / `WebSearchLimits`, but not the new `GlobLimits.{MaxEntries,
  MaxResults,Timeout}` that `core/tools/builtin_registration.go:129` (`NewGlobToolWithLimits(cfg.
  GlobLimits)`) now feeds from `toolLimits.globMaxEntries/globMaxResults` + `timeouts.globTimeout`
  (`core/builder.go:3245-3249`, `backend/configadapter.go:354-356`). A reader cannot discover that
  the glob walk is now bounded, nor where the knobs live.
- `specs/domains/crash-logging.md:36,41,50` — its file inventory and its "every confirmed quit
  funnels through `App.Shutdown`; the closing-bracket invariant is unaffected" claims make no
  mention of the new `desktop/shutdown_watchdog.go` / `shutdown.hardDeadline`, which is precisely a
  path that ends the log abruptly (see #29).
- (Weaker, same class) `specs/contracts/core-sp4rk.md` does not list the newly consumed sp4rk API
  (`SetToolCallTimeout`, `GlobLimits`, `NewGlobToolWithLimits`).

A further distinct c0wrk location:
- `specs/domains/tool-system/README.md:117-137` — its embedded `toolLimits:` / `timeouts:` sample
  block (which even notes *"Both conventions match the yaml struct tags in
  `backend/config/config.go`"*) still lists `readDefaultLines`/`webSearchMaxResults`/`perToolTruncation`
  and the old timeout keys, but not the new `globMaxEntries`/`globMaxResults`/`globTimeout`/
  `toolCallTimeout`. Verified: `grep -rn "globTimeout|globMaxEntries|toolCallTimeout" specs/ docs/`
  returns nothing.

**Why it is a problem:** same convention as #25 (`AGENTS.md`, `specs/META.md` "never silently
drifting"); the glob-bound behaviour and the new abnormal-exit path are reader-visible interface
changes left undocumented.

**Suggested fix:**
a) Extend the #25 update to these files (add the `GlobLimits` row to `builtins.md:172-180`; add the
   shutdown watchdog + `shutdown.hardDeadline` to `crash-logging.md`'s inventory and quit-path
   invariant; add the new sp4rk API to `core-sp4rk.md`).
b) Or record in each that the behaviour is deliberately out of scope for that document.

---

### 27. SHOULD FIX — sp4rk: the commit adds public API and silently changes the `PauseChecker` invocation contract, but updates no sp4rk spec or doc

`git show c8cd0dd --stat` touches only `agent/*`, `orchestration/conductor.go` and
`tools/builtins/*` — no spec/ADR/doc — while it adds public API (`WithToolCallTimeout`,
`SetToolCallTimeout`, `ErrToolTimeout`, `DefaultToolCallTimeout`, `GlobLimits`, `DefaultGlobLimits`,
`NewGlobToolWithLimits`, `ConductorConfig.ToolCallTimeout`) and changes documented behaviour. The
sibling knobs it sits beside (`WithPauseChecker`, `WithUserMessageSource`, `SetVerifyOnEdit`) *are*
enumerated, so the omission is an inconsistency. Stale anchors: `specs/domains/orchestration/
executor.md:26` (Options list) and its `Run` error-contract paragraph, `specs/domains/orchestration/
conductor.md:124` and its `ConductorConfig` listing, `specs/domains/tool-system/builtins.md:32`
(glob row), `specs/contracts/agent-execution.md:36`, `docs/agent-executor.md:61-62,96,117,130-133`,
`docs/tools.md:440`, `docs/orchestration.md` (config listing), and `specs/domains/orchestration/
README.md` (its `ConductorConfig` field list and notable-defaults table).

Separately, the `PauseChecker` invocation contract changed: the field/option/setter comments
(`agent/executor.go:702-708` — "it is invoked **once per step**"; `:535-540`),
`ConductorConfig.PauseChecker` (`orchestration/conductor.go:116-124`), `docs/agent-executor.md:61,
130` and `specs/domains/orchestration/executor.md:40` all still promise "once per step (boundary)",
but `executeToolCall` now calls the checker on a 250 ms ticker (`agent/tool_watchdog.go:96-102`,
`defaultToolWatchdogInterval`) for as long as a tool is in flight — ≈4 calls/second on a long
`bash_exec`/`ask_user`/blocking `delegate` (≈1200 over a 5-minute call). A host that wrote a cheap
per-step checker (possibly with logging, counters, or a one-shot drain) now sees it hammered
mid-call. Additional same-class "once per step" anchors: `agent/subagent.go:236`,
`orchestration/types.go:97`, `orchestration/stepoutput_adapter.go:22`, `agent/executor.go:534`
(field comment) and `:1285` (the `ErrPaused` sentinel string `"executor paused at step boundary"`,
now also emitted mid-tool-call).

**Why it is a problem:** `specs/META.md` requires a spec update "after any change that alters
documented behavior", and sp4rk's exported surface is the SDK contract consumed by hosts; both the
missing docs and the silently changed hook cadence mislead downstream consumers.

**Suggested fix:**
a) Update the sp4rk specs/docs named above (add the ceiling + `ErrToolTimeout` + glob limits +
   `ConductorConfig.ToolCallTimeout`; restate the `PauseChecker` cadence as "polled every
   `toolWatchdogInterval` while a tool call is in flight" and require it cheap and idempotent).
b) Or make the mid-call pause poll opt-in (a separate flag/interval, default off) so the documented
   "once per step" contract holds for existing hosts, and document both paths.

---

### 28. SHOULD FIX — sp4rk: the new test file fails the mandatory lint gate (`golangci-lint` `revive: context-as-argument`)

`agent/tool_watchdog_test.go:100`:

```go
func runExecutorBounded(t *testing.T, exec *Executor, ctx context.Context, defs []tools.ToolDescriptor) (*ExecutorResult, error)
```

`context.Context` is the third parameter; `revive`'s `context-as-argument` rule (enabled in
`.golangci.yml`) requires it first. Verified: `golangci-lint run ./agent/...` → exit 1,
`1 issues: * revive: 1 — agent/tool_watchdog_test.go:100:55: context-as-argument` (local
golangci-lint v2.13.2, the version CI pins). `make lint` and the CI `Lint (Go)` job are therefore
red on this commit — the only mechanical failure in the three changed packages (`gofmt` and
`go vet` are clean).

**Why it is a problem:** `AGENTS.md` (both repos) lists `make lint` as a hard pre-PR gate, so the
commit as-is cannot pass CI.

**Suggested fix:**
a) Reorder the helper's parameters so `ctx context.Context` is first and update its call sites.
b) Or drop the parameter and use `t.Context()` / `context.Background()` inside the helper.

---

### 29. SHOULD FIX — c0wrk: the shutdown watchdog's forced `os.Exit(0)` bypasses the crash-logging clean-exit hooks, fabricating an "unclean shutdown" on the next launch

`desktop/shutdown_watchdog.go:73-77` runs `w.onExpiry()` on the watchdog's own goroutine; the
production wiring is `shutdownExit := func() { os.Exit(0) }` (`desktop/startup.go:596,600`). Because
`os.Exit` never returns, the process dies before `wails.Run` returns, so `mainImpl`'s
`activeCapture.RemoveMarker()` (`main.go:205`) and `main`'s `activeCapture.LogExit(code)`
(`main.go:31`) never run: the liveness marker `app.running.json` survives and the run has no exit
banner. `crashlog.ReportUncleanShutdown` (`backend/crashlog/crashlog.go:288`; marker semantics
`:23,161`) then logs an unclean-shutdown WARN on the *next* launch.

Two routine triggers: (1) the #24 race — teardown completes, `timer.C` wins, and a **healthy** quit
still leaves the marker; (2) the shipped default `hardDeadline: 20 s` (#4) during an embedded cold
load (stop ≤ ~46 s) — every such normal quit fires the watchdog. This contradicts
`specs/domains/crash-logging.md:41` ("a clean quit always ends with a closing bracket") and `:50`
("every confirmed quit still funnels through `App.Shutdown`").

**Why it is a problem:** a forced-but-legitimate quit becomes indistinguishable from a crash on the
next launch, and the documented clean-quit invariant is broken by construction.

**Suggested fix:**
a) Route the forced exit through a small crashlog helper (e.g. `crashlog.ForceExit(log, code)` that
   `RemoveMarker()` + `LogExit(code)` then `os.Exit(code)`) so the marker/banner match every other
   exit path.
b) Or, via the existing `a.shutdownExitFn` seam, make the default expiry action first run the
   crashlog clean-exit hooks and then exit.
c) Or document the forced-exit path explicitly in `specs/domains/crash-logging.md` and exit with a
   distinct non-zero code so the exit code and the surviving marker agree.

---

### 30. SHOULD FIX — c0wrk: the never-shrink guard only blocks *empty* trajectory syncs, so a subagent's first non-empty (shorter) sync still overwrites the parent's checkpoint

`core/conductor.go:37-52` guards `trajectoryHolder.Sync` with `if len(steps) == 0 && len(h.steps) > 0
{ return }` — it ignores only an *empty* sync, not a *shorter* one, although its comment claims the
"persisted trajectory never goes backwards" invariant. A subagent shares the holder: `subagentCtx`
(`core/conductor.go:1483-1511`) clears the delegation/plan/goal keys but not
`agent.WithTrajectoryStore`, which the conductor installed at `:2811`; `RunSubAgent` then runs its
executor on that ctx, and the executor calls `ts.Sync(state.allSteps)` at the top of every loop
iteration (sp4rk `agent/executor.go:1372-1374`). So a subagent's sync sequence is `[]` (ignored) →
`[sub1]` (**not** ignored) → …, and each non-empty sync replaces the parent conductor's already-held
N-step trajectory with the subagent's shorter list. `compositeTrajectoryStore.Sync` (`:316`) forwards
to the holder and then snapshots `c.memory.Steps()` into `SaveTrajectory`, so the shorter list is
what is persisted.

**Why it is a problem:** a delegation longer than one step (the normal case) followed by a
quit/crash persists the subagent's 1–2 step list instead of the parent's completed steps, so a
resume loses the parent's work — the exact "checkpoint lost on restart" failure the guard exists to
prevent. It also makes #1's characterisation ("worked around on the c0wrk side by making
`trajectoryHolder.Sync` never shrink") inaccurate: the guard is empty-only.

**Suggested fix:**
a) Make the guard monotonic (`if len(steps) < len(h.steps) { return }`), or key the holder
   per-executor so only the conductor's trajectory is persisted.
b) Or seed the holder with the task's own steps so a subagent's list is always the parent's
   trajectory plus its own append, never a replacement.

---

### 31. SHOULD FIX — sp4rk: the two new glob bounds and `NewGlobToolWithLimits` have no test coverage

The change's headline guarantee ("a filesystem runaway must neither hang the tool nor exhaust
memory") rests on `errGlobEntryBudget` (`tools/builtins/globfs.go:19`, raised by `charge()` at
`:70`) and `errGlobResultsLimited` (`tools/builtins/glob.go:164-166`), but no test constructs
`NewGlobToolWithLimits` with a small budget or asserts either sentinel (verified:
`grep -rn "errGlobEntryBudget|errGlobResultsLimited|NewGlobToolWithLimits" tools/builtins/*_test.go`
matches nothing; the only `MaxResults` tests reference the removed per-tool notion). The
`TestGlobTool_SymlinkLoopTerminates` / `ContextCancelMidWalk` tests exercise only `errGlobCanceled`,
produced by the **caller's** context.

**Why it is a problem:** a regression that inverted the budget comparison (`globfs.go:70`) or dropped
the result cap would keep the whole suite green, and the result-discarding semantics flagged in #14
are unverified.

**Suggested fix:**
a) Add tests calling `NewGlobToolWithLimits(GlobLimits{MaxResults: 3})` / `{MaxEntries: 2}` over a
   small tree and assert the abort message and (per the #14 decision) the returned/withheld results.
b) At minimum, unit-test `boundedFS.charge()` (entry budget + ctx-done) and all three
   `walkErrorMessage` branches.

---

### 32. SHOULD FIX — sp4rk: the new symlink test's "never escapes the search root" assertion is inert

`tools/builtins/glob_test.go:376-386` (`TestGlobTool_SymlinkLoopTerminates`) asserts every returned
line is neither `filepath.IsAbs(line)` nor `strings.HasPrefix(line, "..")`, to prove the walk "never
escapes the search root". But `doublestar.GlobWalk` hands the callback paths that are slash-separated
and **relative to the FS root** (`path.Join(dir, name)` in doublestar `globwalk.go:281,338`), and
`glob.go` appends them verbatim — so an escaping walk over `link -> /` yields paths like
`link/etc/hosts`, neither absolute nor `..`-prefixed, and the check can never fail.

**Why it is a problem:** the test gives false assurance for the claim it exists to prove, and the
genuinely-unclosed shape (a literal symlink prefix such as `link/**` under `WithNoFollow`, which
doublestar still traverses — `globoptions.go:75-88`) is neither executed by this test nor detectable
by its assertion.

**Suggested fix:**
a) Assert containment properly (e.g. require no result line carries a `link/`/`self/` prefix, or
   match the exact expected set), and add a literal-prefix `link/**` case.
b) Or drop the escape loop and assert the concrete expected result set per case.

---

## Notes

- All 21 modified tracked files and all 9 untracked files of the c0wrk working tree, and all 7
  files of sp4rk `c8cd0dd`, were read and are accounted for above. No finding is recorded for a
  file that merely carries wiring/plumbing that is correct end-to-end (e.g. the `ToolCallTimeout`
  thread from `config → builder → OrchestratorConfig → conductorDeps → {main, subagent, E2S}
  executors`, and the new `shutdown_watchdog` unit seam) — the plumbing itself is sound. Where such
  a path is sound as plumbing but defective in behaviour, the defect is recorded above: the
  `ActiveSessionInfo.Hung` field's contract drift and false-positive semantics (#17, #18), the
  tool-call-timeout's bounding of interactive tools (#13), and the glob/executor behaviours the new
  wiring enables (#12, #14, #15, #16, #20, #21). The same holds for the later-pass additions
  (#23–#29): the verify-on-edit flush sites, the watchdog teardown/select races, the spec/doc drift,
  the sp4rk lint-gate failure, and the crashlog-marker bypass are all recorded defects in otherwise
  sound wiring, not plumbing complaints.
- No reviewed source file was modified while producing this review; this file only records
  findings.
