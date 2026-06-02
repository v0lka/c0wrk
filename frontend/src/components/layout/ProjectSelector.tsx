import { useState, useCallback, useRef } from 'react'
import { useProjectStore } from '@/stores/projectStore'
import { renameProject, deleteProject } from '@/api/projects'
import { useProjectSwitchState } from '@/hooks/useProjectSwitchState'
import { CreateProjectDialog } from '@/components/project/CreateProjectDialog'
import { logger } from '@/lib/logger'
import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent,
  DropdownMenuItem, DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { ChevronDown, Check, FolderPlus, Pencil, Plus, Trash2 } from 'lucide-react'

export function ProjectSelector() {
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const removeProject = useProjectStore((s) => s.removeProject)
  const updateProject = useProjectStore((s) => s.updateProject)
  const switchProjectWithState = useProjectSwitchState()

  const [createOpen, setCreateOpen] = useState(false)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [dropdownOpen, setDropdownOpen] = useState(false)
  const renameRef = useRef<HTMLInputElement>(null)

  const activeProject = projects?.find((p) => p.id === activeProjectId)

  const handleSwitch = useCallback(async (id: string) => {
    if (id === activeProjectId) return
    try {
      await switchProjectWithState(id)
      setDropdownOpen(false)
    } catch (error) {
      logger.error('Failed to switch project:', error)
    }
  }, [activeProjectId, switchProjectWithState])

  const handleDelete = useCallback(async (id: string) => {
    try {
      await deleteProject(id)
      removeProject(id)
      if (id === activeProjectId) {
        const remaining = useProjectStore.getState().projects
        if (remaining && remaining.length > 0) {
          await switchProjectWithState(remaining[0]!.id)
        }
      }
    } catch (error) { logger.error('Failed to delete project:', error) }
  }, [removeProject, activeProjectId, switchProjectWithState])

  const startRename = useCallback((id: string, currentName: string) => {
    setRenamingId(id)
    setRenameValue(currentName)
    setTimeout(() => renameRef.current?.focus(), 50)
  }, [])

  const commitRename = useCallback(async () => {
    if (!renamingId || !renameValue.trim()) { setRenamingId(null); return }
    try {
      await renameProject(renamingId, renameValue.trim())
      updateProject(renamingId, { name: renameValue.trim() })
    } catch (error) { logger.error('Failed to rename project:', error) }
    setRenamingId(null)
  }, [renamingId, renameValue, updateProject])

  if (renamingId) {
    return (
      <div className="px-2 py-1">
        <Input
          ref={renameRef}
          value={renameValue}
          onChange={(e) => setRenameValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') commitRename()
            if (e.key === 'Escape') setRenamingId(null)
          }}
          onBlur={commitRename}
          className="h-7 text-sm"
        />
      </div>
    )
  }

  return (
    <>
      <div className="px-2 py-1">
        <div className="flex items-center gap-1">
          <DropdownMenu open={dropdownOpen} onOpenChange={setDropdownOpen}>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" className="h-7 flex-1 min-w-0 justify-between gap-1 px-2 text-sm font-medium">
                <span className="truncate">{activeProject?.name ?? 'Select project'}</span>
                <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="w-56">
              {projects?.map((project) => (
                <DropdownMenuItem
                  key={project.id}
                  className="group/item gap-2"
                  onSelect={() => handleSwitch(project.id)}
                >
                  {project.id === activeProjectId && <Check className="size-3.5 shrink-0" />}
                  <span className="flex-1 truncate">{project.name}</span>
                  <span
                    className="ml-auto flex shrink-0 gap-0.5 opacity-0 transition-opacity group-hover/item:opacity-100 group-focus-within/item:opacity-100"
                    onPointerDown={(e) => e.stopPropagation()}
                    onPointerUp={(e) => e.stopPropagation()}
                    onClick={(e) => e.stopPropagation()}
                  >
                    <button type="button" className="rounded p-0.5 hover:bg-accent" onClick={() => { startRename(project.id, project.name); setDropdownOpen(false) }}>
                      <Pencil className="size-3" />
                    </button>
                    <button type="button" className="rounded p-0.5 hover:bg-destructive/20" onClick={() => { handleDelete(project.id); setDropdownOpen(false) }}>
                      <Trash2 className="size-3 text-destructive" />
                    </button>
                  </span>
                </DropdownMenuItem>
              ))}
              {projects && projects.length > 0 && <DropdownMenuSeparator />}
              <DropdownMenuItem onClick={() => setCreateOpen(true)}>
                <FolderPlus className="size-3.5" />
                New Project...
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <button type="button" className="rounded p-0.5 hover:bg-muted/50 active:bg-muted/30" onClick={() => setCreateOpen(true)}>
            <Plus className="size-3.5" />
          </button>
        </div>
      </div>
      <CreateProjectDialog open={createOpen} onOpenChange={setCreateOpen} />
    </>
  )
}
