// Unit tests for api/auth.ts — the ChatGPT subscription-auth RPC wrappers'
// boundary validation and the chatgpt_auth:state event fan-out.

import { describe, it, expect, vi, beforeEach } from 'vitest'

// Mocks created via vi.hoisted so they exist before vi.mock factories run
// (the factories dereference the object itself, not just lazy closures).
const { mockApp, runtimeMocks } = vi.hoisted(() => {
  const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}
  return {
    mockApp,
    runtimeMocks: {
      getApp: () => mockApp,
      onGlobalEvent: vi.fn(
        (_eventName: string, _callback: (...data: unknown[]) => void) => () => {},
      ),
      openExternalURL: vi.fn(),
      reportDroppedEvent: vi.fn(),
    },
  }
})

vi.mock('@/api/runtime', () => runtimeMocks)

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import {
  cancelChatGPTSignIn,
  fetchChatGPTModels,
  getChatGPTAuthStatus,
  getChatGPTModelPreset,
  normalizeChatGPTAuthMode,
  onChatGPTAuthState,
  signOutChatGPT,
  startChatGPTSignIn,
} from './auth'

beforeEach(() => {
  vi.clearAllMocks()
  for (const key of Object.keys(mockApp)) delete mockApp[key]
})

describe('normalizeChatGPTAuthMode', () => {
  it('maps the two real modes through unchanged', () => {
    expect(normalizeChatGPTAuthMode('api_key')).toBe('api_key')
    expect(normalizeChatGPTAuthMode('oauth')).toBe('oauth')
  })

  it('reads anything else as the documented default api_key', () => {
    expect(normalizeChatGPTAuthMode('')).toBe('api_key')
    expect(normalizeChatGPTAuthMode('OAuth')).toBe('api_key')
    expect(normalizeChatGPTAuthMode(undefined)).toBe('api_key')
    expect(normalizeChatGPTAuthMode(42)).toBe('api_key')
  })
})

describe('getChatGPTAuthStatus', () => {
  it('returns a valid snapshot with a normalized mode', async () => {
    mockApp.GetChatGPTAuthStatus = vi.fn().mockResolvedValue({
      signed_in: true,
      email: 'dev@example.com',
      account_id: 'acct-1',
      expires_at: '2026-10-01T10:00:00Z',
      mode: 'oauth',
    })
    const st = await getChatGPTAuthStatus()
    expect(st.signed_in).toBe(true)
    expect(st.email).toBe('dev@example.com')
    expect(st.mode).toBe('oauth')
  })

  it('normalizes a drifted mode value to api_key', async () => {
    mockApp.GetChatGPTAuthStatus = vi.fn().mockResolvedValue({
      signed_in: false,
      mode: 'weird-mode',
      last_error: 'keychain unavailable',
    })
    const st = await getChatGPTAuthStatus()
    expect(st.mode).toBe('api_key')
    expect(st.last_error).toBe('keychain unavailable')
  })

  it('throws on a payload missing the signed_in flag', async () => {
    mockApp.GetChatGPTAuthStatus = vi.fn().mockResolvedValue({ mode: 'api_key' })
    await expect(getChatGPTAuthStatus()).rejects.toThrow('invalid data')
  })
})

describe('startChatGPTSignIn', () => {
  it('returns the URL and opens the system browser with it', async () => {
    mockApp.StartChatGPTSignIn = vi.fn().mockResolvedValue({
      auth_url: 'https://auth.openai.com/authorize?x=1',
    })
    const resp = await startChatGPTSignIn()
    expect(resp.auth_url).toBe('https://auth.openai.com/authorize?x=1')
    expect(runtimeMocks.openExternalURL).toHaveBeenCalledWith('https://auth.openai.com/authorize?x=1')
  })

  it('throws without opening a browser when the payload is malformed', async () => {
    mockApp.StartChatGPTSignIn = vi.fn().mockResolvedValue({ auth_url: '' })
    await expect(startChatGPTSignIn()).rejects.toThrow('invalid data')
    expect(runtimeMocks.openExternalURL).not.toHaveBeenCalled()
  })

  it('propagates the backend refusal (a flow already in progress)', async () => {
    mockApp.StartChatGPTSignIn = vi.fn().mockRejectedValue(
      new Error('a ChatGPT sign-in is already in progress — cancel it before starting another'),
    )
    await expect(startChatGPTSignIn()).rejects.toThrow('already in progress')
    expect(runtimeMocks.openExternalURL).not.toHaveBeenCalled()
  })
})

describe('cancelChatGPTSignIn / signOutChatGPT', () => {
  it('call through to the RPCs', async () => {
    mockApp.CancelChatGPTSignIn = vi.fn().mockResolvedValue(undefined)
    mockApp.SignOutChatGPT = vi.fn().mockResolvedValue(undefined)
    await cancelChatGPTSignIn()
    await signOutChatGPT()
    expect(mockApp.CancelChatGPTSignIn).toHaveBeenCalledTimes(1)
    expect(mockApp.SignOutChatGPT).toHaveBeenCalledTimes(1)
  })

  it('propagate failures', async () => {
    mockApp.SignOutChatGPT = vi.fn().mockRejectedValue(new Error('signing out of ChatGPT: boom'))
    await expect(signOutChatGPT()).rejects.toThrow('boom')
  })
})

