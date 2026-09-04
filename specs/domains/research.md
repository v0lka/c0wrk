# RESEARCH Mode

## Purpose

RESEARCH mode is an experimental, project-scoped methodology workspace for maintaining research briefs, prior art, hypothesis cards, a hypothesis DAG, progress metrics, and synthesis reports. It parses Markdown/Mermaid artifacts under a workspace-contained research root, seeds versioned `research-*` skills, and exposes the same active-project graph to the orchestrator and frontend.

## Key Files

- `core/research/model.go` - hypothesis lifecycle types, graph traversal, and metric computation
- `core/research/parser.go` - pure Markdown/Mermaid parsers and best-effort research-root filesystem parsing
- `core/research/skillpack.go` - embedded seven-skill research pack and non-destructive versioned seeding
- `core/research/skills/` - embedded `research-*` skill sources
- `backend/frontend_api_research.go` - experimental gate, enable/disable/status/graph RPC behavior, persistence, and skill rescan
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
  -> experimental.enabled gate
  -> resolve root (default <workspace>/.research)
     and reject an explicit root outside the workspace
  -> create root and recursively watch its current/future directories
  -> seed seven research-* skills into <workspace>/.agents/skills
     using per-skill .seed-version markers
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
(`ResearchEventBridge`, gated on `experimental.enabled`); the Research panel
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

A flat single-project root containing `brief.md` or `hypotheses/` is also parsed when no `R-NNN-*` directory exists. Missing optional artifacts produce a valid partial model: an empty hypothesis graph and zero metrics are normal states.

The active project is the project referenced by the last chronological `index.md` entry when that project exists; otherwise it is the highest-numbered parsed `R-NNN` directory. `ResearchRoot.ActiveProjectID`, orchestrator research context, and the frontend panel all use this selection rule.

Metrics are derived from the reconciled graph:

- `confirmation_rate = confirmed / (confirmed + refuted)`, or zero before any verdict
- `depth` is the longest root-to-leaf path measured in edges
- `breadth` is the widest depth level containing non-terminal hypotheses
- `active_front` is the sorted set of `open` and `in-progress` hypothesis IDs

## Invariants

- RESEARCH mode is available only for real projects and only while `experimental.enabled` is true.
- The persisted research root is absolute and contained within the project workspace; the default root is `<workspace>/.research`.
- Enabling is idempotent: it may reparse, reseed, repersist, rescan, and re-emit without duplicating domain state.
- Disabling clears the persisted toggle and recursive watch while preserving research artifacts and seeded skills.
- Skill seeding preserves every existing skill directory without a `.seed-version` marker, preserves same-version seeded directories, and replaces only marked directories from an older pack version.
- Skill-seeding failure is logged while the research toggle remains enabled; `SeedResult` reports per-skill outcomes only when seeding returns a result.
- Root/project parsing is best-effort: malformed or missing optional artifacts do not invalidate other parseable projects or cards.
- Hypothesis nodes and edges are normalized, de-duplicated, and deterministically ordered; malformed cycles terminate metric traversal without unbounded recursion.
- The recursive watcher covers existing and newly created subdirectories beneath the active research root.
- `GetResearchGraph` and `GetResearchStatus` parse the same full root; the graph RPC reduces wire payload, not parse cost.
- The active-project selection rule is shared by backend orchestration and frontend presentation.

## Configuration

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| `experimental.enabled` | `false` | Master gate for RESEARCH and other experimental features |
| `ProjectInfo.ResearchRoot` | empty (disabled) | Persisted per-project absolute research root |
| Enable `rootPath` | `<workspace>/.research` | Optional explicit root; must remain inside the workspace |
| Skill-pack seed version | `1` (`research.CurrentSeedVersion`) | Marker version controlling updates of pack-owned skill directories |

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
- [small-llm.md](small-llm.md) - the other feature gated by `experimental.enabled`
- [frontend/README.md](frontend/README.md) - frontend panel architecture
