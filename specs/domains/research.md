# RESEARCH Mode

## Purpose

RESEARCH mode is a project-scoped methodology workspace for maintaining research briefs, prior art, hypothesis cards, a hypothesis DAG, progress metrics, and synthesis reports. It is always on for every real project — there is no per-project toggle: it parses Markdown/Mermaid artifacts under the workspace-contained research root `<workspace>/.research`, seeds versioned `research-*` skills (globally, once at app startup), and exposes the same active-project graph to the orchestrator and frontend.

## Key Files

- `core/research/model.go` - hypothesis lifecycle types, graph traversal, and metric computation
- `core/research/parser.go` - pure Markdown/Mermaid parsers and best-effort research-root filesystem parsing
- `core/research/rootwriter.go` - root-level mutations: `index.md` row moves for activation, minimal row/index creation, and project deletion with containment
- `core/research/recommend.go` - pure next-step recommendation, project-wide and hypothesis-scoped
- `core/research/skillpack.go` - embedded seven-skill research pack and non-destructive versioned seeding
- `core/research/skills/` - embedded `research-*` skill sources
- `core/papers/skillpack.go` - the embedded `study-paper` pack; seeded globally by the same startup seed into the c0wrk global skills directory (which outranks a same-named `~/.agents` skill in discovery)
- `backend/frontend_api_research.go` - always-on status/graph RPC behavior, workspace containment, and `seedGlobalPacks` — the single global pack seed run once per launch
- `backend/frontend_api_project.go` - recursive research-tree watcher integration and incremental file-change emission
- `frontend/src/components/research/index.tsx` - Research panel (the `[Dashboard | Papers]` segmented control + graph/status presentation)
- `frontend/src/components/papers/PapersView.tsx` - Papers segment: the `study-paper` invocation surface over the studied-paper list, plus multi-select ("Compare selected", enabled at ≥2 papers) that dispatches a library comparison
- `frontend/src/components/papers/CompareMatrix.tsx` - paper Compare section renderer: a multi-paper comparison artifact (`<research-root>/comparisons/<slug>.md`) rendered as the papers table + dimension × paper matrix + the fairness / agreement / gaps / synthesis-verdict sections (pure parsing in `frontend/src/lib/paperComparison.ts`)
- `frontend/src/components/papers/useComparisons.ts` - loads every comparison artifact under `<research-root>/comparisons/` for the Compare section

- `frontend/src/components/research/PriorArtRow.tsx` - the research dashboard's prior-art row: Open (the raw `prior-art.md`) plus Deep read (dispatches `study-paper` at the forced deep-appraisal depth over the catalog's referenced works)
- `frontend/src/components/papers/paperActions.ts` - the audit module for every paper-study dispatch prompt — the Papers panel's field/row gestures AND the invoke-from-anywhere surfaces (the file-tree PDF menu, the chat-attachment Study action, the prior-art Deep read)
- `frontend/src/lib/papersByHypothesis.ts` - the reverse paper ← hypothesis projection (pure `buildPapersByHypothesis` fold + `useInformingPapers`/`useDanglingPaperLinks` hooks); `frontend/src/components/research/InformingPapers.tsx` - its two rendered faces (the hypothesis card's "Informing papers (N)" section and the research dashboard's dangling-link warning)

## Core Types

```go
type HypothesisStatus string // open | in-progress | confirmed | refuted | cancelled

type HypothesisNode struct {
    ID       string
    Title    string
    Status   HypothesisStatus
    Parents  []string
    Timebox  string
    Result   string
}

type HypothesisGraph struct {
    Nodes []*HypothesisNode
    Edges []HypothesisEdge
}

type Metrics struct {
    Total            int
    ByStatus         map[HypothesisStatus]int
    ConfirmationRate float64
    Depth            int
    Breadth          int
    ActiveFront      []string
}

type ResearchProject struct {
    ID            string
    Brief         Brief
    Graph         HypothesisGraph
    Metrics       Metrics
    PriorArtCount int
    HasReport     bool
}

type ResearchRoot struct {
    Path            string
    Index           []IndexEntry
    Projects        []*ResearchProject
    ActiveProjectID string
}
```

