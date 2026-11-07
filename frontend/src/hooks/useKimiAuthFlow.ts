import { useMemo } from 'react'
import {
  cancelKimiSignIn,
  getKimiAuthStatus,
  onKimiAuthState,
  signOutKimi,
  startKimiSignIn,
} from '@/api/auth'
import { useProviderAuthFlow, type ProviderAuthFlowApi } from './useProviderAuthFlow'

const kimiAuthFlowApi: ProviderAuthFlowApi = {
  displayName: 'Kimi',
  getStatus: getKimiAuthStatus,
  startSignIn: startKimiSignIn,
  cancelSignIn: cancelKimiSignIn,
  signOut: signOutKimi,
  onState: onKimiAuthState,
}

export function useKimiAuthFlow(beforeSignIn?: () => Promise<void>) {
  return useProviderAuthFlow(useMemo(() => ({ ...kimiAuthFlowApi, beforeSignIn }), [beforeSignIn]))
}
