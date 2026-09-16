# Changelog

All notable changes to **c0wrk**. Dates follow the tag date.

## Unreleased

### Added
- **Vector-index memory program: GC soft limit, content-less index, park budget, live RSS indicator** — the desktop process's multi-gigabyte indexing footprint is attacked on four fronts (see [ADR-049](./specs/decisions/049-vector-index-memory-reduction.md)):
  - **GC soft limit** (`runtime.memory_soft_limit_mb`): default AUTO = clamp(50% physical RAM, 2-8 GiB); `>0` = explicit MiB; `-1` = off. An explicit value beats a `GOMEMLIMIT` env var, which beats AUTO. A `FreeOSMemory` scavenge runs after every full indexing pass, park eviction, and the migration, removing the x2 GC overlay and handing RAM back to the OS.
  - **Content-less index**: documents store ID + embedding + the four chunk-position metadata fields; per-file bookkeeping moves to the file-hash sidecar; chunk text is reconstructed best-effort from source files at search time (a missing file degrades to an explicit placeholder, never an error, and `must_match` excludes it). A one-time background migration per collection is marker-gated, cancel-safe, and replays on the next open, rewriting shrunken per-document gobs in place.
  - **Sidecar v5**: the entry grows a 5th field - the committed chunk-index set - so full-collection enumeration, Browse, and lexical rebuilds no longer materialize every document or spend ONNX inference on enumeration.
  - **Park budget**: project parking is bounded by bytes as well as count (`vector_index.park_budget_mb`, default 1024 MiB). The oldest parked states are evicted - sidecar flushed, RAM freed - while the summed estimated footprints exceed it; the effective bound is `min(park_capacity, park_budget_mb)`.
  - **Live RSS indicator**: a `RSS NNNN MiB` status-bar indicator (5 s polling, exact-bytes tooltip, silent when unavailable) makes the program's effect observable in-app.
- **Linux webview GPU policy override via `C0WRK_WEBVIEW_GPU_POLICY`** — the Linux webview's hardware-acceleration policy (WebKitGTK) is now set explicitly and can be overridden from the environment: `always`, `on-demand` (alias `ondemand`), or `never` (the default, matching the workaround Wails itself applies for [wails#2977](https://github.com/wailsapp/wails/issues/2977)). The mapping compensates for the Go-enum/C-mapping mismatch in the pinned Wails v2.15.0, so the named policy is the one WebKit actually receives; an invalid value logs a warning and falls back to `never`. No effect on macOS/Windows. See `desktop/webview_gpu_policy.go`.

### Changed
- **"Small LLM" / "SLM" renamed to "Model Profiles"** — the tuning-profile feature is renamed across every surface. The old name claimed the models were "small", but the catalog targets 10–35B models (local / mid-size) while the industry reserves "SLM" for sub-10B — so the label mis-described its own audience. The config section becomes `model_profiles:` (`model_profiles.enabled` / `model_profiles.active_profile`), custom profiles move to `~/.c0wrk/model-profiles.yaml`, Go identifiers/packages/files become `ModelProfile*` / `core/modelprofiles` / `model_profiles*.go`, the RPCs become `GetModelProfiles` / `CreateModelProfile` / `UpdateModelProfile` / `DeleteModelProfile` / `SelectModelProfile` / `SetModelProfilesEnabled`, the `agent_metrics` block key becomes `model_profiles` (the frontend still accepts the legacy `slm` / `small_llm` keys on read), and the settings tab reads "Model Profiles". This is a deliberate clean break for the on-disk config and profile store — a legacy `slm:` section and an old `slm-profiles.yaml` are ignored (sanctioned reset migration, as with the earlier `small_llm` → `slm` switch), so custom profiles must be re-created. See [ADR-043](./specs/decisions/043-model-profiles-rename.md).

## v0.8.0 — 2026-09-12

