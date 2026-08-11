# ADR-009: Extract Domain Logic from Backend to Core

## Status

Superseded by [ADR-011](./011-sp4rk-to-core-extraction.md)

> vectorindex and proxy were subsequently moved from sp4rk to `core/`.

## Context

The layer architecture spec defines `backend/` as a ViewModel — the thin "app layer" that the desktop UI interacts with, responsible for path validation, session resolution, icon assignment, and delegating to domain services. In practice, three packages within `backend/` contained pure domain logic with no ViewModel concerns:

- `backend/vectorindex/` (~2000 LOC) — embedding, BM25+chromem hybrid RRF search, git branch monitoring for index freshness
- `backend/terminal/` (~285 LOC) — PTY lifecycle management, shell environment, I/O
- `backend/workspace/` (~173 LOC) — fsnotify watcher with debouncing

Additionally, `backend/frontend_api_workspace.go` (~574 LOC) contained git operations (status, diff, gitignore parsing) and file tree building logic — all domain logic that should live in `core/`.

These packages had zero dependency on `backend/` types (they only imported `core/` and sp4rk), confirming they were incorrectly placed. The only thing tying them to `backend/` was their directory location.

## Decision

Move all three packages and extract the git/filetree logic into `core/`:

| Source | Destination (at ADR-009 time) |
|--------|-------------|
| `backend/vectorindex/` | sp4rk `vectorindex/` (later moved to `core/vectorindex/` per ADR-011) |
| `backend/terminal/` | `core/terminal/` |
| `backend/workspace/` | `core/workspace/` |
| `backend/frontend_api_workspace.go` (git logic) | `core/workspace/git.go` |
| `backend/frontend_api_workspace.go` (filetree logic) | `core/workspace/filetree.go` |

Type aliases in `backend/api_types.go` preserve backward compatibility:

```go
type FileNode = workspace.FileNode
type GitStatusEntry = workspace.GitStatusEntry
```

Backend ViewModel methods now delegate to core domain functions:
- `GetGitStatus()` → `workspace.GitStatus()`
- `GetFileDiff()` → `workspace.GetFileDiff()`
- `ListDirectory()` → `workspace.ListDirFlat()` / `workspace.ListDirRecursive()`

Desktop imports were updated from `backend/vectorindex` → sp4rk `vectorindex` (subsequently `core/vectorindex` per ADR-011) and `backend/terminal` → `core/terminal`. Per ADR-008, desktop may import `core/` directly — this is cleaner because desktop now imports domain logic from its proper layer.

## Consequences

**Positive:**

- Layer boundaries match the spec — `backend/` contains only ViewModel code (path validation, session resolution, icon assignment, delegation)
- Core packages can be tested independently without instantiating a `FrontendAPI`
- Cleaner import graph: `desktop/` → `core/` (domain) is more honest than `desktop/` → `backend/` (ViewModel) for domain types
- `backend/frontend_api_workspace.go` reduced from ~574 to ~230 lines
- Git repo cache (a pure ViewModel concern) stays in backend; domain functions are stateless and cache-independent

**Negative:**

- Two additional packages in `core/` increase its surface area
- Type aliases in `backend/api_types.go` add one level of indirection (mitigated by Go's transparent alias resolution)

## Related

- Supersedes the implicit placement of domain services in `backend/`
- Follows [ADR-008](008-backend-sp4rk-direct-import.md) — desktop and backend may import core directly
