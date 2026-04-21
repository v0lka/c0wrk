import { useState } from 'react'
import { FolderKanban } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { CreateProjectDialog } from './CreateProjectDialog'

export function NoProjectEmptyState() {
  const [createOpen, setCreateOpen] = useState(false)

  return (
    <div className="flex-1 flex items-center justify-center bg-background">
      <div className="flex flex-col items-center gap-4 text-center max-w-sm px-6">
        <FolderKanban className="h-16 w-16 text-muted-foreground/30" />
        <div className="space-y-1">
          <h2 className="text-lg font-semibold">Create your first project</h2>
          <p className="text-sm text-muted-foreground">
            Projects organize your sessions and workspace
          </p>
        </div>
        <Button onClick={() => setCreateOpen(true)}>
          New Project
        </Button>
      </div>
      <CreateProjectDialog open={createOpen} onOpenChange={setCreateOpen} />
    </div>
  )
}
