# Specs Subsystem

## Purpose

The specification subsystem gives every workspace a spec-lifecycle capability and a human surface for it: the vendored `spec-*` skillset (six Agent Skills adapted from the external vibespec skillset and then evolved independently) plus the `specs` Subagent Profile, both seeded globally at startup like the RESEARCH packs; and the Specs sidebar panel — a read-only browser of the workspace's `specs/` tree (task→spec navigation, section tree, ADR timeline, deterministic integrity findings) whose every mutating gesture dispatches a `spec-*` skill into the chat. See [ADR-104](../decisions/104-vendor-spec-skillset.md) and [ADR-105](../decisions/105-specs-sidebar-panel.md).

## Key Files

- `core/specs/skillpack.go` - embedded six-skill `spec-*` pack (`core/specs/skills/`) and its versioned global seeding (`SeedSkills`; a thin consumer of the shared core below)
- `core/specs/skills/` - the vendored skill sources: six `spec-*/SKILL.md` bodies (`spec-init`, `spec-explore`, `spec-create`, `spec-update`, `spec-check`, `spec-consult`; tool-agnostic; `spec-init` carries `assets/meta-template.md` + `assets/adr-template.md`)
- `core/specs/agentpack.go` - embedded `specs` Subagent Profile (`core/specs/agents/specs/AGENT.md`) and its seeding (`SeedAgents`)
- `core/specs/LICENSE.md` - the upstream MIT attribution (vibespec); the origin record of the vendored tree
- `core/specs/model.go` - pure domain types of the parsed specs/ tree (`SpecsRoot`, `SpecFile`, `SpecsIndex`, `IndexTask`, `ADRMeta`, `Finding`); no I/O
- `core/specs/parse.go` - the tolerant read-only parser (`ParseSpecsRoot`) and the tier-1 integrity findings
- `core/seedpack` - the shared crash-safe seeding core (content-hash classification, staged write + atomic rename, `.seed-version` marker handling, leftovers sweep) behind the seeding invariants below; `core/research`, `core/papers`, and `core/specs` are thin consumers (ADR-104 decision 5)
- `backend/frontend_api_research.go` - `seedGlobalPacks`: the single per-launch global seed that seeds the specs packs alongside research/papers, before the skill/agent watchers start
- `backend/frontend_api_specs.go` - `GetSpecsStatus` RPC and the `SpecsStatusDTO`/`SpecsCountsDTO` wire shapes
- `backend/config/paths.go` - `ProjectSpecsPath` (`<workspace>/specs`) — the one path constructor for the specs root
- `frontend/src/api/specs.ts` - the RPC wrapper: boundary validation + normalization of the status payload
- `frontend/src/stores/specsStore.ts` - the panel's store (status, fetch ticket, error line)
- `frontend/src/components/specs/` - the panel: `SpecsPanel.tsx` (orchestrator + in-panel `workspace:tree_changed` subscription), `specsActions.ts` (the auditable dispatch constants), `specsPaths.ts` (pure path/grouping helpers), `useSpecsDispatch.ts` (the single `sendMessage` path), and the presentational subcomponents
- `frontend/src/components/layout/WorkspacePanel.tsx` - the host tab strip; the `specs` tab (after `research`) renders `SpecsPanel`
- `frontend/src/stores/uiStore.ts` - `WorkspaceTab` gains `'specs'`, registered in `WORKSPACE_TAB_VALUES` (per-project tab memory)

## Core Types

```go
type FindingKind string // PATH_MISSING | CROSS_REF_BROKEN | DUPLICATE_ADR_NUMBER | INDEX_STALE

type SpecFile struct {
    Path      string   // specs-root-relative, forward slashes; "INDEX.md", "domains/research.md"
    Section   string   // architecture | domains | contracts | decisions; "" for INDEX.md and META.md
    Title     string   // first ATX heading outside code blocks, verbatim
    KeyFiles  []string // backticked entries of "## Key Files", verbatim
    CrossRefs []string // relative markdown link targets (file path, optional #anchor, or pure #anchor)
}

type IndexTask struct {          // one "## Navigation by Task" row
    Task  string
    Specs []string               // the row's relative link targets, anchors included
}

type ADRMeta struct {            // one decisions/NNN-slug.md record
    Number, Slug, File           // from the file name (the template's authority)
    Title, Status, SupersededBy  // from the body; Status classifies the status line's first word
}

type Finding struct{ Kind FindingKind; File, Detail string }

type SpecsRoot struct {
    Path     string        // exactly as passed to ParseSpecsRoot
    Present  bool          // root existed and was a directory; a missing or non-directory root is the neutral state, never an error
    Files    []SpecFile    // sorted by path; "_"-prefixed files (templates) excluded
    Index    *SpecsIndex   // parsed INDEX.md; nil only when missing/unreadable
    ADRs     []ADRMeta     // sorted by number
    Findings []Finding     // tier-1 integrity findings, sorted by kind, file, detail
}
```

