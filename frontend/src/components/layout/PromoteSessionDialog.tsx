import { useState } from 'react'
import { promoteSessionToProject } from '@/api/sessions'
import { useProjectSwitchState } from '@/hooks/useProjectSwitchState'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

interface PromoteSessionDialogProps {
  /** Promoted session id; null closes/keeps the dialog closed. */
  sessionId: string | null
  /** Pre-filled project name (the session's name). */
  sessionName: string
  onOpenChange: (open: boolean) => void
}

/**
 * Confirm dialog for "Promote to project": turns a CHAT (No Project) session
 * into a CODE project with that single session in it. The name is pre-filled
 * with the session's name and editable; on confirm the backend performs the
 * transform (files move into the project's internal Workspace, the chat
 * history and all artifacts stay with the session) and the frontend switches
 * to the new project. A failed promotion keeps the dialog open with the
 * backend's error message.
 */
export function PromoteSessionDialog({ sessionId, sessionName, onOpenChange }: PromoteSessionDialogProps) {
  const [name, setName] = useState(sessionName)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const switchProjectWithState = useProjectSwitchState()

  const open = sessionId !== null

  const handleClose = (o: boolean) => {
    if (submitting) return
    onOpenChange(o)
    if (!o) {
      setName(sessionName)
      setError(null)
    }
  }

  const handleSubmit = async () => {
    if (!sessionId || !name.trim() || submitting) return
    setSubmitting(true)
    setError(null)
    try {
      const project = await promoteSessionToProject(sessionId, name.trim())
      try {
        await switchProjectWithState(project.id)
      } catch {
        // best effort; errors handled by API layer/logging
      }
      onOpenChange(false)
      setName(sessionName)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Promote to project</DialogTitle>
          <DialogDescription>
            Turns this chat session into a CODE project with this single
            session in it. The session keeps its full history and artifacts;
            the workspace files move into the new project's workspace and the
            mode switches to CODE.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          <label className="text-sm font-medium text-foreground">Project name</label>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="My project"
            autoFocus
            onKeyDown={(e) => e.key === 'Enter' && handleSubmit()}
          />
          {error && (
            <p className="text-xs text-destructive" role="alert">
              {error}
            </p>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => handleClose(false)} disabled={submitting}>
            Cancel
          </Button>
          <Button onClick={handleSubmit} disabled={!name.trim() || submitting}>
            {submitting ? 'Promoting...' : 'Promote'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
