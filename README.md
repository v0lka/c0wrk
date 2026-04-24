# c0wrk

Desktop AI coding agent built with **Wails v2** (Go backend + React/TypeScript frontend).

This repository contains a single Go module (`github.com/user/agent` — this path is intentional, not `c0wrk`; do not "fix" it) with a desktop app that orchestrates planning/execution loops, tool calls, local project access, and a multi-panel UI for agent workflows.

## Overview

`c0wrk` is a local-first desktop application for running an AI coding agent with:
- a Go backend for orchestration, config, persistence, and runtime services,
- a React frontend for chat, plans, file tree, and file viewer,
- Wails bindings between UI and backend methods.

The desktop binary name is **`c0wrk-desktop`**.

## Features

- ReAct-style agent execution with planner/router/reflector flow
- Desktop UI with chat, execution panels, workspace tree, and file viewer
- Tool execution with configurable security policies (`user_confirm`, allow/deny)
- MCP server integration (stdio and HTTP transports)
- SQLite persistence (CGO-free `modernc.org/sqlite`)
- Configurable LLM providers (Anthropic, Gemini, LM Studio, OpenAI-compatible, ChatGPT)
- Configurable runtime limits (timeouts, tool output limits, compaction thresholds)

## Architecture

High-level layers and responsibilities:

- **`desktop/`** — Wails app lifecycle and frontend-exposed API methods (`*desktop.App` methods split by domain in `api_*.go`).
- **`backend/`** — application/view-model layer: config loading, session/project management, persistence wiring, installer/watcher behavior.
- **`core/`** — orchestration logic: planner, router, reflector, tool registry, MCP gateway, security policy application.
- **`sdk/`** — reusable engine components: agent executor, LLM providers, memory/compaction, prompt/tool primitives.
- **`frontend/`** — React + TypeScript UI; communicates with Go via generated Wails bindings (`frontend/wailsjs/go/desktop/App`).

> Important layering rule: SDK usage is routed through `core/`; `backend/` wraps `core`.

### Frontend Stack

- **React 19** + **TypeScript ~5.7** + **Vite 6**
- **Tailwind CSS v4** (One Dark theme via `@theme` custom properties)
- **Zustand 5** for state management (9 domain stores: chat, panels, sessions, projects, file tree, file viewer, scroll, settings, UI)
- **shadcn/ui** (new-york style) + **Radix UI** primitives
- **lucide-react** icons, **react-markdown** 10, **highlight.js** 11, **Mermaid** 11 (lazy-loaded)
- Communication with Go via Wails-generated RPC bindings + session-scoped events (25+ event types)

## Requirements

Verified from project configuration and build files:

- **Go 1.26.1**
- **Node.js + npm** (used by Wails frontend commands and `frontend/package.json` scripts)
- **Wails v2 CLI** (`wails build`, `wails dev` are used by Makefile)
- **golangci-lint** (for `make lint`)
- Platform support in Makefile ONNX fetch logic:
  - macOS (`arm64`, `x86_64`)
  - Linux (`aarch64`, `x64`)
  - Windows (`x64`, via zip runtime artifact path)

## Installation

### 1) Clone repository

```bash
git clone <your-fork-or-repo-url> c0wrk
cd c0wrk
```

### 2) Prepare frontend dependencies

```bash
make frontend-deps
```

### 3) Create user config

Copy the example config and place your runtime config at:

- **`~/.c0wrk/config.yaml`**

Example setup:

```bash
mkdir -p ~/.c0wrk
cp config.example.yaml ~/.c0wrk/config.yaml
```

Then edit provider credentials (for example `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GEMINI_API_KEY`, `TAVILY_API_KEY`) and adjust provider/model settings in `~/.c0wrk/config.yaml`.

## Configuration

Primary config reference: **`config.example.yaml`**.

Key points:

- Environment placeholders are supported as `${ENV_VAR}`.
- Active LLM provider is selected via `llm.active_provider`.
- MCP servers are configured under `mcp.servers`.
- Security defaults and per-tool policies are configured under `security`.
- Runtime limits are configurable under `toolLimits`, `timeouts`, and `executor`.
- Database path defaults to the app directory when `memory.database` is empty (commented in sample config as `${c0wrk}/database.db`).

## Development Commands

Project-level commands (from `Makefile`):

```bash
make frontend-deps   # npm install in frontend/
make test            # go test ./... && cd frontend && npm test
make lint            # golangci-lint run && cd frontend && npm run lint
make dev-desktop     # frontend Vite dev server only
make build           # wails build + fetch ONNX runtime + fetch embedding model
make clean           # remove build/bin, .cache, frontend/dist
```

Asset/runtime fetch commands:

```bash
make fetch-onnx            # download/copy ONNX Runtime library into app bundle
make fetch-embedding-model # download/copy embedding model + tokenizer into app bundle
make clean-onnx            # remove ONNX runtime libs from app bundle/cache
```

## Build / Run

### Development

- Frontend-only development server:

```bash
make dev-desktop
```

- Full desktop hot-reload workflow (from repo root):

```bash
wails dev
```

### Production build

```bash
make build
```

This runs:
1. frontend dependency install,
2. `wails build`,
3. ONNX runtime fetch/copy,
4. embedding model/tokenizer fetch/copy.

### ONNX runtime requirement

The app bundle needs the ONNX runtime library in `build/bin/c0wrk-desktop.app/Contents/MacOS/`.

After `make build`, this is handled automatically. If you run `wails build` directly, run this afterward:

```bash
make fetch-onnx
```

## Project Structure

```text
.
├── desktop/        # Wails app entrypoints, lifecycle, and API methods exposed to frontend
├── backend/        # App/view-model layer: config/session/project/persistence/workspace services
├── core/           # Planner/router/reflector/orchestration/tool + MCP wiring
├── sdk/            # Reusable agent engine (LLM, tools, memory, execution primitives)
├── frontend/       # React + TS app and generated Wails JS bindings
├── config.example.yaml
├── wails.json
└── Makefile
```

## Troubleshooting / Notes

- **Config not detected**: ensure file is exactly at `~/.c0wrk/config.yaml`.
- **App fails after build due to missing ONNX library**: run `make fetch-onnx`.
- **Missing embedding files in app bundle**: run `make fetch-embedding-model`.
- **`make dev-desktop` shows only frontend**: this command runs Vite only; use `wails dev` for full desktop runtime loop.
- **Generated Wails bindings drift** (`frontend/wailsjs/go/desktop/App.*`): regenerate via `wails build` or `wails dev` (do not hand-edit generated files).

---

## Contributing

**CI is not configured in this repo** — local verification is the gate. Before opening a PR, run the full validation sequence:

```bash
make build
make lint
make test
```

All three must pass clean. There is no frontend test suite; `make test` covers only Go tests.

## License

Licensed under the [MIT License](LICENSE) — see [LICENSE](LICENSE) for details.

## About

Built by c0wrk team with warmth, love, and c0wrk.
