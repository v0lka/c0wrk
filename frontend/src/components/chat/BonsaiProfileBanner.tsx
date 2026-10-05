import { AlertTriangle } from 'lucide-react'
import { useConfigData } from '@/hooks/useConfigData'
import { resolveModelContext } from '@/lib/vision'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useModelProfilesGateStore } from '@/stores/modelProfilesGateStore'
import { useSettingsStore } from '@/stores/settingsStore'

/** Advisory only: never switches the profile or its master toggle. */
export function BonsaiProfileBanner() {
  const enabled = useModelProfilesGateStore((s) => s.enabled)
  const activeProfileId = useModelProfilesGateStore((s) => s.activeProfileId)
  const selectedModel = useInputModeStore((s) => s.selectedModel)
  const openSettings = useSettingsStore((s) => s.openSettings)
  const { allModels, defaultModel } = useConfigData()
  const { modelInfo } = resolveModelContext(allModels, selectedModel, defaultModel)

  // Resolve through the model catalog, not the default-model suggestion:
  // a per-message override wins, and another provider may expose the same name.
  if (!enabled || activeProfileId === null || activeProfileId === 'bonsai-2-27b'
    || modelInfo?.provider !== 'embedded' || modelInfo.name !== 'Bonsai 2 27B') return null

  return (
    <button
      type="button"
      className="flex w-full min-w-0 shrink-0 items-center gap-2 border-b border-warning/30 bg-warning/10 px-3 py-1 text-left text-xs text-warning hover:bg-warning/15"
      onClick={() => openSettings('model-profiles')}
      title="Open Settings → Model Profiles"
    >
      <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span className="truncate">Bonsai 2 27B works best with the Bonsai profile. Open Model Profiles settings.</span>
    </button>
  )
}
