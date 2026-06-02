import { useState } from 'react'
import { createProject, pickDirectory } from '@/api/projects'
import { useProjectSwitchState } from '@/hooks/useProjectSwitchState'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { FolderOpen } from 'lucide-react'

interface CreateProjectDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function CreateProjectDialog({ open, onOpenChange }: CreateProjectDialogProps) {
  const [name, setName] = useState('')
  const [isExternal, setIsExternal] = useState(false)
  const [externalPath, setExternalPath] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const switchProjectWithState = useProjectSwitchState()

  const reset = () => {
    setName('')
    setIsExternal(false)
    setExternalPath('')
  }

  const handlePickDir = async () => {
    try {
      const path = await pickDirectory()
      if (path) {
        setExternalPath(path)
        if (!name) setName(path.split('/').pop() ?? '')
      }
    } catch {
      // user cancelled
    }
  }

  const handleSubmit = async () => {
    if (!name.trim()) return
    setSubmitting(true)
    try {
      const project = await createProject(name.trim(), isExternal ? externalPath : undefined)
      try {
        await switchProjectWithState(project.id)
      } catch {
        // best effort; errors handled by API layer/logging
      }
      onOpenChange(false)
      reset()
    } catch {
      // error handled by API layer
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { onOpenChange(o); if (!o) reset() }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Create Project</DialogTitle>
          <DialogDescription>
            Give your project a name and choose workspace type.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">Project name</label>
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="My project"
              autoFocus
              onKeyDown={(e) => e.key === 'Enter' && handleSubmit()}
            />
          </div>

          <div className="space-y-2">
            <label className="text-sm font-medium text-foreground">Workspace</label>
            <div className="flex gap-2">
              <Button
                variant={!isExternal ? 'default' : 'outline'}
                size="sm"
                onClick={() => { setIsExternal(false); setExternalPath('') }}
              >
                Internal
              </Button>
              <Button
                variant={isExternal ? 'default' : 'outline'}
                size="sm"
                onClick={() => setIsExternal(true)}
              >
                External
              </Button>
            </div>
          </div>

          {isExternal && (
            <div className="space-y-2">
              <Button variant="outline" size="sm" className="gap-1.5" onClick={handlePickDir}>
                <FolderOpen className="size-4" />
                Choose directory
              </Button>
              {externalPath && (
                <p className="truncate text-xs text-muted-foreground" title={externalPath}>
                  {externalPath}
                </p>
              )}
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            onClick={handleSubmit}
            disabled={!name.trim() || (isExternal && !externalPath) || submitting}
          >
            {submitting ? 'Creating...' : 'Create'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
