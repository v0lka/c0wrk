import { describe, it, expect, beforeEach } from 'vitest'
import {
  useUpdateStore,
  type UpdatePhase,
} from '@/stores/updateStore'
import type { UpdateInfo } from '@/api/updater'

// Build a minimal valid UpdateInfo for tests.
function makeInfo(latest: string, current = '1.0.0'): UpdateInfo {
  return {
    available: true,
    current_version: current,
    latest_version: latest,
    release_notes: '',
    published_at: '',
    html_url: '',
    asset_name: '',
  }
}

function resetStore() {
  // Reset to a clean idle state while keeping the action identity intact.
  const { setCurrentVersion } = useUpdateStore.getState()
  useUpdateStore.setState({
    phase: 'idle',
    info: null,
    currentVersion: '',
    progress: null,
    errorMessage: null,
    isChecking: false,
    isDownloading: false,
  })
  void setCurrentVersion
}

describe('updateStore', () => {
  beforeEach(() => {
    resetStore()
  })

  it('starts idle with empty fields', () => {
    const s = useUpdateStore.getState()
    expect(s.phase).toBe('idle')
    expect(s.info).toBeNull()
    expect(s.currentVersion).toBe('')
    expect(s.progress).toBeNull()
    expect(s.errorMessage).toBeNull()
    expect(s.isChecking).toBe(false)
    expect(s.isDownloading).toBe(false)
  })

  it('onAvailable transitions to available and stores the info, clearing flags', () => {
    const info = makeInfo('1.2.3')
    // Seed an error state first to prove onAvailable clears it.
    useUpdateStore.getState().onError('boom')
    useUpdateStore.getState().onAvailable(info)
    const s = useUpdateStore.getState()
    expect(s.phase).toBe('available')
    expect(s.info).toEqual(info)
    expect(s.errorMessage).toBeNull()
    expect(s.isChecking).toBe(false)
    expect(s.isDownloading).toBe(false)
  })

  it('onProgress transitions to downloading and records done/total', () => {
    useUpdateStore.getState().onProgress(256, 1024)
    const s = useUpdateStore.getState()
    expect(s.phase).toBe('downloading')
    expect(s.progress).toEqual({ done: 256, total: 1024 })
    expect(s.errorMessage).toBeNull()
  })

  it('onProgress overwrites previous progress ticks', () => {
    useUpdateStore.getState().onProgress(100, 1000)
    useUpdateStore.getState().onProgress(500, 1000)
    expect(useUpdateStore.getState().progress).toEqual({ done: 500, total: 1000 })
  })

  it('onProgress leaves info intact (download follows an available release)', () => {
    const info = makeInfo('2.0.0')
    useUpdateStore.getState().onAvailable(info)
    useUpdateStore.getState().onProgress(1, 2)
    const s = useUpdateStore.getState()
    expect(s.phase).toBe('downloading')
    expect(s.info).toEqual(info)
  })

  it('onDownloaded transitions to downloaded and clears progress', () => {
    useUpdateStore.getState().onProgress(1000, 1000)
    useUpdateStore.getState().onDownloaded()
    const s = useUpdateStore.getState()
    expect(s.phase).toBe('downloaded')
    expect(s.progress).toBeNull()
    expect(s.isDownloading).toBe(false)
  })

  it('onError transitions to error and stores the message, clearing busy flags', () => {
    useUpdateStore.getState().setChecking(true)
    useUpdateStore.getState().setDownloading(true)
    useUpdateStore.getState().onError('network failed')
    const s = useUpdateStore.getState()
    expect(s.phase).toBe('error')
    expect(s.errorMessage).toBe('network failed')
    expect(s.isChecking).toBe(false)
    expect(s.isDownloading).toBe(false)
  })

  it('onNone resets to idle and clears info/progress', () => {
    const info = makeInfo('9.9.9')
    useUpdateStore.getState().onAvailable(info)
    useUpdateStore.getState().onNone()
    const s = useUpdateStore.getState()
    expect(s.phase).toBe('idle')
    expect(s.info).toBeNull()
    expect(s.progress).toBeNull()
    expect(s.errorMessage).toBeNull()
    expect(s.isChecking).toBe(false)
  })

  it('setCurrentVersion sets the running version', () => {
    useUpdateStore.getState().setCurrentVersion('1.4.2')
    expect(useUpdateStore.getState().currentVersion).toBe('1.4.2')
  })

  it('setChecking toggles the explicit-check flag', () => {
    useUpdateStore.getState().setChecking(true)
    expect(useUpdateStore.getState().isChecking).toBe(true)
    useUpdateStore.getState().setChecking(false)
    expect(useUpdateStore.getState().isChecking).toBe(false)
  })

  it('setDownloading toggles the download-initiation flag', () => {
    useUpdateStore.getState().setDownloading(true)
    expect(useUpdateStore.getState().isDownloading).toBe(true)
    useUpdateStore.getState().setDownloading(false)
    expect(useUpdateStore.getState().isDownloading).toBe(false)
  })

  it('dismiss returns to idle but preserves currentVersion', () => {
    useUpdateStore.getState().setCurrentVersion('3.1.0')
    useUpdateStore.getState().onAvailable(makeInfo('3.2.0', '3.1.0'))
    useUpdateStore.getState().dismiss()
    const s = useUpdateStore.getState()
    expect(s.phase).toBe('idle')
    expect(s.info).toBeNull()
    expect(s.progress).toBeNull()
    expect(s.errorMessage).toBeNull()
    // currentVersion survives dismissal — it's the running build, not toast state.
    expect(s.currentVersion).toBe('3.1.0')
  })

  it('full lifecycle: available → downloading → downloaded', () => {
    useUpdateStore.getState().onAvailable(makeInfo('2.0.0', '1.0.0'))
    expect(useUpdateStore.getState().phase).toBe<UpdatePhase>('available')
    useUpdateStore.getState().onProgress(10, 100)
    expect(useUpdateStore.getState().phase).toBe<UpdatePhase>('downloading')
    useUpdateStore.getState().onDownloaded()
    expect(useUpdateStore.getState().phase).toBe<UpdatePhase>('downloaded')
  })
})

