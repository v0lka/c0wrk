# ADR-029: Embedded terminal env conventions (TERM_PROGRAM + terminal.env)

## Status

Accepted

## Context

The embedded terminal panel runs the user's login shell (`$SHELL -l`) over a PTY with
`cmd.Dir` set to the session workspace. Users whose shell rc files auto-attach tmux
(e.g. `[[ -z "$TMUX" ]] && tmux attach`) therefore land inside their live personal tmux
instance when they open the in-app terminal: the panel shows an unrelated session, in an
unrelated working directory, and every keystroke is delivered into their real tmux
session. This is both inconvenient and unsafe. IntelliJ IDEA suffers the same issue;
VSCode does not, because it sets `TERM_PROGRAM=vscode` — a de-facto (unstandardized)
convention that popular tmux auto-attach snippets already guard against. There was no
terminal config section at all, so users had no way to feed marker variables to their rc
files without editing the rc files themselves.

## Decision

1. `buildTermEnv()` (`core/terminal/manager_common.go`, shared by the Unix PTY and
   Windows ConPTY paths) force-sets `TERM_PROGRAM=c0wrk` and
   `TERM_PROGRAM_VERSION=<core/version.Version>` on every terminal shell process,
   overriding any inherited values (the marker describes the terminal the shell actually
   runs in — xterm.js inside c0wrk — not the terminal the app was launched from).
   `TERM`/`COLORTERM` keep their existing fill-only-if-missing semantics.
2. A new `terminal.env` config section (`map[string]string`) is applied last and wins
   over both the inherited environment and the built-in defaults, letting users set any
   marker their rc files check without editing the rc files. `${VAR}` references are
   expanded at startup via `config.ExpandEnvVars` (existing convention); values are
   never logged.
3. `config.example.yaml` documents the section and the recommended guarded tmux
   auto-attach snippet.

## Consequences

- Shell rc files can now reliably detect the c0wrk embedded terminal and skip behaviors
  that assume a standalone terminal — most importantly tmux auto-attach. Users guarded on
  `TERM_PROGRAM` need a one-line snippet change (identical to what VSCode requires).
- The working directory was never broken (`cmd.Dir` is the workspace root); once
  auto-attach is skipped the panel lands in the project directory as expected.
- A bare unguarded `exec tmux` in an rc file still takes over the terminal — no
  application can intercept an unconditional exec; this is documented as a known limit.
- Env values come from user config (trusted zone) and stay inside the terminal PTY; the
  agent's own exec tools are unaffected.

## Alternatives Considered

- `INSIDE_EMACS=true` (the JetBrains-project workaround): rejected — it is an unrelated
  legacy Emacs/IDEA hack; modern tmux does not act on it, and lying about the emulator
  can misfire other rc integrations.
- Spawning the shell non-interactively or with `--norc`/`--noprofile`: rejected — loses
  user settings, which users explicitly want ("a fresh shell with my configuration").
- Shadowing the `tmux` binary via PATH: rejected — fragile, dishonest, and breaks manual
  tmux use inside the terminal.
- Bundling/managing a multiplexer inside c0wrk: rejected — massive scope creep and the
  opposite of the requested behavior.
