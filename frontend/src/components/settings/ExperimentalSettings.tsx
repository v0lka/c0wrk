import { useCallback, useState } from 'react'
import { FlaskConical } from 'lucide-react'
import { updateExperimentalFeatures } from '@/api/config'
import { useExperimentalFeatures } from '@/hooks/useExperimentalFeatures'
import { useExperimentalStore } from '@/stores/experimentalStore'
import { logger } from '@/lib/logger'
import { Toggle } from './SmallLLMControls'

/**
 * General-tab control for the master experimental-features switch. The switch
 * gates only the Small-LLM settings tab: when disabled, that tab is hidden
 * and the backend treats the Small-LLM profile as off. RESEARCH mode is
 * always available and is unaffected by this toggle.
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
        description="Enable the Small-LLM settings tab. When disabled, that tab is hidden and the Small-LLM profile is treated as off. RESEARCH mode is always available."
      />
    </div>
  )
}
