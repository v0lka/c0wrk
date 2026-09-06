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
- `core/vectorindex/search_result.go` — search result types and filtering
- `core/vectorindex/hybrid.go` — hybrid search (vector ∪ lexical) with RRF fusion, per-side filters
- `core/vectorindex/lexical/` — bleve/BM25 lexical index with `c0wrk_code` analyzer (camelCase split + lowercase + stop-en)
- `github.com/v0lka/sp4rk/embedding/` — embedding model interface
- `backend/frontend_api_vector.go` — FrontendAPI vector RPC methods (call the lazy `getVectorManager` accessor)
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

// VectorIndexStatus — indexing progress for frontend
type VectorIndexStatus struct {
    State        string
    Progress     float64
    FilesIndexed int
    TotalFiles   int
    CurrentFile  string
    Branch       string
    Phase        string   // "both" | "embedding" | "lexical"
    Indices      []string // ["vector", "lexical"]
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
  → On file change:
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

Repositories are untrusted input: a workspace can arrive as files with a hostile `.git/config`, and the config can be planted mid-session. Git executes config-driven programs (hooks, filters, merge drivers, textconv, fsmonitor, signing binaries, editors) on routine operations — including read-only ones — so c0wrk hardens every git process it spawns (full rationale and canary evidence: [ADR-033](../decisions/033-git-subprocess-hardening.md), control-model view: [../architecture/security-model.md](../architecture/security-model.md#git-subprocess-hardening)):

- **Baseline (every git process):** `sysproc.GitCmd` prepends `-c core.fsmonitor=false`, `-c core.hooksPath=<empty safe dir under ~/.c0wrk/git>`, `-c commit.gpgsign=false`, and sets `GIT_EDITOR=true`. `-c` wins over repo config; the repository is never modified.
- **Per-repo neutralization (every repo-scoped call):** `GitCmdInRepo` scans the config of the repository git itself would discover for the path (the `.git` chain is walked up from the given root, covering workspaces rooted at a subdirectory of a repository) fresh on every invocation (no cache) via `ScanGitConfig` and prepends the scanner's `NeutralizingArgv()` (`-c filter.<n>.process=` + `clean=cat`/`smudge=cat`, `-c merge.<n>.driver=false %O %A %B`, `-c diff.<n>.textconv=cat`, `-c diff.external=`/`-c diff.<n>.command=` for the external diff drivers plain `git diff` executes by default, `-c core.sshCommand=ssh`/`-c core.askPass=`/`-c credential.helper=` (+ a per-URL `credential.<url>.helper=` pin) for the transport keys the Git panel's remote operations reach, `-c protocol.git.allow=never` for `core.gitProxy` (no value of the key neutralizes it — git:// operations fail closed instead of executing the repository's proxy), a finding-gated `GIT_WORK_TREE` environment pin for `core.worktree` (no `-c` form beats the key; checkout/reset writes stay confined to the discovered repository root), and — narrowed per review [56] — `-c attr.tree=<empty-tree>` only when include directives exist: the one case per-name pins cannot cover, since an included file may hide a driver definition routed from the in-tree `.gitattributes` (the empty-tree hash matches the repository's object format, SHA-1 or SHA-256). Include-bearing configs additionally derive the name-independent pins `core.sshCommand=ssh`/`core.askPass=`/`credential.helper=`/`diff.external=` for command-bearing keys an included file may arm invisibly, and fail closed outright when the resolved git version is < 2.45 or unresolvable — older git silently ignores `attr.tree` (the version is probed once per process through the hardened chokepoint). Visible drivers no longer derive the blanket kill — their per-name pins cover every routing source, in-tree included — so benign eol/text attributes keep working and a CRLF-normalized repository does not show falsely-modified files or whole-file numstat diffs; while the kill IS active (includes, or a repo-set `attr.tree`) its collateral — normalization off, text files may report modified though unchanged — is disclosed in the intake warning (`(attributes disabled)` finding). Every porcelain `git diff` call site additionally passes `--no-ext-diff` so its output stays usable on armed repositories; the one patch producer without the flag — plumbing `diff-tree -p` — executes no external drivers absent an explicit `--ext-diff` (verified on git 2.50.1). Linked worktrees scan the common config (resolved through the worktree gitdir's `commondir`) plus the `config.worktree` overlay when `extensions.worktreeConfig` is enabled, merged last-wins like git. The attribute routing sources `attr.tree` does not cover — `.git/info/attributes` and the file named by `core.attributesFile` — are scanned too: every routed driver name gets the same `-c` name pins, and `core.attributesFile=` (empty) disables that source; an attribute source that exists but cannot be scanned fails the whole scan closed (no kill-switch exists for the mechanism). Unscannable config (unreadable, oversized, non-regular file, malformed pointer) → error, no git execution. All repo-scoped call sites in this domain (`git.go`, `vectorindex/git.go`) and the backend git-panel wrappers route through it.
- **Intake warning:** when a project is switched to or an auxiliary work directory is added, `notifyGitConfigRisk` (`backend/frontend_api_gitconfig_risk.go`) scans the config and emits the global `project:git_config_risk` event when it is not provably clean (dangerous keys, include directives — deliberately not followed — malformed or unreadable config). A clean repo emits nothing. Detection-only: neutralization in the spawn layers holds regardless of the UI.
- **Agent-side write gate (sp4rk):** mutating file tools refuse-to-execute-silently on any target inside a workspace `.git` tree — a hard `git_internal_path` judge reason forces the confirmation funnel under any group policy, so the agent cannot be steered into planting hooks/filters for later execution outside c0wrk.
- **Trade-off:** repository-defined hooks and filters — including legitimate ones like git-lfs — never run inside c0wrk; hooks are stripped and the strip is announced in the warning notice (strip-and-warn, never silent, never operation-failing).

### Vector Index

- Dual-indexed: chromem-go vector store (cosine similarity on ONNX embeddings) and bleve BM25 lexical store with a custom `c0wrk_code` analyzer (camelCase splitter → lowercase → stop-en). Both indices share a deterministic document ID (`sha256(path)[:8hex]:{chunkIndex}`); lexical hits enrich their content via `chromem.Collection.GetByID`.
- Embeddings computed via ONNX Runtime (local, no API calls)
- Model: quantized embedding model downloaded by `make fetch-embedding-model` (Unix) or the Makefile-routed `scripts/fetch-embedding-model.ps1` (Windows); the Makefile is the single source of truth — version/URL/dir values are passed from Makefile variables to the PowerShell recipe
- ONNX Runtime shared library: fetched by `make fetch-onnx` (Unix) or the Makefile-routed `scripts/fetch-onnx.ps1` (Windows); the Makefile pins `ONNX_VERSION` plus per-platform SHA256 digests of both the release archive and the extracted shared library, and is the single source of truth
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
- Vector documents are handed to chromem/ONNX in fixed sub-batches of at most 200 documents. Cancellation is checked between sub-batches, and the manager tracks/drains every indexing goroutine during teardown so no background writer outlives its collection.
- Non-UTF-8 file content (legacy single-byte encodings, corrupted bytes passing the NUL-header sniff) is sanitized in `processFile` before chunking — undecodable sequences become U+FFFD, matching the lossy conversion sp4rk's `Tokenizer.Encode` applies — because sugarme/tokenizer panics on invalid UTF-8. The recorded `content_hash` still covers the RAW on-disk bytes so `ValidateCollection` unchanged-file detection keeps matching.
- Content-triggered embedding failures are isolated, not pass-fatal: when an embedding chunk fails as a unit, each text is retried individually and only texts that also fail alone are dropped (WARN-logged) from BOTH the chromem commit and the lexical upsert, so the indexes never diverge; the file-hash sidecar still records the file (a deterministic content failure must not burn embedding work on every pass). If EVERY text in a chunk fails individually, the embedder itself is broken and the pass aborts with an error.
- Used for:
  - `semantic_search` tool (agent searches code by meaning; accepts `mode`, `must_match`, `file_pattern`, `top_k`)
  - File Viewer **Find Similar**: when text is selected, the context-menu action runs vector search with that selection and opens the standard vector-results panel
  - RAG hint injection before routing/planning (top-5 relevant files)

Lifecycle:

```
App started
  → ONNX embedder loads in background goroutine (after EventBackendReady)
  → Vector index manager created and wired into FrontendAPI via SetVectorManager()
  → Emit vector_index:status event (state=ready)

Project switched (after vector index ready)
  → No Project: switch to empty collection (clears stale CODE-project results)
  → vectorindex.CurrentBranch(ctx, workspacePath) via git CLI
  → core/vectorindex manager: SwitchBranch(branch) → Start indexing (background goroutine)
  → GitMonitor watches .git/HEAD for subsequent branch changes
  → Emit vector_index:status events (progress updates)
  → Index ready → semantic_search tool unblocked (via PreExecuteHook)
```

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
- Per-side filters (FilePattern, MustMatch) are applied BEFORE RRF fusion so vector and lexical rank spaces remain comparable
- Pre-fusion score thresholds discard noise-tail hits (low cosine similarity or low BM25) before RRF fusion so they cannot earn a double RRF contribution; thresholds apply only in the hybrid path, not vector-only or lexical-only modes
- A single `ready atomic.Bool` on `Service`; hybrid auto-falls-back to vector-only when `lexical.Count() == 0`
- The indexer rejects binary files via a bounded 512-byte header pre-read (null-byte presence, `binaryHeaderSize`) before loading the file into memory; the frontend file viewer detects binary content via null bytes in the first 8KB
- Every index path applies `max_file_size` before a full file read, and every chunked file stays at or below `max_chunks_per_file`; oversized/pathological files are skipped without aborting the pass
- Embedding writes use sub-batches of at most 200 documents, observe cancellation between batches, and all manager-launched indexing goroutines are drained before teardown
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
| Embedding sub-batch size | Internal constant (200 documents) | Bounds each chromem/ONNX `AddDocuments` call and cancellation interval |
| Hybrid RRF k     | `vector_index.hybrid_rrf_k` (default 60) | Reciprocal Rank Fusion constant k |
| Hybrid fanout    | `vector_index.hybrid_fanout_multiplier` (default 4) / `hybrid_fanout_min` (default 100) | Per-side candidate pool size for RRF |
| Vector score floor | `vector_index.hybrid_vector_score_floor` (default 0.0) | Absolute cosine-similarity floor; vector hits below this are discarded before fusion |
| Vector score ratio | `vector_index.hybrid_vector_score_ratio` (default 0.25) | Relative cosine cutoff; vector hits below ratio × top similarity are discarded before fusion |
| Lexical score ratio | `vector_index.hybrid_lexical_score_ratio` (default 0.1) | Relative BM25 cutoff; lexical hits below ratio × top BM25 are discarded before fusion |
| Hybrid toggle    | `vector_index.hybrid` (pointer bool, default true) | Master toggle: hybrid (vector + BM25) when true, vector-only when false |
| Embedding threads | `vector_index.embedding_threads` (default 0) | Caps the ONNX intra-op thread pool during indexing; 0 = all cores (default) |

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
- [tool-system/builtins.md](tool-system/builtins.md) — semantic_search tool
