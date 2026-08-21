# Changelog

All notable changes to **c0wrk**. Dates follow the tag date.

## v0.6 — 2026-08-21

### Added
- **In-app self-update** — check for newer GitHub releases, download, SHA256-verify, and apply updates without leaving the app. The pipeline (`core/updater/`) uses a two-process re-exec (`--self-update`) so the running binary is never overwritten while executing: a staged updater waits for the parent to exit, atomically swaps the install tree (keeping a `.old` backup for manual rollback), and relaunches. Verification is fail-closed SHA256 against the release `SHA256SUMS`; unsafe install locations (temp/Downloads/read-only) are rejected. See [ADR-023](./specs/decisions/023-auto-update.md).
- **File attachments** — paste images and files from the clipboard or drag-and-drop them onto the input; documents are converted to markdown, images are gated by vision-model support, and attachment metadata survives reloads.
- **RESEARCH mode (experimental)** — a workspace-contained methodology workspace (research briefs, prior art, a hypothesis DAG, progress metrics and synthesis reports) with recursive file watching that keeps the graph in sync. Gated by the new experimental master switch.
- **Universal session pause/resume** — pause any running task (not just goal mode) and resume it later; subagents cooperate with the pause, and unfinished tasks are checkpointed as paused on shutdown.
- **Live interjections** — send a follow-up message while a task is running; it is delivered at the next step boundary.
- **Resumable plan execution** — `execute_plan` can resume after a failure and re-run selected steps (with their dependents) instead of restarting the whole plan.
- **File-viewer pin/unpin** — pin the viewer into a docked column or float it as an overlay, with persisted width/collapse/pin state.
- **Interactive Mermaid diagrams** — a pan/zoom canvas for rendered diagrams.
- **"Find similar"** — search the vector index for files similar to the selected text from the file-viewer context menu.
- **Sound notifications** — synthesized cues for session lifecycle events, including background sessions.
- **Verify-on-edit** — a user-configured command (tests/linter/build) runs after each successful file edit in CODE tasks and its output is fed back to the agent. See [verify-on-edit](./specs/domains/verify-on-edit.md).
- **Agent metrics** — per-run quality counters (parse errors, invalid tool calls, loop-detector nudges/aborts, steps, tokens) surfaced via an opt-in session-stats row.
- **Git branch management** — rename, delete, and remote operations (push, checkout and delete remote branches) from the Git panel.
- **Window geometry persistence** — window size/position and sidebar width survive restarts.
- **Per-session terminals** — PTYs stay alive across session switches and stop only on deletion or shutdown.
- **Blackboard & step-output search** — search stored facts and load step outputs on demand.
- **Auto-discovered work directories** — directories mentioned in the prompt are added as working directories.
- **Archive/delete busy sessions** — archive or delete sessions that are still running, with confirmation.
- **Small-LLM context overrides** — context-management overrides (e.g. compaction thresholds) added to the Small-LLM profile.
- **Smart Approve** — an optional strict judge (OWASP ASI policy) that resolves pending tool confirmations automatically, cutting down on confirmation prompts.

### Improved
- Non-blocking config reads: `GetConfig` is network-free and stays responsive while saves are in progress.
- Broader built-in model catalog (Kimi K2.7-code, Qwen 3.8) with smarter model-ID normalization and offline-friendly resolution.
- One-shot service calls (titles, commit messages, prompt optimization) now use a dedicated timeout and purpose-aware sampling.
- The vector index skips oversized files and caps chunks per file to bound memory.

### Fixed
- Preserved pending input and session state across restarts.
- Resolved relative file links against the correct workspace.
- Kept the floating viewer open (and prevented its collapse) during popover interactions.
- Restored @-file completions after a transient empty listing.
- Kept Mermaid labels intact through HTML sanitization.
- Deduplicated goal-proposal cards on session reload.
- Cleaned up cancellations of paused, unfinished tasks.

