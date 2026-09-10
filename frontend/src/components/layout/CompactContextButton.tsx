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
import { emit } from '@/api/runtime'
import { COMPACTION_STRATEGIES } from './compactionStrategies'
import { logger } from '@/lib/logger'

/**
 * Status-bar control for manual context compaction, rendered immediately left
 * of the context-fill indicator. Clicking opens an upward strategy menu; the
 * chosen strategy starts CompactSessionContext (the backend pauses a running
 * task first — exactly like the Pause button — compacts, then auto-resumes).
 * While a compaction is in flight the button swaps to a cancel affordance.
 *
 * Availability is per strategy: the backend predicts whether each strategy
 * would actually shrink the conversation history right now (an exact
 * structural verdict). The button is enabled when at least one strategy is
 * available; unavailable strategies are shown disabled with their reason, and
 * available ones carry a predicted "reclaims ~N tokens" hint. Absent/unknown
 * availability (older backend) fails OPEN — every strategy is clickable.
 */
export function CompactContextButton() {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const compacting = useChatStore((s) =>
    activeSessionId ? s.compacting[activeSessionId] ?? false : false,
  )
  // Backend-predicted per-strategy availability (absent key/undefined → unknown,
  // fail-open). Direct store reference from a selector — stable identity.
  const availability = useChatStore((s) =>
    activeSessionId ? s.compactionAvailability[activeSessionId] : undefined,
  )

  const startCompaction = useCallback(
    async (strategy: string) => {
      if (!activeSessionId) return
      try {
        await compactSessionContext(activeSessionId, strategy)
      } catch (err) {
        logger.error('Compact context failed:', err)
        // Surface the dead-end: the dropdown closed and nothing else reacts
        // to the rejection — without a toast the failure is invisible.
        emit('runtime_error', {
          id: crypto.randomUUID(),
          message: 'Failed to start context compaction',
        })
      }
    },
    [activeSessionId],
  )

  const handleCancel = useCallback(() => {
    if (!activeSessionId) return
    cancelSessionCompaction(activeSessionId).catch((err) => {
      logger.error('Cancel compaction failed:', err)
      emit('runtime_error', {
        id: crypto.randomUUID(),
        message: 'Failed to cancel compaction',
      })
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

  // Availability is only known after the first runtime-status reconcile.
  // Fail-open: an unknown verdict (undefined) never disables anything.
  const known = availability ?? []
  const anyAvailable = known.length === 0 || known.some((a) => a.available)
  const availabilityByStrategy = new Map(known.map((a) => [a.strategy, a]))

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-xs"
          disabled={!anyAvailable}
          title={
            anyAvailable
              ? 'Compact context'
              : 'Nothing to compact — every strategy would leave the context unchanged'
          }
          aria-label={anyAvailable ? 'Compact context' : 'Compact context (nothing to compact)'}
          className="h-6 w-6 shrink-0 text-muted-foreground hover:text-foreground disabled:text-muted-foreground/50"
        >
          <Shrink className="size-3.5" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent side="top" align="end" className="w-72">
        <DropdownMenuLabel>Compact context</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {COMPACTION_STRATEGIES.map(({ id, name, hint, tooltip, icon: Icon }) => {
          const avail = availabilityByStrategy.get(id)
          // Fail-open: an unknown strategy (older backend, no availability
          // snapshot) is clickable; a known-unavailable one is disabled with
          // its reason.
          const available = avail === undefined || avail.available
          const reason = !available
            ? 'Would not shrink the context right now — the dialogue already fits this strategy'
            : avail && avail.reclaim_tokens > 0
              ? `Reclaims ~${avail.reclaim_tokens.toLocaleString()} tokens${avail.exact ? '' : ' (estimated)'}`
              : tooltip
          return (
            <DropdownMenuItem
              key={id}
              title={reason}
              disabled={!available}
              onClick={() => void startCompaction(id)}
              className="gap-2 cursor-pointer data-[disabled]:cursor-not-allowed"
            >
              <Icon className="size-3.5 shrink-0 text-muted-foreground" />
              <span className="flex min-w-0 flex-col">
                <span className="text-xs font-medium">{name}</span>
                <span className="text-[10px] text-muted-foreground">{hint}</span>
              </span>
            </DropdownMenuItem>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