Hypothesis IDs normalize to `H-NNN`; research IDs normalize to `R-NNN`. `open` and `in-progress` form the active front, while `confirmed`, `refuted`, and `cancelled` are terminal.

## Flow

```
App startup
  -> seedGlobalPacks seeds every c0wrk-owned pack into ~/.c0wrk/.agents:
     the seven research-* skills AND the study-paper skill into
     .agents/skills, the research Subagent Profile into .agents/agents
     (content-hash-verified, staged + atomically swapped, non-destructive)
     — BEFORE the skill/agent directory watchers are created
  -> invalidate the skill and agent caches (the freshly created directories
     are themselves watched on the same launch)

Open / switch to a real project (incl. the app-startup restore replay)
  -> the research root is unconditionally <workspace>/.research
     (config.ProjectResearchPath) — RESEARCH is always on, no persisted
     per-project root, no reconcile/seed on switch
  -> the project's recursive watcher covers the research root's current and
     future directories; the frontend loads the panel via GetResearchStatus
  -> nothing is seeded on the switch

Research artifact changes
  -> recursive workspace watcher batches changed paths
  -> emit research:file_changed for the active project
     (workspace:tree_changed is annotated research_scoped=true — true when
      at least one changed path was inside the research root — so the
      frontend skips its immediate full refetch: the incremental path owns it)
  -> frontend calls GetResearchGraph
  -> parse full root, select active R-NNN, return lightweight graph + metrics
  -> loadGraph applies the update and follows a changed active R-NNN
     (the response's PickActiveProject choice is newer than the cached
      snapshot); an unknown brand-new R-NNN, a snapshot fetched before the
      store's last sync (stale — a slow fetch resolving after a newer sync
      must not regress the panel), or a failed RPC falls back to a
      full GetResearchStatus refetch
  -> watchdog: the delayed check in useResearchStatusEvents runs a full
     refetch unless a successful incremental sync (lastGraphSyncAt) landed
     after the research_scoped tree change — the panel always converges
```

Both frontend sync paths are mounted exactly once at the App root
(`ResearchEventBridge`); the Research panel
and the workspace tab are pure views over `researchStore` (and the panel's
Papers segment over `paperStore`) and never mount the
hooks themselves (a double mount would duplicate every watchdog and fallback
refetch). The bridge additionally mounts `usePapersEvents`, which loads the
active project's paper library and re-fetches it on `papers:changed` — the
library is watched independently of any particular `R-NNN` research project. The workspace's hypothesis selection is keyed to the research
project it was made in (`selectedHypothesisProjectId`): an active-R-NNN
switch leaves a stale selection — and its unsaved draft — unrendered instead
of rebinding it to the new project's same-id card.

The canonical nested artifact shape is:

```
<research-root>/
  index.md
  R-NNN-<slug>/
    brief.md
    prior-art.md
    report.md                 (optional)
    hypotheses/
      graph.md                (Mermaid graph + catalog)
      H-NNN.md                (hypothesis cards)
```

Missing optional artifacts produce a valid partial model: an empty hypothesis graph and zero metrics are normal states.

The paper library and multi-paper comparisons are global siblings of the research projects — both direct children of `<research-root>`: `papers/<slug>/` (the studied-paper cards) and `comparisons/<slug>.md` (one multi-paper comparison artifact per set, written by the study-paper Compare intent).

The active project is the project referenced by the last chronological `index.md` entry when that project exists; otherwise it is the highest-numbered parsed `R-NNN` directory. `ResearchRoot.ActiveProjectID`, orchestrator research context, and the frontend panel all use this selection rule.

Metrics are derived from the reconciled graph:

- `confirmation_rate = confirmed / (confirmed + refuted)`, or zero before any verdict
- `depth` is the longest root-to-leaf path measured in edges
- `breadth` is the widest depth level containing non-terminal hypotheses
- `active_front` is the sorted set of `open` and `in-progress` hypothesis IDs

