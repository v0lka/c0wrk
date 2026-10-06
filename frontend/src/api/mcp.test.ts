// Unit tests for api/mcp.ts — the mode normalization on the GetMCPServers
// boundary (absent `mode` means "auto": the backend marshals it `omitempty`)
// and the GetMCPMentionableServers wrapper (shape guard, failure degrade,
// and the auto/manual mentionable filter that hides disabled servers).

import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import {
  getMCPServers,
  getMCPMentionableServers,
  isMentionableMCPMode,
  mentionableMCPNames,
} from '@/api/mcp'

describe('getMCPServers mode normalization', () => {
  beforeEach(() => {
    delete mockApp.GetMCPServers
  })

  it('normalizes an absent mode to "auto" (omitempty wire shape)', async () => {
    mockApp.GetMCPServers = vi.fn(() => Promise.resolve({
      legacy: { transport: 'stdio', command: 'cmd' },
      manual: { transport: 'stdio', command: 'cmd', mode: 'manual' },
      off: { transport: 'http', url: 'http://x', mode: 'disabled' },
    }))

    const servers = await getMCPServers()

    expect(servers['legacy']!.mode).toBe('auto')
    expect(servers['manual']!.mode).toBe('manual')
    expect(servers['off']!.mode).toBe('disabled')
  })

  it('keeps normalizing the omitted timeout keys alongside mode', async () => {
    mockApp.GetMCPServers = vi.fn(() => Promise.resolve({
      srv: { transport: 'stdio', command: 'cmd' },
    }))

    const servers = await getMCPServers()

    expect(servers['srv']!.timeout).toBe('')
    expect(servers['srv']!.call_timeout).toBe('')
    expect(servers['srv']!.mode).toBe('auto')
  })
})

describe('getMCPMentionableServers', () => {
  beforeEach(() => {
    delete mockApp.GetMCPMentionableServers
  })

  it('returns the validated {name, mode} listing', async () => {
    mockApp.GetMCPMentionableServers = vi.fn(() => Promise.resolve([
      { name: 'context7', mode: 'auto' },
      { name: 'github', mode: 'manual' },
    ]))

    const servers = await getMCPMentionableServers()

    expect(servers).toEqual([
      { name: 'context7', mode: 'auto' },
      { name: 'github', mode: 'manual' },
    ])
  })

  it('degrades to [] on an unexpected response shape', async () => {
    mockApp.GetMCPMentionableServers = vi.fn(() => Promise.resolve([
      { name: 'context7', mode: 'not-a-mode' },
    ]))

    const servers = await getMCPMentionableServers()

    expect(servers).toEqual([])
  })

  it('degrades to [] when the RPC fails (never blocks the input)', async () => {
    mockApp.GetMCPMentionableServers = vi.fn(() => Promise.reject(new Error('rpc down')))

    const servers = await getMCPMentionableServers()

    expect(servers).toEqual([])
  })
})

describe('mentionable mode helpers', () => {
  it('treats auto and manual as mentionable, disabled as not', () => {
    expect(isMentionableMCPMode('auto')).toBe(true)
    expect(isMentionableMCPMode('manual')).toBe(true)
    expect(isMentionableMCPMode('disabled')).toBe(false)
  })

  it('filters a listing down to auto+manual names, preserving order', () => {
    const names = mentionableMCPNames([
      { name: 'zeta', mode: 'manual' },
      { name: 'alpha', mode: 'disabled' },
      { name: 'mid', mode: 'auto' },
    ])

    expect(names).toEqual(['zeta', 'mid'])
  })
})
