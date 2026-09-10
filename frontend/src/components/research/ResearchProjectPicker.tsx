import { useCallback, useMemo, useState } from 'react'
import { Check, ChevronDown, Loader2, Pin, PinOff, Plus, Trash2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { logger } from '@/lib/logger'
import {
  setActiveResearch,
  deleteResearch,
  setResearchPinned,
} from '@/api/research'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useProjectStore } from '@/stores/projectStore'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { ItemAction, ItemActions } from '@/components/layout/ItemAction'
import { fullResearchRefresh, refreshNextStep } from './applyGraphOrRefresh'
import { NEXT_STEP_PROMPTS } from './researchActions'
import { projectDir, isResearchProjectPinned } from './researchDagRender'

/** Numeric R-NNN sort key (ids without a canonical form sort last). */
function researchSortKey(id: string): number {
  const m = /^R-(\d+)$/i.exec(id.trim())
  return m ? Number(m[1]) : Number.MAX_SAFE_INTEGER
}

/**
 * Research-project combobox for the panel header — the R-NNN counterpart of
 * the sidebar's ProjectSelector (DropdownMenu + Button + Check on the active
 * entry, hover ItemActions per row). The whole research root's projects are
 * listed, pinned first, then by R-NNN. Selecting an entry makes it the
 * active research through the SetActiveResearch RPC (the response is applied
 * via loadStatus with the [60] LWW ticket captured at RPC start, then the
 * next step is refetched scoped to the reconciled current card); the plus
 * button dispatches `research-init` into a brand-new session.
 */
export function ResearchProjectPicker() {
  const { send } = useMessageSender()
  const root = useResearchStore((s) => s.status?.root)
  const pinnedResearch = useResearchStore((s) => s.pinnedResearch)
  const activeProject = useResearchStore(selectActiveProject)

  const [dropdownOpen, setDropdownOpen] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState<{
    id: string
    title: string
  } | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const projects = useMemo(() => root?.projects ?? [], [root])
  const activeId = activeProject?.id ?? null

  // Pinned set: a project is pinned when one of the persisted pin paths lies
  // inside its directory (see isResearchProjectPinned).
  const pinnedProjects = useMemo(() => {
    const set = new Set<string>()
    for (const p of projects) {
      if (isResearchProjectPinned(pinnedResearch, projectDir(root, p.id))) {
        set.add(p.id)
      }
    }
    return set
  }, [projects, root, pinnedResearch])

  // Pinned first, then ascending R-NNN.
  const sortedProjects = useMemo(
    () =>
      [...projects].sort((a, b) => {
        const pa = pinnedProjects.has(a.id) ? 0 : 1
        const pb = pinnedProjects.has(b.id) ? 0 : 1
        if (pa !== pb) return pa - pb
        return researchSortKey(a.id) - researchSortKey(b.id)
      }),
    [projects, pinnedProjects],
  )

  const handleSelect = useCallback(
    async (researchId: string) => {
      const projectId = useProjectStore.getState().activeProjectId
      if (!projectId || researchId === activeId) return
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
    },
    [activeId],
  )

  const handlePin = useCallback(async (researchId: string, pinned: boolean) => {
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId) return
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

  const handleDelete = useCallback(async () => {
    if (!confirmDelete) return
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId) return
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

  // [22]a: send() renders sendMessage failures in-chat itself, but RETHROWS
  // when the auto-created session fails (the splash race). The rejection is
  // surfaced on the research panel's error banner (the research store).
  const handleInit = () =>
    Promise.resolve(
      send(
        NEXT_STEP_PROMPTS['research-init'],
        ['research-init'],
        undefined,
        undefined,
        { newSession: true },
      ),
    ).catch((err) => {
      useResearchStore
        .getState()
        .setError(
          `Failed to dispatch research-init: ${
            err instanceof Error ? err.message : 'unknown error'
          }`,
        )
    })

  const hasProjects = projects.length > 0

  return (
    <div className="flex min-w-0 flex-1 items-center gap-1">
      <DropdownMenu open={dropdownOpen} onOpenChange={setDropdownOpen}>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            data-testid="research-project-picker"
            disabled={!hasProjects}
            title={
              hasProjects
                ? 'Switch the active research project'
                : 'No research projects yet — start one with +'
            }
            className="h-6 min-w-0 flex-1 justify-between gap-1 px-1.5 text-xs"
          >
            <span className="truncate text-muted-foreground">
              {activeProject?.brief.title ?? 'Select research'}
            </span>
            <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-72">
          {sortedProjects.map((p) => {
            const pinned = pinnedProjects.has(p.id)
            return (
              <DropdownMenuItem
                key={p.id}
                className="group/item gap-2"
                onSelect={() => void handleSelect(p.id)}
              >
                <div className="flex min-w-0 flex-1 items-center gap-1.5">
                  {p.id === activeId && <Check className="size-3.5 shrink-0" />}
                  <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                    {p.id}
                  </span>
                  <span
                    className={cn(
                      'min-w-0 flex-1 truncate',
                      p.id === activeId && 'font-medium',
                    )}
                    title={p.brief.title}
                  >
                    {p.brief.title}
                  </span>
                </div>
                <ItemActions>
                  <ItemAction
                    label={pinned ? 'Unpin' : 'Pin'}
                    onClick={() => {
                      void handlePin(p.id, !pinned)
                      setDropdownOpen(false)
                    }}
                  >
                    {pinned ? (
                      <PinOff className="size-3 text-info" />
                    ) : (
                      <Pin className="size-3 text-info" />
                    )}
                  </ItemAction>
                  <ItemAction
                    label="Delete"
                    onClick={() => {
                      setConfirmDelete({ id: p.id, title: p.brief.title })
                      setDropdownOpen(false)
                    }}
                  >
                    <Trash2 className="size-3 text-destructive" />
                  </ItemAction>
                </ItemActions>
              </DropdownMenuItem>
            )
          })}
        </DropdownMenuContent>
      </DropdownMenu>

      <button
        type="button"
        data-testid="research-project-init"
        onClick={() => void handleInit()}
        title="Initialize a new research project (research-init) — new session"
        aria-label="Initialize a new research project"
        className="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-muted/50 active:bg-muted/30"
      >
        <Plus className="size-3.5" />
      </button>

      <Dialog
        open={confirmDelete !== null}
        onOpenChange={(o) => {
          if (!o && !deleting) {
            setConfirmDelete(null)
            setDeleteError(null)
          }
        }}
      >
        <DialogContent className="sm:max-w-md" showCloseButton={false}>
          <DialogHeader>
            <div className="flex items-center gap-3">
              <div className="flex size-10 shrink-0 items-center justify-center rounded-full bg-destructive/15">
                <Trash2 className="size-5 text-destructive" />
              </div>
              <div className="flex flex-col gap-1">
                <DialogTitle>Delete research project?</DialogTitle>
                <DialogDescription>
                  {confirmDelete?.id} — {confirmDelete?.title}. Its directory tree,
                  index rows, and pins are removed permanently. This cannot be
                  undone.
                </DialogDescription>
              </div>
            </div>
          </DialogHeader>

          {deleteError && (
            <div className="text-xs text-destructive" role="alert">
              {deleteError}
            </div>
          )}

          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setConfirmDelete(null)
                setDeleteError(null)
              }}
              disabled={deleting}
              autoFocus
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => void handleDelete()}
              disabled={deleting}
            >
              {deleting && <Loader2 className="size-3.5 animate-spin" />}
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