### Added
- **RESEARCH mode is always available and grows a full dashboard** — research is no longer gated behind `experimental.enabled` (the switch now gates only the Small-LLM profile) and a built-in research subagent profile is seeded when the mode is enabled. The dashboard gains root-level project management: switching the active research project, deleting a project's research tree (containment-checked against symlink escapes), and per-project pins for research briefs and hypothesis cards; the next-step recommendation can be scoped to the currently selected hypothesis. Hypothesis cards are fully editable — title, parents, decision, statement, verification criterion, experiment notes join status, timebox, and result — with parent updates validated against the reconciled graph (existence, self-reference, and cycles are rejected, and an invalid update writes nothing). The hypothesis DAG moves to an interactive pan/zoom canvas (drag to pan, cursor-anchored wheel zoom, floating zoom toolbar) with a left-to-right layout whose labels cannot overlap by construction, portal-rendered markdown hover cards, and a resizable bottom card panel; mentioning a hypothesis opens its markdown card as a read-only viewer tab. The research log shows the full uncapped history, and quick actions dispatch into a fresh session when Shift is held. Research writes are crash-safe (content-hash-verified seeding via staged renames, append-only logging, per-root mutation mutex).
- **App-wide UI scale** — a persisted 50–200% scale, configured in a new Appearance settings tab (joined by the theme), is applied as CSS `zoom` on `<html>` before first paint. Because zoom splits screen coordinates into visual and layout spaces, every interaction is compensated: the shared `@floating-ui/dom` platform is patched so popovers, tooltips, and menus anchor correctly at any scale; panel-resize drag deltas are divided by the zoom factor; pan/zoom wheel anchors and fit measurements are corrected; the embedded terminal refits its rows/cols on scale change. Full-height containers size with percentages, viewport-derived sizes go through the zoom-corrected `--ui-vh` primitive, and pointer-anchored panels route through dedicated placement helpers. A project-wide guard test fails the build on raw viewport units, `*-screen` utilities, and positioning fed from raw pointer coordinates. See [UI Scale](./specs/domains/frontend/ui-scale.md).
- **Automatic background git fetch** — the active CODE project's remote-tracking refs stay fresh without user action: a background `git fetch` runs on project switch (app startup rides along), on a periodic ticker, and on window focus, all feeding one server-side gated funnel (`git.auto_fetch` master gate defaulting to on, `git.auto_fetch_interval` ticker period default 2m, `"0"` disables only the ticker, a 60s shared min-interval across triggers, and an instant skip while manual remote operations hold the lock). The fetch carries `GIT_TERMINAL_PROMPT=0` so it fails fast instead of blocking on a credential prompt; failures are Debug-only, and manual Git-panel fetches are never gated. See [git auto-fetch](./specs/domains/git-auto-fetch.md).
- **Faster vector indexing: streaming batches, embedding cache, content filter** — inference batches now fill to the configured ONNX capacity across file boundaries; a content-addressed embedding cache under each project's index (`vector_index.embedding_cache_max_bytes`, default 512 MiB) embeds identical normalized chunks once and reuses them across branches, renames, and re-chunks; and a deterministic content filter (`vector_index.content_filter`) skips generated, minified, and pathological files before chunking, with policy changes re-validating affected files automatically. Recently closed projects keep their vector-index state resident in RAM (`vector_index.park_capacity`, default 3), so returning skips the dominant reopen cost. Backed by a reproducible benchmark suite with quality gates. See [ADR-036](./specs/decisions/036-vector-index-embedding-optimization-policy.md).
- **Manual force-reindex action** — the file-tree explorer header gains a force-reindex button that drives a full pass over the active project's vector index (reconciling the existing index, falling back to a full build when empty), with in-flight spin state and duplicate-RPC suppression; it replaces the stale "Refresh file tree" button. The status-bar progress unit is fixed to a 0–1 fraction (it previously pinned at 100% after ~1% of files).
- **Manual compaction shows per-strategy availability** — the compact menu predicts exactly which strategy will help instead of targeting a token budget: configurable compression-ratio forecast seeds are refined by EWMA calibration after each real compaction, the predictions survive restarts, and availability itself remains an exact verdict.
- **System-driven resume with context** — resuming a paused task settles everything that was paused in one wave before the conductor's first LLM call: unreached plan steps continue through the plan DAG engine, and paused delegates are rebuilt from their persisted specs (a new `task_delegations` table, copied when a session is forked) and relaunched under the same ids, children before parents. The wave appends a factual summary of settled outcomes to the task message, and a duplicate guard in `delegate` replays already-settled results instead of re-running the work. The resumed conversation carries prior session history again (previously dropped), minus the trailing failed exchange so the model never sees a duplicated request, and text typed while paused is sent as a resume nudge instead of being silently discarded.
- **Window title shows the active context** — `c0wrk - Project: session` (or `c0wrk - CHAT: session` in No Project mode), driven from the frontend stores so switches, renames, and deletions are reflected immediately.
- **Update lifecycle inline in About** — download/apply progress and errors render directly in the About settings tab.
- **Per-project panel memory** — the workspace panel tab and the Git panel tab are remembered per project (persisted, cleaned up on project delete) instead of one shared global tab that leaked across projects.
- **Small-LLM essential-tools picker** — the settings dialog offers an always-present tool picker built from the live tool registry, with atomic workflow clusters (plan, subagents) documented by markdown hover tooltips.
- **Smaller additions** — optional line wrapping for mini code editor fields (e.g. the hypothesis RESULT); new `webFetchProxyTimeout` and `webFetchRetries` config knobs where each retry doubles the active timeout; a new transparent-background app icon.

