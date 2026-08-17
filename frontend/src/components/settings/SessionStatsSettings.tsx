import { BarChart3 } from 'lucide-react'
import { useUIStore } from '@/stores/uiStore'
import { Toggle } from './SmallLLMControls'

/**
 * General-tab control for the per-run session statistics row under the chat
 * (finish state, steps, output tokens, loop-detector counters). Display-only:
 * the counters are always collected and persisted; the toggle only shows or
 * hides the summary row. Off by default.
 */
export function SessionStatsSettings() {
  const enabled = useUIStore((s) => s.showSessionStats)
  const setEnabled = useUIStore((s) => s.setShowSessionStats)

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <BarChart3 className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium">Session Statistics</span>
      </div>
      <Toggle
        checked={enabled}
        onChange={setEnabled}
        label={enabled ? 'Visible' : 'Hidden'}
        description="Show the per-run summary row under the chat (finish state, steps, output tokens, loop-detector counters). The numbers are always collected and stored; this only controls their display."
      />
    </div>
  )
}
