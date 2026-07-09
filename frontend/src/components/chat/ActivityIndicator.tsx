import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'

export function ActivityIndicator() {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const activityStatus = useChatStore(s => activeSessionId ? s.activityStatus[activeSessionId] : undefined)

  if (!activityStatus) return null

  return (
    <div className="flex items-center gap-2 px-4 py-2 text-xs text-muted-foreground">
      <span className="relative flex h-2 w-2">
        <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-info opacity-75" />
        <span className="relative inline-flex rounded-full h-2 w-2 bg-primary" />
      </span>
      <span className="animate-pulse">{activityStatus}</span>
    </div>
  )
}
