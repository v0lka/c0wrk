# Workspace

## Purpose

Manages the project workspace: file tree loading, filesystem watching for changes, git status integration, and vector index for semantic code search.

## Key Files

- `backend/workspace/watcher.go` — filesystem watcher (fsnotify)
- `backend/frontend_api_workspace.go` — FrontendAPI workspace methods + file tree loading + git CLI wrappers
- `backend/vectorindex/git.go` — git CLI wrapper for branch detection and branch-change monitoring
- `backend/vectorindex/manager.go` — vector index lifecycle (branch-partitioned collections)
- `backend/vectorindex/service.go` — vector index indexing and search
- `backend/vectorindex/collection.go` — collection data structure (per-branch document store)
- `backend/vectorindex/indexer.go` — file-to-document indexing logic
- `backend/vectorindex/search_result.go` — search result types and filtering
- `sdk/embedding/` — embedding model interface

## Core Types

```go
// FileNode — flat file/directory entry for tree display
type FileNode struct {
    Name       string
    Path       string
    IsDir      bool
    Hidden     bool
    GitStatus  string
    GitIgnored bool
}

// GitStatusEntry — per-file git status
type GitStatusEntry struct {
    Status    string // "M", "A", "D", "?"
    Staged    bool
    FileName  string
}

// VectorIndexStatus — indexing progress for frontend
type VectorIndexStatus struct {
    State        string
    Progress     float64
    FilesIndexed int
    TotalFiles   int
    CurrentFile  string
    Branch       string
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

- Indexes workspace files into a vector database (chromem-go, in-memory with persistence)
- Embeddings computed via ONNX Runtime (local, no API calls)
- Model: quantized embedding model downloaded by `make fetch-embedding-model`
- Collections are partitioned per git branch; switching branches produces a new collection
- Branch detection on project switch uses `vectorindex.CurrentBranch`; detection failure propagates and aborts the switch rather than silently degrading
- A `GitMonitor` watches `.git/HEAD` via fsnotify and triggers re-partitioning on branch change
- Used for:
  - `semantic_search` tool (agent searches code by meaning)
  - RAG hint injection before routing/planning (top-5 relevant files)

Lifecycle:

```
Project switched / app started
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

Filename search (`search_files`) is available as a built-in tool (`sdk/tools/builtins/file_search.go`), not as a direct Workspace API method.

## Invariants

- File tree is always relative to active project's workspace path
- Watcher emits events only for the active project's workspace
- Vector index collection is partitioned by git branch and rebuilt when the branch changes
- Binary files detected by null byte presence in first 8KB
- File operations are sandboxed to workspace path (no directory traversal)
- Every git invocation flows through `exec.CommandContext`; git errors propagate to the caller (no silent fallback)
- Missing `git` binary is a fatal startup condition, never a runtime surprise

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
