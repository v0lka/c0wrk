import { useChatStore } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'

export function ActivityIndicator() {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const activityStatus = useChatStore(s => activeSessionId ? s.activityStatus[activeSessionId] : undefined)
  // While a cooperative pause is in flight the ReAct loop keeps emitting
  // progress events (step_start sets "Thinking...", streaming sets
  // "Generating response...", ...) until the step boundary lands. The
  // render-time override pins the label to "Pausing" for the whole wait
  // instead of letting those events flip it back.
  const pausing = useChatStore(s => activeSessionId ? s.pausing[activeSessionId] ?? false : false)

  const label = pausing ? 'Pausing' : activityStatus
  if (!label) return null

  return (
    <div className="flex items-center gap-2 px-4 py-2 text-xs text-muted-foreground">
      <span className="relative flex h-2 w-2">
        <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-info opacity-75" />
        <span className="relative inline-flex rounded-full h-2 w-2 bg-primary" />
      </span>
      <span className="animate-pulse">{label}</span>
    </div>
  )
}
