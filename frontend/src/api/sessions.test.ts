// Unit tests for api/sessions.ts — forkSession wrapper.

import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import { listAllSessions, forkSession } from '@/api/sessions'
import { isSessionInfo } from '@/types/guards'

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

/** Session from a second project carrying the new unfinished_task_status
 *  field — listAllSessions returns one flat cross-project list. */
const sessionWithStatus = {
  ...validSessionInfo,
  id: 'all-2',
  project_id: 'proj-2',
  name: 'Cross-project session',
  unfinished_task_status: 'in_progress',
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

describe('listAllSessions', () => {
  beforeEach(() => {
    delete mockApp.ListAllSessions
  })

  it('returns the flat cross-project session list', async () => {
    mockApp.ListAllSessions = vi.fn(() => Promise.resolve([sessionWithStatus, validSessionInfo]))

    const result = await listAllSessions()

    expect(mockApp.ListAllSessions).toHaveBeenCalledTimes(1)
    expect(result).toHaveLength(2)
    expect(result.map(s => s.project_id)).toEqual(['proj-2', 'proj-1'])
    expect(result.find(s => s.id === 'all-2')?.unfinished_task_status).toBe('in_progress')
  })

  it('returns [] when the backend returns an unexpected response shape', async () => {
    mockApp.ListAllSessions = vi.fn(() => Promise.resolve({ nope: true }))

    await expect(listAllSessions()).resolves.toEqual([])
  })

  it('returns [] when any element fails the guard', async () => {
    mockApp.ListAllSessions = vi.fn(() =>
      Promise.resolve([sessionWithStatus, { id: 'bad', project_id: 'proj-1' }]),
    )

    await expect(listAllSessions()).resolves.toEqual([])
  })

  it('propagates RPC errors', async () => {
    mockApp.ListAllSessions = vi.fn(() => Promise.reject(new Error('db is locked')))

    await expect(listAllSessions()).rejects.toThrow('db is locked')
  })
})

describe('isSessionInfo: unfinished_task_status', () => {
  it('accepts payloads without the field (backward compatibility)', () => {
    expect(isSessionInfo(validSessionInfo)).toBe(true)
  })

  it('accepts payloads with a string status, including empty', () => {
    expect(isSessionInfo(sessionWithStatus)).toBe(true)
    expect(isSessionInfo({ ...validSessionInfo, unfinished_task_status: '' })).toBe(true)
    expect(isSessionInfo({ ...validSessionInfo, unfinished_task_status: 'failed' })).toBe(true)
  })

  it('rejects payloads where the field is present but not a string', () => {
    expect(isSessionInfo({ ...validSessionInfo, unfinished_task_status: 3 })).toBe(false)
    expect(isSessionInfo({ ...validSessionInfo, unfinished_task_status: null })).toBe(false)
  })
})
