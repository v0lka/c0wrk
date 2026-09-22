import { useCallback, useState } from 'react'
import { FlaskConical } from 'lucide-react'
import { updateExperimentalFeatures } from '@/api/config'
import { useExperimentalFeatures } from '@/hooks/useExperimentalFeatures'
import { useExperimentalStore } from '@/stores/experimentalStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { logger } from '@/lib/logger'
import { Toggle } from './ModelProfilesControls'

/**
 * General-tab control for the experimental-features switch. The switch gates
 * the E2S explicit-state execution mode: while disabled the per-message E2S
 * control is hidden and disarmed. Model Profiles is a first-class settings tab
 * and is not affected by this switch. RESEARCH mode is always available and is
 * unaffected by this toggle.
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
      // Disabling the master switch also disarms the persisted per-message E2S
      // toggle: while the gate is off the E2S control is hidden, leaving no way
      // to disarm it — so re-enabling must not silently re-arm E2S.
      if (!next) {
        useInputModeStore.getState().setE2sEnabled(false)
      }
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
        description="Enable experimental features (the E2S execution mode). When disabled, the E2S control is hidden and treated as off."
      />
    </div>
  )
}
