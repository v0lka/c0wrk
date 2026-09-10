# RESEARCH Mode

## Purpose

RESEARCH mode is a project-scoped methodology workspace for maintaining research briefs, prior art, hypothesis cards, a hypothesis DAG, progress metrics, and synthesis reports. It parses Markdown/Mermaid artifacts under a workspace-contained research root, seeds versioned `research-*` skills, and exposes the same active-project graph to the orchestrator and frontend.

## Key Files

- `core/research/model.go` - hypothesis lifecycle types, graph traversal, and metric computation
- `core/research/parser.go` - pure Markdown/Mermaid parsers and best-effort research-root filesystem parsing
- `core/research/rootwriter.go` - root-level mutations: `index.md` row moves for activation, minimal row/index creation, and project deletion with containment
- `core/research/recommend.go` - pure next-step recommendation, project-wide and hypothesis-scoped
- `core/research/skillpack.go` - embedded seven-skill research pack and non-destructive versioned seeding
- `core/research/skills/` - embedded `research-*` skill sources
- `backend/frontend_api_research.go` - enable/disable/status/graph RPC behavior, persistence, and skill rescan
- `backend/frontend_api_project.go` - recursive research-tree watcher integration and incremental file-change emission
- `frontend/src/components/research/index.tsx` - Research panel and graph/status presentation

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
User enables RESEARCH for a real project
  -> resolve root (default <workspace>/.research)
     and reject an explicit root outside the workspace
  -> create root and recursively watch its current/future directories
  -> seed seven research-* skills into <workspace>/.agents/skills
     (content-hash-verified, staged + atomically swapped) and the research
     Subagent Profile into <workspace>/.agents/agents
  -> persist ProjectInfo.ResearchRoot
  -> invalidate the skill cache and rescan running project sessions
  -> parse the root and emit research:changed

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
and the workspace tab are pure views over `researchStore` and never mount the
hooks themselves (a double mount would duplicate every watchdog and fallback
refetch). The workspace's hypothesis selection is keyed to the research
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

The active project is the project referenced by the last chronological `index.md` entry when that project exists; otherwise it is the highest-numbered parsed `R-NNN` directory. `ResearchRoot.ActiveProjectID`, orchestrator research context, and the frontend panel all use this selection rule.

Metrics are derived from the reconciled graph:

- `confirmation_rate = confirmed / (confirmed + refuted)`, or zero before any verdict
- `depth` is the longest root-to-leaf path measured in edges
- `breadth` is the widest depth level containing non-terminal hypotheses
- `active_front` is the sorted set of `open` and `in-progress` hypothesis IDs

## Invariants

- RESEARCH mode is available only for real projects.
- The persisted research root is absolute and contained within the project workspace; the default root is `<workspace>/.research`.
- Enabling is idempotent: it may reparse, reseed, repersist, rescan, and re-emit without duplicating domain state.
- Disabling clears the persisted toggle and recursive watch while preserving research artifacts and seeded skills.
- Skill/agent seeding classifies each destination by CONTENT HASH against the embedded pack (never mtime/size, never the marker alone): content equal to the pack is Current (a missing/stale `.seed-version` marker on it is re-stamped); a pack-marked truncated subset of the pack (interrupted write) is repaired; a pack-marked same-version directory whose content diverges from the pack is a local edit (or a spoofed marker) — preserved untouched and reported `Modified`; a marker-less diverging directory is user-owned and preserved; a marker from an older pack version is overwritten in full.
- Seeding writes are crash-safe: each entry is staged in a hidden sibling temp directory and swapped in with a single rename, so an interrupted run never leaves a truncated tree at the destination; staging/backup leftovers from a hard kill are swept on the next seeding run.
- Skill-seeding failure is logged while the research toggle remains enabled; `SeedResult` reports per-skill outcomes only when seeding returns a result.
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
- The root-level RPCs (`SetActiveResearch`/`DeleteResearch`) mirror the hypothesis-mutation posture: the research root is workspace-containment-checked, the whole resolve→write chain runs under the per-root mutation mutex, and the caller's R-NNN is ownership-checked via `ProjectDir` before any file is touched. Both return the refreshed `ResearchStatusDTO` (the toggle stays on after deleting the last project) and emit `research:changed` (action=`active_changed` / `project_deleted`).
- Pins (`ProjectInfo.ResearchPins`) store research-root-relative, forward-slash document paths: a pinned research project records its brief (`R-NNN-*/brief.md`), a pinned hypothesis card records `R-NNN-*/hypotheses/H-NNN.md` keyed by hypothesis id (a list per key — the same H-NNN exists across R-NNN projects). The pin RPCs are idempotent in both directions, ownership-check the R-NNN, serialize under the per-root mutation mutex (against `DeleteResearch`'s pin cleanup, so a concurrent toggle cannot resurrect a deleted project's pins), and emit no event. Pinning a card requires the card file to exist; unpinning tolerates a deleted card, so stale pins stay removable. `DeleteResearch` removes every pin under the deleted R-NNN's directory and persists the cleaned record.
- `ResearchStatusDTO.pinned_research`/`pinned_hypotheses` mirror the persisted pins in `GetResearchStatus`, `EnableResearch`, `SetActiveResearch`, and `DeleteResearch`, normalized to non-nil collections (empty `[]`/`{}`, never `null`).
- `RecommendNextStepForHypothesis` scopes the next-step recommendation to one hypothesis: open/in-progress → `research-experiment` on it; terminal without a recorded Decision → `research-decision` on it; otherwise (unknown hypothesis, nil project, empty ID, or a terminal hypothesis already carrying a Decision) the plain `RecommendNextStep` result. `GetResearchNextStep(projectID, hypothesisID)` exposes this at the RPC boundary: an empty `hypothesisID` means the project-level recommendation.
- `ResearchNextStepDTO.project_id` (and `ResearchGraphDTO.project_id`) are dual-namespace by design: they name the recommendation's/graph's subject — the active R-NNN when one exists, the c0wrk project UUID otherwise (the pre-R-NNN setup state). They are not stable identities for the requesting c0wrk project; frontend state keying must not rely on them across a project switch (the research store drops the recommendation and selection on cross-project loads instead).

## Configuration

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| `ProjectInfo.ResearchRoot` | empty (disabled) | Persisted per-project absolute research root |
| Enable `rootPath` | `<workspace>/.research` | Optional explicit root; must remain inside the workspace |
| Skill-pack seed version | `2` (`research.CurrentSeedVersion`) | Pack version stamped into `.seed-version` markers; agent-pack version (`research.AgentSeedVersion`, `1`) bumps independently |

## Extension Points

- Add a hypothesis field by extending the pure parser/model, DTO mapping, frontend type guard, and Research panel together.
- Add a research metric in `ComputeMetrics`, then extend `ResearchMetrics`, frontend graph types, and presentation.
- Update bundled methodology skills by changing `core/research/skills/` and incrementing `CurrentSeedVersion` when existing marked copies must refresh.
- Add a research artifact by extending `ParseProject`; preserve the best-effort partial-state contract.
- Change watcher payloads or RPC DTOs only with matching updates to the desktop/frontend and event contracts.

## Related Specs

- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) - RESEARCH RPC surface and DTO boundary
- [../contracts/event-catalog.md](../contracts/event-catalog.md) - `research:changed` and `research:file_changed` events
- [architecture/security-model.md](../architecture/security-model.md) - workspace containment and untrusted persisted artifacts
- [small-llm.md](small-llm.md) - the Small-LLM profile, the only feature gated by `experimental.enabled`
- [frontend/README.md](frontend/README.md) - frontend panel architecture