## Flow

```
App startup
  -> seedGlobalPacks seeds every c0wrk-owned pack into ~/.c0wrk/.agents:
     the six spec-* skills into .agents/skills/spec-*, the specs profile
     into .agents/agents/specs/ (content-hash classified, staged +
     atomically swapped, non-destructive) — BEFORE the skill/agent
     watchers; the skill/agent caches are invalidated afterwards

Specs tab mounted (a real project)
  -> SpecsPanel fetches GetSpecsStatus(projectId) — one light read-only
     parse of <workspace>/specs
  -> specsStore applies the payload under its fetch ticket
     (last-write-wins by initiation order)

specs/ file changes (agent-written or human)
  -> workspace watcher emits workspace:tree_changed
  -> the panel's own subscription filters it to specs/ paths and refetches
     (fail-open when the payload carries no paths) — no dedicated event

Human gestures
  -> Open spec / Key File -> fileViewerStore.openFile (the existing viewer)
  -> Initialize / Check drift / Consult / Explore -> sendMessage with the
     spec-* skill in the activeSkills slot and the constant prompt from
     specsActions.ts (Shift = new session)
```

## Invariants

- The vendored pack contains exactly six skills — `spec-init`, `spec-explore`, `spec-create`, `spec-update`, `spec-check`, `spec-consult` — and one Subagent Profile (`specs`); every `SKILL.md` frontmatter `name:` matches its directory name.
- The vendored tree is adapted once to c0wrk's layout and then evolved independently of upstream: there is no sync obligation, and the upstream MIT attribution stays in `core/specs/LICENSE.md`.
- Skill bodies stay tool-agnostic (no c0wrk tool names), so they remain portable; the profile's directive lives in `core/specs/agents/specs/AGENT.md`.
- `spec-init` emits the c0wrk tree shape (META.md / INDEX.md / architecture / domains / contracts / decisions + `_template.md`); `specs/WORKFLOW.md` is deliberately absent — `AGENTS.md` owns the "how to use the spec system" pointer role, and the vendored `assets/` carry only `meta-template.md` and `adr-template.md`.
- The `specs` profile is least-privilege by group tokens (`tools: local-read, local-write` — spec work reads and writes Markdown and executes nothing), and its `skills:` list (`spec-consult, spec-create, spec-update, spec-check`) resolves fail-closed against the skill catalog at launch.
- Seeding runs once per launch from `seedGlobalPacks` BEFORE the skill and agent directory watchers are created, is per-pack fail-soft (a failure is logged and never fatal to startup), and is idempotent — a repeat run re-seeds nothing (all destinations classify as `Current`).
- Seeding classifies each destination by CONTENT HASH against the embedded pack (never mtime/size, never the marker alone): equal content is `Current` (a missing/stale `.seed-version` marker is re-stamped); a pack-marked truncated subset is repaired; a pack-marked diverging directory is a local edit — preserved and reported `Modified`; a marker-less diverging directory is user-owned and preserved byte-identically; a marker from an older pack version is overwritten in full.
- Seeding writes are crash-safe: each entry is staged in a hidden sibling temp directory and swapped in with a single rename; staging/backup leftovers from a hard kill are swept on the next run. The specs pack versions (`specs.CurrentSeedVersion`, `specs.AgentSeedVersion`) bump independently of the research/papers packs.
- Skill/agent discovery precedence is project-local `<workspace>/.agents/{skills,agents}` → c0wrk global `~/.c0wrk/.agents/{skills,agents}` → user `~/.agents/{skills,agents}`: the seeded spec pack outranks a stale same-named user copy, and `ListSkills`/`ListAgents` expose it with no catalog change.
- The specs root is unconditionally `<workspace>/specs` (`config.ProjectSpecsPath`), read-only for the application: the panel and the parser never create, write, or delete anything under it — every mutation is dispatched to a `spec-*` skill.
- The parser is tolerant: a MISSING or non-directory `specs/` root parses to the neutral `Present=false` state; an existing EMPTY or PARTIAL tree parses `Present=true` with whatever content is readable — never an error in either case; files whose base name starts with `_` (templates) are excluded from the model; the parser keys off the documented `specs/` layout, not c0wrk-specific paths (any workspace with a `specs/` directory renders the panel).
- `GetSpecsStatus` requires a real project: the No-Project pseudo-project returns the neutral `present=false` DTO (the defensive contract — the workspace tab strip, and with it the panel, is not rendered in CHAT mode at all), and every other error path is a transport-level Go error.
- Every DTO collection and the counts histogram are non-nil on the wire (empty `[]`/`{}`, never `null`); the frontend boundary re-validates the outer shape, drops malformed entries per-entry, and recomputes the counts from the normalized collections so the badges always agree with the rendered lists.
- Tier-1 integrity findings are computed by the parser on every call (`PATH_MISSING` — a Key Files path that does not exist; `CROSS_REF_BROKEN` — a relative link or anchor that does not resolve; `DUPLICATE_ADR_NUMBER` — two `decisions/NNN-*.md` sharing NNN; `INDEX_STALE` — a section file the INDEX.md directory listing does not name) and are never persisted — the parse is cheap and always fresh.
- Tier-2 semantic drift (type/behavior/config mismatch, violated invariants — `spec-check`'s vocabulary) stays conversational: the panel surfaces it only through the "Check drift" quick action, which dispatches `spec-check` into the chat; no machine-readable check artifact exists in v1.
- The panel refreshes on mount (tab activation) and on `workspace:tree_changed` events scoped to `specs/` paths — there is no dedicated `specs:*` event pair (research needed one only for its status/graph split); the subscription lives inside `SpecsPanel` (the single consumer of `specsStore`), and the filter is fail-open when the event payload carries no paths.
- Reads open through the existing file viewer (`fileViewerStore.openFile` + `setCollapsed(false)`); spec-relative links resolve lexically (`specsPaths.joinSpecPath` handles `./`, `#anchor`, and `../` escapes to absolute viewer paths).
- Every mutating quick action dispatches exactly one `spec-*` skill through `sendMessage`'s `activeSkills` slot with a constant prompt from `specsActions.ts` (`Initialize specs` is always enabled and shared verbatim with the empty state's CTA; `Check drift`/`Consult`/`Explore` require a present tree); a failed dispatch surfaces on `specsStore`'s error line (the panel keeps the previous status rendered), and Shift dispatches into a new session.
- `specsStore` fetches are last-write-wins by initiation order (a monotonic ticket discards superseded responses), and a failed fetch keeps the previous status and records the error.

