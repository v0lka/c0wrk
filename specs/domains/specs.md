# Specs Subsystem

## Purpose

The Specs subsystem answers one question per workspace: WHICH spec-driven-development framework is installed here, and what does its tree look like? It recognises frameworks from the agent skills their own `init` installs, renders the active one through its declarative DSL ([sdd-dsl.md](sdd-dsl.md)) and shows the result in the single Specs sidebar panel — a header with the integrity badge and framework-neutral counts, a framework selector when several frameworks are recognised, the quick-actions row, the declared view sections and the findings list — where every mutating gesture previews its framework-declared prompt verbatim in the panel's dispatch dialog and, on confirm, dispatches a DECLARED skill into the chat — and nothing is ever written by c0wrk. c0wrk ships no skillset of its own: a workspace's SDD skills are ordinary agent-skill data ([ADR-126](../decisions/126-sdd-framework-dsl.md)).

## Key Files

- `core/sdd/framework.go` - the framework document model and its strict parser (`sdd-framework/v1`)
- `core/sdd/dsl.go` - the operator layer over the built-ins: `Load` (global `~/.c0wrk/specs.yaml` then checkout `<workspace>/.c0wrk/specs.yaml`), `ParseDefinitions` (fail-soft per document) and `Merge` (by ID, whole-document, `priority` then `id` order)
- `core/sdd/builtin.go` + `core/sdd/frameworks/{sdd,openspec,spec-kit}.yaml` - the embedded catalog; `sdd` describes c0wrk's own tree, its eight checks and its `spec-*` lifecycle actions (including `fix-findings`)
- `core/sdd/detect.go` - the pure detector: recognised frameworks (skill evidence only), the default selection order, the `spec-`/`vibespec-` alias matching, the three skill tiers
- `core/sdd/parse.go`, `core/sdd/model.go` - `ParseTree` (the one filesystem entry point, containment-checked and read-only) and the parsed model (`Tree`, `Document`, `Node`, `View`, `Finding`, `Counts`); the model itself does no I/O and carries no environment metadata
- `core/specs/model.go`, `core/specs/parse.go` - the RETIRED hand-written `specs/` reader, kept only as the parity oracle `core/sdd/tree_test.go` compares the DSL parse against; no production package imports it
- `backend/frontend_api_sdd.go` - the whole RPC surface: `SpecsStatusDTO` and its element DTOs, `GetSpecsStatus`, `SetActiveSDD`, the detection-evidence collection (installed skills with tiers, present markers, present roots), the model→DTO mapping and the action availability evaluation
- `backend/frontend_api_specs.go` - the two roots of the subsystem: `sddWorkspace` (the tree — the active session's workspace, else the checkout) and `sddCheckout` (the operator DSL file — always the checkout), plus `sddRootPath`
- `backend/frontend_api_specs_adrdates.go` - the fail-soft git-derived add date for decision documents (`specsADRDates`, `enrichSpecsDocumentDates`; ONE bounded `git log --diff-filter=A` per call under `gitCmdTimeout`, Debug-only failures)
- `backend/frontend_api_specs_adrdates_test.go`, `backend/frontend_api_sdd_test.go`, `backend/frontend_api_specs_session_root_test.go` - the date-enrichment, DTO/detection and session-root contracts
- `backend/frontend_api_packgates.go` - `SetSpecsEnabled`: the pure visibility gate (persist + `config:updated`, no pack side-effect); `SetResearchEnabled` in the same file keeps the research pack lifecycle
- `backend/config/sdd_store.go` - `SDDPath` / `ProjectSDDPath` (+ `ProjectMetaDirName`), read-only derivations that never materialize a file
- `backend/project/persistence.go` - `project_ui_state.active_sdd` (the per-project selection) with `SaveActiveSDD` / `LoadActiveSDD`
- `frontend/src/api/specs.ts` - the RPC wrappers (`getSpecsStatus`, `setActiveSDD`) with fail-closed outer validation, per-entry fail-soft normalization and non-nil collections
- `frontend/src/stores/specsStore.ts` - the parse: fetch state under a monotonic LWW ticket, stamped with the (project, session) pair, plus the ONLY persisted slice (`treeExpandedNodes`, zustand key `c0wrk-specs-view`)
- `frontend/src/stores/sddStore.ts` - the framework projection the tab strip and the selector read: detected frameworks, the rendered vs confirmed selection, `selectorNeeded`, `present`, `warnings`, and the tri-state `known`; fed by `specsStore` (one direction, no second fetch)
- `frontend/src/components/specs/` - the panel: `SpecsPanel.tsx` (section orchestration + the `workspace:tree_changed` subscription), `SpecsHeader.tsx` (integrity badge, counts, selector), `SpecsFrameworkSelector.tsx`, `SpecsQuickActions.tsx`, `SpecsSectionTree.tsx`, `SpecsFindings.tsx`, `SpecsEmptyState.tsx`, plus `specsActions.ts` (declared-action → dispatch helpers, `{path}`/`{name}` substitution, `findingLine`), `specsPaths.ts` (workspace-path joining, change relevance, dates, labels) and `useSpecsDispatch.ts` (the single `sendMessage` path)
- `frontend/src/components/layout/WorkspacePanel.tsx` - the tab strip: `specsVisible = specsEnabled && (known ? present : true)`, one detection probe per project switch, and the remembered-tab fallback
- `frontend/src/types/models.ts` - the `SpecsStatus` / `SDDFramework` / `SDDRoot` / `SDDDocument` / `SDDNode` / `SDDView` / `SDDFinding` / `SDDAction` / `SDDCounts` wire types

## Core Types

The DSL's own types (`Framework`, `Definition`, `Detection`, `Tree`) live in [sdd-dsl.md](sdd-dsl.md). The wire shape is:

```go
type SpecsStatusDTO struct {
    Present        bool               // gate && len(detected) > 0 — the VISIBILITY answer
    TreePresent    bool               // the active framework's roots exist in the parsed workspace
    Root           string             // absolute path of the first PRESENT declared root (else the first declared one)
    Workspace      string             // absolute workspace the tree was parsed in
    Detected       []SDDFrameworkDTO  // every recognised framework, detection default order
    ActiveID       string             // "" when none is active
    Active         *SDDFrameworkDTO   // the matching element of Detected
    SelectorNeeded bool               // >1 detected and no persisted choice
    Roots          []SDDRootDTO       // declared roots of the active framework + presence
    Documents      []SDDDocumentDTO   // concept instances that are files
    Nodes          []SDDNodeDTO       // the flat forest the tree views index into
    Views          []SDDViewDTO       // declared view sections, in order
    Findings       []SDDFindingDTO    // declared-check failures
    Actions        []SDDActionDTO     // declared gestures + the `available` / `skill_available` facts
    Counts         SDDCountsDTO       // documents, findings, per-concept, per-finding-kind
    Warnings       []string           // operator DSL faults, parse warnings — never errors
}
```

`SDDFrameworkDTO` carries the definition's identity (id, label, description, docs URL), its `origin` (`builtin` | `global` | `project`), the strongest matched `scope` (`project` | `global` | `user`), the two tree signals (`root_present`, `marker_present`) and the matched `skills`. `SDDDocumentDTO` carries the concept, section, `number`/`slug`, the extracted metadata the concept declared (title, headings, key files, cross-refs, frontmatter, status/superseded_by, purpose, requirements, scenarios, deltas, principles, user stories, task items, index tasks, `missing` companions) and the optional git-derived `date`. `SDDNodeDTO` is `{id, kind, path, label, concept, name, parent, children, missing}`; `SDDFindingDTO` is `{kind, file, detail, message, severity}`; `SDDActionDTO` is `{kind, label, skill, prompt, concept, requires, available}`.

## Flow

```
Workspace with an installed SDD framework
  -> the framework's own `init` installed its skills somewhere in the
     discovery tiers: <workspace>/.agents/skills, a framework-declared
     project directory (.claude/skills), ~/.c0wrk/.agents/skills, ~/.agents/skills

Workspace tab strip (before any panel mounts)
  -> specsVisible = specs.enabled ∧ (sddStore.known ? sddStore.present : true)
     — one probe per project switch, through the SAME GetSpecsStatus the
     panel uses (skipped when this project already has a parse)

Specs tab mounted (a real project)
  -> SpecsPanel fetches GetSpecsStatus(projectId):
       sdd.Load(global, project)
                    -> embedded sdd/openspec/spec-kit documents merged with
                       ~/.c0wrk/specs.yaml and <checkout>/.c0wrk/specs.yaml
       detect       -> installed skills (tiers) + present markers/roots
       selection    -> project_ui_state.active_sdd, else detection default
       parse        -> sdd.ParseTree(active session's workspace, active framework)
       enrich       -> git add dates for decision documents (fail-soft)
  -> specsStore applies the payload under its fetch ticket (LWW by
     initiation order, stamped by (project, session)); sddStore.applyStatus
     projects the framework context for the tab strip and the selector

specs/ file changes (agent-written or human)
  -> workspace watcher emits workspace:tree_changed
  -> the panel's own subscription filters it to the active root and refetches
     (fail-open when the payload names no paths) — no dedicated event

Human gestures
  -> Framework selector (only when >1 recognised) -> sddStore.beginSwitch
     renders the choice at once; setActiveSDD persists it, then fetchStatus
     re-parses; a failed switch reverts to the confirmed selection and
     surfaces the panel's own error banner
  -> Open a document -> fileViewerStore.openFile with
     joinWorkspacePath(status.workspace, node.path). (`key_files` is CHECK
     DATA — the `required_files {from: key_files}` input — not navigation:
     the panel renders no key-file links; a viewer-side markdown preview
     resolves such links when the document itself is open.)
  -> Quick actions -> one declared panel-level action's skill + prompt via
     sendMessage's activeSkills slot (Shift = new session)
  -> A node action -> the action declared for that node's concept, with
     {path}/{name} substituted
  -> The findings badge -> ONE dispatch of the framework's declared
     `fix-findings` action; each finding card's fix badge -> the same action
     scoped to that single finding; a framework declaring no such action
     keeps both badges INERT (never a button)
  -> All three mutating gestures park in ONE preview gate first
     (`SpecsPanel`'s dispatch dialog): the DECLARED prompt is shown verbatim
     — it is repository-authored content, not UI copy — and only the
     dialog's confirm sends it through `useSpecsDispatch`; cancel, Esc or
     the overlay drops it and nothing reaches the chat
  -> The findings copy button (the shared CopyIconButton -> useCopyFlash ->
     clipboardSetText path) -> the card's findingLine verbatim, the exact
     bullet the single-finding fix prompt carries
  -> The empty state's CTA -> the framework's declared bootstrap action with
     no unmet requirement; no such action means no CTA
  -> Header refresh (specs-refresh) -> the same refetch as mount/tree_changed
     (no new RPC, no event)
```

## Invariants

- The Specs surface is framework-aware and ships no workflow of its own: what it shows is decided by the recognised frameworks and their documents, and c0wrk neither embeds nor installs SDD skills — a workspace's SDD skills are ordinary agent-skill data that detection recognises wherever they are installed ([ADR-126](../decisions/126-sdd-framework-dsl.md)).
- Visibility is `specs.enabled ∧ at least one framework detected`. Detection is decided by installed-skill evidence alone; markers and roots only order the default selection and feed the empty state. Zero recognised frameworks means the Specs tab does not exist.
- `present` is the VISIBILITY answer, not the tree state: a recognised framework whose declared roots are absent reports `tree_present=false` (the empty state, with the bootstrap action) while `present` stays `true`.
- `tree_present` is a FILESYSTEM predicate (a declared root exists), so a root directory that exists before its first document reports `tree_present=true` with zero documents: the panel then renders the SAME empty-state surface as the absent case, with the empty-tree reason naming the root and the bootstrap CTA, instead of a header claiming a result over an empty body — and the integrity badge says the tree had nothing to check rather than "Integrity OK".
- Detection is refreshed from the permanently mounted workspace panel for BOTH projections on `workspace:tree_changed`/`skills:changed` (one debounced `fetchStatus` funnel): the negative one because the Specs tab does not exist to own the refetch, the positive one because the panel's own subscription only lives while the Specs tab is mounted — a removed root or skill must retire the tab, not leave a dead one.
- The active framework is the per-project persisted selection (`project_ui_state.active_sdd`) when it is still detected, else detection's default. `SetActiveSDD` refuses a framework that is not detected for that project, refuses the No Project pseudo-project, clears the selection on an empty id, and never affects another project; the selection survives an app restart.
- `selector_needed` is raised exactly when more than one framework is recognised and the persisted selection names none of them; the selector renders the chosen framework immediately and the previous parse stays on screen until the re-parse lands.
- The tree follows the ACTIVE SESSION: `<workspace-of-the-active-session>` when one resolves, the project checkout as fallback ([ADR-116](../decisions/116-specs-root-follows-active-session.md), ADR-099's protocol). The root itself is the active framework's first declared root, not a fixed path.
- The checkout-local operator DSL file is read from the project CHECKOUT, never from the session workspace: it is repository data travelling with the clone, not agent output (`sddCheckout` vs `sddWorkspace`).
- The skill-discovery catalog merges the SAME checkout-local operator DSL detection reads: the session factory threads the project checkout through the builder (`OrchestratorFactory` → `Build` → the `SetSDDCatalog` seam), so the catalog provider is keyed on the project CHECKOUT — repository data — and never on a managed worktree's session tree (an empty checkout value falls back to the session workspace, the CHAT/No Project shape that has no checkout-local DSL). This closes ADR-126's former Known residual: the worktree window where a detected framework's declared custom skill directories did not reach session discovery while its actions stayed visible but inert.
- The subsystem is strictly read-only: neither the parser nor the panel creates, modifies or deletes anything under a root. Every mutation is dispatched to a skill the active framework declares, and a framework declaring no action renders no gesture.
- Every DTO collection and every counts map is non-nil on the wire (empty `[]`/`{}`, never `null`); `SpecsStatus` normalizes `[]`, `null` and missing alike, drops malformed entries per-entry and recomputes the counts from the normalized collections, so badges always agree with the rendered lists.
- Findings exist only because a DECLARED check failed; the set, the keys, the messages and the severities belong to the active framework's document. A finding's `kind` and `severity` travel as open strings — an unknown kind keeps its own humanized label (rendered from the declared `message` when the document carries one) and no finding is dropped for a value the frontend does not recognise.
- A mutating gesture is a declared action with a `skill`; the panel never invents one, never writes the tree, and an action whose `requires` are unmet renders inert with a reason instead of disappearing (an unknown requirement token fails closed to unavailable).
- Every gesture that dispatches goes through two gates: the panel's dispatch dialog first (the DECLARED prompt verbatim, sent on confirm — a repo-authored prompt never enters the chat unseen), then the single send path (`useSpecsDispatch` → `sendMessage` with the skill in the `activeSkills` slot); a dispatch failure lands on the panel's own error banner, not a global toast, and Shift dispatches into a new session.
- The tab strip's answer precedes the panel: `sddStore` holds the framework projection fed by the panel's parse (no second fetch, no second parse), and before a project's first answer the tab stays visible — unknown is not absent. A remembered Specs tab falls back to the layout's primary tab when the gate is off or nothing is detected (the indexing and research gates' contract).
- `specsStore` fetches are last-write-wins by initiation order and stamped with the (project, session) pair read at fetch start; a failed fetch keeps the previous status and records the error; a same-(project, session) refetch keeps the previous status rendered (no flicker).
- The panel refreshes on mount (tab activation), on an active-session switch, on `workspace:tree_changed` scoped to the active root, and on the header's manual refresh — all through the same fetch funnel; there is no `specs:*` event and no refresh-specific RPC.
- Decision documents' `date` is derived from git history in the backend AFTER the pure parse (one bounded `git log --diff-filter=A` call, skipped when no document's declared concept classifies it as a decision (concept kind `decision`)), in the workspace the tree was parsed in; it is never written into the documents, and a non-git workspace, a missing git binary, an unborn HEAD or any git error leaves dates absent while the RPC still succeeds (Debug-only logging).
- `GetSpecsStatus` requires a real project: the No Project pseudo-project returns the neutral `present=false` DTO (the defensive contract — the tab strip and the panel are not rendered in CHAT mode), and only a transport-level problem (unknown project, uninitialized subsystem) is a Go error. An unparseable workspace degrades to detected-but-empty with a warning.
- The project-scoped DSL file is untrusted-adjacent input: strict validation, workspace-contained reads, no execution — a declared action's prompt only reaches the model after the user has seen it verbatim in the panel's dispatch dialog and confirmed the send, and the dispatched task still runs through the ordinary policy/judge/confirmation pipeline ([sdd-dsl.md](sdd-dsl.md) § Security Model).
- `specs.enabled` is a PURE visibility gate: `SetSpecsEnabled` persists (a failed write rolls the in-memory value back; a same-value request is a no-op), announces `config:updated`, and touches nothing on disk. Recognition, the DSL files, the roots and the read RPCs are unaffected by the toggle.
- This repository's own `specs/decisions/` tree carries a hard version of three of its own declared checks: `internal/specslint`'s `TestADRNumberingContract` runs in the default Go suite and fails on a file that is neither `NNN-slug.md` nor `_template.md`, on a missing `_template.md`, on a first line that is not an `# ADR-NNN:` heading, on an H1 number disagreeing with the file name's, and on one NNN claimed by two files.

## Configuration

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| Surface gate | `specs.enabled: true` | Settings → General toggle ([ADR-115](../decisions/115-sidebar-pack-gates-reconcile-and-serialization.md), specs half amended by [ADR-126](../decisions/126-sdd-framework-dsl.md)): off hides the Specs tab and the panel; no pack side-effect |
| Global definitions file | `~/.c0wrk/specs.yaml` (`config.SDDPath`) | Optional operator DSL stream, merged after the built-ins, never created |
| Project definitions file | `<checkout>/.c0wrk/specs.yaml` (`config.ProjectSDDPath`) | Optional operator DSL stream travelling with the repository, merged last, never created |
| Active framework | detection default | Per-project `project_ui_state.active_sdd`; `SetActiveSDD` writes it, an empty value restores the default |
| Document size cap | 8 MiB | One oversized document is skipped with a warning |

There is no per-project toggle and no experimental gate: the surface is available wherever a framework is recognised and the gate is on. The framework definitions themselves are the configuration surface ([sdd-dsl.md](sdd-dsl.md) § Configuration).

## Extension Points

- **Support another SDD framework** by adding a `sdd-framework/v1` document to `~/.c0wrk/specs.yaml` or `<checkout>/.c0wrk/specs.yaml` (or embedding it in `core/sdd/frameworks/`) — no Go and no frontend change ([sdd-dsl.md](sdd-dsl.md) § Extension Points).
- **Change what a gesture does** by editing the declaring action's `skill`/`prompt` in the framework document; the panel renders whatever is declared and has no gesture vocabulary of its own.
- **Add a view** by declaring a `views.sections` entry; the panel renders `tree` sections as nested trees and `findings`/`checks` sections as the findings list, in declaration order.
- **Add a check** as a new DSL kind (a reviewed code change in `core/sdd`), then reference it from the framework documents.
- **Add a framework-neutral field to the status** by extending the DTO, `applySDDTree` and the frontend boundary guard together — additive only, so an older payload still validates.

## Related Specs

- [sdd-dsl.md](sdd-dsl.md) - the declarative framework description this subsystem renders: format, primitives, merge rules, security.
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) - `GetSpecsStatus` / `SetActiveSDD` and the `SpecsStatusDTO` wire shape.
- [../decisions/126-sdd-framework-dsl.md](../decisions/126-sdd-framework-dsl.md) - the decision that replaced the fixed layout and the shipped skillset with detection + DSL.
- [../decisions/106-specs-sidebar-panel.md](../decisions/106-specs-sidebar-panel.md), [../decisions/108-specs-panel-usability-redesign.md](../decisions/108-specs-panel-usability-redesign.md), [../decisions/109-specs-panel-simplification.md](../decisions/109-specs-panel-simplification.md) - the panel's shape: one read RPC, the sidebar-only surface, the findings gesture.
- [../decisions/116-specs-root-follows-active-session.md](../decisions/116-specs-root-follows-active-session.md) - the session-following root protocol this subsystem reuses.
- [../decisions/115-sidebar-pack-gates-reconcile-and-serialization.md](../decisions/115-sidebar-pack-gates-reconcile-and-serialization.md) - the sidebar gate contract (research half authoritative; specs half amended).
- [research.md](research.md) - the sibling always-on research surface, for the contrast in how the two sidebar gates behave.
- [../architecture/security-model.md](../architecture/security-model.md) - the policy pipeline a dispatched action's task runs under.
