import { cn } from '@/lib/utils'
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
  // A RUNNING task owns this slot even between two tracked events: the ReAct
  // loop briefly has no label (a strict-judge verdict just cleared it, an
  // ask_user prompt was just answered, ...). Rendering nothing there collapses
  // the trailing block, so the next label makes the whole chat jump. Hold the
  // slot with a stable "Idle…" placeholder while the task runs. When NO task is
  // running (idle session) the slot still collapses, exactly as before.
  const taskActive = useChatStore(s => activeSessionId ? s.taskActive[activeSessionId] ?? false : false)

  const isIdle = !pausing && !activityStatus && taskActive
  const label = pausing ? 'Pausing' : activityStatus || (isIdle ? 'Idle…' : undefined)
  if (!label) return null

  return (
    <div className="flex items-center gap-2 px-4 py-2 text-xs text-muted-foreground">
      <span className="relative flex h-2 w-2">
        {!isIdle && (
          <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-info opacity-75" />
        )}
        <span className={cn(
          'relative inline-flex rounded-full h-2 w-2',
          isIdle ? 'bg-muted-foreground/40' : 'bg-primary',
        )} />
      </span>
      <span className={cn(!isIdle && 'animate-pulse')}>{label}</span>
    </div>
  )
}
