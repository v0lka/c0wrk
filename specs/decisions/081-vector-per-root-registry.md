# ADR-081: Per-Root Vector Index Registry

## Status

Accepted

## Context

The vector index was a process-wide singleton: one `vectorindex.Manager`
whose single active target was re-pointed on every send
(`maybeReScopeVectorIndexToSession`) and on project switch. Under
[worktree sessions](076-session-worktrees.md) that model breaks twice:

- **Agent-side wrongness.** A background session's `semantic_search` tool
  and RAG-hint injection resolved against whatever tree the singleton last
  targeted — not the session's own workspace. Two sessions on different
  trees could not search simultaneously without flipping the shared target,
  and every flip cancelled the outgoing root's in-flight index build.
- **User-side wrongness.** The Search sidebar (`SearchVectorStore`) followed
  the last driven session instead of the tree the Git panel displays.

The routing primitive already existed: the session manager stamps the task
context with the session's workspace root
(`sdktools.WithWorkspacePathNoProbe`) before `HandleMessage`, and both the
RAG-hint path and every tool execution run under that context.

## Decision

Replace the singleton with **one `vectorindex.Manager` per workspace root**,
owned by a backend registry (`backend/vector_roots.go`, `VectorRoots`):

1. **Agent routing is context-borne.** The shared search closures
   (`Application.buildVectorRouter` → `builder.RegisterVectorSearch`) resolve
   the manager from the executor context's workspace root
   (`sdktools.WorkspacePathFrom`). A session searches its own tree at any
   moment, regardless of which session is active, visible, or focused.
2. **User routing is focus-borne.** `SearchVectorStore`,
   `GetVectorIndexStatus`, and `ReindexVectorIndex` resolve the Git-panel
   focus root (`SetGitPanelFocus`) — the user searches what the panel
   displays. With no session context (RPC calls), the focus root is the only
   sensible target.
3. **One manager per root, ever.** Creation is single-flight per root under
   the registry lock; two managers can never open the same chromem
   persistent storage. A small LRU of live managers (focus pinned, capacity
   4) bounds RAM; evicted roots reopen transparently from their persisted
   gob state on the next routed search.
4. **Sends never mutate shared index state.** The send-time re-scope path is
   deleted outright — a send no longer re-points anything.
5. **Focus moves are non-destructive.** A project switch or
   `SetGitPanelFocus` creates the target root's manager on demand and leaves
   every other live manager running (a background session keeps searching
   its own tree while the user looks elsewhere). `vector_index:status`
   events stream only from the focused manager's callbacks, and a focus
   change emits the new manager's status snapshot once.
6. **Cache roots never nest.** Each root's embedding cache is a SIBLING of
   the project cache (`config.WorktreeEmbeddingCachePath`) because the cache
   accounting walk is recursive — one cache root containing another's would
   let eviction delete a sibling's entries.
7. **Bounded shutdown.** `ShutdownAll` (from `FrontendAPILifecycle.Cleanup`)
   closes every live manager, then the process-global embedder exactly once.
   A factory wired after shutdown closes the embedder itself, so quitting
   during the background ONNX init leaks nothing.

The desktop layer no longer builds per-call closures or stores a manager
pointer: the background init hands the registry a **manager factory** plus a
readiness channel (`SetVectorRootsFactory`), and the startup handshake
deferring the first project's setup drains once the factory lands.

## Consequences

- Concurrent sessions on different trees get simultaneously live, isolated,
  searchable index state; the focused tree is never evicted.
- RAM is bounded by live-managers × resident index (same order as the old
  park LRU allowed); evicted trees pay a gob-decode reopen on their next
  search.
- Cross-tree embedding dedup is deliberately reduced: each worktree root
  owns its cache dir (single-writer per cache instance beats cross-tree
  cache sharing now that managers run concurrently).
- `core/` and sp4rk are untouched: the router lives in `backend/`, and the
  `builtins.VectorSearchFunc` contract is unchanged.

## Alternatives Considered

- **Keep the singleton, re-point per search call** — rejected: switching
  cancels the previous root's in-flight index build, so concurrent sessions
  on different trees would starve each other's indexing, and every
  cross-session search would pay a target restore.
- **Per-session managers with per-session storage dirs** — rejected: when a
  session tree is also the focus target, two live managers would open the
  same chromem storage; per-ROOT identity is the collision-free key, and
  sessions sharing a tree share one index naturally.
- **Read-only session managers over the focus manager's index** — rejected:
  a background session's tree would only be indexed while focused,
  violating the always-correct routing requirement.
