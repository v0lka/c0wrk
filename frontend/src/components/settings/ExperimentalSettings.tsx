import { useCallback, useState } from 'react'
import { FlaskConical } from 'lucide-react'
import { updateExperimentalFeatures } from '@/api/config'
import { useExperimentalFeatures } from '@/hooks/useExperimentalFeatures'
import { useExperimentalStore } from '@/stores/experimentalStore'
import { logger } from '@/lib/logger'
import { Toggle } from './SmallLLMControls'

/**
 * General-tab control for the master experimental-features switch. A single
 * toggle governs all gated features (RESEARCH mode + Small-LLM settings);
 * when disabled their UI affordances are hidden and the backend treats each
 * feature as off.
 */
export function ExperimentalSettings() {
  const enabled = useExperimentalFeatures()
  const loaded = useExperimentalStore((s) => s.loaded)
  const [saving, setSaving] = useState(false)

  const handleChange = useCallback(async (next: boolean) => {
    setSaving(true)
    try {
      await updateExperimentalFeatures(next)
      useExperimentalStore.getState().setEnabled(next)
    } catch (err) {
      logger.error('Failed to toggle experimental features:', err)
    } finally {
      setSaving(false)
    }
  }, [])

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <FlaskConical className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium">Experimental Features</span>
      </div>
      <Toggle
        checked={enabled}
        onChange={handleChange}
        disabled={!loaded || saving}
        label={enabled ? 'Enabled' : 'Disabled'}
        description="Enable RESEARCH mode and the Small-LLM settings tab. When disabled, these features are hidden and treated as off."
      />
    </div>
  )
}