## Configuration

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| Specs root | `<workspace>/specs` | Unconditional per-project root (`config.ProjectSpecsPath`); read-only, never persisted, no override |
| Global seed dir | `~/.c0wrk/.agents/` (`config.SkillsDir`/`config.AgentsDir` on the agent dir) | Destination of the startup `seedGlobalPacks` run; outranks `~/.agents` in discovery |
| Skill-pack seed version | `1` (`specs.CurrentSeedVersion`) | Stamped into each seeded skill's `.seed-version`; bump to refresh pack-marked copies |
| Agent-pack seed version | `1` (`specs.AgentSeedVersion`) | The `specs` profile's independent version |

The subsystem is always on: there is no `specs` config section, no experimental gate, and no per-project toggle (the panel's availability follows the workspace having a `specs/` tree; the seed follows the RESEARCH always-on precedent, [ADR-056](../decisions/056-research-always-on-global-seeding.md)).

## Extension Points

- Update the vendored skills by changing `core/specs/skills/` (and `core/specs/agents/specs/AGENT.md`) and incrementing `CurrentSeedVersion`/`AgentSeedVersion` when existing pack-marked copies must refresh; evaluate upstream vibespec changes by hand — never pull them.
- Add a tier-1 finding kind in `core/specs` (a new `FindingKind` + its check in `parse.go`), then mirror it in `frontend/src/types/models.ts` (`SpecFindingKind`) and `specsPaths.findingKindLabel` (an unknown kind passes through the boundary with a generic label, so the frontend degrades instead of dropping).
- Extend the parsed model (a new `SpecFile`/`ADRMeta` field) by extending the pure parser, the DTO, the frontend boundary validation, and the panel together — the DTO contract is additive-only for the same hand-written-guard reasons as the other boundaries ([../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) § Specs).
- Add a quick action in `specsActions.ts` (one constant: label + `spec-*` skill + prompt); the dispatch path (`useSpecsDispatch`) and the row render it without further wiring.
- Change the panel's refresh model only with matching updates to this document and the desktop-frontend contract; a dedicated `specs:*` event requires superseding [ADR-105](../decisions/105-specs-sidebar-panel.md) decision 3.

## Related Specs

- [ADR-104](../decisions/104-vendor-spec-skillset.md) - the vendoring, the c0wrk adaptation, the independent-evolution policy, and the `core/seedpack` consolidation.
- [ADR-105](../decisions/105-specs-sidebar-panel.md) - the panel's six design decisions (placement, data source, refresh, interaction split, two-tier drift, scope boundary).
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) - the `GetSpecsStatus` RPC surface and wire shapes (§ Specs).
- [research.md](research.md) - the global pack-seeding precedent this pack inherits (ADR-056) and the closest panel precedent.
- [frontend/stores.md](frontend/stores.md) - `specsStore` and the `WorkspaceTab` extension in `uiStore`.
