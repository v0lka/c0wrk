// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'

const onGlobalEventMock = vi.fn()
const reportDroppedEventMock = vi.fn()
const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('./runtime', () => ({
  onGlobalEvent: (...args: unknown[]) => onGlobalEventMock(...(args as [])) as never,
  getApp: () => mockApp,
  reportDroppedEvent: (...args: unknown[]) => reportDroppedEventMock(...(args as [])) as never,
}))

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn() },
}))

import { onGitConfigRisk, getHardenGitRepos, hardenGitRepo, removeHardenGitRepo } from './gitConfigRisk'
import type { GitConfigRiskData } from '@/types/events'

const valid: GitConfigRiskData = {
  path: '/repo',
  source: 'project',
  notice: 'Repository-defined git hooks do not run inside c0wrk.',
  findings: [{ key: 'core.fsmonitor', description: 'runs a monitor command' }],
}

beforeEach(() => {
  onGlobalEventMock.mockReset()
  reportDroppedEventMock.mockReset()
  onGlobalEventMock.mockImplementation((_event: string, handler: (data: unknown) => void) => handler)
})

describe('onGitConfigRisk payload validation', () => {
  it('reports malformed payloads instead of dropping them silently (finding [33])', () => {
    const cb = vi.fn()
    const handler = onGitConfigRisk(cb) as unknown as (data: unknown) => void

    handler({ not: 'valid' })

    expect(cb).not.toHaveBeenCalled()
    expect(reportDroppedEventMock).toHaveBeenCalledTimes(1)
    expect(reportDroppedEventMock).toHaveBeenCalledWith('project:git_config_risk', { not: 'valid' })
  })

  it('reports a null payload and still does not invoke the callback', () => {
    const cb = vi.fn()
    const handler = onGitConfigRisk(cb) as unknown as (data: unknown) => void

    handler(null)

    expect(cb).not.toHaveBeenCalled()
    expect(reportDroppedEventMock).toHaveBeenCalledWith('project:git_config_risk', null)
  })

  it('passes valid payloads through untouched', () => {
    const cb = vi.fn()
    const handler = onGitConfigRisk(cb) as unknown as (data: unknown) => void

    handler(valid)

    expect(cb).toHaveBeenCalledWith(valid)
    expect(reportDroppedEventMock).not.toHaveBeenCalled()
  })
})

describe('hardened git repository RPC wrappers', () => {
  beforeEach(() => {
    delete mockApp.GetHardenGitRepos
    delete mockApp.HardenGitRepo
    delete mockApp.RemoveHardenGitRepo
  })

  it('getHardenGitRepos returns the validated string array', async () => {
    mockApp.GetHardenGitRepos = vi.fn().mockResolvedValue(['/repo/a', '/repo/b'])
    await expect(getHardenGitRepos()).resolves.toEqual(['/repo/a', '/repo/b'])
  })

  it('getHardenGitRepos rejects a non-array response', async () => {
    mockApp.GetHardenGitRepos = vi.fn().mockResolvedValue({ 0: '/repo/a' })
    await expect(getHardenGitRepos()).rejects.toThrow('getHardenGitRepos: backend returned invalid data')
  })

  it('getHardenGitRepos rejects an array with non-string entries', async () => {
    mockApp.GetHardenGitRepos = vi.fn().mockResolvedValue(['/repo/a', 42])
    await expect(getHardenGitRepos()).rejects.toThrow('getHardenGitRepos: backend returned invalid data')
  })

  it('hardenGitRepo calls the RPC with the path', async () => {
    mockApp.HardenGitRepo = vi.fn().mockResolvedValue(undefined)
    await hardenGitRepo('/repo/a')
    expect(mockApp.HardenGitRepo).toHaveBeenCalledWith('/repo/a')
  })

  it('hardenGitRepo propagates backend errors', async () => {
    mockApp.HardenGitRepo = vi.fn().mockRejectedValue(new Error('config not initialized'))
    await expect(hardenGitRepo('/repo/a')).rejects.toThrow('config not initialized')
  })

  it('removeHardenGitRepo calls the RPC with the path', async () => {
    mockApp.RemoveHardenGitRepo = vi.fn().mockResolvedValue(undefined)
    await removeHardenGitRepo('/repo/a')
    expect(mockApp.RemoveHardenGitRepo).toHaveBeenCalledWith('/repo/a')
  })

  it('removeHardenGitRepo propagates backend errors', async () => {
    mockApp.RemoveHardenGitRepo = vi.fn().mockRejectedValue(new Error('config not initialized'))
    await expect(removeHardenGitRepo('/repo/a')).rejects.toThrow('config not initialized')
  })
})
