# Verify on Edit

## Purpose

Mechanical edit verification: after every successful `write_file` / `edit_file` in a CODE task, c0wrk runs a user-configured verification command (tests, linter, build) and injects its truncated output back into the agent context as a system observation. This replaces self-attested "done" claims with an edit → verify → result cycle the model cannot skip.

## Key Files

- `backend/config/config.go` - `VerifyOnEditConfig` (`executor.verify_on_edit.{enabled, command, timeout, max_output_chars}`)
- `config.example.yaml` - documented example block (default off)
- `backend/config/verify_on_edit_test.go` - config parsing/timeout fallback tests
- `backend/configadapter.go` - maps `VerifyOnEditConfig` into `core.BuilderVerifyOnEditConfig`
- `core/builder.go` - `Build` arms the runner when enabled AND a workspace is active; `applyProviderOutputReserves` neighborhood
- `core/verify_on_edit.go` - `buildEditVerifyRunner` (the runner itself), `parseVerifyOnEditTimeout` (invalid/empty → 2m fallback), `parseExitStatus`, `Orchestrator.verifyOnEditForMode` (CHAT suppression)
- `core/tools/registry_unattended.go` - `ToolRegistry.ExecuteUnattended` — the confirmation-free execution path with every hard security gate intact
- `core/orchestrator.go` / `core/conductor.go` - plumbing into `OrchestratorDeps` / `conductorDeps` → sp4rk `agent.Executor.SetVerifyOnEdit`
- `core/orchestrator_goal.go` - goal-loop suppression (`deps.verifyOnEdit = nil` per turn)
- sp4rk `agent/verify_on_edit.go` - SDK-side debounce, `EditVerifyResult`, `FormatVerifyNote` (rune-safe output truncation)
- sp4rk `orchestration/conductor.go` - `ConductorConfig.VerifyOnEdit` wiring into the executor
- `core/prompts/orchestrator_lite*.md` - "Edit → Verify Cycle" prompt sections instructing the model to fix failures before finishing

## Core Types

```go
// c0wrk side (core/verify_on_edit.go) — the runner is built ONLY from config.
type EditVerifyRunner = agent.EditVerifyRunner // func(ctx) agent.EditVerifyResult

type VerifyOnEditConfig struct {
    Enabled        bool   // master switch, default false
    Command        string // shell command; required when enabled
    Timeout        string // Go duration; empty/invalid falls back to 2m; capped by timeouts.bashMaxTimeout
    MaxOutputChars int    // 0 → agent.DefaultVerifyOnEditCap (4000)
}
```

The runner marshals `{command, timeout, working_directory}` and calls `registry.ExecuteUnattended(ctx, tools.ShellExecToolName(), input)` — the platform shell tool (`bash_exec` on Unix, `posh_exec` on Windows; both share the input schema, the `[Process killed: timeout exceeded]` marker, and the trailing `exit status N` exec error, so the mapping below is platform-portable). The result maps to `agent.EditVerifyResult{Output, ExitCode, TimedOut, Timeout, Err}` (timeout detected via the `[Process killed: timeout exceeded]` marker, exit code parsed from the trailing `exit status N`). The configured timeout is clamped to `timeouts.bashMaxTimeout` (with a warning logged) because the shell tool enforces its own `MaxTimeout` on every command — without the clamp, a larger `verify_on_edit.timeout` would silently never take effect, and `EditVerifyResult.Timeout` echoes the effective limit into the timeout note.

## Flow

```
config.yaml executor.verify_on_edit.enabled=true
  → builder.Build: buildEditVerifyRunner(sessionRegistry, workspace, command, timeout, bashMaxTimeout)
  → OrchestratorDeps.VerifyOnEdit → conductorDeps.verifyOnEdit
  → RunConductor → orchestration.ConductorConfig.VerifyOnEdit
  → executor.SetVerifyOnEdit(runner, maxOutputChars)
  → per response group with ≥1 successful write_file/edit_file:
      runner() runs ONCE (debounced per group)
      FormatVerifyNote → "[verify_on_edit] …" appended to the group's last
      observation (before ToolResult emission: both LLM context and UI see it)
```

Suppression points: No Project (CHAT) mode (`verifyOnEditForMode` → nil), plain goal-loop turns (`orchestrator_goal.go`), and specialized passes (`RunConductor` nils `deps.verifyOnEdit` when `systemPromptOverride != nil`).

## Security Model

The command ALWAYS originates from user configuration, never from model output — which is why running it requires no interactive confirmation. `ExecuteUnattended` nevertheless keeps every hard gate, fail-closed:

1. required-field validation,
2. disabled tools (No Project mode),
3. per-session extra shell blacklist,
4. execute-group policy `deny`,
5. hard safety reasons (judge + symlink detection) — a hard reason blocks outright because there is no confirmation flow to escalate to.

Deliberately skipped (input is fixed config, not model output): pre/post-execute hooks, advisory judging of soft reasons, HITL confirmation. `ExecuteUnattended` must never be wired into a model-facing tool-execution path.

## Invariants

- The verification command comes exclusively from `executor.verify_on_edit.command` in user config; the model cannot set, change, or suggest it.
- The runner is nil (feature fully inert) when disabled, when the command is empty, or when no workspace is active.
- Verification never runs in CHAT (No Project) mode or in goal-loop / specialized passes.
- A successful edit marks the run dirty; the command runs at most once per response group (debounce), never twice for the same edit.
- Failed edits, reads, and rejected tool calls never trigger verification.
- Injected output is capped (`max_output_chars`, SDK default 4000) with an explicit truncation marker; the cut is rune-safe (never splits a multi-byte UTF-8 rune).
- A non-zero exit is reported as `VERIFICATION FAILED` and a timeout as `NOT verified` — never as a pass.
- The effective timeout is `min(executor.verify_on_edit.timeout, timeouts.bashMaxTimeout)`; a clamp logs a warning and `EditVerifyResult.Timeout` carries the effective limit into the timeout note, so the model is never advised to raise a knob that cannot take effect.
- Fail-surface mapping is exact: infrastructure failures (input marshaling, registry execution errors) surface as the `Err` note ("could not run verification command"); policy/blacklist denials and signal kills carry no `exit status N`, so they parse to a negative exit code and surface as the `ExitCode < 0` note ("verification command did not complete (blocked or killed — no exit code). The edit was NOT verified"). Either way the edit is never reported as verified — never as a silent pass.
