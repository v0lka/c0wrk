// Unit tests for api/sessions.ts — forkSession wrapper.

import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import { forkSession } from '@/api/sessions'

const validSessionInfo = {
  id: 'fork-1',
  project_id: 'proj-1',
  name: 'Original (fork 1)',
  created_at: '2024-01-01T00:00:00Z',
  last_active_at: '2024-01-01T00:00:00Z',
  archived: false,
  active: false,
  total_input_tokens: 0,
  total_output_tokens: 0,
  model: '',
  family: '',
  fill_percent: 0,
}

describe('forkSession', () => {
  beforeEach(() => {
    delete mockApp.ForkSession
  })

  it('returns the forked SessionInfo on success', async () => {
    mockApp.ForkSession = vi.fn(() => Promise.resolve(validSessionInfo))

    const result = await forkSession('src-1')

    expect(mockApp.ForkSession).toHaveBeenCalledWith('src-1')
    expect(result.id).toBe('fork-1')
    expect(result.name).toBe('Original (fork 1)')
  })

  it('throws when the backend returns invalid data', async () => {
    mockApp.ForkSession = vi.fn(() => Promise.resolve({ nope: true }))

    await expect(forkSession('src-1')).rejects.toThrow(/invalid data/)
  })

  it('propagates RPC errors', async () => {
    mockApp.ForkSession = vi.fn(() => Promise.reject(new Error('cannot fork a session that has an unfinished task')))

    await expect(forkSession('src-1')).rejects.toThrow('unfinished task')
  })
})
