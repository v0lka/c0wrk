# Isolated instance runs (per-profile `$HOME`)

How to run a second, fully isolated c0wrk instance — a test build, an experimental-flag run, a second "account" — while the real instance keeps running.

Since [ADR-086](../../specs/decisions/086-per-profile-single-instance-identity.md) the single-instance identity is derived from the agent directory: `desktop.SingleInstanceLockIDFor(agentDir)` = `com.wails.c0wrk-desktop` + `"_" + hex(sha256(canonical agentDir))[:12]` (`desktop/singleinstance.go`). Two processes pointing at different agent directories are independent lock/relay domains, so the second launch boots its own app instead of relaying into the running window. The agent directory is `<home>/.c0wrk` — `config.AgentDir()` is `os.UserHomeDir()` + `.c0wrk` (`backend/config/paths.go`) — therefore replacing the launch environment's home directory relocates everything: config, database, sessions, tools, embedded-LLM artifacts, crash diagnostics, and the lock identity itself.

This document operationalizes the hermetic whole-app launch convention from [specs/domains/testing.md](../../specs/domains/testing.md). Prerequisites: the binary must have been built (`make build`, which also installs ONNX Runtime next to the executable — the app will not launch without it).

## Commands

Rules that hold on every platform:

- The override must reach the **exec'd process**, not merely the shell that produced it — prefix the command (or `export` in the same shell) so every child (`make`, `wails`, the app binary) inherits it.
- First start regenerates `config.yaml` when absent (fresh providers, experimental toggles off). Copy the real `config.yaml` in when the test needs real provider settings.
- `tools/` may be copied from the real home — it is stateless managed binaries (~538 MiB); copying skips the re-download. Everything else is created fresh per isolated run.
- A second launch **with the same override** relays into the isolated instance (per-profile relay, ADR-086), exactly as two plain launches relay into the real one. One profile, one instance.

### Linux

```bash
export TEST_HOME=$(mktemp -d)
mkdir -p "$TEST_HOME/.c0wrk"
cp -r ~/.c0wrk/tools "$TEST_HOME/.c0wrk/"   # optional: skip tool re-download

# built binary (build/bin/c0wrk-desktop):
HOME="$TEST_HOME" ./build/bin/c0wrk-desktop

# dev loop (the Makefile adds the required Linux -tags webkit2_41):
HOME="$TEST_HOME" make dev-desktop
```

A command-line variable assignment reaches `make`'s recipes, so the `wails dev`-spawned app inherits the isolated home.

### macOS

Terminal exec of the bundle binary is the only way to express a non-default profile: Finder/Dock launches, notification banner clicks and `open`/`open -n` all go through LaunchServices and never inherit the operator's shell environment. On macOS startup runs `config.LoadShellEnvironment()` (the Finder workaround, `backend/config/shell_env.go`); it deliberately does not override already-set non-PATH variables, so an explicitly exported `HOME` survives startup.

```bash
export TEST_HOME=$(mktemp -d)
mkdir -p "$TEST_HOME/.c0wrk"
cp -R ~/.c0wrk/tools "$TEST_HOME/.c0wrk/"   # optional

# built binary (inside the bundle):
HOME="$TEST_HOME" ./build/bin/c0wrk-desktop.app/Contents/MacOS/c0wrk-desktop

# dev loop:
HOME="$TEST_HOME" make dev-desktop
```

See *Updater caveats* below: on macOS an update applied by the isolated profile relaunches through LaunchServices and drops the override.

### Windows (PowerShell)

`os.UserHomeDir()` reads `%USERPROFILE%` on Windows, so the profile switch keys on `USERPROFILE` (not `HOME`). Run this in a dedicated shell — the override affects every other process started from it (git, installers):

```powershell
$testHome = Join-Path $env:TEMP ("c0wrk-test-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force "$testHome\.c0wrk" | Out-Null
Copy-Item -Recurse "$env:USERPROFILE\.c0wrk\tools" "$testHome\.c0wrk\"   # optional

$env:USERPROFILE = $testHome

.\build\bin\c0wrk-desktop.exe    # built binary
wails dev                        # dev loop
```

## What is isolated (per agent directory)

Everything below resolves under `<home>/.c0wrk` and therefore lands in the isolated home:

| Path | Content |
| --- | --- |
| `config.yaml` | full runtime config; regenerated on first start when absent |
| `database.db` | SQLite: sessions, projects, messages, tasks, blackboard |
| `projects/` | per-project session storage and LLM/step dumps |
| `logs/` | session logs plus crash capture: liveness marker, `stderr.log` (+ `stderr.old.log`) |
| `app.lock` | the app-level advisory lock (first-instance gate) |
| `tools/` | tool-manager artifacts (rg, uv, markitdown, venv) — stateless, copyable |
| `runtimes/` | embedded-LLM inference runtimes (`llama-<tag>-<backend>`) |
| `models/bonsai-2-27b/` | embedded-LLM weights + manifest — each profile provisions its own |
| `update-staging/` | self-update download/verify staging (per profile by construction) |
| `update_state.json` | last auto-check timestamp, skipped version — per profile |
| `window_state.json` | window geometry — per profile |

Plus the behavioral identity:

