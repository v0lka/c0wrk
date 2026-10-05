import { useMemo, useState } from 'react'
import { Check, ChevronDown, Pin, PinOff, Plus, Trash2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import { ItemAction, ItemActions } from '@/components/layout/ItemAction'
import { projectDir, isResearchProjectPinned } from './researchDagRender'
import { useResearchProjectActions } from './useResearchProjectActions'
import { ConfirmDeleteResearchDialog } from './ConfirmDeleteResearchDialog'

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
 * active research through the SetActiveResearch RPC; the plus button
 * dispatches `research-init` into a brand-new session; the pin/delete row
 * actions and the delete-confirm dialog flow live in
 * useResearchProjectActions / ConfirmDeleteResearchDialog.
 */
export function ResearchProjectPicker() {
  const root = useResearchStore((s) => s.status?.root)
  const pinnedResearch = useResearchStore((s) => s.pinnedResearch)
  const activeProject = useResearchStore(selectActiveProject)

  const [dropdownOpen, setDropdownOpen] = useState(false)
  const {
    handleSelectResearch,
    handleResearchPin,
    dispatchResearchInit,
    confirmDelete,
    deleting,
    deleteError,
    requestDelete,
    closeDeleteDialog,
    handleDelete,
  } = useResearchProjectActions()

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
                onSelect={() => void handleSelectResearch(p.id)}
              >
                <div className="flex min-w-0 flex-1 items-center gap-1.5">
                  {p.id === activeId && <Check className="size-3.5 shrink-0" />}
                  <span className="shrink-0 font-mono text-xs text-muted-foreground">
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
                      void handleResearchPin(p.id, !pinned)
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
                      requestDelete({ id: p.id, title: p.brief.title })
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
        onClick={() => void dispatchResearchInit()}
        title="Initialize a new research project (research-init) — new session"
        aria-label="Initialize a new research project"
        className="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-muted/50 active:bg-muted/30"
      >
        <Plus className="size-3.5" />
      </button>

      <ConfirmDeleteResearchDialog
        target={confirmDelete}
        deleting={deleting}
        deleteError={deleteError}
        onConfirm={() => void handleDelete()}
        onClose={closeDeleteDialog}
      />
    </div>
  )
}
