# Workspace

## Purpose

Manages the project workspace: file tree loading, filesystem watching for changes, git status integration, and vector index for semantic code search.

## Key Files

- `core/workspace/watcher.go` — filesystem watcher (fsnotify)
- `core/workspace/filetree.go` — file tree building logic (lazy listing with git ignores)
- `core/workspace/git.go` — git CLI wrappers (status, diff, gitignore parsing, branch detection)
- `backend/frontend_api_workspace.go` — FrontendAPI workspace methods (thin delegation to core/workspace)
- `sdk/vectorindex/git.go` — git CLI wrapper for branch detection and branch-change monitoring
- `sdk/vectorindex/manager.go` — vector index lifecycle (branch-partitioned collections)
- `sdk/vectorindex/service.go` — vector index indexing and search
- `sdk/vectorindex/collection.go` — collection data structure (per-branch document store)
- `sdk/vectorindex/indexer.go` — file-to-document indexing logic (dual-writes chromem + lexical)
- `sdk/vectorindex/search_result.go` — search result types and filtering
- `sdk/vectorindex/hybrid.go` — hybrid search (vector ∪ lexical) with RRF fusion, per-side filters
- `sdk/vectorindex/lexical/` — bleve/BM25 lexical index with `c0wrk_code` analyzer (camelCase split + lowercase + stop-en)
- `sdk/embedding/` — embedding model interface
- `backend/frontend_api_vector.go` — FrontendAPI vector methods + lazy manager access (SetVectorManager/getVectorManager with RWMutex)

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
    Status    string // "M", "A", "R", "C", or "U"
    Staged    bool
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

### Git Integration

- `GetGitStatus()` returns per-file status (modified, added, deleted, untracked)
- `GetFileDiff(path)` returns unified diff for modified files
- Status integrated into file tree nodes (icon indicators in UI)
- `.gitignore` filtering in directory listings uses `git ls-files --others --ignored --exclude-standard --directory -z`
- `vectorindex.CurrentBranch(ctx, repoPath)` detects the active branch via `git symbolic-ref --short HEAD` (falls back to `git rev-parse --short=12 HEAD` for detached HEAD)
- All git calls use `exec.CommandContext(ctx, "git", ...)` with stdout/stderr capture; errors are propagated, never swallowed
- Non-repository paths are distinguished from failures by matching `"not a git repository"` in stderr; a legitimate non-repo returns an empty result, any other error is returned to the caller
- The `git` binary is a hard runtime dependency — its absence is detected at startup by `desktop.verifyExternalDependencies` (fatal modal + quit), not at call sites

### Vector Index

- Dual-indexed: chromem-go vector store (cosine similarity on ONNX embeddings) and bleve BM25 lexical store with a custom `c0wrk_code` analyzer (camelCase splitter → lowercase → stop-en). Both indices share a deterministic document ID (`sha256(path)[:8hex]:{chunkIndex}`); lexical hits enrich their content via `chromem.Collection.GetByID`.
- Embeddings computed via ONNX Runtime (local, no API calls)
- Model: quantized embedding model downloaded by `make fetch-embedding-model`
- Collections are partitioned per git branch; switching branches produces a new pair of collections (`vector/` and `lexical/<branch>/`)
- Branch detection on project switch uses `vectorindex.CurrentBranch`; detection failure propagates and aborts the switch rather than silently degrading
- A `GitMonitor` watches `.git/HEAD` via fsnotify and triggers re-partitioning on branch change
- Hybrid search fuses the two ranked lists via Reciprocal Rank Fusion (`score = Σ 1/(k+rank)`, `k=60`). Per-side fanout is `max(topK*4, 100)`; `FilePattern` (doublestar glob) and `MustMatch` post-filters are applied **before** fusion so rank spaces are comparable.
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
  → vectorindex.CurrentBranch(ctx, workspacePath) via git CLI
  → backend/vectorindex: SwitchBranch(branch) → Start indexing (background goroutine)
  → GitMonitor watches .git/HEAD for subsequent branch changes
  → Emit vector_index:status events (progress updates)
  → Index ready → semantic_search tool unblocked (via PreExecuteHook)
```

### File Operations (Workspace API)

| Method                              | Description                                         |
| ----------------------------------- | --------------------------------------------------- |
| `ListDirectory(dirPath, recursive)` | Lazy directory tree with git status                 |
| `ReadFile(path)`                    | Read file content (binary detection via null bytes) |
| `GetFileDiff(path)`                 | Unified git diff for file                           |
| `GetGitStatus()`                    | Workspace-level git status summary                  |

Filename search is available via the `glob` built-in tool (`sdk/tools/builtins/glob.go`), not as a direct Workspace API method.

## Invariants

- File tree is always relative to active project's workspace path
- Watcher emits events only for the active project's workspace
- Vector index collection is partitioned by git branch and rebuilt when the branch changes
- Chromem is the source of truth; lexical is a best-effort mirror reconciled via `RebuildLexical`
- Per-side filters (FilePattern, MustMatch) are applied BEFORE RRF fusion so vector and lexical rank spaces remain comparable
- A single `ready atomic.Bool` on `Service`; hybrid auto-falls-back to vector-only when `lexical.Count() == 0`
- Binary files detected by null byte presence in first 8KB
- File operations are sandboxed to workspace path (no directory traversal)
- Every git invocation flows through `exec.CommandContext`; git errors propagate to the caller (no silent fallback)
- Missing `git` binary is a fatal startup condition, never a runtime surprise
- ONNX embedder loading runs asynchronously after EventBackendReady; it never blocks the critical startup path
- Vector search RPC returns empty results (not an error) if invoked before the embedder is ready

## Configuration

| Parameter        | Source                | Description                        |
| ---------------- | --------------------- | ---------------------------------- |
| Workspace path   | Active project config | Root directory for file operations |
| Ignore patterns  | config.yaml           | Patterns excluded from tree/index  |
| Index chunk size | Internal (hardcoded)  | Characters per embedding chunk     |

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
