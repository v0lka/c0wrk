// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  const map = new Map<string, string>()
  win.localStorage = {
    getItem: (key: string) => map.get(key) ?? null,
    setItem: (key: string, value: string) => { map.set(key, value) },
    removeItem: (key: string) => { map.delete(key) },
    clear: () => map.clear(),
    key: (index: number) => Array.from(map.keys())[index] ?? null,
    get length() { return map.size },
  }
})

import { useUIStore } from '@/stores/uiStore'

const UI_STORAGE_KEY = 'c0wrk-sidebar-collapsed'
const CURRENT_VERSION = 8

describe('sessionListGrouped persistence', () => {
  beforeEach(() => {
    localStorage.clear()
    useUIStore.setState({ sessionListGrouped: false })
  })

  it('defaults to false on fresh installs', async () => {
    await useUIStore.persist.rehydrate()
    expect(useUIStore.getState().sessionListGrouped).toBe(false)
  })

  it('setSessionListGrouped flips the flag', () => {
    useUIStore.getState().setSessionListGrouped(true)
    expect(useUIStore.getState().sessionListGrouped).toBe(true)

    useUIStore.getState().setSessionListGrouped(false)
    expect(useUIStore.getState().sessionListGrouped).toBe(false)
  })

  it('survives an app restart (persist round-trip)', async () => {
    useUIStore.getState().setSessionListGrouped(true)
    expect(useUIStore.getState().sessionListGrouped).toBe(true)

    const persisted = localStorage.getItem(UI_STORAGE_KEY)
    expect(JSON.parse(persisted ?? '{}').state.sessionListGrouped).toBe(true)

    useUIStore.getState().setSessionListGrouped(false)
    localStorage.setItem(UI_STORAGE_KEY, persisted!)

    await useUIStore.persist.rehydrate()

    expect(useUIStore.getState().sessionListGrouped).toBe(true)
  })

  it('drops a same-version wrong-typed value on rehydrate (merge, not migrate)', async () => {
    // A corrupt/missing-type flag in SAME-version storage (hand-edited
    // localStorage) never reaches `migrate` (version matches), so the
    // non-boolean value must be discarded by `merge` and fall back to the
    // default — mirrors the tabsEnabled/showSessionStats contract.
    localStorage.setItem(
      UI_STORAGE_KEY,
      JSON.stringify({
        state: {
          sessionListGrouped: 'yes',
        },
        version: CURRENT_VERSION,
      }),
    )

    await useUIStore.persist.rehydrate()

    expect(useUIStore.getState().sessionListGrouped).toBe(false)
  })

  it('keeps a valid persisted true through merge on rehydrate', async () => {
    localStorage.setItem(
      UI_STORAGE_KEY,
      JSON.stringify({
        state: {
          sessionListGrouped: true,
        },
        version: CURRENT_VERSION,
      }),
    )

    await useUIStore.persist.rehydrate()

    expect(useUIStore.getState().sessionListGrouped).toBe(true)
  })

  it('migrates a v7 payload: previous fields preserved, flag defaults to false', async () => {
    localStorage.setItem(
      UI_STORAGE_KEY,
      JSON.stringify({
        state: {
          sidebarCollapsed: true,
          sidebarWidth: 250,
          chatSessionListRatio: 0.3,
          showSessionStats: true,
          tabsEnabled: true,
          workspaceTabByProject: { p1: 'research' },
          researchSegmentByProject: { p1: 'papers' },
        },
        version: 7,
      }),
    )

    await useUIStore.persist.rehydrate()

    const state = useUIStore.getState()
    expect(state.sessionListGrouped).toBe(false)
    expect(state.sidebarCollapsed).toBe(true)
    expect(state.sidebarWidth).toBe(250)
    expect(state.chatSessionListRatio).toBe(0.3)
    expect(state.showSessionStats).toBe(true)
    expect(state.tabsEnabled).toBe(true)
    expect(state.workspaceTabByProject).toEqual({ p1: 'research' })
    expect(state.researchSegmentByProject).toEqual({ p1: 'papers' })
  })
})

describe('workspaceTabByProject — specs tab value (merge validation)', () => {
  beforeEach(() => {
    localStorage.clear()
    useUIStore.setState({ workspaceTabByProject: {} })
  })

  it('keeps a same-version persisted specs tab through merge', async () => {
    // The Specs workspace tab (ADR-105) is a new VALID value: the same-version
    // merge must accept it entry-by-entry so a project remembered on the
    // Specs panel restores there after a reload.
    localStorage.setItem(
      UI_STORAGE_KEY,
      JSON.stringify({
        state: { workspaceTabByProject: { p1: 'specs', p2: 'research' } },
        version: CURRENT_VERSION,
      }),
    )

    await useUIStore.persist.rehydrate()

    expect(useUIStore.getState().workspaceTabByProject).toEqual({
      p1: 'specs',
      p2: 'research',
    })
  })

  it('still drops unknown tab values beside a valid specs entry (fail-closed)', async () => {
    localStorage.setItem(
      UI_STORAGE_KEY,
      JSON.stringify({
        state: { workspaceTabByProject: { p1: 'specs', p2: 'bogus' } },
        version: CURRENT_VERSION,
      }),
    )

    await useUIStore.persist.rehydrate()

    expect(useUIStore.getState().workspaceTabByProject).toEqual({ p1: 'specs' })
  })

  it('setWorkspaceTab persists the specs tab value', () => {
    useUIStore.getState().setWorkspaceTab('p1', 'specs')

    expect(useUIStore.getState().workspaceTabByProject).toEqual({ p1: 'specs' })
  })
})
