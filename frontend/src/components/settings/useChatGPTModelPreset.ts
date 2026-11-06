import { fetchChatGPTModels, getChatGPTAuthStatus, getChatGPTModelPreset, onChatGPTAuthState } from '@/api/auth'
import {
  useSubscriptionModelPreset,
  type SubscriptionModelListSource,
  type SubscriptionModelListState,
  type SubscriptionModelPresetApi,
} from './useSubscriptionModelPreset'

export type { SubscriptionModelListSource, SubscriptionModelListState }
export type ChatGPTModelListSource = SubscriptionModelListSource
export type ChatGPTModelListState = SubscriptionModelListState

const chatGPTModelPresetApi: SubscriptionModelPresetApi = {
  displayName: 'ChatGPT',
  getPreset: getChatGPTModelPreset,
  fetchModels: fetchChatGPTModels,
  getStatus: getChatGPTAuthStatus,
  onState: onChatGPTAuthState,
}

export function useChatGPTModelPreset(enabled: boolean): ChatGPTModelListState {
  return useSubscriptionModelPreset(enabled, chatGPTModelPresetApi)
}