// --- Referential stability of selectors & actions ---
//
// React 19's useSyncExternalStore compares snapshots by reference. A selector
// that allocates a fresh array/object on every read triggers an infinite
// re-render loop (React error #185). These tests assert the invariant at the
// store level: actions and the state slices surfaced by selector hooks keep
// stable identity / value references across unrelated updates.

describe('updateStore referential stability', () => {
  beforeEach(() => {
    resetStore()
  })

  it('actions keep stable identity across state changes', () => {
    const before = useUpdateStore.getState()
    before.onAvailable(makeInfo('1.0.1'))
    before.setCurrentVersion('1.0.0')
    const after = useUpdateStore.getState()

    // All action functions are defined once and must not be recreated.
    expect(after.onAvailable).toBe(before.onAvailable)
    expect(after.onProgress).toBe(before.onProgress)
    expect(after.onDownloaded).toBe(before.onDownloaded)
    expect(after.onError).toBe(before.onError)
    expect(after.onNone).toBe(before.onNone)
    expect(after.dismiss).toBe(before.dismiss)
    expect(after.setCurrentVersion).toBe(before.setCurrentVersion)
    expect(after.setChecking).toBe(before.setChecking)
    expect(after.setDownloading).toBe(before.setDownloading)
  })

  it('null slices keep the same null reference when untouched', () => {
    // Setting an unrelated primitive must not replace the null info/progress
    // objects with a new reference.
    useUpdateStore.getState().setCurrentVersion('1.0.0')
    const s1 = useUpdateStore.getState()
    useUpdateStore.getState().setChecking(true)
    const s2 = useUpdateStore.getState()
    expect(s2.info).toBe(s1.info) // both null, same reference
    expect(s2.progress).toBe(s1.progress)
  })
})
