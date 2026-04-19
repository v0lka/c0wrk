import { useSessionStore } from '@/stores/sessionStore'
import { usePanelStore } from '@/stores/panelStore'
import { useChatStore } from '@/stores/chatStore'
import { Badge } from '@/components/ui/badge'
import { Separator } from '@/components/ui/separator'
import { ContextBadge } from '@/components/chat/ContextBadge'
import { Cpu, Activity, Loader2 } from 'lucide-react'
import { IndexingStatus } from './IndexingStatus'

export function StatusBar() {
  const sessions = useSessionStore(s => s.sessions)
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const isThinking = useChatStore(s => s.isThinking)
  const stats = usePanelStore(s => s.sessionStats)

  const activeSession = sessions?.find(s => s.id === activeSessionId)
  const domainLabel = stats.routingDomain || 'idle'

  return (
    <>
    <div className="h-8 border-t border-border bg-muted/50 flex items-center px-3 gap-4 text-xs">
      {/* Session name */}
      <div className="flex items-center gap-2 min-w-0">
        {isThinking && <Loader2 className="h-3 w-3 animate-spin text-blue-500" />}
        <span className="text-muted-foreground truncate">
          {activeSession ? activeSession.name : 'No session selected'}
        </span>
      </div>

      <Separator orientation="vertical" className="h-4" />

      {/* Routing domain badge */}
      <div className="flex items-center gap-2">
        <Cpu className="h-3 w-3 text-muted-foreground" />
        <Badge variant="secondary" className="text-[10px] h-5 px-1.5">
          {domainLabel}
        </Badge>
      </div>

      <Separator orientation="vertical" className="h-4" />

      {/* Attempt count */}
      <div className="flex items-center gap-2">
        <Activity className="h-3 w-3 text-muted-foreground" />
        <span className="text-muted-foreground">
          Attempt: {stats.attempt}/{stats.maxAttempts}
        </span>
      </div>

      <Separator orientation="vertical" className="h-4" />

      {/* Context fill percentage */}
      <div className="flex items-center gap-2">
        <ContextBadge />
      </div>

      {/* Vector index status */}
      <IndexingStatus />

      {/* Spacer */}
      <div className="flex-1" />

      {/* Version */}
      <span className="text-muted-foreground">c0wrk v0.1.0</span>
    </div>
    </>
  )
}
