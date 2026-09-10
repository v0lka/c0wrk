// Editing state for the research workspace's hypothesis detail card.
//
// Owns everything between "a DAG node was clicked" and "the UpdateHypothesis
// RPC round-tripped": the selection click (snapshotting the draft at click
// time), the dirty derivation, the save round-trip, and the open-card viewer
// link. Extracted from ResearchWorkspace.tsx so the component file stays a
// thin layout shell; the draft itself lives in researchStore because it must
// survive the floating viewer's unmount/remount cycles.
import { useCallback, useMemo, useRef, useState } from 'react'
import { logger } from '@/lib/logger'
import { updateHypothesis } from '@/api/research'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { draftFromNode, buildUpdateFields } from './researchWorkspaceUtils'
import { applyGraphOrRefresh } from './applyGraphOrRefresh'
import { projectDir, hypothesisCardPath } from './researchDagRender'
import type {
  HypothesisGraph,
  HypothesisNode,
  HypothesisDraft,
} from '@/types/models'

export function useHypothesisEditor(
  graph: HypothesisGraph,
  selectedNode: HypothesisNode | null,
  draft: HypothesisDraft | null,
) {
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  const project = useResearchStore(selectActiveProject)
  const root = useResearchStore((s) => s.status?.root)
  const rootPath = useResearchStore((s) => s.status?.research_root ?? '')

  // [71] Action generation: bumped at every save START and captured by the
  // saving action. Effects on this hook's local state (saving / saveError)
  // apply only while the captured generation is still current, so a late
  // resolve/failure of an OLDER save can neither clear nor annotate over a
  // newer save's state (the same-path re-entry race the UI's disabled Save
  // button cannot fully rule out — e.g. programmatic re-entry mid-flight).
  const generationRef = useRef(0)

  // Latest-graph ref: the draft is snapshotted from the live graph at click
  // time (not via an effect, which would re-run on remount and wipe a
  // preserved unsaved draft) — so background file-change updates never
  // clobber an in-progress edit.
  const graphRef = useRef(graph)
  graphRef.current = graph

  const selectNode = useCallback((id: string) => {
    setSaveError(null)
    const store = useResearchStore.getState()
    // Clicking the already-selected node toggles the selection off — compared
    // as the composite (project, id) key so a stale selection from another
    // research project never swallows a click on the current project's
    // same-id node.
    const activeId = selectActiveProject(store)?.id ?? null
    if (
      store.selectedHypothesisProjectId === activeId &&
      store.selectedHypothesisId === id
    ) {
      store.selectHypothesis(null, null)
      return
    }
    const node = graphRef.current.nodes.find((n) => n.id === id)
    store.selectHypothesis(id, node ? draftFromNode(node) : null)
  }, [])

  const dirty = useMemo(() => {
    if (!selectedNode || !draft) return false
    return Object.keys(buildUpdateFields(selectedNode, draft)).length > 0
  }, [selectedNode, draft])

  const handleSave = useCallback(async () => {
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId || !selectedNode || !draft) return
    const fields = buildUpdateFields(selectedNode, draft)
    if (Object.keys(fields).length === 0) return

    // [19]a: the card (and its Save button) were resolved against the
    // store's snapshot at RENDER time; re-check freshness against the LIVE
    // store immediately before the RPC. The composite selection key must
    // still name the active research project — if the backend's active
    // R-NNN moved on (a research-init landed inside the sync lag) and the
    // new project holds a same-id hypothesis, an unconditional save would
    // overwrite the WRONG project's card. Abort and tell the user.
    const store = useResearchStore.getState()
    const researchId = store.selectedHypothesisProjectId
    if (!researchId || researchId !== selectActiveProject(store)?.id) {
      setSaveError(
        'The active research project changed — re-select the hypothesis and save again.',
      )
      return
    }
    // Cross-project guard: the research store's snapshot is stamped with the
    // workspace project it was loaded for (`projectId`). After a workspace
    // project switch the store can keep rendering the OLD project's graph
    // until the new project's status fetch lands, and every workspace's
    // research starts at R-001 with H-NNN ids — an unconditional save would
    // target the NEW project's research root with the old project's card id,
    // overwriting the new project's card. Bail without sending.
    if (store.projectId !== projectId) {
      setSaveError(
        'The research view belongs to a different project — re-open it after the project switch and save again.',
      )
      return
    }

    // [71]: capture this action's generation; a newer save started since
    // must not have its state clobbered by this one's completion.
    const generation = ++generationRef.current
    setSaving(true)
    setSaveError(null)
    try {
      // [60] LWW ticket: capture the sync sequence at RPC START so the
      // store can reject this response when a newer sync (watchdog refresh,
      // file-watcher fallback) lands while the save is in flight — applying
      // the older snapshot would visually revert the save.
      const startedSeq = useResearchStore.getState().graphSyncSeq
      // The expected R-NNN rides along ([19]b): the backend validates it
      // against the project's own research root and targets exactly this
      // project, closing the cross-R-NNN save race end-to-end.
      const res = await updateHypothesis(projectId, researchId, selectedNode.id, fields)
      // [18]b: apply through the shared convergence helper — the
      // active-project re-check, the incremental loadGraph, and the
      // full-refetch fallback live in one place.
      if (generation === generationRef.current) {
        await applyGraphOrRefresh(res, projectId, startedSeq)
      }
      // Keep the selection; the store now carries the persisted values.
    } catch (err) {
      if (generation === generationRef.current) {
        logger.error('Failed to update hypothesis:', err)
        setSaveError(
          err instanceof Error ? err.message : 'Failed to update hypothesis',
        )
      }
    } finally {
      if (generation === generationRef.current) {
        setSaving(false)
      }
    }
  }, [selectedNode, draft])

  // Open a hypothesis's markdown card as a sibling read-only tab in the file
  // viewer.
  const dir = project ? projectDir(root, project.id) : ''
  const openHypothesisCard = useCallback(
    (id: string) => {
      const store = useFileViewerStore.getState()
      store.setCollapsed(false)
      store.openFile(hypothesisCardPath(rootPath, dir, id))
    },
    [rootPath, dir],
  )

  return { saving, saveError, selectNode, dirty, handleSave, openHypothesisCard }
}
