import { useState, useCallback, useRef } from 'react'
import { useProjectStore } from '@/stores/projectStore'
import { switchProject, renameProject, deleteProject } from '@/api/projects'
import { listSessions } from '@/api/sessions'
import { useSessionStore } from '@/stores/sessionStore'
import { CreateProjectDialog } from '@/components/project/CreateProjectDialog'
import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent,
  DropdownMenuItem, DropdownMenuSeparator, DropdownMenuSub,
  DropdownMenuSubTrigger, DropdownMenuSubContent,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { ChevronDown, Check, FolderPlus, Pencil, Trash2 } from 'lucide-react'

export function ProjectSelector() {
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const setActiveProjectId = useProjectStore((s) => s.setActiveProjectId)
  const removeProject = useProjectStore((s) => s.removeProject)
  const updateProject = useProjectStore((s) => s.updateProject)

  const [createOpen, setCreateOpen] = useState(false)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const renameRef = useRef<HTMLInputElement>(null)

  const activeProject = projects?.find((p) => p.id === activeProjectId)

  const handleSwitch = useCallback(async (id: string) => {
    if (id === activeProjectId) return
    try {
      await switchProject(id)
      setActiveProjectId(id)
      const sessions = await listSessions()
      useSessionStore.getState().setSessions(sessions)
      if (sessions.length > 0) {
        useSessionStore.getState().setActiveSessionId(sessions[0]!.id)
      } else {
        useSessionStore.getState().setActiveSessionId(null)
      }
    } catch { /* ignore */ }
  }, [activeProjectId, setActiveProjectId])

  const handleDelete = useCallback(async (id: string) => {
    try {
      await deleteProject(id)
      removeProject(id)
      if (id === activeProjectId) {
        const remaining = useProjectStore.getState().projects
        if (remaining && remaining.length > 0) {
          await switchProject(remaining[0]!.id)
          setActiveProjectId(remaining[0]!.id)
        }
      }
    } catch { /* ignore */ }
  }, [activeProjectId, removeProject, setActiveProjectId])

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
    } catch { /* ignore */ }
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
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" className="h-7 w-full justify-between gap-1 px-2 text-sm font-medium">
              <span className="truncate">{activeProject?.name ?? 'Select project'}</span>
              <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-56">
            {projects?.map((project) => (
              <DropdownMenuSub key={project.id}>
                <DropdownMenuSubTrigger
                  className="gap-2"
                  onClick={(e) => { e.preventDefault(); handleSwitch(project.id) }}
                >
                  {project.id === activeProjectId && <Check className="size-3.5" />}
                  <span className="flex-1 truncate">{project.name}</span>
                </DropdownMenuSubTrigger>
                <DropdownMenuSubContent>
                  <DropdownMenuItem onClick={() => startRename(project.id, project.name)}>
                    <Pencil className="size-3.5" />
                    Rename
                  </DropdownMenuItem>
                  <DropdownMenuItem variant="destructive" onClick={() => handleDelete(project.id)}>
                    <Trash2 className="size-3.5" />
                    Delete
                  </DropdownMenuItem>
                </DropdownMenuSubContent>
              </DropdownMenuSub>
            ))}
            {projects && projects.length > 0 && <DropdownMenuSeparator />}
            <DropdownMenuItem onClick={() => setCreateOpen(true)}>
              <FolderPlus className="size-3.5" />
              New Project...
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <CreateProjectDialog open={createOpen} onOpenChange={setCreateOpen} />
    </>
  )
}