### Improved
- **`make dev-desktop` launches the desktop app** — it previously ran only the Vite dev server: a browser page that cannot leave the startup splash without the Go bridge. It now runs `wails dev` with the correct platform tags (`-tags webkit2_41` on Linux, where a plain `wails dev` fails against webkit2gtk and waits silently without a window); the old Vite-only behavior moved to a new `make dev-frontend` target.
- **Paginated git history** — the unified History tab loads commits one page at a time (default 300, capped at 1000; scroll or "Load more" appends the next page) instead of fetching every commit at once. The two heavy per-repo git subprocesses (`status --porcelain`, `ls-files --ignored`) are memoized and invalidated event-first — on mutating git RPCs, debounced watcher batches, and project switch — so rapid tree refreshes stop respawning git.
- **Atomic project switching** — teardown, watcher, and vector setup all complete before the activation commit, so a late failure leaves the previous project active; the switch-lock wait is bounded by a deadline instead of hanging. The frontend caches each project's UI snapshot (LRU 3) for an instant switch back, and clicking the already-active project reconciles through the idempotent switch RPC.
- **Workspace layout follows git detection** — a git-repo workspace hosts the file explorer as the first "files" section inside the Git panel, while a non-repo workspace keeps the standalone Explorer tab and hides the Git entry point entirely (git-only context-menu actions hidden accordingly).
- **Plan review renders as Markdown** — both the live card and the persisted history rows (previously raw preformatted text, or raw JSON after a reload).
- **Reliable notification sounds** — the audio-context unlock is registered at the App root instead of requiring an active session; resume is awaited and bounded (WebKit's wedged `interrupted` context no longer swallows later cues); gesture listeners persist and a statechange listener attempts recovery; a closed AudioContext is replaced instead of staying silent.
- **Failed sessions are visible** — sessions whose last task failed while the app was closed render a red status dot from the persisted task status (priority pending > failed > active > paused).

### Fixed
- **Linux: the app could start with no window at all** — the window was created hidden and revealed from the backend startup goroutine, whose reveals could be overtaken by Wails' own queued hide (deterministic with a minimal or unparseable config). The window is now created visible, with OnDomReady as an extra guaranteed reveal; the `C0WRK_START_HIDDEN` switch is gone.
- **Terminal starts no longer hang behind session restores** — terminal startup no longer lazily restores the session (a path that queued for minutes on the shared SQLite connection under load): the workspace resolves through a read-only lookup bounded by 10s, the restore path's own reads carry a 15s deadline so contention surfaces as a retryable error, and a failed start marks the frontend shell dead so lazy resurrection retries instead of staying blank.
- **Self-sustaining watcher loop from git reads** — read-only git commands no longer rewrite `.git/index` (`GIT_OPTIONAL_LOCKS=0` pinned at the hardened git chokepoint) and attribute-only Chmod events are skipped, so git-driven refreshes no longer re-fire tree changes ~3×/second and steal text selection; NTFS directory write-echoes reported through parent watches (e.g. reflog appends) are dropped as well.
- **File-tree resilience** — a failed root listing surfaces an error state with Retry (plus manual Refresh) instead of caching a silently empty tree that never self-heals, and the listing degrades to flag-less entries when the repo's ignore set cannot be computed.
- **Startup project stayed unindexed** — the first SwitchProject usually fired before background ONNX init finished, silently skipping vector setup; the skipped project is now recorded and drained immediately after the vector manager is built.
- **Review feedback visible immediately** — submitting review feedback creates the optimistic chat card (covering fresh-task, nudge-resume, and live-interjection dispatches, with rollback on failure) instead of the message staying invisible until a session reload.
- **Live-session coverage after rapid toggles** — switching away from a running session before its status RPC landed no longer flags it idle and drops its sound/pending-action listeners; the switch-time restore is now the sole corrector with fresher-event protection, and the watched set is re-derived from the backend's authoritative snapshot.
- **Default-model updates validated atomically** — invalid provider changes are rejected before committing and roll back failed persistence, so stale model selections or broken settings can no longer reach the API.
- **Windows paths in the project dialog** — the project name derives from the final segment of the OS-native path (`C:\Users\me\proj` → `proj`, previously the whole path), and long paths ellipsize instead of stretching the dialog.
- **Chat polish** — bookmark and plan-step navigation scroll below the floating sticky user-message bar (targets were hidden underneath); bookmark titles clip with a CSS ellipsis at panel width; the programmatic hover state ends accumulating bookmark stars during fast pointer sweeps; the floating file viewer no longer opens and instantly vanishes when launched from a Radix portal menu; submitted-diff refetches preserve state objects when nothing changed, protecting in-progress text selection.
- **Research panel robustness** — stale incremental updates converge via a research-scoped watchdog and an App-root-mounted sync bridge; workspace view state (hypothesis selection, unsaved drafts, filter, sidebar width) survives remounts and cannot rebind across research-project switches; the card-panel divider follows the pointer and arrow keys; the DAG layout box hugs painted label extents so left-hanging ids are visible and fit() centers the actual graph; pointer capture engages only after the drag threshold so plain node clicks work; the research log scrolls inside its own block.
- **Data races** — the composite trajectory store's writer lifecycle is serialized (Flush could deadlock against concurrent Sync); the gitconfig risk recorder's event list is guarded; macOS clipboard tests run against a private pasteboard instead of the user's system clipboard.
- **Config re-sync** — every persisted config mutation emits a `config:updated` event, and the experimental-features gate no longer stays unknown until an app restart after a startup race.

### Security
- **Git subprocess hardening against untrusted repository config** ([ADR-033](./specs/decisions/033-git-subprocess-hardening.md)) — a repository is treated as attacker-controlled data: a hostile `.git/config` can make git execute hooks, filters, fsmonitor daemons, merge drivers, and editors during ordinary (even read-only) operations. Four cooperating layers: a global baseline on every git process (fsmonitor off, an empty safe `core.hooksPath`, `commit.gpgsign=false`, `GIT_EDITOR=true`); per-repo neutralization that re-scans the discovered config fresh on every invocation and fails closed when it cannot be scanned; intake detection emitting `project:git_config_risk` as a dismissable warning on project switch and work-directory add; and an agent-side write gate (`git_internal_path` as a canonical hard judge reason) that always confirms mutating file tools targeting a workspace `.git` tree. A review pass closed the remaining gaps: transport keys reachable from remote operations (`core.sshCommand`, `core.askPass`, credential helpers, `core.gitProxy`), external diff drivers (`--no-ext-diff`), `core.worktree` redirects, attribute-routing sources `attr.tree` cannot cover, linked-worktree common config, SHA-256 repos, include-bearing configs on git < 2.45 (fail closed), a Stat-to-Open TOCTOU, and `GIT_ATTR_*` environment stripping — while narrowing the `attr.tree` kill so CRLF-normalized repositories no longer report falsely-modified files. Hooks and filters, including legitimate git-lfs, never run inside c0wrk unless the repository is explicitly trusted.
- **Trusted git repositories with snapshot-bound trust** ([ADR-034](./specs/decisions/034-git-trust-opt-out.md)) — `security.trusted_git_repos` records repository roots the user explicitly trusted (via the warning toast's "Trust this repo" action, which also offers "Ignore" and an agent-task "Fix", or via Settings → Security → Trusted repos). Trusting a repository opts it back into its own git configuration — hooks, filters, and signing run as they would outside c0wrk — but the trust is bound to a fingerprint of the scanned config: any drift evicts the trust, re-hardens the repository, and re-warns with a reason and a diff. `security.harden_git_repos` is the mutually exclusive inverse for roots that must always be hardened.
- **Validated command-substitution exception** ([ADR-038](./specs/decisions/038-validated-command-substitution.md)) — the safety judge now promotes a pure `VAR=$(...)` assignment whose inner command is fully assessable to assessable, ending a class of false-positive confirmations. Bare argument-position substitutions, unbound references such as `$HOME`, and any name poisoned by a literal or non-assessable binding stay suspicious via a fail-closed union over all bindings; bash and PowerShell parity rules are documented, with regression tests driving the real judge.
- **Reproducible CI dependency installs** — `frontend/package-lock.json` is committed (previously gitignored), CI and release workflows switched to `npm ci` on Node 24 with lockfile-keyed caching, ending flaky live resolution of floating ranges.

### Internal
- Go toolchain bumped to 1.27.1 (`go.mod` and CI pins in lockstep), with several sp4rk SDK bumps across the cycle (notably: delegation-spec persistence, per-strategy compaction predictions, fixed-shape persistent session defaults, and the six-label tool-description rubric).
- Small-LLM: the `max_tools` budget and semantic router tool matching are removed in favor of a static union of always-present pins, protected tools, MCP tools, and turn-scoped guarantees — `SelectTools` no longer trims or matches ([ADR-035](./specs/decisions/035-remove-small-llm-tool-budget.md)); requested `#subagents` keep the `delegate` tool under narrowing, and the lite system prompt retains essential operational guidance.
- Subagent Profiles gain a `skills:` frontmatter list: required skills resolve fail-closed against the skill catalog, are activated for the delegated subagent, and are inherited on redelegation ([ADR-037](./specs/decisions/037-agent-profile-skills.md)).
- Tests: Windows-compatible temp-dir/handle cleanup ordering and a cross-platform unreadable-file helper; startup tests use explicit benchmarks instead of timing budgets; tool-description tests enforce all six rubric labels per line; an integration test pins the sound-listener coverage invariant across CHAT↔CODE toggles.
- CI: Windows jobs disable HTTP/2 for Go module downloads to dodge intermittent proxy.golang.org stream resets.
- New specs: [ui-scale](./specs/domains/frontend/ui-scale.md), [sound-notifications](./specs/domains/frontend/sound-notifications.md), [git-auto-fetch](./specs/domains/git-auto-fetch.md), and ADRs 033–038.

## v0.7.3 — 2026-09-02

### Added
- **Persistent chat-event bookmarks** — every chat item gains a bookmark star in its left gutter: an outline star revealed on hover (muted, like Copy/Save) that fills highlight-yellow once set, toggling a session-scoped bookmark persisted in SQLite; the sticky pinned user message renders its star inside the floating row. A collapsible Bookmarks panel above the message input lists the active session's bookmarks — each row shows a shortened title with inline rename and delete (mirroring session-list item actions), hovering previews the bookmarked event exactly as it renders in chat (without a nested star), and clicking a row smooth-scrolls the chat to the event. A bookmark's identity is the stable `DisplayItem` key produced by `groupMessages`, so navigation and preview survive history reloads and map back to the rendered item; a per-session unique constraint allows one bookmark per event, the table cascade-deletes with its session, and adding an already-bookmarked event is an upsert. Backend RPCs (add/list/delete/rename) are scoped by session — a bookmark id can never read, rename, or delete another session's bookmark — and the frontend store is keyed per session with optimistic adds and cleanup when a session is deleted.
- **Cross-project live-sessions indicator** — a Radar button in the sidebar header carries an overlapping status-dot cluster (red failed → yellow awaiting you → green running → gray paused) summarizing what is live across ALL projects, and is disabled and dotless when nothing anywhere is live. Clicking opens a dropdown listing every live session of every project — pending/failed first, each row a status dot, mode icon, truncated name and relative activity — as a pure navigation surface: jump straight to a running, paused, or blocked task, switching project when needed; no management actions here. Backend support: `ListAllSessions` returns all projects in one list (pinned first, then effective activity, archived sessions dropped, live in-memory state overlaid), and the session-list queries now also report the most recent unfinished task's machine status (`in_progress` / `paused` / `failed`, optional and backward-compatible on the wire), so the UI can tell a failed task from a running or paused one — including after a restart, where live flags are gone. The indicator's snapshot store refreshes debounced and, on dropdown open, does an immediate refresh plus a `GetPendingActions` sweep so sessions blocked on a confirmation surface instantly; live execution state stays in `chatStore` (single source of truth — no dual bookkeeping) while pure, unit-tested helpers fold per-session statuses (pending > failed > active > paused) into the badge flags. Below a 228px header width the button yields its layout slot via a Tailwind container query, so the sidebar header renders exactly as before at the 180px minimum.

### Fixed
- **Cached and batched tool cards showed a bare tool name instead of the real file** — cached (`tool_result_read`) and batched sub-call cards titled themselves with the bare tool name rather than the actual file or URL. Every card variant now extracts its title from the call's own args: batched sub-calls already carried theirs, and a cached read resolves back to the original tool — the executor rewrites the emitted event with that tool's merged args, with the fragment window overlaid — so cached cards show real titles, file links, and the fragment-range note too. Cards persisted before that rewrite keep hash-only args and fall back to the extractor's generic placeholder (`file`, `URL`, …), never to a bare tool name. The MCP/cached/batched badges and the fragment-range note are `shrink-0` + `whitespace-nowrap`, so when the header row runs out of room the title is what truncates and the banners stay fully visible. Covered by a new vitest suite exercising every card variant.

### Internal
- Dependency updates (sp4rk SDK): the pinned commit now includes the memoized per-directory case-sensitivity probes that the security-model spec described under v0.7.2, plus cached tool-result reads that resolve the original tool's name and merged args — the backend half of the tool-card title fix.

## v0.7.2 — 2026-09-01

### Fixed
- **User messages reordered to the end of history after reload** — `SendMessage` persisted user messages with a local-time RFC3339 string (offset suffix like `+03:00`) while every other writer stored UTC `Z` strings, and `LoadMessages` orders by lexicographic comparison of `created_at` — so in timezones east of UTC every user row sorted after all other rows of the same chat, rendering all user messages at the end of the history (persisted, so a restart did not help). User-message timestamps are now written in UTC, and an idempotent startup migration rewrites legacy offset-suffixed rows to canonical UTC RFC3339 via `strftime`, healing already-corrupted history; rows already canonical or unparseable are left untouched.

### Internal
- The security-model spec now documents that the SDK's `pathutil.DetectCaseInsensitive` memoizes its result per probed directory for the process lifetime, so callers outside the session Manager (the file-tree ignore resolver, the vector indexer) create the case-sensitivity probe file at most once per root per app run.

## v0.7.1 — 2026-09-01

### Added
- **Session and project context restore on restart** — the last open session is persisted at selection time and restored saved-first on the next start (falling back to the latest non-archived session by effective activity), the last active project — including No Project — is remembered in a new `app_state` store and reopened exactly, session lists order by effective activity (the newest persisted chat message or terminal command, not just the last send), and archived sessions are never auto-selected. See [ADR-030](./specs/decisions/030-session-context-restore.md).
- **Continuable plan resume across pauses** — a plan step or subagent stopped at a cooperative pause checkpoint now emits `plan_step_paused` / `subagent_paused` — persisted, so the paused block reappears after a restart — instead of surfacing as a failure, and the UI shows a "paused" badge on the step. Resuming a task whose approved plan still has unreached steps seeds a continuable run: `execute_plan` continues the remaining steps without a re-declare, `declare_plan` returns a soft "already approved" hint, and steps that already succeeded in a previous run are replayed as successes.
- **Save message as Markdown** — holding Shift turns a chat message's copy button into a Save action that opens a native save-file dialog (defaulting to the active project's directory), normalizes the chosen name to `.md`, and fails closed when the normalized name already exists on disk.
- **Offline-first tool reconciliation at startup** — a failed download of a managed tool (`rg`, `uv`, `markitdown`) no longer aborts startup with a fatal dialog: strictly local work (version probes, installs from SHA256-verified cached archives) runs synchronously, network retries continue in the background, and remaining failures surface once as a `runtime_error` toast. Stale binaries and Python envs are removed only after replacement bytes are secured, so a failed offline reinstall never leaves the machine with less than it had; markitdown now installs from a fully pinned embedded requirements lock, and ONNX Runtime is bumped to 1.28.1.

### Fixed
- **Updater ignored short release tags** — tags lacking minor/patch components (e.g. `v0.7-beta`) were rejected by semver parsing, so the in-app updater never offered newer releases; numeric tags are now padded to the strict `vMAJOR.MINOR.PATCH` form.
- **Vector-index panics on legacy-encoded files** — non-UTF-8 content is sanitized to U+FFFD before chunking (the tokenizer panicked on it, and legacy single-byte encodings passed the binary check). A failing embedding chunk is retried per text and only genuinely pathological texts are dropped — from both the vector and lexical indexes, so the two never diverge.
- **macOS crash after ONNX fetch** — rewriting the signed onnxruntime dylib's install name invalidated its code signature, and macOS SIGKILLed the process at `dlopen` (CODESIGNING Invalid Page). The installed copy is now byte-identical to the SHA256-verified cache.

### Security
- **Supply-chain gates** — `make vulncheck` (govulncheck, version pinned in the Makefile) is a mandatory pre-PR step and the CI `security` job runs the identical command, so local and CI vulnerability results cannot drift. CI also gains an npm audit gate and PowerShell syntax checks, and the ONNX Runtime / embedding-model / tokenizer fetches are SHA256-verified fail-closed in the Makefile and fetch scripts.

### Internal
- Go toolchain bumped to 1.27.0 (`go.mod` and CI go-version pins in lockstep), golangci-lint to v2.13.2.
- Dependency updates (sp4rk SDK): cooperative pause checkpoints with continuable plan resume.
- The dual-repo `go.work` moved from the shared parent directory to a gitignored file at the repository root (`use . ../sp4rk`), so sibling Go checkouts are no longer silently pulled into the c0wrk/sp4rk workspace. See [ADR-031](./specs/decisions/031-gowork-repo-root.md).

## v0.7 — 2026-08-30

### Added
- **Linux arm64 build** — releases now ship a `c0wrk-desktop-linux-arm64.tar.gz` archive alongside macOS, Linux amd64, and Windows, and the in-app updater recognizes the new platform. See [ADR-027](./specs/decisions/027-linux-arm64-build.md).
- **Manual context compaction** — a status-bar button compacts the session's LLM-visible history on demand, with a choice of strategy (sliding window, summarization, hierarchical). A running task is paused and auto-resumed around the compaction, the compacted snapshot persists and reseeds the history on reload, and a token-budget mode trims to a configurable target fill (`executor.compaction.manualTargetPercent`, default 30%). When compaction would change nothing, the button is disabled with an explanation.
- **Vision-assisted document conversion** — when the active model is vision-capable, document attachments convert through an embedded markitdown driver that captions embedded images with that model: pptx pictures, pdf illustrations (extracted via pdfminer/Pillow), and docx/html/epub data URIs. Every unique image is captioned, and conversion degrades gracefully to the plain CLI path under a shared per-file deadline.
- **Crash and exit evidence on disk** — stdout/stderr are mirrored into an append-only, size-rotated `stderr.log` so panics and native errors survive Finder-launched runs, and a liveness marker distinguishes clean quits from crashes and surfaces an unclean-shutdown warning on the next start. Opt out with `C0WRK_DISABLE_CRASH_CAPTURE=1`. See [crash logging](./specs/domains/crash-logging.md).
- **Quit confirmation with live work** — quitting while a session has live work (a running task or a compaction) is intercepted and asks for confirmation; paused tasks stay resumable and do not block quitting, and an updater-driven quit presents restart context.
- **Terminal environment configuration** — every embedded terminal shell gets `TERM_PROGRAM=c0wrk` so rc files can detect the in-app terminal and skip behaviors like tmux auto-attach, and a new `terminal.env` config section defines extra environment variables with `${VAR}` expansion. See [ADR-029](./specs/decisions/029-terminal-env-conventions.md).
- **Vector-index tuning knobs** — `vector_index` gains `embedding_batch_size`, `prep_workers`, `debounce_ms`, `chunk_overlap`, and `search_wait_timeout_ms`, all defaulting to the historical values. Indexing is faster (batched embedding, parallel file preparation, a stat-based skip for unchanged files), search readiness waits are bounded instead of blocking on a stuck index, and chunker-config changes re-index affected files automatically.
- **Session-pinned safety judge** — each session's Smart Approve judge is bound to that session's own provider/model instead of the global default, so a default-model change can no longer strand a session's judge on a foreign or unreachable provider (which fail-safed into confirmation floods). See [ADR-028](./specs/decisions/028-session-pinned-judge.md).
- **Small-LLM profile retune** — a new `presence_penalty` sampling knob (validated [0, 2], inherit-by-default), an unset `reasoning_effort` now seeds "medium" (cutting thinking-token spend 60–90% on qwen thinking models), and the context variant's output reserve default rises to 16384. Every default is backed by an [external-evidence review](./docs/development/model-profiles-defaults-research.md).
- **Per-session input state** — chat drafts, staged attachments, and optimize/send errors are keyed by session, and the git commit box by project: text typed in one session no longer vanishes — or lands in another — on a switch.
- **Optimistic attachment chips** — staging an attachment (picker, drop, paste) shows a cancellable spinner chip immediately instead of waiting silently for the backend conversion.
- **Themable settings comboboxes** — native `<select>` popups are replaced by a design-token combobox that renders correctly on every platform and inside modal dialogs.
- **Honest activity status** — the status bar reports "Safety judge evaluating..." while Smart Approve runs and "Awaiting confirmation..." while the agent blocks on a decision; collapsed sticky messages show an icon when they are live interjections.
- **Friendlier dialogs** — native directory pickers reopen at the last chosen directory, and truncated file names in the Git panel show a full-path tooltip.

### Improved
- No Project (CHAT) mode is no longer over-restricted: code-flavored questions route honestly, `ripgrep`/`glob` are re-enabled, shell commands follow the normal `security.groups` execute policy instead of a hard-coded blacklist, and the real home directory is shown in the environment block.
- Plan-first agent behavior: conductor guidance defaults `declare_plan` to `await_approval`, so the user signs off on a roadmap before implementation.
- The chat toolbar's model/reasoning/goal/budget selectors lock while a task runs, pauses, or compacts.

### Fixed
- Project switches: eliminated a frontend/backend desync that made the file tree reject its own root ("path outside project workspace") and emptied @-completions until an app restart; switches are now serialized, and the completion root is memoized per session.
- Session state: stale task status across resume and terminal events, frozen activity/streaming indicators on session switch, and tasks orphaned by early continuation failures.
- Settings: deduplicated model lists and kept enabled models visible; configured MCP servers that failed to start no longer vanish from the status list.
- Chat rendering: long tool arguments wrap instead of clipping behind a scrollbar, and the autocomplete tooltip renders in a body-level container.
- Windows: environment variables fold case-insensitively (`Path` vs `path`), so `terminal.env` overrides and built-ins can no longer duplicate inherited entries with undefined lookup order.
- Delegation results return in registration order; the status bar no longer allows text selection.

### Security
- **Unified Smart Approve funnel with a hard-reason backstop** ([ADR-026](./specs/decisions/026-smart-approve-unified-funnel.md)). Every escalated call — whether from an effective `user_confirm` policy or a hard safety reason surfaced by an allow-group tool — now goes through the strict judge; there is no separate bypass path. A deterministic backstop overrides a strict ALLOW to CONFIRM for canonical hard reasons (command blacklist, SSRF private address, symlink escape, degraded SSRF protection, unassessable URL/path), matched by typed reason codes rather than prose — a fired security control or a structurally unassessable input always reaches the user. `deny` groups and workspace auto-approval are unchanged.

### Internal
- Wails upgraded from v2.12.0 to v2.15.0.
- Dependency updates (sp4rk SDK): token-budget compaction with no-op prediction, typed judge reason codes, session-scoped judge binding.
- CI builds and tests Linux on amd64 and arm64, and workflows can be triggered manually.
- The self-hosted models guide moved into the [llm-providers spec](./specs/domains/llm-providers.md); specs synced with the code and new ADRs recorded (026–029).

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
