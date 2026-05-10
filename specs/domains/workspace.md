# Workspace

## Purpose

Manages the project workspace: file tree loading, filesystem watching for changes, git status integration, and vector index for semantic code search.

## Key Files

- `backend/workspace/watcher.go` — filesystem watcher (fsnotify)
- `backend/workspace/filetree.go` — file tree loading (lazy, depth-limited)
- `backend/frontend_api_workspace.go` — FrontendAPI workspace methods
- `backend/vectorindex/index.go` — vector index management
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

### Git Status Integration

- `GetGitStatus()` returns per-file status (modified, added, deleted, untracked)
- `GetFileDiff(path)` returns unified diff for modified files
- Status integrated into file tree nodes (icon indicators in UI)
- Uses go-git library (pure Go, no git CLI dependency)

### Vector Index

- Indexes workspace files into a vector database (chromem-go, in-memory with persistence)
- Embeddings computed via ONNX Runtime (local, no API calls)
- Model: quantized embedding model downloaded by `make fetch-embedding-model`
- Used for:
  - `semantic_search` tool (agent searches code by meaning)
  - RAG hint injection before routing/planning (top-5 relevant files)

Lifecycle:

```
Project switched / app started
  → backend/vectorindex: Start indexing (background goroutine)
  → Emit vector_index:status events (progress updates)
  → Index ready → semantic_search tool unblocked (via PreExecuteHook)
```

### File Operations (Workspace API)

| Method                     | Description                                         |
| -------------------------- | --------------------------------------------------- |
| `GetFileTree(path, depth)` | Lazy directory tree with git status                 |
| `ReadFile(path)`           | Read file content (binary detection via null bytes) |
| `GetFileDiff(path)`        | Unified git diff for file                           |
| `GetGitStatus()`           | Workspace-level git status summary                  |
| `SearchFiles(query)`       | Filename search within workspace                    |

## Invariants

- File tree is always relative to active project's workspace path
- Watcher emits events only for the active project's workspace
- Vector index is rebuilt when project switches
- Binary files detected by null byte presence in first 8KB
- File operations are sandboxed to workspace path (no directory traversal)

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
