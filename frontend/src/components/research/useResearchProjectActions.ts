// Mutation flows for the research pickers — the H-NNN hypothesis picker's
// status flip + pin, and the R-NNN project picker's pin / delete flows.
// Extracted from the picker components (mirroring useHypothesisEditor) so
// each picker file stays a thin composition shell under the project's
// ~200-line component target.
//
// Every RPC entry point carries the same cross-project guard the codebase
// already enforces elsewhere (changeStatus / useHypothesisEditor.handleSave):
// the research store's snapshot is stamped with the workspace project it was
// loaded for, and after a workspace project switch the store can keep
// rendering the OLD project's graph until the new project's status fetch
// lands. R-NNN / H-NNN ids collide across projects (every workspace's
// research starts at R-001), so an unguarded mutation would hit the NEW
// project's same-id record — the delete path permanently.
import { useCallback, useRef, useState } from 'react'
import { logger } from '@/lib/logger'
import {
  deleteResearch,
  setActiveResearch,
  setResearchPinned,
  updateHypothesis,
  setHypothesisPinned,
} from '@/api/research'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import { applyGraphOrRefresh, fullResearchRefresh, refreshNextStep } from './applyGraphOrRefresh'
import { CREATE_HYPOTHESIS_ACTION, NEXT_STEP_PROMPTS } from './researchActions'
import type { HypothesisNode, HypothesisStatus } from '@/types/models'

/** Delete-flow target captured when the user opens the confirm dialog. */
export interface DeleteTarget {
  id: string
  title: string
}

const CROSS_PROJECT_MESSAGE =
  'The research view belongs to a different project — re-open it after the project switch.'

