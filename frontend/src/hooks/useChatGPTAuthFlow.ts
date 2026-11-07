import {
  cancelChatGPTSignIn,
  getChatGPTAuthStatus,
  onChatGPTAuthState,
  signOutChatGPT,
  startChatGPTSignIn,
} from '@/api/auth'
import { useProviderAuthFlow, type ProviderAuthFlowApi } from './useProviderAuthFlow'

const chatGPTAuthFlowApi: ProviderAuthFlowApi = {
  displayName: 'ChatGPT',
  getStatus: getChatGPTAuthStatus,
  startSignIn: startChatGPTSignIn,
  cancelSignIn: cancelChatGPTSignIn,
  signOut: signOutChatGPT,
  onState: onChatGPTAuthState,
}

export function useChatGPTAuthFlow() {
  return useProviderAuthFlow(chatGPTAuthFlowApi)
}
