import { AlertTriangle } from 'lucide-react'
import { useConfigData } from '@/hooks/useConfigData'
import { resolveModelContext } from '@/lib/vision'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useModelProfilesGateStore } from '@/stores/modelProfilesGateStore'
import { useSettingsStore } from '@/stores/settingsStore'

/**
 * The predefined embedded preset's id. The '.' keeps it outside the custom
 * profile slug namespace (see specs/domains/model-profiles.md).
 */
const BONSAI_PROFILE_ID = 'bonsai.2-27b'

/**
 * Advisory only: never switches the profile or its master toggle.
 *
 * The line names the dedicated preset instead of claiming the model "works
 * best" with it: the Bonsai preset currently shares every value with the
 * model-agnostic `generic` preset, so a superiority claim would recommend a
 * no-op switch. The existence wording stays truthful even if the two catalogs
 * later diverge.
 */
export function BonsaiProfileBanner() {
  const enabled = useModelProfilesGateStore((s) => s.enabled)
  // The RESOLVED id, not the verbatim stored one: a legacy dashed id stored
  // by the intermediate dev builds already resolves to the preset, and the
  // banner must not advise a value-wise no-op.
  const resolvedProfileId = useModelProfilesGateStore((s) => s.resolvedProfileId)
  const selectedModel = useInputModeStore((s) => s.selectedModel)
  const openSettings = useSettingsStore((s) => s.openSettings)
  const { allModels, defaultModel } = useConfigData()
  const { modelInfo } = resolveModelContext(allModels, selectedModel, defaultModel)

  // Resolve through the model catalog, not the default-model suggestion:
  // a per-message override wins, and another provider may expose the same name.
  if (!enabled || resolvedProfileId === null || resolvedProfileId === BONSAI_PROFILE_ID
    || modelInfo?.provider !== 'embedded' || modelInfo.name !== 'Bonsai 2 27B') return null

  return (
    <button
      type="button"
      className="flex w-full min-w-0 shrink-0 items-center gap-2 border-b border-warning/30 bg-warning/10 px-3 py-1 text-left text-xs text-warning hover:bg-warning/15"
      onClick={() => openSettings('model-profiles')}
      title="Open Settings → Model Profiles"
    >
      <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span className="truncate">A dedicated Bonsai 2 27B profile preset is available. Open Model Profiles settings.</span>
    </button>
  )
}
