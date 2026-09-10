import { useMemo, useRef, useState } from 'react'
import { Check, ChevronDown, Loader2, Pin, PinOff, Plus } from 'lucide-react'
import { cn } from '@/lib/utils'
import { logger } from '@/lib/logger'
import { updateHypothesis, setHypothesisPinned } from '@/api/research'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useProjectStore } from '@/stores/projectStore'
import {
  useResearchStore,
  selectActiveProject,
  selectActiveHypothesisId,
} from '@/stores/researchStore'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import { ItemAction, ItemActions } from '@/components/layout/ItemAction'
import {
  applyGraphOrRefresh,
  fullResearchRefresh,
  refreshNextStep,
} from './applyGraphOrRefresh'
import {
  statusColorVar,
  projectDir,
  isHypothesisPinned,
} from './researchDagRender'
import { statusOptions } from './hypothesisStatus'
import { CREATE_HYPOTHESIS_ACTION } from './researchActions'
import type { HypothesisNode, HypothesisStatus } from '@/types/models'

/** Numeric H-NNN sort key (ids without a canonical form sort last). */
function hypothesisSortKey(id: string): number {
  const m = /^H-(\d+)$/i.exec(id.trim())
  return m ? Number(m[1]) : Number.MAX_SAFE_INTEGER
}

/**
 * Hypothesis combobox for the dashboard (under the Next step card) — the
 * control surface for the dashboard's CURRENT card (the card the recommended
 * next step is scoped to). Lists every hypothesis of the active research
 * (pinned first, then the active front, then the rest — each group by
 * H-NNN) with status dots; picking one stamps it as the current card
 * (`setActiveHypothesis`) and refetches the next step scoped to it. The
 * adjacent status select persists a flip through the UpdateHypothesis RPC
 * (inherited from the former ResearchQuickMutate block: the [71] action
 * generation mutex, the cross-project guard, the [60] LWW ticket, and the
 * shared applyGraphOrRefresh convergence path), and the plus button
 * dispatches the Create-hypothesis gesture.
 */