## Invariants

- RESEARCH mode is always on for real projects: there is no per-project toggle and no persisted root. The No-Project pseudo-project has no workspace, so RESEARCH is unavailable there (`loadProjectForResearch` rejects it) and the panel renders a neutral empty state.
- RESEARCH mode is not gated by the `experimental.enabled` switch (which gates only the E2S execution mode) — it stays available for every real project.
- The research root is unconditionally `<workspace>/.research` (`config.ProjectResearchPath`) — an absolute, workspace-contained path, never persisted per project and with no user override.
- `seedGlobalPacks` runs once per launch from `NewFrontendAPI`, BEFORE the skill and agent directory watchers are created, so the c0wrk global directories (`~/.c0wrk/.agents/{skills,agents}`) are freshly seeded and watched on the same launch; it serializes on `researchSeedMu` and invalidates the skill/agent caches afterwards. It is idempotent: a second run re-seeds nothing new and never duplicates domain state.
- Skill/agent discovery precedence is project-local `<workspace>/.agents/{skills,agents}` → c0wrk global `~/.c0wrk/.agents/{skills,agents}` → user `~/.agents/{skills,agents}`: the c0wrk global directory outranks the user's portable `~/.agents` library, so a seeded c0wrk pack wins over a stale same-named user copy.
- A project switch seeds nothing — `reconcileResearchPacks` is gone; the global startup seed is the only c0wrk pack writer.
- Skill/agent seeding classifies each destination by CONTENT HASH against the embedded pack (never mtime/size, never the marker alone): content equal to the pack is Current (a missing/stale `.seed-version` marker on it is re-stamped); a pack-marked truncated subset of the pack (interrupted write) is repaired; a pack-marked same-version directory whose content diverges from the pack is a local edit (or a spoofed marker) — preserved untouched and reported `Modified`; a marker-less diverging directory is user-owned and preserved; a marker from an older pack version is overwritten in full.
- Seeding writes are crash-safe: each entry is staged in a hidden sibling temp directory and swapped in with a single rename, so an interrupted run never leaves a truncated tree at the destination; staging/backup leftovers from a hard kill are swept on the next seeding run.
- A global seeding failure is per-pack and logged, and is never fatal to startup; outcomes are reported through structured logs (per-pack `Seeded`/`Updated`/`Current`/`Preserved` counts), not through an RPC result.
- Root/project parsing is best-effort: malformed or missing optional artifacts do not invalidate other parseable projects or cards.
- Hypothesis nodes and edges are normalized, de-duplicated, and deterministically ordered; malformed cycles terminate metric traversal without unbounded recursion.
- The recursive watcher covers existing and newly created subdirectories beneath the active research root.
- `GetResearchGraph` and `GetResearchStatus` parse the same full root; the graph RPC reduces wire payload, not parse cost.
- The active-project selection rule is shared by backend orchestration and frontend presentation.
- Hypothesis mutation RPCs (`UpdateHypothesis`/`CreateHypothesis`) serialize their whole load→mutate→write chain under one mutex per research root (per `FrontendAPI`), so concurrent calls cannot lose card/graph updates or duplicate the max+1 H-NNN id assignment. The lock is in-process only — a second app instance sharing a workspace is not covered.
- `UpdateHypothesis` carries the caller's expected R-NNN and resolves it inside the requesting project's own research root before mutating: a foreign or malformed R-NNN is rejected before any file is touched, and the update targets the expected project rather than blindly following the backend's active one (which may have changed since the caller loaded its graph).
- `UpdateHypothesis` mutates the card's editable fields (title, status, result, timebox, decision, statement, verification criterion, experiment notes, parents). Status transitions follow the methodology's state machine (no backward jumps); a Parents update is validated against the reconciled graph — every parent must exist, self-reference is rejected, and a parent that would close a cycle is rejected — before any write, and is synchronized across the card's Parent(s) row, the Mermaid diagram's incoming edges (adding missing node definitions for card-only parents), and the catalog's Parent(s) column. Any invalid update returns an error and leaves every file byte-for-byte unchanged.
- `SetActiveResearch` makes an R-NNN active by rewriting `index.md` so its row is the last table entry — the chronological rule `PickActiveProject` applies. A missing row gets a minimal brief-linking row appended, and a missing `index.md` is created with the canonical skeleton; the rid must resolve under the requesting research root (the `ProjectDir` ownership check) before any file is touched, and the write goes through the same atomic temp+rename path as the hypothesis mutations.
- `DeleteResearchProject` removes every `index.md` entry line for the project and its `R-NNN-*` directory tree. The project must resolve under the requesting research root, and the symlink-resolved project directory must sit strictly inside the symlink-resolved research root (a directory equal to the root is rejected) before anything is touched; `os.RemoveAll` runs on the validated resolved location and unlinks symlinked children rather than following them.
- The root-level RPCs (`SetActiveResearch`/`DeleteResearch`) mirror the hypothesis-mutation posture: the research root is workspace-containment-checked, the whole resolve→write chain runs under the per-root mutation mutex, and the caller's R-NNN is ownership-checked via `ProjectDir` before any file is touched. Both return the refreshed `ResearchStatusDTO` (RESEARCH stays on after deleting the last project) and emit `research:changed` (action=`active_changed` / `project_deleted`).
- Pins (`ProjectInfo.ResearchPins`) store research-root-relative, forward-slash document paths: a pinned research project records its brief (`R-NNN-*/brief.md`), a pinned hypothesis card records `R-NNN-*/hypotheses/H-NNN.md` keyed by hypothesis id (a list per key — the same H-NNN exists across R-NNN projects). The pin RPCs are idempotent in both directions, ownership-check the R-NNN, serialize under the per-root mutation mutex (against `DeleteResearch`'s pin cleanup, so a concurrent pin change cannot resurrect a deleted project's pins), and emit no event. Pinning a card requires the card file to exist; unpinning tolerates a deleted card, so stale pins stay removable. `DeleteResearch` removes every pin under the deleted R-NNN's directory and persists the cleaned record.
- `ResearchStatusDTO.pinned_research`/`pinned_hypotheses` mirror the persisted pins in `GetResearchStatus`, `SetActiveResearch`, and `DeleteResearch`, normalized to non-nil collections (empty `[]`/`{}`, never `null`).
- `RecommendNextStepForHypothesis` scopes the next-step recommendation to one hypothesis: open/in-progress → `research-experiment` on it; terminal without a recorded Decision → `research-decision` on it; otherwise (unknown hypothesis, nil project, empty ID, or a terminal hypothesis already carrying a Decision) the plain `RecommendNextStep` result. `GetResearchNextStep(projectID, hypothesisID)` exposes this at the RPC boundary: an empty `hypothesisID` means the project-level recommendation.
- `ResearchNextStepDTO.project_id` (and `ResearchGraphDTO.project_id`) are dual-namespace by design: they name the recommendation's/graph's subject — the active R-NNN when one exists, the c0wrk project UUID otherwise (the pre-R-NNN setup state). They are not stable identities for the requesting c0wrk project; frontend state keying must not rely on them across a project switch (the research store drops the recommendation and selection on cross-project loads instead).

