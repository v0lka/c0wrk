# Workspace

## Purpose

Manages the project workspace: file tree loading, filesystem watching for changes, git status integration, and vector index for semantic code search.

## Key Files

- `core/workspace/watcher.go` — filesystem watcher (fsnotify)
- `core/workspace/filetree.go` — file tree building logic (lazy listing with git ignores)
- `core/workspace/git.go` — git CLI wrappers (status, diff, gitignore parsing, branch detection)
- `backend/frontend_api_workspace.go` — FrontendAPI workspace methods (thin delegation to core/workspace)
- `core/vectorindex/git.go` — git CLI wrapper for branch detection and branch-change monitoring
- `core/vectorindex/manager.go` — vector index lifecycle (branch-partitioned collections)
- `core/vectorindex/service.go` — vector index indexing and search
- `core/vectorindex/collection.go` — collection data structure (per-branch document store)
- `core/vectorindex/indexer.go` — file-to-document indexing logic (dual-writes chromem + lexical)
- `core/vectorindex/search_result.go` — search result types and filtering
- `core/vectorindex/hybrid.go` — hybrid search (vector ∪ lexical) with RRF fusion, per-side filters
- `core/vectorindex/lexical/` — bleve/BM25 lexical index with `c0wrk_code` analyzer (camelCase split + lowercase + stop-en)
- `sdk/embedding/` — embedding model interface
- `backend/frontend_api_vector.go` — FrontendAPI vector methods + lazy manager access (SetVectorManager/getVectorManager with RWMutex)
- `backend/api_types.go` — `VectorIndexStatus` struct (shared API response type, also used by frontend_api_vector.go)

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
- Excludes: `.git/`, `node_modules/`, `build/`, `.cache/`, etc. (configurable ignores)
- Returns `FileNode` list: name, path, isDir, hidden, gitStatus, gitIgnored (flat list, no children)

### Filesystem Watcher

```
backend/workspace.StartWatcher(projectPath)
  → fsnotify watches project directory (recursive)
  → On file change:
      ├─ Debounce (batch rapid changes)
      ├─ Emit global event: workspace:tree_changed
      └─ Frontend: fileTreeStore refreshes affected subtree
```

File watching is enabled for No Project using the active session's
workspace directory (`__no_project__/<sessionID>/workspace/`) as the
watcher root (falling back to the project base directory when no
session exists). Git operations and vector indexing remain skipped.

### Git Integration

- `GetGitStatus()` returns per-file status (modified, added, deleted, untracked)
- `GetFileDiff(path)` returns unified diff for modified files
- Status integrated into file tree nodes (icon indicators in UI)
- `.gitignore` filtering in directory listings uses `git ls-files --others --ignored --exclude-standard --directory -z`
- `vectorindex.CurrentBranch(ctx, repoPath)` detects the active branch via `git symbolic-ref --short HEAD` (falls back to `git rev-parse --short=12 HEAD` for detached HEAD)
- All git calls use `exec.CommandContext(ctx, "git", ...)` with stdout/stderr capture; errors are propagated, never swallowed
- Non-repository paths are distinguished from failures by matching `"not a git repository"` in stderr; a legitimate non-repo returns an empty result, any other error is returned to the caller
- The `git` binary is required for CODE mode. Its absence is detected on project switch in `backend/frontend_api_project.go` via `exec.LookPath`, emitting a `runtime_error` event (dismissable toast) and rejecting the switch. CHAT mode (No Project) never invokes git.
- No Project: `isGitRepo()` returns false (no git operations), `GetGitStatus` returns empty map, `GetFileDiff` returns empty string

### Vector Index

