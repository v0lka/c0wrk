import { fetchKimiModels, getKimiAuthStatus, getKimiModelPreset, onKimiAuthState } from '@/api/auth'
import type { KimiAuthRegion } from '@/types/models'
import type { SubscriptionModelListState } from './useSubscriptionModelPreset'
import { useSubscriptionModelPreset, type SubscriptionModelPresetApi } from './useSubscriptionModelPreset'

const kimiModelPresetApi: SubscriptionModelPresetApi = {
  displayName: 'Kimi',
  getPreset: getKimiModelPreset,
  fetchModels: fetchKimiModels,
  getStatus: getKimiAuthStatus,
  onState: onKimiAuthState,
}

export function useKimiModelPreset(
  enabled: boolean,
  region: KimiAuthRegion = 'global',
): SubscriptionModelListState {
  return useSubscriptionModelPreset(enabled, kimiModelPresetApi, region)
}
