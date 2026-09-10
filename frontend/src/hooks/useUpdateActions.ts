// Shared self-update action handlers.
//
// Extracts the side-effecting update actions from the toast so they can be
// reused verbatim by the inline About-tab flow without duplicating the
// store/API choreography. Behaviour matches the previous toast handlers:
//   • handleUpdate   — initiate the download (busy-guarded)
//   • handleSkip     — persist a skip for the reported version, then dismiss
//   • handleRestart  — apply the staged update (fire-and-forget)
//   • handleDismiss  — hide the surface and return to idle
//   • isDownloading  — busy flag while a download is being initiated

import { useCallback } from 'react'
import {
  useUpdateInfo,
  useUpdateDownloading,
  useUpdateStore,
} from '@/stores/updateStore'
import { downloadUpdate, applyUpdate, skipVersion } from '@/api/updater'

export interface UpdateActions {
  handleUpdate: () => Promise<void>
  handleSkip: () => Promise<void>
  handleRestart: () => void
  handleDismiss: () => void
  isDownloading: boolean
}

export function useUpdateActions(): UpdateActions {
  const info = useUpdateInfo()
  const isDownloading = useUpdateDownloading()
  const dismiss = useUpdateStore((s) => s.dismiss)
  const setDownloading = useUpdateStore((s) => s.setDownloading)

  const handleUpdate = useCallback(async () => {
    if (isDownloading) return
    setDownloading(true)
    try {
      await downloadUpdate()
    } catch {
      // update:error event drives the error surface; just clear the busy flag.
      setDownloading(false)
    }
  }, [isDownloading, setDownloading])

  const handleSkip = useCallback(async () => {
    if (!info) return
    const latest = info.latest_version
    try {
      await skipVersion(latest)
    } catch {
      // skipVersion already logs the failure; still dismiss below so the
      // surface doesn't reappear for this version within the session.
    } finally {
      // Whether or not the persist succeeded, hide the surface for this version
      // this session. SkipVersion invalidates the cached check on the backend.
      dismiss()
    }
  }, [info, dismiss])

  const handleRestart = useCallback(() => {
    applyUpdate().catch(() => {
      // update:error event surfaces the failure; keep the surface so the user
      // can retry or dismiss.
    })
  }, [])

  const handleDismiss = useCallback(() => {
    dismiss()
  }, [dismiss])

  return { handleUpdate, handleSkip, handleRestart, handleDismiss, isDownloading }
}