- Dual-indexed: chromem-go vector store (cosine similarity on ONNX embeddings) and bleve BM25 lexical store with a custom `c0wrk_code` analyzer (camelCase splitter → lowercase → stop-en). Both indices share a deterministic document ID (`sha256(path)[:8hex]:{chunkIndex}`); lexical hits enrich their content via `chromem.Collection.GetByID`.
- Embeddings computed via ONNX Runtime (local, no API calls)
- Model: quantized embedding model downloaded by `make fetch-embedding-model`
- Collections are partitioned per git branch; switching branches produces a new pair of collections (`vector/` and `lexical/<branch>/`)
- Storage location: `~/.c0wrk/projects/<projectID>/vector_index/` (per-project, co-located with session data)
- Branch detection on project switch uses `vectorindex.CurrentBranch`; detection failure propagates and aborts the switch rather than silently degrading
- A `GitMonitor` watches `.git/HEAD` via fsnotify and triggers re-partitioning on branch change
- Hybrid search fuses the two ranked lists via Reciprocal Rank Fusion (`score = Σ 1/(k+rank)`, `k=60` by default). Per-side fanout is `max(topK × fanout_multiplier, fanout_min)` (defaults 4 / 100); `FilePattern` (doublestar glob) and `MustMatch` post-filters are applied **before** fusion so rank spaces are comparable. Pre-fusion **score thresholds** discard noise-tail hits before fusion: vector hits below `hybrid_vector_score_floor` (absolute) or `hybrid_vector_score_ratio × top similarity` (relative, default 0.25) are dropped; lexical hits below `hybrid_lexical_score_ratio × top BM25` (relative, default 0.1) are dropped. This prevents weakly semantic chunks that merely contain a query term from earning a double RRF contribution and jumping above one-sided relevant hits. Thresholds apply only in the hybrid path; vector-only and lexical-only modes return raw top-K without score gating. See ADR-013.
- Auto-fallback: `ModeHybrid` silently degrades to `ModeVector` when the lexical index is empty or unavailable; `ModeLexical` returns empty (or an error if no lexical index has been opened at all).
- Dual-write invariant: chromem commits first (source of truth); lexical upsert/delete is best-effort and drift is repaired by `Indexer.RebuildLexical`, which the manager invokes during `SwitchProject` when `chromem.Count() > 0 && lexical.Count() == 0`.
- Indexing progress is phased: `PhaseBoth` (parallel full index of a fresh project), `PhaseEmbedding` (chromem-only chunk), `PhaseLexical` (bleve backfill). The phase is surfaced to the frontend via the `vector_index:status` event `phase` field.
- Used for:
  - `semantic_search` tool (agent searches code by meaning; accepts `mode`, `must_match`, `file_pattern`, `top_k`)
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
| `ListDirectory(dirPath, recursive)` | Lazy directory tree with git status                 |
| `ReadFile(path)`                    | Read file content (no backend-side binary detection; frontend `isBinaryContent` checks null bytes in first 8KB) |
| `GetFileDiff(path)`                 | Unified git diff for file                           |
| `GetGitStatus()`                    | Workspace-level git status summary                  |

Filename search is available via the `glob` built-in tool (`sdk/tools/builtins/glob.go`), not as a direct Workspace API method.

## Invariants

- File tree is always relative to active project's workspace path
- Watcher emits events only for the active project's workspace
- Vector index collection is partitioned by git branch and rebuilt when the branch changes
- Chromem is the source of truth; lexical is a best-effort mirror reconciled via `RebuildLexical`
- Per-side filters (FilePattern, MustMatch) are applied BEFORE RRF fusion so vector and lexical rank spaces remain comparable
- Pre-fusion score thresholds discard noise-tail hits (low cosine similarity or low BM25) before RRF fusion so they cannot earn a double RRF contribution; thresholds apply only in the hybrid path, not vector-only or lexical-only modes
- A single `ready atomic.Bool` on `Service`; hybrid auto-falls-back to vector-only when `lexical.Count() == 0`
- Binary files detected by null byte presence in first 8KB
- File operations are sandboxed to workspace path (no directory traversal)
- Every git invocation flows through `exec.CommandContext`; git errors propagate to the caller (no silent fallback)
- Missing `git` binary is a fatal startup condition, never a runtime surprise
- ONNX embedder loading runs asynchronously after EventBackendReady; it never blocks the critical startup path
- Vector search RPC returns an error if invoked before the embedder is ready (graceful "vector search not available" error, not empty results)
- No Project: git operations and vector indexing are skipped (deactivated at the FrontendAPI layer). File watching is scoped to the active session's workspace directory (`__no_project__/<sessionID>/workspace/`), falling back to the project base directory when no session exists.
- No Project: git status and diff always return empty (no git process spawned)
- No Project: vector search never returns results (indexer is never started, semantic_search tool is disabled anyway)

## Configuration

| Parameter        | Source                | Description                        |
| ---------------- | --------------------- | ---------------------------------- |
| Workspace path   | Active project config | Root directory for file operations |
| Ignore patterns  | config.yaml           | Patterns excluded from tree/index  |
| Index chunk size | Internal (hardcoded)  | Characters per embedding chunk     |
| Hybrid RRF k     | `vector_index.hybrid_rrf_k` (default 60) | Reciprocal Rank Fusion constant k |
| Hybrid fanout    | `vector_index.hybrid_fanout_multiplier` (default 4) / `hybrid_fanout_min` (default 100) | Per-side candidate pool size for RRF |
| Vector score floor | `vector_index.hybrid_vector_score_floor` (default 0.0) | Absolute cosine-similarity floor; vector hits below this are discarded before fusion |
| Vector score ratio | `vector_index.hybrid_vector_score_ratio` (default 0.25) | Relative cosine cutoff; vector hits below ratio × top similarity are discarded before fusion |
| Lexical score ratio | `vector_index.hybrid_lexical_score_ratio` (default 0.1) | Relative BM25 cutoff; lexical hits below ratio × top BM25 are discarded before fusion |

## Extension Points

- **Custom ignore patterns**: add patterns to config.yaml to exclude directories from tree and index
- **Alternative embedding model**: replace ONNX Runtime with a different model by implementing the embedding interface
- **Vector store backend**: replace chromem-go with an alternative vector database by implementing the service interface
- **Git monitor hooks**: add custom callbacks on branch change detected by `GitMonitor`
- **Additional file attributes**: extend `FileNode` with custom fields and populate them in `ListDirectory()`

## Related Specs

- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — workspace RPC surface
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — workspace:tree_changed, vector_index:status
- [tool-system/builtins.md](tool-system/builtins.md) — semantic_search tool
