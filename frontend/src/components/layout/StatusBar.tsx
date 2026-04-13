import { useState, useEffect } from 'react'
import { useSessionStore } from '@/stores/sessionStore'
import { usePanelStore } from '@/stores/panelStore'
import { useChatStore } from '@/stores/chatStore'
import { Badge } from '@/components/ui/badge'
import { Separator } from '@/components/ui/separator'
import { ContextBadge } from '@/components/chat/ContextBadge'
import { useWails } from '@/hooks/useWails'
import { Cpu, Activity, Loader2, Database, AlertCircle } from 'lucide-react'

type IndexingStatus = 'idle' | 'indexing' | 'error'

export function StatusBar() {
  const sessions = useSessionStore(s => s.sessions)
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const isThinking = useChatStore(s => s.isThinking)
  const stats = usePanelStore(s => s.sessionStats)
  const { runtime } = useWails()

  const [indexingStatus, setIndexingStatus] = useState<IndexingStatus>('idle')
  const [indexError, setIndexError] = useState<string>('')

  // Listen for code memory indexing events
  useEffect(() => {
    if (!runtime) return

    const unsubscribe = runtime.EventsOn('codememory:indexing', (data: unknown) => {
      const eventData = data as { status: string; path?: string; error?: string }
      if (eventData.status === 'start') {
        setIndexingStatus('indexing')
        setIndexError('')
      } else if (eventData.status === 'done') {
        setIndexingStatus('idle')
        setIndexError('')
      } else if (eventData.status === 'error') {
        setIndexingStatus('error')
        setIndexError(eventData.error || 'Unknown error')
        // Auto-dismiss error after 5 seconds
        setTimeout(() => {
          setIndexingStatus('idle')
          setIndexError('')
        }, 5000)
      }
    })

    return unsubscribe
  }, [runtime])

  const activeSession = sessions.find(s => s.id === activeSessionId)
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

      {/* Spacer */}
      <div className="flex-1" />

      {/* Version */}
      <span className="text-muted-foreground">c0wrk v0.1.0</span>
    </div>

    {/* Indexing indicator row - appears below the main status bar */}
    {indexingStatus === 'indexing' && (
      <div className="h-6 border-t border-border/50 bg-muted/30 flex items-center px-3 gap-2 text-xs text-muted-foreground">
        <Database className="h-3 w-3" />
        <Loader2 className="h-3 w-3 animate-spin" />
        <span>Indexing codebase...</span>
      </div>
    )}

    {indexingStatus === 'error' && (
      <div className="h-6 border-t border-destructive/30 bg-destructive/10 flex items-center px-3 gap-2 text-xs text-destructive">
        <AlertCircle className="h-3 w-3" />
        <span>Indexing failed: {indexError}</span>
      </div>
    )}
    </>
  )
}
