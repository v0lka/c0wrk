# Workspace

## Purpose

Manages the project workspace: file tree loading, filesystem watching for changes, git status integration, and vector index for semantic code search.

## Key Files

- `core/workspace/watcher.go` — filesystem watcher (fsnotify)
- `core/workspace/filetree.go` — file tree building logic (lazy listing with git ignores)
- `core/workspace/git.go` — git CLI wrappers (status, diff, gitignore parsing, branch detection) + the hardened repo-scoped spawn helper `GitCmdInRepo`
- `core/workspace/gitconfig.go` — exec-free `.git/config` scanner (`ScanGitConfig`): dangerous-key findings, include recording, per-repo neutralizing `-c` set
- `internal/sysproc/git.go` — hardened git spawn choke point (`GitCmd`): global baseline overrides on every git process
- `backend/frontend_api_gitconfig_risk.go` — intake scan + global `project:git_config_risk` warning event (`notifyGitConfigRisk`)
- `backend/frontend_api_workspace.go` — FrontendAPI workspace methods (thin delegation to core/workspace)
- `core/vectorindex/git.go` — git CLI wrapper for branch detection and branch-change monitoring
- `core/vectorindex/manager.go` — vector index lifecycle (branch-partitioned collections)
- `core/vectorindex/service.go` — vector index indexing and search
- `core/vectorindex/collection.go` — collection data structure (per-branch document store)
- `core/vectorindex/indexer.go` — file-to-document indexing logic (dual-writes chromem + lexical)
- `core/vectorindex/telemetry.go` — bounded, content-free stage telemetry snapshots for indexing benchmarks
- `core/vectorindex/BENCHMARKS.md` — reproducible corpus, runner, metric definitions, and baseline
- `core/vectorindex/search_result.go` — search result types and filtering
- `core/vectorindex/hybrid.go` — hybrid search (vector ∪ lexical) with RRF fusion, per-side filters
- `core/vectorindex/lexical/` — bleve/BM25 lexical index with `c0wrk_code` analyzer (camelCase split + lowercase + stop-en)
- `github.com/v0lka/sp4rk/embedding/` — embedding model interface; execution-provider selection (`EmbedderConfig.ExecutionProvider`/`DeviceID`, `ExecutionProviderCPU`/`ExecutionProviderCUDA`), CUDA-enabled session options (`onnx.go`), and the locked-thread `ortRunner` (`runner.go`) live here — see [Embedding Execution Provider (GPU)](#embedding-execution-provider-gpu) and [ADR-045](../decisions/045-gpu-embedding-provider.md)
- `backend/frontend_api_vector.go` — FrontendAPI vector RPC methods (call the lazy `getVectorManager` accessor), including `ListVectorIndexGPUs` for the settings device picker
- `backend/frontend_api_config.go` — `UpdateVectorIndexSettings` (validate → mutate → persist; no hot application — restart-only semantics)
- `frontend/src/components/settings/VectorIndexSettings.tsx` — General-tab settings section for the embedding execution provider (provider Combobox, debounced save with unmount flush, restart hint)
- `frontend/src/components/settings/VectorIndexDevicePicker.tsx` — GPU device control: named dropdown from `ListVectorIndexGPUs` or validating numeric fallback
- `frontend/src/components/settings/VectorIndexDiagnostics.tsx` — read-only runtime diagnostics (requested → effective provider, fallback reason, `cuda_verified` badge) fed from `vectorIndexStore.status`
- `backend/frontend_api.go` — lazy vector-manager accessors + state: `SetVectorManager` (on `FrontendAPILifecycle`, kept off the Wails RPC surface), `getVectorManager`, and the `vectorManagerMu sync.RWMutex` guarding the `vectorManager` field
- `backend/api_types.go` — `VectorIndexStatus` struct (shared API response type, also used by frontend_api_vector.go)
- `frontend/src/components/fileViewer/FileViewerContextMenu.tsx` — file-viewer actions, including selection-based Find Similar vector search
- `frontend/src/components/layout/VectorSearchResults.tsx` — vector result presentation

## Core Types

```go
// FileNode — flat file/directory entry for tree display
type FileNode struct {
    Name       string
    Path       string
    IsDir      bool
    Icon       string // Nerd Font icon name
    IconColor  string // hex color for icon
    Hidden     bool
    GitIgnored bool
}

// GitStatusEntry — per-file git status (map key is the absolute file path)
type GitStatusEntry struct {
    Status         string // legacy primary status char: "M", "A", "R", "C", or "U"
    Staged         bool   // legacy: true=index, false=worktree
    IndexStatus    string // status in the index (staged): "M", "A", "R", "C", "U", "?" or ""
    WorkTreeStatus string // status in the work tree (unstaged): "M", "A", "R", "C", "U", "?" or ""
}

// VectorIndexStatus — indexing progress + embedder runtime facts for frontend
type VectorIndexStatus struct {
    State        string
    Progress     float64
    FilesIndexed int
    TotalFiles   int
    CurrentFile  string
    Branch       string
    Phase        string   // "both" | "embedding" | "lexical"
    Indices      []string // ["vector", "lexical"]

    // Embedder creation-time facts (empty/zero when no embedder exists)
    ExecutionProvider          string // effective: "cpu" | "cuda" — never "auto"
    RequestedExecutionProvider string // config value at creation: "auto" | "cpu" | "cuda"
    ProviderFallbackReason     string // CUDA init failure text when a fallback happened
    CUDAVerified               *bool  // nvidia-smi verdict; only when effective is "cuda"
    DeviceID                   int    // created-with device id; omitted (0) in JSON when 0
}

// SearchOptions — hybrid search request
type SearchOptions struct {
    Query       string     // may contain `+token` sugar for must-match
    TopK        int
    Mode        SearchMode // "hybrid" | "vector" | "lexical" (default hybrid)
    FilePattern string     // doublestar glob applied per-side before fusion
    MustMatch   []string   // post-filter substrings enforced per-side before fusion
}
```

## Behavior

### File Tree

- Loaded lazily on frontend request (not preloaded)
- Depth-limited to avoid scanning huge directories
- Excludes files/dirs matching `.gitignore` or `.aiignore` rules (root and nested, resolved by an `ignore.Resolver` over the workspace) plus any hidden entry (leading-dot name); no hardcoded directory/extension list remains. See [ADR-016](../decisions/016-aiignore.md).
- Returns `FileNode` list: name, path, isDir, hidden, gitStatus, gitIgnored (flat list, no children). The `gitIgnored` flag is set for paths excluded by `.gitignore` **or** `.aiignore`, not strictly git-ignored paths.

### Filesystem Watcher

```
core/workspace.NewWatcher(root string, onChange ChangeHandler, loggers ...*slog.Logger)  // wired in backend/frontend_api_project.go
  → fsnotify watches root + .git (not recursive; recursion is opt-in via Watcher.WatchTree)
  → Event filters before debounce (keep the read-only-git refresh loop from
    self-sustaining): pure-Chmod events (kqueue NOTE_ATTRIB on .git/index reads)
    and Write-only events on watched directories (NTFS/kqueue directory-mtime
    churn — on Windows its delivery can lag the child events by seconds) are
    dropped; every real change arrives as its own event on the child path
      ├─ Debounce (batch rapid changes)
      ├─ Emit global event: workspace:tree_changed
      └─ Frontend: fileTreeStore refreshes affected subtree
```

File watching is enabled for No Project using the active session's
workspace directory (`__no_project__/<sessionID>/workspace/`) as the
watcher root. When no session exists yet, any previous watcher is torn
down and watcher creation is deferred until the first session activates
(there is no project-base-directory fallback). Git operations and vector
indexing remain skipped.

### Git Integration

- `GetGitStatus(dirPath)` returns whole-repository per-file status (modified, added, deleted, untracked) computed over the project root; `dirPath` is used only for containment validation against the project root
- `GetFileDiff(path)` returns unified diff for modified files; for paths outside the active project root (or a non-git path) it returns `("", nil)` — no baseline to diff against, so the frontend does not render a diff panel
- Status integrated into file tree nodes (icon indicators in UI)
- `.gitignore` filtering in directory listings uses `git ls-files --others --ignored --exclude-standard --directory -z`. In a git repo `.aiignore` is layered on top of that git-derived set via an `ignore.Resolver` (only `IgnoredByAIIgnore` is OR-merged, so the resolver's negation-less matching cannot override a git `!pattern` un-ignore). In a non-git workspace (including No Project) the resolver is the sole authority for both files. A resolver-construction failure is non-fatal; the listing returns with whatever flags were already computed.
- `vectorindex.CurrentBranch(ctx, repoPath)` detects the active branch via `git symbolic-ref --short HEAD` (falls back to `git rev-parse --short=12 HEAD` for detached HEAD)
- All git processes are spawned through the hardened choke points — `sysproc.GitCmd` (global baseline overrides) and `GitCmdInRepo` for repo-scoped work (fresh per-repo scan + neutralization, fail-closed); stdout/stderr capture and error propagation are unchanged (errors are never swallowed)
- Non-repository paths are distinguished from failures by matching `"not a git repository"` in stderr; a legitimate non-repo returns an empty result, any other error is returned to the caller. A repo whose `.git/config` cannot be scanned safely reports as non-repo in the boolean predicates (fail closed); the non-repo fallbacks are degraded rather than silently git-free — every git invocation, `--no-index` diff included, spawns through the same fresh scan (a `--no-index` run inside a repository still consults repo config), so the scan failure resurfaces as an error instead of producing output — git never runs un-neutralized (review [53])
- The `git` binary is required for CODE mode. Its absence is detected on project switch in `backend/frontend_api_project.go` via `exec.LookPath`, emitting a `runtime_error` event (dismissable toast) and rejecting the switch. CHAT mode (No Project) never invokes git.
- No Project: `isGitRepo()` returns false (no git operations), `GetGitStatus` returns empty map, `GetFileDiff` returns empty string

### Git Subprocess Hardening

Repositories are untrusted input: a workspace can arrive as files with a hostile `.git/config`, and the config can be planted mid-session. Git executes config-driven programs (hooks, filters, merge drivers, textconv, fsmonitor, signing binaries, editors) on routine operations — including read-only ones — so c0wrk hardens every git process it spawns (full rationale and canary evidence: [ADR-033](../decisions/033-git-subprocess-hardening.md), trust/harden opt-out: [ADR-034](../decisions/034-git-trust-opt-out.md), control-model view: [../architecture/security-model.md](../architecture/security-model.md#git-subprocess-hardening)):

- **Baseline (every git process):** `sysproc.GitCmd` prepends `-c core.fsmonitor=false`, `-c core.hooksPath=<empty safe dir under ~/.c0wrk/git>`, `-c commit.gpgsign=false`, and sets `GIT_EDITOR=true`. `-c` wins over repo config; the repository is never modified.
- **Per-repo neutralization (every repo-scoped call):** `GitCmdInRepo` scans the config of the repository git itself would discover for the path (the `.git` chain is walked up from the given root, covering workspaces rooted at a subdirectory of a repository) fresh on every invocation (no cache) via `ScanGitConfig` and prepends the scanner's `NeutralizingArgv()` (`-c filter.<n>.process=` + `clean=cat`/`smudge=cat`, `-c merge.<n>.driver=false %O %A %B`, `-c diff.<n>.textconv=cat`, `-c diff.external=`/`-c diff.<n>.command=` for the external diff drivers plain `git diff` executes by default, `-c core.sshCommand=ssh`/`-c core.askPass=`/`-c credential.helper=` (+ a per-URL `credential.<url>.helper=` pin) for the transport keys the Git panel's remote operations reach, `-c protocol.git.allow=never` for `core.gitProxy` (no value of the key neutralizes it — git:// operations fail closed instead of executing the repository's proxy), a finding-gated `GIT_WORK_TREE` environment pin for `core.worktree` (no `-c` form beats the key; checkout/reset writes stay confined to the discovered repository root), and — narrowed per review [56] — `-c attr.tree=<empty-tree>` only when include directives exist: the one case per-name pins cannot cover, since an included file may hide a driver definition routed from the in-tree `.gitattributes` (the empty-tree hash matches the repository's object format, SHA-1 or SHA-256). Include-bearing configs additionally derive the name-independent pins `core.sshCommand=ssh`/`core.askPass=`/`credential.helper=`/`diff.external=` for command-bearing keys an included file may arm invisibly, and fail closed outright when the resolved git version is < 2.45 or unresolvable — older git silently ignores `attr.tree` (the version is probed once per process through the hardened chokepoint). Visible drivers no longer derive the blanket kill — their per-name pins cover every routing source, in-tree included — so benign eol/text attributes keep working and a CRLF-normalized repository does not show falsely-modified files or whole-file numstat diffs; while the kill IS active (includes, or a repo-set `attr.tree`) its collateral — normalization off, text files may report modified though unchanged — is disclosed in the intake warning (`(attributes disabled)` finding). Every patch-producing porcelain `git diff` call site additionally passes `--no-ext-diff` so its output stays usable on armed repositories; the `--numstat` call sites (`GetDiffStat`, `GetDiffStats`) need no flag (`git diff --numstat` never runs the external driver), and the other patch producer — plumbing `diff-tree -p` — executes no external drivers absent an explicit `--ext-diff` (verified on git 2.50.1). Linked worktrees scan the common config (resolved through the worktree gitdir's `commondir`) plus the `config.worktree` overlay when `extensions.worktreeConfig` is enabled, merged last-wins like git. The attribute routing sources `attr.tree` does not cover — `.git/info/attributes` and the file named by `core.attributesFile` — are scanned too: every routed driver name gets the same `-c` name pins, and `core.attributesFile=` (empty) disables that source; an attribute source that exists but cannot be scanned fails the whole scan closed (no kill-switch exists for the mechanism). Unscannable config (unreadable, oversized, non-regular file, malformed pointer) → error, no git execution. All repo-scoped call sites in this domain (`git.go`, `vectorindex/git.go`) and the backend git-panel wrappers route through it.
- **Intake warning:** when a project is switched to or an auxiliary work directory is added, `notifyGitConfigRisk` (`backend/frontend_api_gitconfig_risk.go`) scans the config and emits the global `project:git_config_risk` event when it is not provably clean (dangerous keys, include directives — deliberately not followed — malformed or unreadable config). A clean repo emits nothing. Detection-only: neutralization in the spawn layers holds regardless of the UI. A trusted repo is rechecked against its stored snapshot on open — a drifted (or now-unreadable) config evicts the trust and re-warns with `reason` + `diff`.
- **Trust opt-out & harden:** *Trust this repo* stores the root plus the fingerprint of a canonical snapshot of every scanned config source (common config, `config.worktree` overlay, `.git/info/attributes`, `core.attributesFile`) and mirrors the root into the process-wide `core/gittrust` registry, so the spawn layer runs raw git (`sysproc.GitCmdRaw`) for it — its own hooks, filters and signing apply (see [ADR-034](../decisions/034-git-trust-opt-out.md)). The trust is snapshot-bound and rechecked on every open; drift evicts it back to hardening (fail-closed). *Harden this repo* (`security.harden_git_repos`) is the inverse — always hardened, mutually exclusive with trust. Legacy string entries (no fingerprint) keep suppressing the warning unconditionally until re-trusted.
- **Agent-side write gate (sp4rk):** mutating file tools refuse-to-execute-silently on any target inside a workspace `.git` tree — a hard `git_internal_path` judge reason forces the confirmation funnel under any group policy, so the agent cannot be steered into planting hooks/filters for later execution outside c0wrk.
- **Trade-off:** repository-defined hooks and filters — including legitimate ones like git-lfs — do not run inside c0wrk by default; hooks are stripped and the strip is announced in the warning notice (strip-and-warn, never silent, never operation-failing). A trusted repository opts out — its own hooks/filters/signing run — but the opt-out is snapshot-bound and fails closed back to hardening on any config drift.

### Vector Index

- Dual-indexed: chromem-go vector store (cosine similarity on ONNX embeddings) and bleve BM25 lexical store with a custom `c0wrk_code` analyzer (camelCase splitter → lowercase → stop-en). Both indices share a deterministic document ID (`sha256(path)[:8hex]:{chunkIndex}`); lexical hits enrich their content via `chromem.Collection.GetByID`.
- Embeddings computed via ONNX Runtime (local, no API calls)
- Model: quantized embedding model downloaded by `make fetch-embedding-model` (Unix) or the Makefile-routed `scripts/fetch-embedding-model.ps1` (Windows); the Makefile is the single source of truth — version/URL/dir values are passed from Makefile variables to the PowerShell recipe
- ONNX Runtime shared library: fetched by `make fetch-onnx` (Unix) or the Makefile-routed `scripts/fetch-onnx.ps1` (Windows); the Makefile pins `ONNX_VERSION` plus per-platform SHA256 digests of both the release archive and the extracted shared library, and is the single source of truth. The GPU flavor (cuda13) is a separate opt-in fetched by `make fetch-onnx-gpu`; both targets verify digests fail-closed and keep disjoint version stamps (see the flavor-stamp policy in [ADR-045](../decisions/045-gpu-embedding-provider.md))
- Build-time artifact verification is fail-closed: downloaded, extracted, and cached copies are each SHA256-verified against the pinned digest; a mismatch removes the bad file and aborts with a non-zero exit. An empty digest is an error, never a skip (the no-Intel-macOS-asset case fails closed with a clear message). Installed embedding-model/tokenizer copies are digest-verified too (byte-identical copies); the installed ONNX Runtime library is a byte-identical copy of the verified cache and is never rewritten in place — modifying the signed dylib after signing invalidates its embedded code signature, which macOS enforces with a `CODESIGNING` Invalid Page SIGKILL at `dlopen`. It is additionally guarded by a version stamp (`build/bin/.onnxruntime-version`, mirrored by `scripts/fetch-onnx.ps1`), so an `ONNX_VERSION` bump replaces a stale installed library rather than skipping on the installed-copy short-circuit
- Digest provenance: onnxruntime releases attach no checksum files, so archive digests are pinned trust-on-first-use from fresh downloads of official release URLs and cross-verified against the server-computed `digest` (sha256) field GitHub publishes for release assets in its REST API; the model digest is the official Hugging Face LFS oid from the repository pointer file; the tokenizer digest is trust-on-first-use (a plain git blob there). Every digest is recomputed on any `ONNX_VERSION`/URL bump
- Collections are partitioned per git branch; switching branches produces a new pair of collections (`vector/` and `lexical/<branch>/`)
- Storage location: `~/.c0wrk/projects/<projectID>/vector_index/` (per-project, co-located with session data)
- Branch detection on project switch uses `vectorindex.CurrentBranch`; detection failure propagates and aborts the switch rather than silently degrading
- A `GitMonitor` watches `.git/HEAD` via fsnotify and triggers re-partitioning on branch change
- Hybrid search fuses the two ranked lists via Reciprocal Rank Fusion (`score = Σ 1/(k+rank)`, `k=60` by default). Per-side fanout is `max(topK × fanout_multiplier, fanout_min)` (defaults 4 / 100); `FilePattern` (doublestar glob) and `MustMatch` post-filters are applied **before** fusion so rank spaces are comparable. Pre-fusion **score thresholds** discard noise-tail hits before fusion: vector hits below `hybrid_vector_score_floor` (absolute) or `hybrid_vector_score_ratio × top similarity` (relative, default 0.25) are dropped; lexical hits below `hybrid_lexical_score_ratio × top BM25` (relative, default 0.1) are dropped. This prevents weakly semantic chunks that merely contain a query term from earning a double RRF contribution and jumping above one-sided relevant hits. Thresholds apply only in the hybrid path; vector-only and lexical-only modes return raw top-K without score gating. See ADR-013.
- Auto-fallback: `ModeHybrid` silently degrades to `ModeVector` when the lexical index is empty or unavailable; `ModeLexical` returns empty (or an error if no lexical index has been opened at all).
- Dual-write invariant: chromem commits first (source of truth); lexical upsert/delete is best-effort and drift is repaired by `Indexer.RebuildLexical`, which the manager invokes during `SwitchProject` when `chromem.Count() > 0 && lexical.Count() == 0`.
- Indexing progress is phased: `PhaseBoth` (parallel full index of a fresh project), `PhaseEmbedding` (chromem-only chunk), `PhaseLexical` (bleve backfill). The phase is surfaced to the frontend via the `vector_index:status` event `phase` field.
- Oversized files are skipped before full reads during project walk and collection validation, with `processFile` repeating the size check as the universal incremental-index backstop. Files whose structure-aware split exceeds `max_chunks_per_file` are skipped as one unit; the remaining index pass continues.
- ONNX inference uses the configured embedding batch capacity and carries the unfinished tail across file boundaries and 200-document commit windows; only the final inference of an indexing pass may be underfilled. Already-embedded documents are committed to chromem in windows of at most 200, preserving bounded memory/cancellation latency. A file's hash sidecar entry is published only after every one of its chunks has committed (or has been deliberately dropped by poisoned-text fallback), so a partial large-file commit is never classified as complete.
- Embedding inference policy is a cross-repo contract (see [ADR-036](../decisions/036-vector-index-embedding-optimization-policy.md)): generic ONNX logic — persistent fixed-shape sessions (eager batch-1 query session, lazy `[BatchSize, MaxSeqLength]` batch session), opt-in length buckets, opt-in tokenization/inference pipeline — lives in sp4rk's `embedding` package, while embedding persistence (content-addressed cache, commit windows, fingerprints) lives here in `core/vectorindex`, which never imports the ONNX runtime directly. c0wrk consumes the sp4rk defaults (automatic mode) and deliberately exposes no inference-strategy knobs: length buckets, the batch pipeline, and parallel sessions stay behind their benchmark gates (fixed-512 serial single-session remains the production path), and flipping any of those defaults requires a new checked-in benchmark decision, not a host flag.
- Non-UTF-8 file content (legacy single-byte encodings, corrupted bytes passing the NUL-header sniff) is sanitized in `processFile` before chunking — undecodable sequences become U+FFFD, matching the lossy conversion sp4rk's `Tokenizer.Encode` applies — because sugarme/tokenizer panics on invalid UTF-8. The recorded `content_hash` still covers the RAW on-disk bytes so `ValidateCollection` unchanged-file detection keeps matching.
- Content-triggered embedding failures are isolated, not pass-fatal: when an embedding chunk fails as a unit, each text is retried individually and only texts that also fail alone are dropped (WARN-logged) from BOTH the chromem commit and the lexical upsert, so the indexes never diverge; the file-hash sidecar still records the file (a deterministic content failure must not burn embedding work on every pass). If EVERY text in a chunk fails individually, the embedder itself is broken and the pass aborts with an error.

Memory program (see [ADR-049](../decisions/049-vector-index-memory-reduction.md)):

- **Content-less storage contract**: on the batch path chromem stores only `ID` + `Embedding` + the four chunk-position metadata fields (`file_path`, `start_line`, `end_line`, `language`); chunk `Content` and per-file bookkeeping (hash/size/mtime) are NOT resident — per-file facts live in the file-hash sidecar row. Search reconstructs content best-effort from the source file (`contentResolver`: bounded read ≤ `max_file_size`, `lines[start-1:end]`, per-call path→lines cache); a missing/unreadable file yields the placeholder `[content unavailable: source file missing or unreadable: <path>]`, never an error, and `must_match` filters on reconstructed text so deleted files drop out. Hydration fills only the final top-K; legacy-stored content stays authoritative where present.
- **Sidecar entry format v5** (`content_hash|size|mtime_unix_nano|chunker_fingerprint|chunk_set`, `|`-separated; 3/4-field legacy entries keep parsing): the 5th field (`idxset`) records the file's committed chunk-index set — contiguous `0..N-1` as the bare count `"N"` (empty → `"0"`), gapped sets (poison-drop survivors) as `"L:0,1,3"`. Parse is strict and capped (`1<<20` entries): malformed/oversized values downgrade the entry to legacy. An older binary's 3-or-4-field parser treats a 5-field entry as a legacy bare hash — slower but correct.
- **Enumeration without materialization**: with a known chunk set, document IDs are pure arithmetic on the deterministic `DocumentID(path, i)`; `collectDocumentIDs`, `RebuildLexical` v2, migration probing, and `ValidateCollection` stats never issue the materializing full-collection `Query(" ", N)`. Unknown set → all-or-nothing fallback to the legacy space-query. Enumeration uses a first-axis unit query vector (`QueryEmbedding`) when the embedding dimension is known, so Browse costs no ONNX inference. `RebuildLexical` v2 walks the sidecar (known fingerprint + chunk set → re-chunk → same IDs; zero ONNX) with a fallback to the collection-query path for empty/ineligible sidecars.
- **One-time content-less migration**: each collection is migrated in the background to the stripped shape exactly once, tracked by the marker `contentless_<collectionName>.done` next to the branch sidecar. The migration waits for the file-hash backfill, probes documents without the service lock, re-commits them in ≤200-doc windows with the same IDs/vectors and stripped content (chromem rewrites each per-doc gob in place), writes the marker only on full success, then frees OS memory. Cancellation (project switch, park/eviction, close, branch switch, rebuild) leaves every document individually consistent (legacy or stripped, never half) without the marker — the next open replays to completion. `IndexIncremental` waits for the migration before deleting/re-indexing.
- **Park budget in bytes**: parked state footprint is estimated as `documents × (embedding_dims × 4 B + 1 KiB)`; right after parking, the oldest parked states are evicted (sidecar flushed, then `freeOSMemory` outside the lock) while the sum exceeds `park_budget_mb` (default 1024 MiB; negative disables the byte budget; explicit 0 is rejected at load as ambiguous). The effective parked-RAM bound is `min(park_capacity, park_budget_mb)`; `park_capacity: 0` disables parking entirely regardless of the budget.
- **GC memory hygiene**: `runtime.memory_soft_limit_mb` (0/unset = AUTO = clamp(50% physical RAM, 2 GiB..8 GiB); >0 = explicit MiB; -1 = off; < -1 fails load) is resolved once at startup — precedence: off → explicit config (beats a `GOMEMLIMIT` env var, logged) → `GOMEMLIMIT` env (beats AUTO) → AUTO — and applied via `debug.SetMemoryLimit` before background indexing. `debug.FreeOSMemory` runs after every full indexing pass, after park evictions, and after the migration; incremental passes do not scavenge.
- **RSS observability**: the `GetProcessMemory` RPC feeds the StatusBar's live `RSS NNNN MB` indicator (5 s polling, exact-bytes tooltip); failures hide the indicator silently.
- Used for:
  - `semantic_search` tool (agent searches code by meaning; accepts `mode`, `must_match`, `file_pattern`, `top_k`)
  - File Viewer **Find Similar**: when text is selected, the context-menu action runs vector search with that selection and opens the standard vector-results panel
  - RAG hint injection before routing/planning (top-5 relevant files)

Lifecycle:

```
App started
  → ONNX embedder loads in background goroutine (after EventBackendReady)
  → Vector index manager created and wired into FrontendAPI via SetVectorManager()
  → InitVectorIndexForActiveProject applies any setup deferred by the frontend's
    first SwitchProject (which fires on backend:ready, before the manager exists —
    switchProjectSetupVector records the project instead of silently skipping it)
  → Emit vector_index:status event (state=ready)

Project switched (manager ready)
  → No Project: switch to empty collection (clears stale CODE-project results)
  → vectorindex.CurrentBranch(ctx, workspacePath) via git CLI
  → core/vectorindex manager: SwitchBranch(branch) → Start indexing (background goroutine)
  → GitMonitor watches .git/HEAD for subsequent branch changes
  → Emit vector_index:status events (progress updates)
  → Index ready → semantic_search tool unblocked (via PreExecuteHook)
```

Manual forced reindex: the explorer sidebar's reindex action (which replaced the former file-tree refresh button — the tree already reloads itself on `workspace:tree_changed`) calls `ReindexVectorIndex`. The manager cancels any in-flight pass, then reconciles the current index against the workspace (`IndexIncremental`), falling back to a full build (`IndexFull`) when no index exists yet (empty collection). It is rejected for No Project (CHAT) mode, where vector indexing is disabled — the sidebar action is hidden entirely there rather than rendered disabled — and reports progress through the same `vector_index:status` events. The pass targets the workspace stored alongside the indexer (read under the manager lock), so it can never be pointed at a different project than the one the indexer belongs to. The button is disabled (and shown spinning) while a pass is already in flight and, via an optimistic latch, during the brief window between the click and the first `vector_index:status` event — so a fast double-click cannot fire a duplicate RPC; the latch is released once the store reflects the busy state, when the project becomes unavailable, or when the RPC rejects.

### Embedding Execution Provider (GPU)

The embedding inference backend is selectable per configuration: `auto` (default), CPU, or CUDA on an NVIDIA GPU. Full rationale — including the per-thread VRAM blowup and the silent-CPU-slide failure mode — is recorded in [ADR-045](../decisions/045-gpu-embedding-provider.md); the research backing it is in `docs/cuda-embedding-research.md`. The ADR's original "no UI yet" consequence has since been addressed: the settings surface below consumes exactly the observability contract the ADR preserved (status payload fields, config keys) — the ADR text itself is immutable per the spec update protocol, the domain spec documents the current surface.

- Configuration: `vector_index.execution_provider` (`auto` | `cpu` | `cuda`, default `auto`) and `vector_index.device_id` (GPU index, default `0`). `auto` attempts CUDA when the GPU build of ONNX Runtime is present (the CUDA provider shared libraries sit next to the binary) and falls back to CPU otherwise; `cpu` forces the CPU provider; `cuda` requests the GPU and, on a CUDA failure, the startup layer retries the embedder on CPU (WARN + `runtime_error` toast `error_code: vector_cuda_fallback` + mismatch recorded in status; at the sp4rk layer an explicit `cuda` stays a hard error). The PoC-era environment knobs `C0WRK_ONNX_EP`/`C0WRK_ONNX_DEVICE` were removed when the knobs moved into config.
- GPU init path (`auto`): probe whether the CUDA provider is loadable in this build (a CPU-only ONNX Runtime library errors loudly on `AppendExecutionProviderCUDA` — clean early detection); on success create the CUDA session (validating `device_id` against visible devices); on failure log a **WARN** and continue on CPU.
- **Fallback is never silent**: the user decision is that a CPU fallback after a CUDA attempt is tracked and warned about loudly — a previous attempt showed the ORT CUDA library can silently slide to CPU and this is invisible from inside the code. The `auto` fallback is detected and WARN-logged at the sp4rk embedding level (where session creation happens); the explicit-`cuda` fallback lives in c0wrk's startup glue and additionally emits the unmissable toast. c0wrk's half of the contract is observability — the effective provider (the one actually running inference) is surfaced through the vector-index status payload (`execution_provider` + `requested_execution_provider` + `provider_fallback_reason`, plus `cuda_verified` when the effective provider is `cuda`), so `auto`/`cuda`/`cpu` requested vs. effective is always observable.
- **Startup GPU verification (effective provider `cuda`)**: after the embedder initializes, the startup layer runs one warmup inference (the CUDA context is created lazily — probing right after session creation sees a process the driver does not know yet) and then asks the driver via `nvidia-smi` whether this PID is a CUDA compute app. PID found → INFO "CUDA verified via nvidia-smi"; PID absent → WARN "possible silent CPU fallback" and `cuda_verified: false` in the status. Diagnostics only — the probe (bounded to 2s inside sp4rk) never blocks or fails startup; probe errors are "unverified", never "on CPU".
- **Provider changes require an app restart**: the embedder (with its ONNX session) is created exactly once per process during the background vector init and is never re-created — `execution_provider`/`device_id` are read at creation time only.
- **GPU usage verification is external**: `onnxruntime_go` exposes no provider introspection (no `GetAvailableProviders`), so tests asserting GPU execution must verify externally via `nvidia-smi --query-compute-apps=pid,used_gpu_memory` — the process's own PID must appear in the compute-apps list. "Calls succeeded" is not evidence of GPU execution.
- **All ORT touches under a GPU provider go through the single locked thread**: the CUDA execution provider keeps a per-OS-thread state (a cuBLAS handle with its own workspace, ~1 GiB VRAM each); Go's scheduler migrates goroutines across threads, so an unguarded call pattern exhausts VRAM within seconds of indexing (`CUBLAS failure 3`). sp4rk's `ortRunner` (`runtime.LockOSThread`, never unlocked) executes every ONNX Runtime touch — environment init, session-options build, session creation, fast-path and batch inference, teardown — on one pinned thread. With a CPU provider the runner is nil and calls run inline (the CPU path is unchanged).
- Packaging: GPU ONNX Runtime libraries are a separate opt-in — `make fetch-onnx-gpu` installs the **cuda13** flavor only; `make build` / `make fetch-onnx` keep installing the CPU library and never overwrite a GPU install. The GPU flavor is also published as a release archive (`c0wrk-desktop-linux-amd64-cuda13.tar.gz`, linux/amd64 only), and the in-app updater always selects the asset matching the running install's flavor — a cuda13 tree updates only from the cuda13 archive; switching flavors is a manual reinstall (see [ADR-046](../decisions/046-cuda-release-artifact.md)). The GPU flavor needs the system CUDA 13 runtime (`libcudart.so.13`, cuBLAS, cuRAND) — a cuda12-flavor library would fail to load on a CUDA 13 machine and vice versa, hence pinning one flavor. The version stamp records the flavor (e.g. `1.28.1-gpu-cuda13`) and each fetch target compares against its own expected stamp value, so `make build` cannot silently reinstall the CPU library over a GPU install (see ADR-045, stamp policy).
- **Settings UI** — a "Vector Index" section in the Settings modal's **General** tab (below HTTP Proxy), `frontend/src/components/settings/VectorIndexSettings.tsx`: an execution-provider Combobox (`auto` / `cpu` / `cuda` with a per-option description line) and the GPU-device control. Saves are debounced (500 ms) and flushed on unmount — Radix Tabs unmount inactive tab content, so a pending edit must survive a tab switch mid-debounce. Device ids are validated client-side (whole number ≥ 0); an invalid id is buffered and flagged, never persisted — the backend rejects the whole update, so one bad field must not lose the other. The read-only diagnostics block (`VectorIndexDiagnostics.tsx`) renders requested → effective provider **classified by outcome**, not by raw inequality — an "auto" request always diverges (it is resolved to a winner at embedder creation), so `requested !== effective` alone cannot mean "fallback": explicit `cuda` landing on `cpu` is the one true fallback (warning color + the fallback-reason callout; the backend fills `provider_fallback_reason` on exactly this path); `auto → cuda` is Auto's best outcome (success color, no callout); `auto → cpu` is Auto's expected degradation (info color + an info note naming it as expected behavior and pointing to the startup WARN in the app logs for the concrete cause — that cause is not part of the status payload); any honored request renders in success color. Plus the `cuda_verified` badge (only when effective provider is `cuda` and the startup probe ran) and the running device id; it is fed reactively from `vectorIndexStore.status` (`vector_index:status` events) — the section issues no status RPC of its own. Absent `execution_provider` degrades the block to a muted "embedder is not running" note.
- **GPU device picker** (`VectorIndexDevicePicker.tsx`): a named dropdown (`"{index} — {name}"`) when the driver reports GPUs; a validating numeric input (≥ 0) when it does not — there is no nvidia-smi requirement for the control to exist. A muted "Detecting GPUs..." placeholder covers the in-flight probe (bounded 2 s) so the control does not flash between shapes; a probe *failure* (driver present but broken → RPC reject path) is surfaced as a distinct warning line next to the numeric fallback. Disabled when the provider is `cpu` (the device applies only when the provider resolves to CUDA).
- **Settings RPCs**: `UpdateVectorIndexSettings` (`backend/frontend_api_config.go`) validates (`execution_provider` ∈ {`auto`, `cpu`, `cuda`}; `device_id` ≥ 0 — error text mirrors the load-time `validate()` wording so the settings UI shows the same guidance as a bad YAML), mutates, and persists under `configMu`; it deliberately has **no hot application** — it never touches the vector manager or the running embedder, so a rejected request leaves both the in-memory config and the YAML untouched and a restart cannot brick on a bad value. `ListVectorIndexGPUs` (`backend/frontend_api_vector.go`) is the lazy, on-demand counterpart to the startup GPU probe: it delegates to `embedding.ListGPUDevices` (nvidia-smi under the same 2 s probe budget) and returns plain `GPUDeviceResponse` DTOs. nvidia-smi absent from PATH → empty non-nil slice, nil error (absence of the NVIDIA userspace means "no GPUs to offer", not a failure); probe failure (driver down, non-zero exit, timeout) → nil slice + wrapped error, so the UI distinguishes "no hardware" from "hardware present, probe failed". The list reflects what the driver reports right now, independent of the running embedder (whose creation-time facts travel in `VectorIndexStatus`).
- **Restart semantics**: the embedder is created once per process, so a changed provider/device takes effect only after an app restart — the UI makes that contract visible instead of letting edits look ignored. A restart hint is shown whenever the saved config diverges from the running embedder's creation-time facts: `status.requested_execution_provider` ≠ saved provider **or** `status.device_id` (absent ⇒ 0) ≠ saved device; it is hidden when they match or no embedder has ever reported (the provider fields carry the populated/not-populated signal; `device_id` participates in the check for non-zero values only, and a UI treating 0 as the default reads absence as 0). The comparison is against the *requested* provider, not the effective one — a fallback (requested `cuda`, effective `cpu`) is surfaced by the diagnostics block, not by the restart hint.

### File Operations (Workspace API)

| Method                              | Description                                         |
| ----------------------------------- | --------------------------------------------------- |
| `ListDirectory(dirPath, recursive)` | Lazy directory tree with git status (workspace-contained) |
| `ReadFile(path)`                    | Read file content (no backend-side binary detection; frontend `isBinaryContent` checks null bytes in first 8KB). Not workspace-contained — accepts any absolute path so the viewer can display out-of-workspace files the agent cites (e.g. SDK sources); a trailing `#L<n>`/`#L<n>-L<m>` line anchor is stripped before resolution |
| `ReadFileAsDataURL(path)`           | Read a file and return it as a base64 `data:` URL (RFC 2397), for embedding local images in the file-viewer markdown renderer (the webview cannot load `file://` or project-root-relative URLs). **Workspace-contained** — the only read-path RPC that retains containment, because image embedding runs during markdown auto-render without an explicit user action. MIME type from `mime.TypeByExtension`; 8 MiB size guard |
| `GetFileDiff(path)`                 | Unified git diff for file. Not workspace-contained, but returns `("", nil)` for files outside the active project root or a non-git path (no baseline to diff against) |
| `GetGitStatus(dirPath)`             | Whole-repository per-file git status summary; `dirPath` used only for containment validation against the project root |

Filename search is available via the `glob` built-in tool (`github.com/v0lka/sp4rk/tools/builtins/glob.go`), not as a direct Workspace API method.

## Invariants

- File tree is always relative to active project's workspace path
- Watcher emits events only for the active project's workspace
- Git processes spawned in this domain always carry the sysproc baseline overrides; repo-scoped invocations re-scan the discovered repository's config fresh per call (walking up the `.git` chain like git's own discovery) and fail closed on unscannable configs — repository-defined hooks, fsmonitor daemons, filters, merge drivers, and textconv never execute (see [ADR-033](../decisions/033-git-subprocess-hardening.md))
- A repository whose `.git/config` cannot be scanned safely is reported as not-a-repo by the boolean predicates, so callers take their non-repo paths — which the same scan failure then errors out of (fail-closed, review [53]) rather than producing degraded git output: git never runs un-neutralized
- Vector index collection is partitioned by git branch and rebuilt when the branch changes
- Chromem is the source of truth; lexical is a best-effort mirror reconciled via `RebuildLexical`
- Batch-path documents are stored content-less — ID + embedding + the four chunk-position metadata fields only; content is reconstructed best-effort from source files, and a missing/unreadable source yields a placeholder result (never an error) that `must_match` excludes (see [ADR-049](../decisions/049-vector-index-memory-reduction.md))
- A file-hash sidecar entry's chunk set is authoritative for its document IDs (deterministic `DocumentID(path, i)`): when the set is known, enumeration paths use ID arithmetic and never materialize the full collection
- The content-less migration marker is written only after every document of the collection is stripped; a cancelled migration writes no marker and replays on the next open — every document is always individually complete (fully legacy or fully stripped)
- The effective parked-RAM bound is `min(park_capacity, park_budget_mb)`; every eviction flushes the sidecar before dropping state and frees OS memory outside the lock
- The GC soft-limit resolution order is fixed — off → explicit config → `GOMEMLIMIT` env → AUTO — and every branch is logged at startup
- Per-side filters (FilePattern, MustMatch) are applied BEFORE RRF fusion so vector and lexical rank spaces remain comparable
- Pre-fusion score thresholds discard noise-tail hits (low cosine similarity or low BM25) before RRF fusion so they cannot earn a double RRF contribution; thresholds apply only in the hybrid path, not vector-only or lexical-only modes
- A single `ready atomic.Bool` on `Service`; hybrid auto-falls-back to vector-only when `lexical.Count() == 0`
- The indexer rejects binary files via a bounded 512-byte header pre-read (null-byte presence, `binaryHeaderSize`) before loading the file into memory; the frontend file viewer detects binary content via null bytes in the first 8KB
- Every index path applies `max_file_size` before a full file read, and every chunked file stays at or below `max_chunks_per_file`; oversized/pathological files are skipped without aborting the pass
- A deterministic content filter (`vector_index.content_filter`, `core/vectorindex/content_filter.go`) rejects generated (canonical "DO NOT EDIT" header marker within the first 2 KiB), minified (≥16 KiB file with a >10 KiB line and ≤8% whitespace), and pathological (≥32 KiB file with a single >24 KiB unbroken token) files BEFORE chunking — no chunks, no embeddings, no inference. Every heuristic and threshold is individually toggleable; `.aiignore` remains the exact path-level channel. The policy participates in the chunker fingerprint: a policy change invalidates affected sidecar entries, newly excluded files have their documents removed on the next incremental pass, and unchanged re-chunks are served by the embedding cache rather than ONNX. The filter layers ON TOP of the ignore rules and hard limits — it never relaxes them
- Every non-final ONNX inference batch in an indexing pass is filled to the configured capacity; the final tail is bounded by that capacity. Chromem commits contain at most 200 already-embedded documents, cancellation is observed between bounded operations, and all manager-launched indexing goroutines are drained before teardown
- Optional index telemetry uses a fixed stage enum and numeric aggregates only; it never records source text, absolute paths, project IDs, branch names, or caller-defined labels
- Document embeddings are cached under each project's `vector_index/embedding_cache/`, outside branch collections. Keys include cache-format version, SHA256 fingerprints of model/tokenizer bytes, max sequence length, vector dimension, normalization algorithm ID, and SHA256 of normalized chunk content; paths and branch names are excluded so rename/move/branch changes reuse identical chunks
- Equal normalized chunks in one commit window are embedded once and fanned out to every document. A cache hit never calls the batch embedder/ONNX path
- Cache entries are atomically installed (temporary file + rename), carry dimension and SHA256 checksum, and reject truncated, non-finite, wrong-dimension, or checksum-invalid vectors. Corruption is a fail-open miss: the entry is removed best-effort and rebuilt without aborting indexing
- Cache pruning runs after a resolved embedding batch but walks the cache directory only when a tracked-bytes counter crosses 95% of `embedding_cache_max_bytes` or every 4096 puts (an accounting-drift backstop; project switch always walks once to seed the counter). A walked prune evicts oldest entries down to a 90% low-water mark, so a cache running near the cap does not evict on every batch. Cache I/O/pruning failures never disable indexing
- Embedding fallback is never silent: when `execution_provider` is `auto` and CUDA init fails, the embedding layer (sp4rk) logs a WARN and continues on CPU; when `execution_provider` is `cuda` and CUDA init fails, the desktop startup layer (`desktop/startup_phases.go`) retries the embedder on the CPU provider so vector search stays available — loudly: WARN log + `runtime_error` toast (`error_code: vector_cuda_fallback`) + the requested-vs-effective mismatch recorded in every vector-index status. In both cases the effective provider is always reflected in the vector-index status (requested and effective, plus the nvidia-smi `cuda_verified` verdict when the effective provider is `cuda`); the fallback is never a silent CPU slide
- The effective embedding execution provider is always observable (status payload/getter); requested (`auto`/`cpu`/`cuda`) and effective provider are both surfaced, because ORT's CUDA library can silently slide to CPU and that state is invisible from inside the process
- `UpdateVectorIndexSettings` validates before it mutates: a rejected request leaves both the in-memory config and the persisted YAML untouched, and the RPC never touches the vector manager — provider/device changes take effect only at the next embedder creation (app restart), which the settings UI discloses via a restart hint whenever the saved config diverges from the running embedder's creation-time facts
- All ONNX Runtime touches under a GPU provider execute on the single locked thread (sp4rk `ortRunner`, `runtime.LockOSThread`): environment init, session-options build, session creation, fast-path and batch inference, and teardown — a per-OS-thread cuBLAS context (~1 GiB VRAM each) makes any unguarded ORT touch from another thread exhaust VRAM
- Every externally fetched build-time artifact (ONNX Runtime library, embedding model, tokenizer) is SHA256-verified fail-closed against Makefile-pinned digests before installation, and the verification covers installed, cached, and downloaded copies alike
- Write-path and structural RPCs (`WriteFile`, `ListDirectory`) reject paths outside the workspace (directory-traversal containment); read-path RPCs (`ReadFile`, `GetFileIcon`, `GetFileDiff`) accept any absolute path so the viewer can display any file the agent cites — this is a display affordance that does not relax the agent's `read_file` tool containment. `ReadFileAsDataURL` is the exception among read RPCs: it retains workspace containment because image embedding runs during markdown auto-render (no explicit user action) and must not let a document read arbitrary files into the webview DOM
- Every git invocation flows through `exec.CommandContext`; git errors propagate to the caller (no silent fallback)
- Missing `git` binary blocks the CODE-mode project switch: detected lazily via `exec.LookPath` in `SwitchProject`, it emits a dismissable `runtime_error` toast (`error_code: git_not_found`) and rejects the switch; CHAT / No-Project mode never invokes git and never requires it
- ONNX embedder loading runs asynchronously after EventBackendReady; it never blocks the critical startup path
- Vector search RPCs block via `Service.WaitReady` until the index is ready: a search issued while indexing is in progress waits, surfacing a "waiting for index readiness" error only if the request context is cancelled first. When no vector manager is wired (`getVectorManager() == nil`) the RPC returns a "vector search not available" error
- No Project: git operations and vector indexing are skipped (deactivated at the FrontendAPI layer). File watching is scoped to the active session's workspace directory (`__no_project__/<sessionID>/workspace/`); when no session exists yet, the watcher is torn down and creation deferred until the first session activates (no project-base-directory fallback).
- No Project: git status and diff always return empty (no git process spawned)
- No Project: vector search never returns results (indexer is never started, semantic_search tool is disabled anyway)

## Configuration

| Parameter        | Source                | Description                        |
| ---------------- | --------------------- | ---------------------------------- |
| Workspace path   | Active project config | Root directory for file operations |
| Ignore patterns | `.gitignore` / `.aiignore` | Ignore files controlling which files/dirs are excluded from tree and index (hidden dirs always excluded) |
| Index chunk size | `vector_index.max_chunk_size` (default 1500) | Maximum characters per embedding chunk |
| Maximum file size | `vector_index.max_file_size` (default 4194304 / 4 MiB) | Files above this size are skipped before full read/chunk/embed |
| Maximum chunks per file | `vector_index.max_chunks_per_file` (default 4000) | Entire files above this post-split count are skipped as pathological |
| Content filter | `vector_index.content_filter.*` (defaults: enabled; generated-header 2 KiB; minified ≥16 KiB + line >10 KiB + whitespace ≤8%; pathological ≥32 KiB + token >24 KiB) | Deterministic pre-chunk skip of generated/minified/pathological files; every rule and threshold individually configurable; participates in the chunker fingerprint |
| Retrieval-quality gate | `core/vectorindex/retrieval_quality_test.go` (checked-in harness) | Deterministic corpus + synthetic embedder + real-tokenizer truncation audit that any future `max_chunk_size`/`chunk_overlap` default change must pass (quality preserved, unique chunks not increased, truncation share not increased) |
| Inference batch capacity | `vector_index.embedding_batch_size` (default 32) | Fixed ONNX session row capacity; tails stream across files/commit windows and flush only at pass end |
| Embedding cache cap | `vector_index.embedding_cache_max_bytes` (default 536870912 / 512 MiB) | Per-project content-addressed cache cap; negative disables, 0/unset selects default |
| Park capacity | `vector_index.park_capacity` (default 3) | How many recently-closed projects keep their full vector-index state resident in RAM for instant return; 0 disables parking (every switch reopens from disk) |
| Park budget | `vector_index.park_budget_mb` (default 1024) | Cumulative estimated-RAM budget for ALL parked states (`documents × (embedding_dims × 4 B + 1 KiB)`); oldest parked states are evicted — sidecar flushed, OS memory freed — while the sum exceeds it; effective bound `min(park_capacity, park_budget_mb)`; negative disables the byte budget, an explicit 0 is rejected at load as ambiguous |
| Go runtime soft memory limit | `runtime.memory_soft_limit_mb` (default 0 = AUTO) | Process-level GC target applied via `debug.SetMemoryLimit` before background indexing: AUTO = clamp(50% physical RAM, 2 GiB..8 GiB); `>0` explicit MiB; `-1` off; `< -1` fails load. An explicit value beats a `GOMEMLIMIT` env var, which beats AUTO. See [ADR-049](../decisions/049-vector-index-memory-reduction.md) |
| Embedding commit window | Internal constant (200 documents) | Bounds already-embedded rows retained and committed to chromem per operation |
| Hybrid RRF k     | `vector_index.hybrid_rrf_k` (default 60) | Reciprocal Rank Fusion constant k |
| Hybrid fanout    | `vector_index.hybrid_fanout_multiplier` (default 4) / `hybrid_fanout_min` (default 100) | Per-side candidate pool size for RRF |
| Vector score floor | `vector_index.hybrid_vector_score_floor` (default 0.0) | Absolute cosine-similarity floor; vector hits below this are discarded before fusion |
| Vector score ratio | `vector_index.hybrid_vector_score_ratio` (default 0.25) | Relative cosine cutoff; vector hits below ratio × top similarity are discarded before fusion |
| Lexical score ratio | `vector_index.hybrid_lexical_score_ratio` (default 0.1) | Relative BM25 cutoff; lexical hits below ratio × top BM25 are discarded before fusion |
| Hybrid toggle    | `vector_index.hybrid` (pointer bool, default true) | Master toggle: hybrid (vector + BM25) when true, vector-only when false |
| Embedding threads | `vector_index.embedding_threads` (default 0) | Caps the ONNX intra-op thread pool during indexing; 0 = all cores (default) |
| Stage telemetry | Optional `Telemetry` collector in `ManagerConfig` / `ServiceConfig` / `IndexerConfig` | In-process numeric aggregates for benchmarks; nil disables collection |
| Execution provider | `vector_index.execution_provider` (default `auto`) | Embedding inference backend: `auto` (try CUDA when GPU libs are present next to the binary, fall back to CPU with a WARN on failure), `cpu` (force CPU), `cuda` (require CUDA — on init failure the startup layer retries on CPU loudly: WARN + `runtime_error` toast + mismatch recorded in status). Editable at runtime via the Settings → General "Vector Index" section; takes effect after restart. See [ADR-045](../decisions/045-gpu-embedding-provider.md) |
| GPU device | `vector_index.device_id` (default 0) | GPU device index used when the CUDA execution provider is active (validated against visible devices at session creation); editable via the settings device picker (named dropdown when nvidia-smi reports GPUs, numeric input otherwise) |

## Extension Points

- **Custom ignore patterns**: add entries to the project's `.gitignore` (or `.aiignore` for AI-specific ignores) to exclude files/directories from tree and index. `.aiignore` is the recommended channel for files git tracks but the agent/indexer should not waste context on. Recommended `.aiignore` recipe:
  ```
  go.sum
  package-lock.json
  *.lock
  ```
  Hidden directories (entries starting with `.`) are always excluded regardless of ignore files. See [ADR-016](../decisions/016-aiignore.md) for the full rationale and the ripgrep nested-`.aiignore` limitation.
- **Alternative embedding model**: replace ONNX Runtime with a different model by implementing the embedding interface
- **Vector store backend**: replace chromem-go with an alternative vector database by implementing the service interface
- **Git monitor hooks**: add custom callbacks on branch change detected by `GitMonitor`
- **Additional file attributes**: extend `FileNode` with custom fields and populate them in `ListDirectory()`

## Related Specs

- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — workspace RPC surface
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — workspace:tree_changed, vector_index:status, project:git_config_risk
- [../architecture/security-model.md](../architecture/security-model.md) — Git Subprocess Hardening in the layered control model
- [../decisions/033-git-subprocess-hardening.md](../decisions/033-git-subprocess-hardening.md) — threat model, neutralization semantics, and trade-offs
- [../decisions/045-gpu-embedding-provider.md](../decisions/045-gpu-embedding-provider.md) — embedding execution-provider selection (auto/CPU/CUDA), fallback transparency, locked-thread execution, GPU packaging
- [../decisions/049-vector-index-memory-reduction.md](../decisions/049-vector-index-memory-reduction.md) — memory-reduction program: GC soft limit + scavenges, sidecar v5 chunk sets, content-less storage + one-time migration, park byte budget, RSS indicator
- [tool-system/builtins.md](tool-system/builtins.md) — semantic_search tool
