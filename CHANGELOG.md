# Changelog

All notable changes to **c0wrk**. Dates follow the tag date.

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