export function useResearchProjectActions() {
  const { send } = useMessageSender()

  // ── Hypothesis-card surface (H-NNN picker) ────────────────────────────
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // [71] Action generation: bumped at every mutation START and captured by
  // the mutating action. Effects on this local state (saving / error) apply
  // only while the captured generation is current, so a late resolve/failure
  // of an OLDER flip can neither re-enable nor annotate over a NEWER flip's
  // state.
  const generationRef = useRef(0)

  const changeStatus = useCallback(
    async (node: HypothesisNode, status: HypothesisStatus) => {
      const projectId = useProjectStore.getState().activeProjectId
      if (!projectId || status === node.status) return
      // [19]a: resolve the research project from the LIVE store at action
      // start — the click's render snapshot can be one sync behind a
      // research-init that switched the active R-NNN, and H-001-style ids
      // collide across projects. The fresh read (plus the backend's [19]b
      // expected-R validation) keeps the flip on the project the user sees.
      const store = useResearchStore.getState()
      const researchId = selectActiveProject(store)?.id ?? null
      if (!researchId) return
      // Cross-project guard (see useHypothesisEditor's handleSave): bail
      // without sending when the rendered graph belongs to another project.
      if (store.projectId !== projectId) {
        setError(CROSS_PROJECT_MESSAGE)
        return
      }

      // [71]: capture this action's generation.
      const generation = ++generationRef.current
      setSaving(true)
      setError(null)
      try {
        // [60] LWW ticket: capture the sync sequence at RPC START so the
        // store can reject this response when a newer sync (watchdog
        // refresh, file-watcher fallback) lands while the mutation is in
        // flight — applying the older snapshot would visually revert the
        // flip.
        const startedSeq = useResearchStore.getState().graphSyncSeq
        const res = await updateHypothesis(projectId, researchId, node.id, { status })
        // [18]b: apply through the shared convergence helper (active-project
        // re-check + incremental loadGraph + full-refetch fallback).
        if (generation === generationRef.current) {
          await applyGraphOrRefresh(res, projectId, startedSeq)
        }
      } catch (err) {
        if (generation === generationRef.current) {
          logger.error('Failed to update hypothesis status:', err)
          setError(err instanceof Error ? err.message : 'Failed to update hypothesis status')
        }
      } finally {
        if (generation === generationRef.current) {
          setSaving(false)
        }
      }
    },
    [],
  )

  const handleHypothesisPin = useCallback(
    async (hypothesisId: string, pinned: boolean) => {
      const projectId = useProjectStore.getState().activeProjectId
      if (!projectId) return
      const store = useResearchStore.getState()
      const researchId = selectActiveProject(store)?.id ?? null
      if (!researchId) return
      // Cross-project guard: after a workspace project switch the store can
      // keep rendering the OLD project's graph until the new project's
      // status fetch lands, and R-NNN / H-NNN ids collide across projects —
      // an unconditional pin would pin the NEW project's same-id card. Bail
      // without sending.
      if (store.projectId !== projectId) {
        setError(CROSS_PROJECT_MESSAGE)
        return
      }
      try {
        // The pin RPC emits no event — the resolved promise is the refresh
        // signal; the full refetch mirrors the new pins into the store (and
        // re-sorts the list: pinned cards float to the top).
        await setHypothesisPinned(projectId, researchId, hypothesisId, pinned)
        await fullResearchRefresh(projectId)
      } catch (err) {
        logger.error('Failed to toggle hypothesis pin:', err)
        setError(err instanceof Error ? err.message : 'Failed to toggle hypothesis pin')
      }
    },
    [],
  )

  /** Stamp the picked card as current and refetch the scoped next step. */
  const handleSelectHypothesis = useCallback(async (id: string, currentId: string) => {
    if (id === currentId) return
    useResearchStore.getState().setActiveHypothesis(id)
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId) return
    // The recommendation follows the newly picked card (refreshNextStep
    // resolves the current card from the live store and is a no-op fetch
    // guard when the active project moved on).
    await refreshNextStep(projectId)
  }, [])

  // ── Research-project surface (R-NNN picker) ───────────────────────────

  /** Switch the active research through SetActiveResearch and refetch the
   *  next step scoped to the reconciled current card. */
  const handleSelectResearch = useCallback(async (researchId: string) => {
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId) return
    const store = useResearchStore.getState()
    const activeId = selectActiveProject(store)?.id ?? null
    if (researchId === activeId) return
    // Cross-project guard (mirrors the sibling mutations): the clicked R-NNN
    // row can come from a stale rendered list after a workspace switch, and
    // every workspace's research starts at R-001 — the backend accepts a
    // foreign id when it collides, so an unguarded call would silently switch
    // the NEW project's same-id record active. Bail without sending.
    if (store.projectId !== projectId) {
      useResearchStore.getState().setError(CROSS_PROJECT_MESSAGE)
      return
    }
    // [60] LWW ticket: capture the sync sequence at RPC start so the store
    // can reject this status when a newer sync lands while it is in flight.
    const startedSeq = useResearchStore.getState().graphSyncSeq
    try {
      const status = await setActiveResearch(projectId, researchId)
      // [18]a: a workspace project switch mid-flight drops the payload —
      // the new project's own load is authoritative.
      if (useProjectStore.getState().activeProjectId !== projectId) return
      useResearchStore.getState().loadStatus(status, projectId, startedSeq)
      await refreshNextStep(projectId)
    } catch (err) {
      logger.error('Failed to switch active research:', err)
      useResearchStore.getState().setError(
        err instanceof Error ? err.message : 'Failed to switch active research',
      )
    }
  }, [])

  // [22]a: send() renders sendMessage failures in-chat itself, but RETHROWS
  // when the auto-created session fails (the splash race). The rejection is
  // surfaced on the research panel's error banner (the research store).
  const dispatchSessionAction = useCallback(
    (prompt: string, skill: string, newSession: boolean, skillLabel: string) =>
      Promise.resolve(
        send(prompt, [skill], undefined, undefined, undefined, { newSession }),
      ).catch((err) => {
        useResearchStore
          .getState()
          .setError(
            `Failed to dispatch ${skillLabel}: ${
              err instanceof Error ? err.message : 'unknown error'
            }`,
          )
      }),
    [send],
  )

  /** The Create-hypothesis plus gesture (Shift = brand-new session). */
  const dispatchCreateHypothesis = useCallback(
    (newSession: boolean) =>
      dispatchSessionAction(
        CREATE_HYPOTHESIS_ACTION.prompt,
        CREATE_HYPOTHESIS_ACTION.skill,
        newSession,
        CREATE_HYPOTHESIS_ACTION.skill,
      ),
    [dispatchSessionAction],
  )

  /** The research-init plus gesture (always a brand-new session). */
  const dispatchResearchInit = useCallback(
    () =>
      dispatchSessionAction(
        NEXT_STEP_PROMPTS['research-init'],
        'research-init',
        true,
        'research-init',
      ),
    [dispatchSessionAction],
  )

  const handleResearchPin = useCallback(async (researchId: string, pinned: boolean) => {
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId) return
    // Cross-project guard: the clicked row comes from the possibly-stale
    // rendered list; every workspace's research starts at R-001, so an
    // unguarded pin would pin the NEW project's same-id record.
    if (useResearchStore.getState().projectId !== projectId) {
      useResearchStore.getState().setError(CROSS_PROJECT_MESSAGE)
      return
    }
    try {
      // The pin RPC emits no event — the resolved promise is the refresh
      // signal; the full refetch mirrors the new pins into the store.
      await setResearchPinned(projectId, researchId, pinned)
      await fullResearchRefresh(projectId)
    } catch (err) {
      logger.error('Failed to toggle research pin:', err)
      useResearchStore.getState().setError(
        err instanceof Error ? err.message : 'Failed to toggle research pin',
      )
    }
  }, [])

  // Delete-flow state: the confirm dialog's captured target plus the
  // in-flight flag and inline error rendered inside it.
  const [confirmDelete, setConfirmDelete] = useState<DeleteTarget | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const requestDelete = useCallback((target: DeleteTarget) => {
    setConfirmDelete(target)
    setDeleteError(null)
  }, [])

  const closeDeleteDialog = useCallback(() => {
    setConfirmDelete(null)
    setDeleteError(null)
  }, [])

  const handleDelete = useCallback(async () => {
    if (!confirmDelete) return
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId) return
    // Cross-project guard on the destructive path: if the workspace project
    // changed while the modal was open, deleteResearch would permanently
    // remove the NEW project's same-id research tree ("cannot be undone").
    if (useResearchStore.getState().projectId !== projectId) {
      setDeleteError('The workspace project changed — re-open the research panel and retry.')
      return
    }
    const startedSeq = useResearchStore.getState().graphSyncSeq
    setDeleting(true)
    setDeleteError(null)
    try {
      const status = await deleteResearch(projectId, confirmDelete.id)
      if (useProjectStore.getState().activeProjectId === projectId) {
        useResearchStore.getState().loadStatus(status, projectId, startedSeq)
      }
      // Deleting the active project moves the active selection — refresh the
      // recommendation for whatever is active now.
      await refreshNextStep(projectId)
      setConfirmDelete(null)
    } catch (err) {
      logger.error('Failed to delete research project:', err)
      // Rendered inline in the dialog (mirroring ExitConfirmDialog): the
      // user can retry or cancel — a banner behind the modal would be hidden.
      setDeleteError(
        err instanceof Error ? err.message : 'Failed to delete research project',
      )
    } finally {
      setDeleting(false)
    }
  }, [confirmDelete])

  return {
    // Hypothesis-card surface
    saving,
    error,
    changeStatus,
    handleHypothesisPin,
    handleSelectHypothesis,
    dispatchCreateHypothesis,
    // Research-project surface
    handleSelectResearch,
    handleResearchPin,
    dispatchResearchInit,
    confirmDelete,
    deleting,
    deleteError,
    requestDelete,
    closeDeleteDialog,
    handleDelete,
  }
}