- **Single-instance identity / relay domain.** The Wails `UniqueId` is derived from the canonical agent dir (Clean → Abs → EvalSymlinks, fail-soft), so the isolated instance is the *first instance of its own profile*: it boots for real while the real instance runs. Launches carrying the real home relay into the real instance and never disturb the isolated one, and vice versa (ADR-086).
- **Crash capture.** `crashlog.Install` writes its marker and `stderr.log` into the isolated `<agentDir>/logs` and is gated on being the first instance *of that profile* (`main.go`); test crashes never touch the real home's forensics, and the real instance's "did not shut down cleanly" warnings can never be fabricated by a test run.
- **Embedded-LLM supervisor.** The loopback port is a per-profile preference, re-checked before every spawn and walking upward when taken — two profiles' `llama-server`s coexist on adjacent ports instead of fighting.

Also isolated because they are home-derived: git global config discovery (`~/.gitconfig` — an isolated instance does not see your identities or credential helpers unless you provide them) and the Linux notification-icon cache (`os.UserCacheDir`).

### Not isolated by the home override

Workspace-scoped state follows the **project directory you open**, not `$HOME`: the git repository itself, project-local skills/agents (`<workspace>/.agents/`), and the research root (`<workspace>/.research`). Opening the same workspace from both instances shares all of it — point the isolated instance at a test workspace unless the sharing is deliberate.

## What is shared

- **OS temp directory.** The self-update two-process staging is created under `os.TempDir()` on every platform (`updaterStagingBase()`), and the darwin relay lockfile lives in the native temp dir as `<UniqueId>.lock`. The home override does not touch `TMPDIR`, so both profiles share the temp dir. This is safe by design (see *Updater caveats*); exporting `TMPDIR` too (Unix) isolates even that, with no ill effect — each profile only ever looks at its own per-`UniqueId` entries.
- **macOS bundle id / LaunchServices.** The bundle id (`com.wails.c0wrk-desktop`, the stable `SingleInstanceLockID` prefix) is a property of the app bundle, not of any profile. Every GUI-spawned launch — Finder double-click, Dock, notification banner click, `open -n` — resolves the REAL home and the real profile: with the real instance running it is focused/relayed into, without one it boots a real-profile first instance. The isolated instance can never be a GUI-spawn target and never receives banner-click routing; reach it only from the terminal that exported the override. (The relay *channel* itself is per-profile — what LaunchServices decides to *spawn* is what is shared.)
- **Dev-server ports (`wails dev`).** The Wails dev server binds its fixed configured address (default `localhost:34115` — two simultaneous dev sessions cannot both bind it), and the Vite server binds 5173 and auto-increments when busy (c0wrk sets no port and uses `frontend:dev:serverUrl: "auto"` in `wails.json`, so Wails discovers the chosen port). Ports are host-scoped: the home override does not change them, and two concurrent `wails dev` sessions contend for the Wails dev address.
- **The install/build tree.** Both profiles run the same binary from the same directory and share the read-only artifacts next to it (ONNX Runtime, the embedding model under `build/bin`). A self-update swaps this shared tree — see caveats.
- **The OS session.** Display, audio and notification daemons, the D-Bus session bus (one bus, per-profile bus names), Windows session-local mutex/window namespaces.

## Updater caveats

1. **Two "first" instances sweep the shared temp dir.** Each profile legitimately runs the first-instance-only startup tail, including `updater.CleanupStaleUpdaters` over `updaterStagingBase()`. This is benign by construction: the sweep skips everything younger than 24 h (`cleanupStaleMaxAge`, `core/updater/installer_util.go` — an in-flight download or a staged apply pending relaunch counts as live), and download staging is per-profile (`<agentDir>/update-staging`). One profile's sweep can only reap the other's genuinely stale (>24 h) leftovers — which is the sweep's purpose.
2. **A self-update swaps the shared install tree.** The two-process flow (`--self-update`) waits only for the updating instance's pid, renames the tree (previous tree retained as the `.old` rollback backup), and relaunches that instance. The other profile keeps executing its current image — two builds transiently side by side is a supported state (ADR-086) — but its *next* launch, and its own later update, pick up whatever tree is current. Expect a test profile to "inherit" an update applied from the real one.
3. **macOS: the update relaunch drops the profile.** `relaunchApp` on darwin goes through `open -n` (LaunchServices), which does not pass the operator's environment — an isolated-profile instance that applies an update comes back as the REAL profile. On Linux and Windows the relaunch execs the new binary directly with the updater's environment, so the profile survives the relaunch there. Practical rule: apply updates from the real profile; on macOS, re-exec the updated isolated binary manually from its terminal if you need it back.
4. **Per-profile update state.** `update_state.json` lives in the agent dir, so the isolated profile auto-checks (or skips versions) independently of the real one. Set `updates.enabled: false` in the isolated `config.yaml` when the test must not update at all.

## Related

- [ADR-086](../../specs/decisions/086-per-profile-single-instance-identity.md) — per-profile identity, isolation modes, banner-click outcomes, updater analysis
- [ADR-075](../../specs/decisions/075-single-instance-lock.md) — single-instance lock, relay mechanics, crash-capture gate
- [specs/domains/testing.md](../../specs/domains/testing.md) — the hermetic launch convention this document details
