import { useMemo } from 'react'
import { Check, ChevronDown, Loader2, Pin, PinOff, Plus } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useResearchStore, selectActiveProject, selectActiveHypothesisId } from '@/stores/researchStore'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import { ItemAction, ItemActions } from '@/components/layout/ItemAction'
import { statusColorVar, projectDir, isHypothesisPinned } from './researchDagRender'
import { statusOptions } from './hypothesisStatus'
import { useResearchProjectActions } from './useResearchProjectActions'
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
 * H-NNN) with status dots; picking one stamps it as the current card and
 * refetches the scoped next step; the status select persists a flip and the
 * plus button dispatches the Create gesture. All RPC flows (cross-project
 * guards, [71] generation mutex, [60] LWW ticket, applyGraphOrRefresh
 * convergence) live in useResearchProjectActions.
 */
export function ResearchHypothesisPicker() {
  const project = useResearchStore(selectActiveProject)
  const currentId = useResearchStore(selectActiveHypothesisId)
  const pinnedHypotheses = useResearchStore((s) => s.pinnedHypotheses)
  const root = useResearchStore((s) => s.status?.root)

  const { saving, error, changeStatus, handleHypothesisPin, handleSelectHypothesis, dispatchCreateHypothesis } =
    useResearchProjectActions()

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
                  onSelect={() => void handleSelectHypothesis(n.id, currentId)}
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
                      onClick={() => void handleHypothesisPin(n.id, !pinned)}
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
          <Combobox
            value={currentNode.status}
            disabled={saving}
            ariaLabel={`Status for ${currentNode.id}`}
            onChange={(v) =>
              void changeStatus(currentNode, v as HypothesisStatus)
            }
            options={statusOptions(currentNode.status).map((s) => ({
              value: s,
              label: s,
            }))}
            className="h-6 w-28 shrink-0 rounded border border-input px-1 text-[11px] text-foreground"
          />
        )}

        {saving && (
          <Loader2 className="size-3 shrink-0 animate-spin text-muted-foreground" />
        )}

        <button
          type="button"
          data-testid="research-create-hypothesis"
          onClick={(e) => void dispatchCreateHypothesis(e.shiftKey)}
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
