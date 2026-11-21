// Tool manager install-status RPC bridge tests — the wrapper is thin, so the
// contract to pin is narrow: the snapshot is forwarded validated, and every
// failure mode (unexpected shape, rejected RPC, missing transport) resolves
// null instead of rejecting — a failed read is the caller's quiet fallback
// to the event-driven splash, never a startup-visible error.
import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, ((...args: unknown[]) => Promise<unknown>) | undefined> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), debug: vi.fn() },
}))

import { getToolInstallStatus } from '@/api/toolmanager'

describe('getToolInstallStatus', () => {
  beforeEach(() => {
    delete mockApp.GetToolInstallStatus
  })

  it('forwards the RPC and resolves the validated snapshot', async () => {
    const dto = { active: true, pending: [{ name: 'rg', version: '15.1.0' }], tools_total: 2 }
    mockApp.GetToolInstallStatus = vi.fn(() => Promise.resolve(dto))
    await expect(getToolInstallStatus()).resolves.toEqual(dto)
    expect(mockApp.GetToolInstallStatus).toHaveBeenCalledTimes(1)
  })

  it('resolves null on an unexpected response shape (event-driven fallback)', async () => {
    mockApp.GetToolInstallStatus = vi.fn(() => Promise.resolve({ active: 'yes' }))
    await expect(getToolInstallStatus()).resolves.toBeNull()
  })

  it('resolves null when the RPC rejects (quiet fallback — startup must not break)', async () => {
    mockApp.GetToolInstallStatus = vi.fn(() => Promise.reject(new Error('transport gone')))
    await expect(getToolInstallStatus()).resolves.toBeNull()
  })

  it('resolves null when the binding is missing (getApp yields no method)', async () => {
    mockApp.GetToolInstallStatus = undefined
    await expect(getToolInstallStatus()).resolves.toBeNull()
  })
})