- Every `study-paper` dispatch surface is fail-closed: the file-tree "Study this paper…" item renders only for PDFs, the chat-attachment Study action only on PDF attachments, and the prior-art Deep-read row only when the active project has prior art — an unavailable input never offers a dispatch, and the file tree's depth picker dispatches nothing unless a depth is chosen.

## Configuration

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| Research root | `<workspace>/.research` | Unconditional per-project root (`config.ProjectResearchPath`); no persisted override |
| Global seed dir | `~/.c0wrk/.agents/` (`config.SkillsDir`/`config.AgentsDir` on the agent dir) | Destination of the startup `seedGlobalPacks` run; outranks `~/.agents` in discovery |
| Skill-pack seed version | `2` (`research.CurrentSeedVersion`) | Pack version stamped into `.seed-version` markers; agent-pack version (`research.AgentSeedVersion`, `1`) bumps independently |
| Papers skill-pack seed version | `3` (`papers.CurrentSeedVersion`) | The `study-paper` pack seeded by the same global startup seed; bumps independently of both research pack versions (ADR-055) |

## Extension Points

- Add a hypothesis field by extending the pure parser/model, DTO mapping, frontend type guard, and Research panel together.
- Add a research metric in `ComputeMetrics`, then extend `ResearchMetrics`, frontend graph types, and presentation.
- Update bundled methodology skills by changing `core/research/skills/` and incrementing `CurrentSeedVersion` when existing marked copies must refresh.
- Add a research artifact by extending `ParseProject`; preserve the best-effort partial-state contract.
- Change watcher payloads or RPC DTOs only with matching updates to the desktop/frontend and event contracts.