describe('getChatGPTModelPreset', () => {
  it('returns the preset entries in order, metadata included', async () => {
    mockApp.GetChatGPTModelPreset = vi.fn().mockResolvedValue({
      models: [
        { name: 'gpt-5.5', context_window: 272000, output_limit: 128000, reasoning: true },
        { name: 'codex-mini-latest' },
      ],
    })
    const resp = await getChatGPTModelPreset()
    expect(resp.models.map((m) => m.name)).toEqual(['gpt-5.5', 'codex-mini-latest'])
    expect(resp.models[0]?.context_window).toBe(272000)
    expect(resp.models[1]?.context_window).toBeUndefined()
  })

  it('throws when the payload is not an entry array', async () => {
    mockApp.GetChatGPTModelPreset = vi.fn().mockResolvedValue({ models: 'nope' })
    await expect(getChatGPTModelPreset()).rejects.toThrow('invalid data')
  })

  it('throws when an entry lacks a name', async () => {
    mockApp.GetChatGPTModelPreset = vi.fn().mockResolvedValue({ models: [{ name: '' }] })
    await expect(getChatGPTModelPreset()).rejects.toThrow('invalid data')
  })
})

describe('fetchChatGPTModels', () => {
  it('returns the live subscription entries in the backend priority order', async () => {
    mockApp.FetchChatGPTModels = vi.fn().mockResolvedValue({
      models: [
        { name: 'gpt-5.5', context_window: 272000, reasoning: true },
        { name: 'gpt-6-luna', context_window: 272000 },
      ],
    })
    const resp = await fetchChatGPTModels()
    expect(mockApp.FetchChatGPTModels).toHaveBeenCalledTimes(1)
    expect(resp.models.map((m) => m.name)).toEqual(['gpt-5.5', 'gpt-6-luna'])
    expect(resp.models[0]?.reasoning).toBe(true)
    expect(resp.models[1]?.output_limit).toBeUndefined()
  })

  it('throws when the payload is malformed', async () => {
    mockApp.FetchChatGPTModels = vi.fn().mockResolvedValue({ models: [{ name: 'x', context_window: 'big' }] })
    await expect(fetchChatGPTModels()).rejects.toThrow('invalid data')
  })

  it('propagates the actionable refusal when not signed in', async () => {
    mockApp.FetchChatGPTModels = vi.fn().mockRejectedValue(
      new Error('fetching the ChatGPT model list requires a subscription sign-in — sign in with ChatGPT first'),
    )
    await expect(fetchChatGPTModels()).rejects.toThrow('sign in')
  })
})

describe('onChatGPTAuthState', () => {
  /** Subscribe with a spy callback and return the raw payload handler the
   *  module registered with onGlobalEvent, so tests simulate backend
   *  transitions by invoking it directly. */
  function subscribeSpy(): { cb: ReturnType<typeof vi.fn>; handler: (...data: unknown[]) => void } {
    const cb = vi.fn()
    onChatGPTAuthState(cb)
    expect(runtimeMocks.onGlobalEvent).toHaveBeenCalledWith(
      'chatgpt_auth:state',
      expect.any(Function),
    )
    const handler = runtimeMocks.onGlobalEvent.mock.calls[
      runtimeMocks.onGlobalEvent.mock.calls.length - 1
    ]![1] as (...data: unknown[]) => void
    return { cb, handler }
  }

  it('passes a valid transition to the callback', () => {
    const { cb, handler } = subscribeSpy()
    handler({ state: 'pending', auth_url: 'https://auth.example/xyz' })
    expect(cb).toHaveBeenCalledWith({ state: 'pending', auth_url: 'https://auth.example/xyz' })
  })

  it('drops a payload with an unknown state and reports it', () => {
    const { cb, handler } = subscribeSpy()
    handler({ state: 'mysterious' })
    expect(cb).not.toHaveBeenCalled()
    expect(runtimeMocks.reportDroppedEvent).toHaveBeenCalledWith('chatgpt_auth:state', {
      state: 'mysterious',
    })
  })

  it('drops a non-object payload', () => {
    const { cb, handler } = subscribeSpy()
    handler(undefined)
    expect(cb).not.toHaveBeenCalled()
    expect(runtimeMocks.reportDroppedEvent).toHaveBeenCalled()
  })

  it('drops a payload with a non-string optional field', () => {
    const { cb, handler } = subscribeSpy()
    handler({ state: 'success', email: 42 })
    expect(cb).not.toHaveBeenCalled()
    expect(runtimeMocks.reportDroppedEvent).toHaveBeenCalled()
  })
})
