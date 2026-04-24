import { FolderPlus } from 'lucide-react'
import { Button } from '@/components/ui/button'

interface NoProjectEmptyStateProps {
  onCreateProject: () => void
}

export function NoProjectEmptyState({ onCreateProject }: NoProjectEmptyStateProps) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 p-8 text-center">
      <div className="flex size-16 items-center justify-center rounded-full bg-muted">
        <FolderPlus className="size-8 text-muted-foreground" />
      </div>
      <div className="space-y-1">
        <h3 className="text-lg font-medium text-foreground">No project selected</h3>
        <p className="text-sm text-muted-foreground">
          Create a project to get started with your coding tasks.
        </p>
      </div>
      <Button onClick={onCreateProject} size="sm">
        <FolderPlus className="size-4" />
        Create Project
      </Button>
    </div>
  )
}