## Informing papers (hypothesis ← paper)

The literature library and the hypothesis graph are wired in ONE direction end-to-end: a paper card declares `research_ids` (H-NNN), the backend resolves each to the owning R-NNN project(s) (`PaperDTO.linked_research`), and the paper's Overview renders them as "Research links". The reverse question — "which papers inform H-003?" — has no stored artifact and no RPC; it is DERIVED on the frontend by a pure fold of the two already-live stores (`paperStore.papers` × the hypothesis graph), keeping the paper cards as the single source of truth.

- `frontend/src/lib/papersByHypothesis.ts` — `buildPapersByHypothesis(papers, nodes)` folds `research_ids × node ids` into `{ byHypothesis: Map<H-NNN, PaperRecord[]>, dangling: Map<H-NNN, PaperRecord[]>, informedCount, danglingCount }`. `normalizeHypothesisId` makes the join spelling-tolerant (`h-3` / `H-03` / `H-003` → `H-003`); a paper contributes at most one entry per distinct normalized id. An id that matches no node feeds the `dangling` bucket (surfaced, never silently dropped). `collectHypothesisNodes(root)` unions the nodes of EVERY project of the root — the paper library is global across the root while a graph node is scoped to one R-NNN, so an id must match a node in ANY project to count as resolved (otherwise a paper legitimately citing another project's hypothesis would be misreported as dangling).
- Hooks — `usePapersByHypothesis()`, `useInformingPapers(id)` and `useDanglingPaperLinks()` read only DIRECT store references (the `papers` array, the `root` object) and allocate exclusively inside `useMemo` (React #185: a Zustand selector must never allocate an array/object).
- Render — `InformingPapers` is the hypothesis card's read-only "Informing papers (N)" list (renders nothing at zero); `DanglingPaperLinks` is the research dashboard's warning for cards that name a nonexistent H-NNN. Every entry opens the paper's reader tab through the file viewer's synthetic paper pseudo-path (`openPaper(slug)` → `c0wrk:paper:<slug>`).
- The projection adds no RPC and no persisted file: it is a pure client-side view over already-live state.

## Related Specs

- [papers.md](papers.md) - the paper ("literature") library: studied-paper cards, flashcards, the literature graph, and multi-paper comparisons, plus the globally-seeded `study-paper` skill-pack (the RESEARCH panel's Papers segment)
- [../decisions/055-research-always-on-global-seeding.md](../decisions/055-research-always-on-global-seeding.md) - why RESEARCH is always on for real projects and every c0wrk pack seeds globally at startup with reordered discovery (supersedes [ADR-051](../decisions/051-research-pack-reconciliation.md))
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) - RESEARCH RPC surface and DTO boundary
- [../contracts/event-catalog.md](../contracts/event-catalog.md) - `research:changed` and `research:file_changed` events
- [architecture/security-model.md](../architecture/security-model.md) - workspace containment and untrusted persisted artifacts
- [model-profiles.md](model-profiles.md) - model profiles, tuning for local / mid-size models (not gated by `experimental.enabled` — the manual `model_profiles.enabled` master toggle is the only switch; see [ADR-044](../decisions/044-model-profiles-out-of-experimental.md))
- [frontend/README.md](frontend/README.md) - frontend panel architecture
