import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { usePlanStore, type SessionStats } from '@/stores/planStore'
import { domainLabels } from '@/constants/routingLabels'
import { formatTokenCount } from '@/lib/formatters'
import { Separator } from '@/components/ui/separator'
import { Badge } from '@/components/ui/badge'
import { IndexingStatus } from './IndexingStatus'
import { Loader2 } from 'lucide-react'

function Sep() {
  return <Separator orientation="vertical" className="mx-1 h-4" />
}

export function StatusBar() {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const sessions = useSessionStore((s) => s.sessions)
  const activityStatus = useChatStore((s) => s.activityStatus)
  const sessionTokens = useChatStore((s) => s.sessionTokens)
  const sessionStats = usePlanStore((s) => s.sessionStats)

  const session = sessions?.find((s) => s.id === activeSessionId)
  const tokens = activeSessionId ? sessionTokens[activeSessionId] : undefined
  const stats: SessionStats | undefined = activeSessionId ? sessionStats[activeSessionId] : undefined

  return (
    <div className="flex h-8 shrink-0 items-center gap-0.5 border-t border-border bg-background px-3 text-xs text-muted-foreground">
      {/* Thinking indicator */}
      {activityStatus && (
        <>
          <Loader2 className="size-3 animate-spin text-info" />
          <span className="truncate">{activityStatus}</span>
          <Sep />
        </>
      )}

      {/* Session name */}
      {session && (
        <>
          <span className="max-w-[140px] truncate">{session.name}</span>
          <Sep />
        </>
      )}

      {/* Routing domain badge */}
      {stats?.routingDomain && (
        <>
          <Badge variant="secondary" className="h-5 px-1.5 text-[10px]">
            {domainLabels[stats.routingDomain] ?? stats.routingDomain}
          </Badge>
          <Sep />
        </>
      )}

      {/* Attempt counter */}
      {stats?.attemptCount != null && stats.attemptCount > 0 && (
        <>
          <span>
            Attempt {stats.attemptCount}
            {stats.maxAttempts != null && stats.maxAttempts > 0 ? `/${stats.maxAttempts}` : ''}
          </span>
          <Sep />
        </>
      )}

      {/* Context badge: model, family, input/output token counts */}
      {tokens && (
        <>
          <span className="flex items-center gap-1">
            {tokens.model && (
              <Badge variant="outline" className="h-5 px-1.5 text-[10px]">
                {tokens.model}
              </Badge>
            )}
            {tokens.family && (
              <span className="text-[10px] text-muted-foreground/70">{tokens.family}</span>
            )}
            <span className="tabular-nums">
              {formatTokenCount(tokens.total_input_tokens)}↑ {formatTokenCount(tokens.total_output_tokens)}↓
            </span>
          </span>
          <Sep />
        </>
      )}

      {/* Vector index status */}
      <IndexingStatus />

      {/* Spacer */}
      <div className="flex-1" />

      {/* Version */}
      <span className="text-muted-foreground/60">c0wrk v{__APP_VERSION__}</span>
    </div>
  )
}
