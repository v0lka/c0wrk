import { useState, useCallback } from 'react'
import { FolderOpen } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useProjectAPI } from '@/hooks/useProject'
import { useProjectStore } from '@/stores/projectStore'
import { logger } from '@/lib/logger'
import { cn } from '@/lib/utils'

interface CreateProjectDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

type WorkspaceType = 'internal' | 'external'

export function CreateProjectDialog({ open, onOpenChange }: CreateProjectDialogProps) {
  const [name, setName] = useState('')
  const [workspaceType, setWorkspaceType] = useState<WorkspaceType>('internal')
  const [externalPath, setExternalPath] = useState('')
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const api = useProjectAPI()
  const addProject = useProjectStore(s => s.addProject)
  const setActiveProject = useProjectStore(s => s.setActiveProject)

  const canSubmit =
    name.trim().length > 0 &&
    (workspaceType === 'internal' || externalPath.length > 0) &&
    !creating

  const handleBrowse = useCallback(async () => {
    try {
      const dir = await api.pickDirectory()
      if (dir) {
        setExternalPath(dir)
      }
    } catch (err) {
      logger.error('Failed to pick directory:', err)
    }
  }, [api])

  const handleCreate = useCallback(async () => {
    if (!canSubmit) return
    setCreating(true)
    setError(null)
    try {
      const extPath = workspaceType === 'external' ? externalPath : ''
      const project = await api.createProject(name.trim(), extPath)
      if (project) {
        addProject(project)
        await api.switchProject(project.id)
        setActiveProject(project.id) // Only after switch succeeds
      }
      // Reset form and close
      setName('')
      setWorkspaceType('internal')
      setExternalPath('')
      onOpenChange(false)
    } catch (err) {
      logger.error('Failed to create project:', err)
      setError(err instanceof Error ? err.message : 'Failed to create project')
    } finally {
      setCreating(false)
    }
  }, [canSubmit, name, workspaceType, externalPath, api, addProject, setActiveProject, onOpenChange])

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && canSubmit) {
      handleCreate()
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[440px] top-[40px] translate-y-0">
        <DialogHeader>
          <DialogTitle>New Project</DialogTitle>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {/* Name */}
          <div className="space-y-2">
            <label htmlFor="project-name" className="text-sm font-medium">
              Project Name
            </label>
            <Input
              id="project-name"
              placeholder="My Project"
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={handleKeyDown}
              autoFocus
            />
          </div>

          {/* Workspace type toggle */}
          <div className="space-y-2">
            <label className="text-sm font-medium">Workspace</label>
            <div className="flex gap-2">
              <Button
                type="button"
                variant={workspaceType === 'internal' ? 'default' : 'outline'}
                size="sm"
                className={cn('flex-1', workspaceType !== 'internal' && 'text-muted-foreground')}
                onClick={() => setWorkspaceType('internal')}
              >
                Internal
              </Button>
              <Button
                type="button"
                variant={workspaceType === 'external' ? 'default' : 'outline'}
                size="sm"
                className={cn('flex-1', workspaceType !== 'external' && 'text-muted-foreground')}
                onClick={() => setWorkspaceType('external')}
              >
                External
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              {workspaceType === 'internal'
                ? 'A managed workspace directory will be created automatically.'
                : 'Point to an existing directory on your filesystem.'}
            </p>
          </div>

          {/* External path picker */}
          {workspaceType === 'external' && (
            <div className="flex items-center gap-2">
              <Input
                readOnly
                value={externalPath}
                placeholder="No directory selected"
                className="flex-1 text-sm"
              />
              <Button type="button" variant="outline" size="sm" onClick={handleBrowse}>
                <FolderOpen className="h-4 w-4 mr-1" />
                Browse
              </Button>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button onClick={handleCreate} disabled={!canSubmit}>
            {creating ? 'Creating...' : 'Create'}
          </Button>
        </DialogFooter>
        {error && <p className="text-sm text-red-500 mt-1 px-6 pb-4">{error}</p>}
      </DialogContent>
    </Dialog>
  )
}
