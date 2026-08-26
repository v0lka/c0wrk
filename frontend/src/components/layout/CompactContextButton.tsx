import { useCallback } from 'react'
import { Shrink, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'
import { cancelSessionCompaction, compactSessionContext } from '@/api/chat'
import { COMPACTION_STRATEGIES } from './compactionStrategies'
import { logger } from '@/lib/logger'

/**
 * Status-bar control for manual context compaction, rendered immediately left
 * of the context-fill indicator. Clicking opens an upward strategy menu; the
 * chosen strategy starts CompactSessionContext (the backend pauses a running
 * task first — exactly like the Pause button — compacts, then auto-resumes).
 * While a compaction is in flight the button swaps to a cancel affordance.
 */
export function CompactContextButton() {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const compacting = useChatStore((s) =>
    activeSessionId ? s.compacting[activeSessionId] ?? false : false,
  )

  const startCompaction = useCallback(
    async (strategy: string) => {
      if (!activeSessionId) return
      try {
        await compactSessionContext(activeSessionId, strategy)
      } catch (err) {
        logger.error('Compact context failed:', err)
      }
    },
    [activeSessionId],
  )

  const handleCancel = useCallback(() => {
    if (!activeSessionId) return
    cancelSessionCompaction(activeSessionId).catch((err) => {
      logger.error('Cancel compaction failed:', err)
    })
  }, [activeSessionId])

  // Compacting: cancel affordance (the dropdown must not open — the flow is
  // already running and the backend rejects a second one).
  if (compacting) {
    return (
      <Button
        variant="ghost"
        size="icon-xs"
        onClick={handleCancel}
        title="Cancel compaction"
        aria-label="Cancel compaction"
        className="h-6 w-6 shrink-0 text-warning hover:text-warning"
      >
        <X className="size-3.5" />
      </Button>
    )
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-xs"
          title="Compact context"
          aria-label="Compact context"
          className="h-6 w-6 shrink-0 text-muted-foreground hover:text-foreground"
        >
          <Shrink className="size-3.5" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" align="end" className="w-72">
        <DropdownMenuLabel>Compact context</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {COMPACTION_STRATEGIES.map(({ id, name, hint, tooltip, icon: Icon }) => (
          <DropdownMenuItem
            key={id}
            title={tooltip}
            onClick={() => void startCompaction(id)}
            className="gap-2 cursor-pointer"
          >
            <Icon className="size-3.5 shrink-0 text-muted-foreground" />
            <span className="flex min-w-0 flex-col">
              <span className="text-xs font-medium">{name}</span>
              <span className="text-[10px] text-muted-foreground">{hint}</span>
            </span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