export function ResearchHypothesisPicker() {
  const { send } = useMessageSender()
  const project = useResearchStore(selectActiveProject)
  const currentId = useResearchStore(selectActiveHypothesisId)
  const pinnedHypotheses = useResearchStore((s) => s.pinnedHypotheses)
  const root = useResearchStore((s) => s.status?.root)

  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // [71] Action generation: bumped at every mutation START and captured by
  // the mutating action. Effects on this block's local state (saving /
  // error) apply only while the captured generation is current, so a late
  // resolve/failure of an OLDER flip can neither re-enable nor annotate
  // over a NEWER flip's state.
  const generationRef = useRef(0)

  const nodes = useMemo(() => project?.graph.nodes ?? [], [project])
  const dir = project ? projectDir(root, project.id) : ''
  const currentNode =
    currentId !== '' ? nodes.find((n) => n.id === currentId) ?? null : null

  // Pinned first, then the active front, then the rest — each by H-NNN.
  const sortedNodes = useMemo(() => {
    const front = new Set(project?.metrics.active_front ?? [])
    const rank = (n: HypothesisNode): number => {
      if (isHypothesisPinned(pinnedHypotheses, n.id, dir)) return 0
      return front.has(n.id) ? 1 : 2
    }
    return [...nodes].sort(
      (a, b) => rank(a) - rank(b) || hypothesisSortKey(a.id) - hypothesisSortKey(b.id),
    )
  }, [nodes, project, pinnedHypotheses, dir])

  const changeStatus = async (node: HypothesisNode, status: HypothesisStatus) => {
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
    // Cross-project guard (see useHypothesisEditor's handleSave): the
    // research store's snapshot is stamped with the workspace project it
    // was loaded for. After a workspace project switch the store can keep
    // rendering the OLD project's graph until the new project's status
    // fetch lands, and R-NNN / H-NNN ids collide across projects — an
    // unconditional flip would overwrite the NEW project's card. Bail
    // without sending.
    if (store.projectId !== projectId) {
      setError(
        'The research view belongs to a different project — re-open it after the project switch.',
      )
      return
    }

    // [71]: capture this action's generation.
    const generation = ++generationRef.current
    setSaving(true)
    setError(null)
    try {
      // [60] LWW ticket: capture the sync sequence at RPC START so the
      // store can reject this response when a newer sync (watchdog refresh,
      // file-watcher fallback) lands while the mutation is in flight —
      // applying the older snapshot would visually revert the flip.
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
  }

  const handleSelect = async (id: string) => {
    if (id === currentId) return
    useResearchStore.getState().setActiveHypothesis(id)
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId) return
    // The recommendation follows the newly picked card (refreshNextStep
    // resolves the current card from the live store and is a no-op fetch
    // guard when the active project moved on).
    await refreshNextStep(projectId)
  }

  const handlePin = async (hypothesisId: string, pinned: boolean) => {
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId) return
    const researchId = selectActiveProject(useResearchStore.getState())?.id ?? null
    if (!researchId) return
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
  }

  // [22]a: send() renders sendMessage failures in-chat itself, but RETHROWS
  // when the auto-created session fails (the splash race); the rejection is
  // surfaced on the research panel's error banner (the research store).
  const handleCreate = (e: React.MouseEvent<HTMLButtonElement>) =>
    Promise.resolve(
      send(
        CREATE_HYPOTHESIS_ACTION.prompt,
        [CREATE_HYPOTHESIS_ACTION.skill],
        undefined,
        undefined,
        { newSession: e.shiftKey },
      ),
    ).catch((err) => {
      useResearchStore
        .getState()
        .setError(
          `Failed to dispatch ${CREATE_HYPOTHESIS_ACTION.skill}: ${
            err instanceof Error ? err.message : 'unknown error'
          }`,
        )
    })

  const hasNodes = nodes.length > 0

  return (
    <div
      data-testid="research-hypothesis-picker"
      aria-busy={saving}
      className="flex shrink-0 flex-col gap-1"
    >
      <span className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
        Hypothesis
      </span>

      <div className="flex items-center gap-1">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              data-testid="research-hypothesis-picker-trigger"
              disabled={!hasNodes}
              title={
                hasNodes
                  ? 'Pick the current hypothesis (scopes Next step and the quick actions)'
                  : 'No hypotheses yet — create one with +'
              }
              className="h-6 min-w-0 flex-1 justify-between gap-1 px-1.5 text-xs"
            >
              {currentNode ? (
                <span className="flex min-w-0 items-center gap-1.5">
                  <span
                    className="size-2 shrink-0 rounded-full"
                    style={{ backgroundColor: statusColorVar(currentNode.status) }}
                    aria-hidden
                  />
                  <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                    {currentNode.id}
                  </span>
                  <span
                    className="min-w-0 truncate text-muted-foreground"
                    title={currentNode.title}
                  >
                    {currentNode.title}
                  </span>
                </span>
              ) : (
                <span className="truncate text-muted-foreground">
                  {hasNodes ? 'Select hypothesis' : 'No hypotheses yet'}
                </span>
              )}
              <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-72">
            {sortedNodes.map((n) => {
              const pinned = isHypothesisPinned(pinnedHypotheses, n.id, dir)
              return (
                <DropdownMenuItem
                  key={n.id}
                  className="group/item gap-2"
                  onSelect={() => void handleSelect(n.id)}
                >
                  <div className="flex min-w-0 flex-1 items-center gap-1.5">
                    {n.id === currentId && <Check className="size-3.5 shrink-0" />}
                    <span
                      className="size-2 shrink-0 rounded-full"
                      style={{ backgroundColor: statusColorVar(n.status) }}
                      aria-hidden
                    />
                    <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                      {n.id}
                    </span>
                    <span
                      className={cn(
                        'min-w-0 flex-1 truncate',
                        n.id === currentId && 'font-medium',
                      )}
                      title={n.title}
                    >
                      {n.title}
                    </span>
                  </div>
                  <ItemActions>
                    <ItemAction
                      label={pinned ? 'Unpin' : 'Pin'}
                      onClick={() => void handlePin(n.id, !pinned)}
                    >
                      {pinned ? (
                        <PinOff className="size-3 text-info" />
                      ) : (
                        <Pin className="size-3 text-info" />
                      )}
                    </ItemAction>
                  </ItemActions>
                </DropdownMenuItem>
              )
            })}
          </DropdownMenuContent>
        </DropdownMenu>

        {currentNode && (
          <select
            value={currentNode.status}
            disabled={saving}
            aria-label={`Status for ${currentNode.id}`}
            onChange={(e) =>
              void changeStatus(currentNode, e.target.value as HypothesisStatus)
            }
            title="Change the current hypothesis's status (legal transitions only)"
            className="h-6 w-28 shrink-0 rounded border border-input bg-background px-1 text-[11px] outline-none focus:border-primary disabled:opacity-50"
          >
            {statusOptions(currentNode.status).map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        )}

        {saving && (
          <Loader2 className="size-3 shrink-0 animate-spin text-muted-foreground" />
        )}

        <button
          type="button"
          data-testid="research-create-hypothesis"
          onClick={(e) => handleCreate(e)}
          title="Create a new hypothesis (research-hypothesis) — Shift = new session"
          aria-label="Create a new hypothesis"
          className="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-muted/50 active:bg-muted/30"
        >
          <Plus className="size-3.5" />
        </button>
      </div>

      {error && (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      )}
    </div>
  )
}
