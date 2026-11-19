import { useCallback } from 'react'
import { FolderClock, FolderOpen } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { revealInFileManager } from '@/api/workspace'
import { emit } from '@/api/runtime'
import { useSessionStore } from '@/stores/sessionStore'
import { logger } from '@/lib/logger'
import { generateUUID } from '@/lib/ids'

interface FileTreeToolbarProps {
  /** False while the workspace root is pending (or nothing resolves) —
   *  mirrors the tree's pending semantics: no reveal until the root settled. */
  workspaceReady: boolean
}

/**
 * The two reveal buttons left of the Files panel's filter input (issue #60):
 * open the active session's workspace root and its temp directory in the OS
 * file manager. The workspace root resolves through the SAME session-first
 * rule the tree uses (specs/domains/session-worktrees.md), so the revealed
 * directory always matches the tree root; the temp dir is resolved
 * server-side — raw app-internal paths never cross the wire.
 */
export function FileTreeToolbar({ workspaceReady }: FileTreeToolbarProps) {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)

  const reveal = useCallback((target: 'workspace' | 'temp') => {
    const sessionId = useSessionStore.getState().activeSessionId
    revealInFileManager(sessionId ?? '', target, '').catch((err) => {
      logger.error('Failed to open directory in file manager:', err)
      emit('runtime_error', {
        id: generateUUID(),
        message: 'Failed to open the directory in the file manager',
      })
    })
  }, [])

  return (
    <div className="flex shrink-0 items-center gap-0.5">
      <Button
        variant="ghost"
        size="sm"
        className="h-7 px-1.5"
        title="Open workspace in file manager"
        aria-label="Open workspace in file manager"
        disabled={!workspaceReady}
        onClick={() => reveal('workspace')}
      >
        <FolderOpen className="size-3.5" />
      </Button>
      <Button
        variant="ghost"
        size="sm"
        className="h-7 px-1.5"
        title="Open session temp directory in file manager"
        aria-label="Open session temp directory in file manager"
        disabled={!activeSessionId}
        onClick={() => reveal('temp')}
      >
        <FolderClock className="size-3.5" />
      </Button>
    </div>
  )
}
