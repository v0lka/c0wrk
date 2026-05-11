# Workspace

## Purpose

Manages the project workspace: file tree loading, filesystem watching for changes, git status integration, and vector index for semantic code search.

## Key Files

- `backend/workspace/watcher.go` — filesystem watcher (fsnotify)
- `backend/frontend_api_workspace.go` — FrontendAPI workspace methods + file tree loading + git CLI wrappers
- `backend/vectorindex/git.go` — git CLI wrapper for branch detection and branch-change monitoring
- `backend/vectorindex/manager.go` — vector index lifecycle (branch-partitioned collections)
- `backend/vectorindex/service.go` — vector index indexing and search
- `sdk/embedding/` — embedding model interface

## Behavior

### File Tree

- Loaded lazily on frontend request (not preloaded)
- Depth-limited to avoid scanning huge directories
- Excludes: `.git/`, `node_modules/`, `build/`, `.cache/`, etc. (configurable ignores)
- Returns `FileNode` tree: name, path, isDir, children, gitStatus

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
| `SearchFiles(query)`                | Filename search within workspace                    |

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
| Index chunk size | config.yaml           | Characters per embedding chunk     |

## Related Specs

- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — workspace RPC surface
- [../contracts/event-catalog.md](../contracts/event-catalog.md) — workspace:tree_changed, vector_index:status
- [tool-system/builtins.md](tool-system/builtins.md) — semantic_search tool
