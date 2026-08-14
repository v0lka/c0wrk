// Self-update RPC wrappers.
//
// Thin wrappers over the desktop App bindings for the self-update flow:
// CheckForUpdates / DownloadUpdate / ApplyUpdate / SkipVersion /
// GetUpdateSettings. Mirror the backend signatures in
// backend/frontend_api_updater.go. All frontend update code routes through
// this module — it never imports wailsjs directly (single RPC path via
// @/api/*).
//
// Global update events (update:available / update:progress / update:downloaded
// / update:error / update:none) are typed via GlobalEventMap in
// @/types/events and consumed through onGlobalEvent / the typed helpers below.

import { getApp, onGlobalEvent } from './runtime'
import { logger } from '@/lib/logger'
import type {
  UpdateInfoData,
  UpdateProgressData,
  UpdateErrorData,
} from '@/types/events'

/** Outcome of an update check. Mirrors backend UpdateInfo (snake_case JSON). */
export interface UpdateInfo {
  available: boolean
  current_version: string
  latest_version: string
  release_notes: string
  published_at: string
  html_url: string
  asset_name: string
}

/** User self-update preferences. Mirrors backend UpdateSettings. */
export interface UpdateSettings {
  /** Mirrors config.yaml updates.auto_check. */
  auto_check: boolean
  /** The release tag the user dismissed (persisted in update_state.json). */
  skipped_version: string
  current_version: string
  /** Operator-level master gate (config.yaml updates.enabled). When false, the
   *  entire update subsystem is disabled by an administrator: CheckForUpdates
   *  reports no update and the background auto-check never runs. */
  operator_enabled: boolean
}

/** Query GitHub for the latest release and report whether an update is
 *  available for the current platform. Also emits the update:available /
 *  update:none / update:error global events. */
export async function checkForUpdates(): Promise<UpdateInfo> {
  try {
    const app = getApp()
    const result = await app.CheckForUpdates()
    return result as UpdateInfo
  } catch (err) {
    logger.error('Failed to check for updates:', err)
    throw err
  }
}

/** Download and integrity-verify the release archive into the update-staging
 *  directory. Streams update:progress events; emits update:downloaded on
 *  success or update:error on failure. Requires a prior successful
 *  CheckForUpdates. */
export async function downloadUpdate(): Promise<void> {
  try {
    const app = getApp()
    await app.DownloadUpdate()
  } catch (err) {
    logger.error('Failed to download update:', err)
    throw err
  }
}

/** Prepare and launch the self-update re-exec, then trigger a coordinated
 *  graceful quit. The staged updater waits for this process to exit, then
 *  atomically swaps the install tree and relaunches the app. Returns before
 *  the swap completes (the process is about to quit). */
export async function applyUpdate(): Promise<void> {
  try {
    const app = getApp()
    await app.ApplyUpdate()
  } catch (err) {
    logger.error('Failed to apply update:', err)
    throw err
  }
}

/** Record (or clear, when version is empty) that the user dismissed a release
 *  tag so the checker suppresses it until a newer release is published. */
export async function skipVersion(version: string): Promise<void> {
  try {
    const app = getApp()
    await app.SkipVersion(version)
  } catch (err) {
    logger.error('Failed to skip version:', err)
    throw err
  }
}

/** Return the current self-update preferences (auto-check, skipped version,
 *  operator gate) plus the running build version. */
export async function getUpdateSettings(): Promise<UpdateSettings> {
  try {
    const app = getApp()
    const result = await app.GetUpdateSettings()
    return result as UpdateSettings
  } catch (err) {
    logger.error('Failed to get update settings:', err)
    throw err
  }
}

/** Persist the auto-check preference to config.yaml (updates.auto_check) and
 *  return the resolved settings. An explicit false is honoured by the backend
 *  (never reset to the default). The master enable/disable switch
 *  (updates.enabled) is not controlled by this call. */
export async function setUpdateSettings(autoCheck: boolean): Promise<UpdateSettings> {
  try {
    const app = getApp()
    const result = await app.SetUpdateSettings(autoCheck)
    return result as UpdateSettings
  } catch (err) {
    logger.error('Failed to set update settings:', err)
    throw err
  }
}

// --- Typed global event subscription helpers ---
//
// Each helper subscribes to its global update event and validates the payload
// with the matching type guard before invoking the callback, so malformed
// emissions are dropped rather than crashing the handler.

/** Subscribe to update:available (a newer release was found). */
export function onUpdateAvailable(cb: (data: UpdateInfoData) => void): () => void {
  return onGlobalEvent('update:available', (data) => {
    if (data && isUpdateInfoData(data)) cb(data)
  })
}

/** Subscribe to update:none (no newer release found). */
export function onUpdateNone(cb: (data: UpdateInfoData) => void): () => void {
  return onGlobalEvent('update:none', (data) => {
    if (data && isUpdateInfoData(data)) cb(data)
  })
}

/** Subscribe to update:progress (download bytes done / total). */
export function onUpdateProgress(cb: (data: UpdateProgressData) => void): () => void {
  return onGlobalEvent('update:progress', (data) => {
    if (data && isUpdateProgressData(data)) cb(data)
  })
}

/** Subscribe to update:downloaded (archive verified, ready to apply). */
export function onUpdateDownloaded(cb: (archive: string) => void): () => void {
  return onGlobalEvent('update:downloaded', (data) => {
    if (data && typeof (data as { archive?: unknown }).archive === 'string') {
      cb((data as { archive: string }).archive)
    }
  })
}

/** Subscribe to update:error (an update step failed). */
export function onUpdateError(cb: (data: UpdateErrorData) => void): () => void {
  return onGlobalEvent('update:error', (data) => {
    if (data && isUpdateErrorData(data)) cb(data)
  })
}

// --- Type guards for update event payloads ---

export function isUpdateInfoData(d: unknown): d is UpdateInfoData {
  return (
    typeof d === 'object' &&
    d !== null &&
    typeof (d as UpdateInfoData).available === 'boolean' &&
    typeof (d as UpdateInfoData).current_version === 'string'
  )
}

export function isUpdateProgressData(d: unknown): d is UpdateProgressData {
  return (
    typeof d === 'object' &&
    d !== null &&
    typeof (d as UpdateProgressData).done === 'number' &&
    typeof (d as UpdateProgressData).total === 'number'
  )
}

export function isUpdateErrorData(d: unknown): d is UpdateErrorData {
  return typeof d === 'object' && d !== null && typeof (d as UpdateErrorData).message === 'string'
}
