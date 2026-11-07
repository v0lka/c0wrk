import { useCallback, useEffect, useRef, useState } from 'react'
import { logger } from '@/lib/logger'
import type { SubscriptionAuthStatusResponse } from '@/types/models'
import type { SubscriptionAuthEventData } from '@/types/events'

export interface ProviderAuthFlowApi {
  displayName: string
  getStatus: () => Promise<SubscriptionAuthStatusResponse>
  startSignIn: () => Promise<unknown>
  beforeSignIn?: () => Promise<void>
  cancelSignIn: () => Promise<void>
  signOut: () => Promise<void>
  onState: (callback: (data: SubscriptionAuthEventData) => void) => () => void
}

/** Shared subscription sign-in state. Status reads are epoch-guarded so a
 * slow earlier snapshot cannot overwrite a newer flow transition. */
export function useProviderAuthFlow(api: ProviderAuthFlowApi) {
  const [status, setStatus] = useState<SubscriptionAuthStatusResponse | null>(null)
  const [signInBusy, setSignInBusy] = useState(false)
  const signInInitiatedLocally = useRef(false)
  const [signOutBusy, setSignOutBusy] = useState(false)
  const [authError, setAuthError] = useState<string | null>(null)
  const statusEpoch = useRef(0)

  const refreshStatus = useCallback(async () => {
    const epoch = ++statusEpoch.current
    try {
      const snapshot = await api.getStatus()
      if (epoch !== statusEpoch.current) return
      setStatus(snapshot)
    } catch (err) {
      logger.warn(`${api.displayName} auth status refresh failed:`, err)
    }
  }, [api])

  useEffect(() => {
    void refreshStatus()
  }, [refreshStatus])

  useEffect(() => {
    return api.onState((data) => {
      switch (data.state) {
        case 'pending':
          setSignInBusy(true)
          break
        case 'success':
          signInInitiatedLocally.current = false
          setSignInBusy(false)
          setAuthError(null)
          void refreshStatus()
          break
        case 'error':
          signInInitiatedLocally.current = false
          setSignInBusy(false)
          setAuthError(data.error || 'Sign-in failed')
          void refreshStatus()
          break
        case 'cancelled':
          signInInitiatedLocally.current = false
          setSignInBusy(false)
          void refreshStatus()
          break
      }
    })
  }, [api, refreshStatus])

  useEffect(() => {
    if (status === null || signInInitiatedLocally.current) return
    setSignInBusy(status.in_flight)
  }, [status])

  const handleSignIn = useCallback(async () => {
    setAuthError(null)
    signInInitiatedLocally.current = true
    setSignInBusy(true)
    try {
      if (api.beforeSignIn) await api.beforeSignIn()
      await api.startSignIn()
    } catch (err) {
      signInInitiatedLocally.current = false
      setSignInBusy(false)
      setAuthError(err instanceof Error ? err.message : String(err))
    }
  }, [api])

  const handleCancel = useCallback(async () => {
    try {
      await api.cancelSignIn()
    } catch (err) {
      signInInitiatedLocally.current = false
      setSignInBusy(false)
      setAuthError(err instanceof Error ? err.message : String(err))
      void refreshStatus()
    }
  }, [api, refreshStatus])

  const handleSignOut = useCallback(async () => {
    setAuthError(null)
    setSignOutBusy(true)
    try {
      await api.signOut()
    } catch (err) {
      setAuthError(err instanceof Error ? err.message : String(err))
    } finally {
      setSignOutBusy(false)
      void refreshStatus()
    }
  }, [api, refreshStatus])

  return { status, signInBusy, signOutBusy, authError, handleSignIn, handleCancel, handleSignOut }
}

export type ProviderAuthFlowState = ReturnType<typeof useProviderAuthFlow>
