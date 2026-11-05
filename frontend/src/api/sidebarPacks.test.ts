// Sidebar surface-gate RPC bridge tests — the wrappers are thin, so the
// contract to pin is narrow: each toggle forwards its boolean to the
// matching backend RPC, and a rejection surfaces (throws) rather than being
// swallowed — a rejected toggle must roll the settings control back, never
// look like a successful persist.
import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import { setResearchEnabled, setSpecsEnabled } from '@/api/sidebarPacks'

describe('setResearchEnabled', () => {
  beforeEach(() => {
    delete mockApp.SetResearchEnabled
  })

  it('forwards the toggle to the backend', async () => {
    mockApp.SetResearchEnabled = vi.fn(() => Promise.resolve(undefined))
    await setResearchEnabled(false)
    expect(mockApp.SetResearchEnabled).toHaveBeenCalledWith(false)
  })

  it('throws on failure (a rejected toggle must surface, unlike a read)', async () => {
    mockApp.SetResearchEnabled = vi.fn(() => Promise.reject(new Error('config not initialized')))
    await expect(setResearchEnabled(true)).rejects.toThrow(/config not initialized/)
  })
})

describe('setSpecsEnabled', () => {
  beforeEach(() => {
    delete mockApp.SetSpecsEnabled
  })

  it('forwards the toggle to the backend', async () => {
    mockApp.SetSpecsEnabled = vi.fn(() => Promise.resolve(undefined))
    await setSpecsEnabled(true)
    expect(mockApp.SetSpecsEnabled).toHaveBeenCalledWith(true)
  })

  it('throws on failure (a rejected toggle must surface, unlike a read)', async () => {
    mockApp.SetSpecsEnabled = vi.fn(() => Promise.reject(new Error('config path not set')))
    await expect(setSpecsEnabled(false)).rejects.toThrow(/config path not set/)
  })
})
