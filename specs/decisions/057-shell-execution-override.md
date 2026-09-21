# ADR-057: Operator-Configurable Shell Invocation for bash_exec/posh_exec

## Status

Accepted

## Context

The shell-execution tool is launched with a hardcoded invocation: `bash -c <command>` on Unix (`bash_exec`) and `powershell.exe -NoProfile -NonInteractive -Command <command>` on Windows (`posh_exec`). Operators who run a different shell — a Homebrew bash 5 on macOS where `/bin/bash` is 3.2, a login zsh, or `pwsh` on Windows — cannot point the tool at it: every agent command runs under the compiled-in shell. Beyond preference, the hardcoded wrapper also locks the tool to a possibly stale or non-default binary path.

Three things are coupled to HOW the command is launched, which is why a naive "swap the binary" knob is unsafe:

1. **The model's mental model.** The tool description says "execute a shell command via bash -c". An agent writing PowerShell syntax into a zsh wrapper (or fish syntax into anything) produces broken commands unless it is told what shell actually runs.
2. **The deterministic safety floor.** The flowsh analyzer ([ADR-052](./052-flowsh-command-analysis.md)) parses the command text with exactly two syntax families — bash-like (`api.LangBash`) and PowerShell-like (`api.LangPowerShell`). The dialect was derived from the tool NAME (`bash_exec` → bash, `posh_exec` → powershell). A renamed binary keeps the name, but a genuinely different shell invalidates the name→dialect assumption.
3. **PowerShell-specific behavior.** `posh_exec` prepends a UTF-8 console-encoding bootstrap that is PowerShell code; it must never reach a non-PowerShell wrapper.

## Decision

The shell tool's launch shape becomes an operator-configurable **shell invocation override**, configured per tool in the `shell_exec` config section (`shell_exec.bash_exec` / `shell_exec.posh_exec` — only the platform-matching entry is live), with two fields:

- **`command`** — an **argv template**: the first element is the binary, and exactly one element equals the `{command}` placeholder, replaced at execution time by the agent's command **as a single argv element**. The command crosses the process boundary verbatim — no string splicing, no extra quoting layer, no parsing of user input by the app. (Format confirmed interactively with the owner: argv array over a free-form string.)
- **`shell`** — a **closed enum** declaring which shell the command text is written in: `bash`, `sh`, `zsh`, `ksh`, `dash` (bash family) and `powershell`, `pwsh` (PowerShell family). (Closed-list option confirmed interactively with the owner.)

The declared shell drives all three coupled concerns:

1. **Prompts.** An override rewrites only the tool description's "Purpose:" header to name the actual launch command and the declared shell; the default invocation keeps the legacy description **byte-for-byte**. The description is part of every tool catalog attached to every request — Conductor, subagents, verifier, E2S loops — so it is the single channel that informs *all agents able to call the tool*, satisfying the prompt-communication requirement without touching any prompt template or the `{shell_tool}` placeholder contract.
2. **Analysis dialect.** The built-in tools expose `DeclaredShellKind()`; `sdktools.ShellAnalysisLangForTool` resolves the flowsh dialect from the declared kind (falling back to the legacy tool-name mapping when absent), and the host registry passes the registered instance to the analysis (`AttachShellAnalysisForTool`), which calls `AnalyzeShellCommandForJudgeWithDialect`. A `pwsh`-declared `bash_exec` analyzes PowerShell syntax with the PowerShell dialect — the name no longer implies the syntax. Digest `lang` stays consistent for every judge.
3. **PowerShell gating.** The UTF-8 bootstrap is applied only when the declared kind belongs to the PowerShell family.

Validation and failure posture:

- Template validation: non-empty binary (never the placeholder itself), exactly one standalone placeholder element (an embedded substring is rejected — it would reintroduce the quoting layer), and a kind inside the closed enum.
- **Load is fail-soft** (the model-profiles precedent): an invalid entry produces a visible load warning and resets the section to the built-in launch shape. A broken override must not keep the app from starting — and must never run half-applied.
- Runtime updates (Settings UI) reuse the blocklist's atomic re-registration path: validate → re-register the tool via `UpdateShellBlocklist` (which now also carries the invocations) → persist; a re-registration failure rolls the section back so the live registry and the stored config never diverge.
- The UI card lives on the **Security tab** next to the execute group's blocklist editor (both are execute-tool settings; the save-with-rollback pattern already existed there).

Everything else is untouched: process containment (Unix process-group kill, Windows Job Object), `working_directory` validation, timeouts, the blocklist, the flowsh criteria C1–C9, and the confirmation funnel. The override changes *how* a command is launched, never *what* is checked. Verify-on-edit commands (run through the same tool) execute under the override as well.

Confirmed interactively with the owner during planning: closed analyzable shell list; Security-tab card; argv-array template format.

## Consequences

- Operators can run any bash-family or PowerShell-family shell without engine changes; the agent is always told (via the description) what it is writing for.
- The deterministic floor is provably preserved for every accepted override: the declared kind always maps onto a flowsh dialect, so no configuration state can silently drop C1–C9.
- Shells outside the two families (fish, nushell, csh, cmd.exe) are unsupported: there is no analyzer for their syntax, and the alternatives (silent floor loss, or fail-closed on every call breaking verify-on-edit) were both rejected.
- The sp4rk cross-repo cycle for this work is **complete** (ADR-031): the new constructors and the dialect entry points are sp4rk changes, and c0wrk's `go.mod` pin was advanced to the published sp4rk commit that carries them (`v0.15.1-0.20260920163716-2e9a87dc2469`), so `GOWORK=off go build ./...` / `go vet ./...` resolve and pass.
- Cost: the tool description is now composed at construction time (one string op per registration, not per call); the analysis dialect resolution gained an instance-based lookup path.

## Alternatives Considered

- **Free-form shell list with fail-closed analysis** (every command escalates as `command_analysis_unavailable`): honest but unusable — interactive confirmation on every command and verify-on-edit permanently blocked. Rejected with the owner.
- **String template** (`command: "/opt/homebrew/bin/zsh -c {command}"` with app-side quote-aware splitting): friendlier to write, but introduces a parser whose edge cases become quoting bugs. Rejected with the owner.
- **Extending the free-form list to fish/nushell with a bash-dialect best effort**: the analyzer would mis-parse real constructs (e.g. `VAR=value cmd` does not exist in fish), producing wrong verdicts instead of none. Rejected.
- **A system-prompt paragraph per agent**: duplicates the description channel, only reaches agents whose prompt assembly the host controls, and busts prompt-cache stability. The dynamic description reaches every tool catalog with zero template churn. Rejected.