### Security
- **Per-tool security policies replaced by capability groups** ([ADR-024](./specs/decisions/024-group-policies.md)). `security.tool_policies` and `security.default_policy` are **removed**; policy is configured exclusively via `security.groups.<group>.policy` (`allow` / `user_confirm` / `deny`) for the seven capability groups (`execute`, `local_read`, `local_write`, `remote_read`, `remote_write`, `local_mcp`, `remote_mcp`; the reserved `system` group is never configurable). The per-tool shell blacklist is unified into `security.groups.execute.blacklist`. A config file that still carries the legacy keys loads them inert — **no automatic migration exists**: the next settings Save silently drops them. Convert by hand: merge `tool_policies.<tool>.blacklist` entries (`bash_exec` and `posh_exec` unify) into `groups.execute.blacklist`, and map each `tool_policies.<tool>.policy` to the policy of the tool's owning group (see the group table in ADR-024). Unconfigured groups keep the safe defaults (reads `allow`, mutations `user_confirm`).
- Documented auto-update as a supply-chain attack surface in [SECURITY.md](./SECURITY.md) (ASI04): unsigned/SHA256-only trade-off recorded as an accepted risk; added a rule forbidding weakening the self-update integrity gate.
- Added the [SECURITY.md](./SECURITY.md) threat model and secure-coding policy; hardened tool execution against prompt injection and secret leakage; narrowed the shell and git blacklists to destructive operations only.

### Internal
- Dependency updates (sp4rk SDK): cooperative pause/resume, capability groups, the strict confirmation judge, purpose-aware sampling, and model-registry additions.

## v0.5.2 — 2026-08-06

### Added
- Regenerate commit message with result validation.

### Improved
- Custom scrollbars in scrollable UI areas.

### Fixed
- Raised the output-limit headroom for local models (32 768) and guarded against invalid limits.
- Settings window freezing while starting MCP gateways.

## v0.5.1 — 2026-08-03

### Internal
- Dependency updates (sp4rk SDK).

## v0.5.0 — 2026-08-03

### Added
- **Small/local-model profile** — a manual set of optimizations (tool-set narrowing, simplified system prompt, sampling override, loop hardening) for running on weaker models.

### Improved
- Automatic Google-protocol detection for local OpenAI-compatible servers.

### Fixed
- Correct update of the tools' Python environment on version change (uv 0.7.0 → 0.12.1, markitdown 0.1.1 → 0.1.4).

## v0.4.1 — 2026-08-02

### Added
- **Persistent model-metadata overrides** — manual configuration of tokenizer type, family, protocol, and capabilities per model via the config dialog.

### Fixed
- Removed the hidden move limit for goals without a cap.
- Use the native Wails clipboard instead of `navigator.clipboard`.

## v0.4.0 — 2026-08-01

### Added
- **Subagents** — specialized agent profiles invokable via `#mention`.
- **Image attachments** with vision-model support and auto-gating.
- **Goal verification** — a standalone verification mode with an isolated verifier and checkpoint evidence.
- **Git tag management** and reset-to-commit via the history context menu.
- `AGENTS.md` injection from multiple sources (project + working directories).
- Flat session list for chat mode with a shared mutable panel.
- Context menu for file-viewer tabs.
- Embedding of local images and opening of external links in the browser.
- Tooltip for truncated tool-card titles.

### Improved
- Asynchronous initialization (environment info, vector index, MCP, LM Studio) — faster startup.

### Fixed
- Terminal file-descriptor leaks, UTF-8 truncation, startup panics.
- Cleanup of all session files on deletion.
- Reset of a "dangling" default provider/model when it is deleted.
- Hiding console windows of spawned processes (Windows).

## v0.3.1 — 2026-07-24

### Added
- **Split-view diff mode** with a toggle.
- File-level comments synced with the working tree.

### Fixed
- Spurious "resume" badge on successful task completion.
- localStorage being overwritten by stale backend tabs on startup.

## v0.3 — 2026-07-21

### Added
- **Light theme** with persisted preference and FOUC protection.
- `embedding_threads` setting for the vector index.

### Fixed
- Default to code-first mode; project-creation dialog at the app root.

## v0.2 — 2026-07-21

### Added
- **Session pinning** (pin/unpin).
- Auto-detection and caching of the LM Studio context size on startup.
- Prev/next hunk navigation in review mode.

### Fixed
- Tool-manager launch on Windows (archive format, binary lookup).

## v0.1 — 2026-07-20

Initial pre alpha release.
