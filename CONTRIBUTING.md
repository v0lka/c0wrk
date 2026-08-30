# Contributing to c0wrk

Thanks for your interest in hacking on **c0wrk** — a desktop AI coding-agent built with Wails v2 (Go backend + React 19 / Vite 6 / TS frontend).

> [!WARNING]
> c0wrk is in **Early Alpha**. Features, APIs, and configuration formats may change without notice.

This document covers everything a contributor needs: architecture, requirements, building from source, development workflow, and the release process. For day-to-day coding conventions, also read [`AGENTS.md`](AGENTS.md) — it is the authoritative guide for coding agents and humans alike.

---

## Table of contents

- [Architecture](#architecture)
- [Frontend stack](#frontend-stack)
- [Requirements](#requirements)
- [Linux build dependencies](#linux-build-dependencies)
- [Build from source](#build-from-source)
- [Configuration](#configuration)
- [Development commands](#development-commands)
- [Build / Run](#build--run)
- [Project structure](#project-structure)
- [Troubleshooting](#troubleshooting)
- [Continuous integration](#continuous-integration)
- [Releasing](#releasing)

---

## Architecture

High-level layers and responsibilities:

- **`desktop/`** — Wails app lifecycle; embeds `*backend.FrontendAPI` whose promoted methods are exposed to the frontend via Wails bindings.
- **`backend/`** — application/view-model layer: config loading, session/project management, persistence wiring, installer/watcher behavior. Frontend-callable methods split across `backend/frontend_api_*.go` by area.
- **`core/`** — orchestration logic: planner, router, reflector, tool registry, MCP gateway, security policy application.
- **`frontend/`** — React + TypeScript UI; communicates with Go via generated Wails bindings (`frontend/wailsjs/go/desktop/App`).

> **Important layering rule:** `backend/` and `desktop/` import `core` directly. `core/` remains the primary consumer of [sp4rk](https://github.com/v0lka/sp4rk). No convenience re-export layers exist — all types are imported from their source packages. See [`specs/decisions/008-backend-sp4rk-direct-import.md`](specs/decisions/008-backend-sp4rk-direct-import.md).

Detailed system specs live in [`specs/`](specs/) — start with [`specs/INDEX.md`](specs/INDEX.md) to find the right document for your task.

## Frontend stack

- **React 19** + **TypeScript ~5.7** + **Vite 6**
- **Tailwind CSS v4** (One Dark default + One Light override via `data-theme="light"`, both via `@theme` custom properties; toggled by `themeStore`)
- **Zustand 5** for state management. Domain stores cover chat, plans, sessions/projects, file tree/viewer, input mode, blackboard, Git, settings/UI/theme/sound, vector index, goals/reviews, attachments/workdirs, updates, and the per-session terminal registry; see [`AGENTS.md`](AGENTS.md#state-management-zustand-stores) for the current catalog.
- **shadcn/ui** (new-york style) + **Radix UI** primitives
- **lucide-react** icons, **react-markdown** 10, **highlight.js** 11, **Mermaid** 11 (lazy-loaded)
- In-app code/markdown editing via **CodeMirror 6**, embedded terminal via **xterm.js**, virtualized lists via **@tanstack/react-virtual**, character-level diffs via **diff** v9, file-tree icons via Nerd Fonts
- Communication with Go goes through typed wrappers in `frontend/src/api/*` over Wails RPC, plus session-scoped and global events. [`specs/contracts/event-catalog.md`](specs/contracts/event-catalog.md) is the authoritative event catalog.

See the "Frontend architecture" section of [`AGENTS.md`](AGENTS.md) for the full design-system, state-management, and event-handling conventions.

## Requirements

Verified from project configuration and build files:

- **Go 1.26.3** (single root module; `go.mod` at repo root)
- **Node.js + npm** (used by Wails frontend commands and `frontend/package.json` scripts)
- **Wails v2 CLI, matching the version pinned in `go.mod`** (`github.com/wailsapp/wails/v2`) — the CI and release workflows install the same pinned version; `wails build`/`wails dev` are used by the Makefile
- **golangci-lint** (for `make lint`)
- **`git`** — required for CODE mode only; checked on first project switch. CHAT mode (No Project) works without git.
- **`rg` (ripgrep)** — auto-downloaded by the tool-manager on first run; no manual install needed.
- Platform support in the Makefile ONNX fetch logic:
  - macOS (`arm64`, `x86_64`)
  - Linux (`aarch64`, `x64`)
  - Windows (`x64`, via zip runtime artifact path)

## Linux build dependencies

Wails v2 requires native libraries for the WebKit GTK backend.

**Ubuntu/Debian 24.04+:**

```bash
sudo apt install libgtk-3-dev libwebkit2gtk-4.1-dev build-essential pkg-config
```

**Fedora 39+:**

```bash
sudo dnf install gtk3-devel webkit2gtk4.1-devel gcc pkg-config
```

**Arch Linux:**

```bash
sudo pacman -S gtk3 webkit2gtk-4.1 base-devel
```

For CI/headless builds, also install `xvfb` (`sudo apt install xvfb` on Debian/Ubuntu).

> End users running a prebuilt binary only need the runtime shared libraries (`libgtk-3-0`, `libwebkit2gtk-4.1-0`) — see [README.md](README.md#linux-amd64).

## Build from source

### 1) Clone the repository

```bash
git clone https://github.com/v0lka/c0wrk.git c0wrk
cd c0wrk
```

### 2) Prepare frontend dependencies

```bash
make frontend-deps
```

### 3) Create user config

Copy the example config into place:

```bash
mkdir -p ~/.c0wrk
cp config.example.yaml ~/.c0wrk/config.yaml
```

Then edit provider credentials (for example `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, and `TAVILY_API_KEY`) and adjust provider/model settings in `~/.c0wrk/config.yaml`.

## Configuration

Primary config reference: **[`config.example.yaml`](config.example.yaml)**.

Key points:

- Environment placeholders are supported as `${ENV_VAR}`.
- The active LLM provider is resolved from `llm.default_model` — the Router looks up which provider has the model in its enabled `models` list.
- MCP servers are configured under `mcp.servers`.
- Security policy is configured exclusively by capability group under `security.groups`; the legacy `security.default_policy` and `security.tool_policies` keys are inert. The `execute` group alone supports a command blacklist. See [`specs/decisions/024-group-policies.md`](specs/decisions/024-group-policies.md).
- `experimental.enabled` gates both RESEARCH and the Small-LLM profile. `executor.verify_on_edit` is opt-in and runs only a config-authored command through the unattended hard-safety path.
- Main agent calls use `timeouts.llmRequestTimeout`; one-shot title, commit-message, and prompt-optimization calls use `timeouts.serviceLLMRequestTimeout`.
- Runtime limits are configurable under `toolLimits`, `timeouts`, `executor`, and `vector_index`; update checks use the `updates` section.
- The SQLite database is always stored at `~/.c0wrk/database.db` (the `memory.database` config key has been retired).

Runtime config lives at `~/.c0wrk/config.yaml` (default dir constant `config.DefaultAgentDir = ".c0wrk"`). On macOS, `startup.go` calls `config.LoadShellEnvironment()` **before** any other init because Finder-launched apps don't inherit the shell env — preserve this ordering if you touch `Startup`.

## Development commands

Project-level commands (from the `Makefile`):

```bash
make frontend-deps   # npm install in frontend/
make test            # go test ./... && cd frontend && npm test (vitest)
make fmt-check       # fail if gofmt would rewrite any Go source
make lint            # make fmt-check + golangci-lint + frontend ESLint
make dev-desktop     # frontend Vite dev server only
make build           # versioned wails build + ONNX runtime + embedding model
make bump            # update the pinned sp4rk revision with GOWORK=off (release point only)
make clean           # remove build/bin, .cache, frontend/dist
```

Asset/runtime fetch commands:

```bash
make fetch-onnx            # download/copy ONNX Runtime library into app bundle
make fetch-embedding-model # download/copy embedding model + tokenizer into app bundle
make clean-onnx            # remove ONNX runtime libs from app bundle/cache
```

### Focused Go workflows

- Single package (root module): `go test ./core/...`
- Single test: `go test ./core -run TestOrchestrator_PlanExecuteMode -v`
- Tests use in-package style (`package agent`, not `agent_test`); many packages have a `testhelpers_test.go`.

### Cross-repository sp4rk workflow

`c0wrk` remains a single published Go module with no checked-in `go.work` or `replace` directive. During a cross-cutting c0wrk/sp4rk development cycle, a developer may use a gitignored `go.work` at the c0wrk repository root (`use . ../sp4rk`). At the release point, publish the sp4rk change first, then run `make bump` to update `go.mod`/`go.sum` from the remote with `GOWORK=off`; verify the standalone module with `GOWORK=off` before release. See [`specs/decisions/031-gowork-repo-root.md`](specs/decisions/031-gowork-repo-root.md).

### Frontend-only workflows

```bash
cd frontend && npm run lint | build | dev | test
```

Frontend tests use **vitest** (`npm test` / `npm run test:watch`); test files live alongside source (`*.test.ts`).

## Build / Run

### Development

Frontend-only development server:

```bash
make dev-desktop
```

Full desktop hot-reload workflow (from repo root):

```bash
wails dev
```

### Production build

```bash
make build
```

This runs:

1. frontend dependency install,
2. `wails build` with `core/version.Version`, `GitCommit`, and `BuildDate` linker metadata,
3. ONNX runtime fetch/copy,
4. embedding model/tokenizer fetch/copy.

Set `VERSION`, `GITCOMMIT`, or `BUILDDATE` to override the Makefile's git/time-derived defaults for a local build. The release workflow supplies the release tag as `VERSION` on every platform.

### ONNX runtime requirement

The built application needs the platform ONNX Runtime library next to the executable (inside the macOS app bundle, or in `build/bin` on Linux/Windows).

After `make build`, this is handled automatically. If you run `wails build` directly, run this afterward:

```bash
make fetch-onnx
```

Vector index needs ONNX Runtime plus a quantized embedding model + tokenizer (fetched by `make fetch-embedding-model`). The embedder loads asynchronously after `EventBackendReady`; vector search RPCs return empty results until ready.

## Project structure

```text
.
├── desktop/        # Wails app entrypoints, lifecycle, embeds backend.FrontendAPI
├── backend/        # App/view-model layer: config/session/project/persistence/workspace services
├── core/           # Planner/router/reflector/orchestration/tool + MCP wiring
├── frontend/       # React + TS app and generated Wails JS bindings
├── specs/          # System specs: architecture, contracts, domains, decisions (see specs/INDEX.md)
├── config.example.yaml
├── wails.json
└── Makefile
```

## Troubleshooting

- **Config not detected**: ensure the file is exactly at `~/.c0wrk/config.yaml`.
- **App fails after build due to missing ONNX library**: run `make fetch-onnx`.
- **Missing embedding model files**: run `make fetch-embedding-model`.
- **`make dev-desktop` shows only frontend**: this command runs Vite only; use `wails dev` for the full desktop runtime loop.
- **Generated Wails bindings drift** (`frontend/wailsjs/go/desktop/App.*`): regenerate via `wails build` or `wails dev` (do not hand-edit generated files).

## Continuous integration

CI runs on pushes to `main` and pull requests targeting `main` across Linux, macOS, and Windows (see [`.github/workflows/ci.yml`](.github/workflows/ci.yml)). Before opening a PR, run the full local validation sequence:

```bash
make build
make lint
make test
```

All three must pass clean. `make lint` includes `fmt-check`; `make test` runs both Go tests (`go test ./...`) and frontend tests (`cd frontend && npm test` via vitest).

Every contribution must also follow [`SECURITY.md`](SECURITY.md). Treat files, web/MCP output, attachments, clipboard/drop content, and generated artifacts as untrusted; preserve capability-group policy, path/symlink/SSRF/blacklist gates, and never place secrets in source, logs, prompts, facts, or test fixtures. If behavior, a cross-layer interface, configuration, or an architectural invariant changes, update the affected documents under [`specs/`](specs/) according to [`specs/META.md`](specs/META.md); accepted ADRs are immutable and are superseded by a new ADR.

See the "Pre-PR checklist" section of [`AGENTS.md`](AGENTS.md) for implementation conventions.

---

## Releasing

This section describes how to cut a release of **c0wrk**, the artifact inventory published by each release, and how end users install the (currently **unsigned**) desktop builds.

> Signed builds are not available yet. See [Follow-ups](#follow-ups-not-done-in-v1) for the notarization / code-signing roadmap.

### Cutting a release

Releases are produced by the `release.yml` GitHub Actions workflow. It builds the app for all four platforms and publishes a GitHub Release with auto-generated notes and four downloadable artifacts.

**Standard flow: tag and push**

```bash
git tag v0.1.0
git push origin v0.1.0
```

What happens next:

1. Pushing a tag named `v*` triggers the **Release** workflow ([`.github/workflows/release.yml`](.github/workflows/release.yml)).
2. The workflow builds the app for **4 targets** (macOS arm64, Linux amd64/arm64, Windows amd64), injects the release tag plus commit/build time into `core/version`, and bundles the ONNX Runtime shared library and embedding models.
3. It generates a `SHA256SUMS` file for the four archives and publishes a GitHub Release tied to that tag with auto-generated release notes.

Verify the result under **Releases** → your tag, then promote / announce the release URL.

### Manual test run (no tag push)

To exercise the workflow without cutting a real release:

1. Open the repo on GitHub → **Actions** tab.
2. Select the **Release** workflow in the left sidebar.
3. Click **Run workflow**.
4. Enter a `tag_name` (e.g. `v0.1.0-test`) in the prompt.
5. Watch the run complete and review the generated GitHub Release + artifacts.
6. **Delete the generated release** (Releases → the test tag → `Delete`) so it does not appear in the project's public release history. Also delete the test tag if you don't want it to linger.

> A manual run still creates a real (draft-quality) GitHub Release — always clean it up after verifying.

### Artifact inventory

Each release publishes four platform archives plus `SHA256SUMS`. Unzip/extract the archive matching your OS and architecture.

| Filename                              | Target                       | Contents                                                                          |
| ------------------------------------- | ---------------------------- | --------------------------------------------------------------------------------- |
| `c0wrk-desktop-macos-arm64.zip`       | macOS (Apple Silicon, arm64) | `c0wrk-desktop.app` bundle with bundled `libonnxruntime.dylib` + embedding models |
| `c0wrk-desktop-linux-amd64.tar.gz`    | Linux (amd64)                | `c0wrk-desktop` binary + `libonnxruntime.so` + embedding models                   |
| `c0wrk-desktop-linux-arm64.tar.gz`    | Linux (arm64)                | `c0wrk-desktop` binary + `libonnxruntime.so` + embedding models                   |
| `c0wrk-desktop-windows-amd64.zip`     | Windows (amd64)              | `c0wrk-desktop.exe` + `onnxruntime.dll` + embedding models                        |

> The ONNX Runtime shared library and the quantized embedding model + tokenizer are bundled so vector search works out of the box — no extra download step is required on the user's machine. The in-app updater verifies the selected archive fail-closed against `SHA256SUMS`; artifacts are still unsigned, so the checksum establishes release-byte integrity but not authorship if the release account and checksum are both compromised.

End-user installation steps for each platform live in [README.md](README.md).

### Follow-ups (not done in v1)

These items are deliberately **out of scope** for the first release and tracked here for future work:

- **Apple notarization + Developer ID** — produce a notarized, stapled macOS app so Gatekeeper does not block it.
- **Windows code-signing certificate** — sign `c0wrk-desktop.exe` to silence SmartScreen.
- **Universal macOS binary** — ship a single `c0wrk-desktop-macos-universal` artifact covering both arm64 and amd64.
