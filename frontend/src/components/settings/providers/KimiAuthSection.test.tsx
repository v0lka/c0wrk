// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { KimiAuthEventData } from '@/types/events'
import type { KimiAuthStatusResponse } from '@/types/models'

const spies = vi.hoisted(() => ({
  getKimiAuthStatus: vi.fn<() => Promise<KimiAuthStatusResponse>>(),
  startKimiSignIn: vi.fn(),
  cancelKimiSignIn: vi.fn(),
  signOutKimi: vi.fn(),
}))
let authStateHandler: ((data: KimiAuthEventData) => void) | null = null

vi.mock('@/api/auth', () => ({
  getKimiAuthStatus: spies.getKimiAuthStatus,
  startKimiSignIn: spies.startKimiSignIn,
  cancelKimiSignIn: spies.cancelKimiSignIn,
  signOutKimi: spies.signOutKimi,
  onKimiAuthState: (cb: (data: KimiAuthEventData) => void) => {
    authStateHandler = cb
    return () => { authStateHandler = null }
  },
}))

import { KimiAuthSection } from './KimiAuthSection'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  vi.clearAllMocks()
  authStateHandler = null
  spies.getKimiAuthStatus.mockResolvedValue({ signed_in: false, mode: 'oauth', in_flight: false })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(async () => {
  await act(async () => root.unmount())
  expect(authStateHandler).toBeNull()
  container.remove()
  document.body.innerHTML = ''
})

function renderSection(authMode: 'api_key' | 'oauth' = 'oauth', beforeSignIn?: () => Promise<void>): void {
  act(() => root.render(<KimiAuthSection authMode={authMode} onAuthModeChange={() => {}} beforeSignIn={beforeSignIn} />))
}

async function settleStatus(): Promise<void> {
  const result = spies.getKimiAuthStatus.mock.results[spies.getKimiAuthStatus.mock.results.length - 1]
  if (!result || result.type !== 'return') throw new Error('no Kimi status RPC')
  await act(async () => { await result.value })
}

function buttonByText(text: string): HTMLButtonElement {
  const button = Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes(text))
  if (!button) throw new Error(`button ${text} not found`)
  return button as HTMLButtonElement
}

describe('KimiAuthSection', () => {
  it('renders Kimi-specific auth controls without subscription UI in api_key mode', async () => {
    renderSection('api_key')
    await settleStatus()
    expect(container.querySelector('button[aria-label="Kimi authentication mode"]')).not.toBeNull()
    expect(container.textContent).not.toContain('Sign in with Kimi')
  })

  it('shows the region selector only in oauth mode', async () => {
    renderSection()
    await settleStatus()
    expect(container.querySelector('button[aria-label="Kimi region"]')).not.toBeNull()
    renderSection('api_key')
    expect(container.querySelector('button[aria-label="Kimi region"]')).toBeNull()
  })

  it('flushes the region save before starting sign-in', async () => {
    const calls: string[] = []
    const beforeSignIn = vi.fn().mockImplementation(async () => { calls.push('flush') })
    spies.startKimiSignIn.mockResolvedValue({ auth_url: 'https://auth.kimi.com/device?user_code=test' })
    renderSection('oauth', beforeSignIn)
    await settleStatus()
    await act(async () => {
      buttonByText('Sign in with Kimi').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await beforeSignIn.mock.results[0]!.value
    })
    expect(beforeSignIn).toHaveBeenCalledTimes(1)
    expect(spies.startKimiSignIn).toHaveBeenCalledTimes(1)
    expect(calls).toEqual(['flush'])
    expect(beforeSignIn.mock.invocationCallOrder[0]!).toBeLessThan(spies.startKimiSignIn.mock.invocationCallOrder[0]!)
  })

  it('does not start sign-in when the region save fails', async () => {
    const beforeSignIn = vi.fn().mockRejectedValue(new Error('save failed'))
    renderSection('oauth', beforeSignIn)
    await settleStatus()
    await act(async () => {
      buttonByText('Sign in with Kimi').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(beforeSignIn).toHaveBeenCalledTimes(1)
    expect(spies.startKimiSignIn).not.toHaveBeenCalled()
    expect(container.textContent).toContain('save failed')
  })

  it('starts sign-in and applies the success transition', async () => {
    renderSection()
    await settleStatus()
    spies.startKimiSignIn.mockResolvedValue({ auth_url: 'https://auth.kimi.com/device?user_code=test' })
    await act(async () => {
      buttonByText('Sign in with Kimi').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await spies.startKimiSignIn.mock.results[spies.startKimiSignIn.mock.results.length - 1]!.value
    })
    expect(container.textContent).toContain('Waiting for browser')

    spies.getKimiAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth', in_flight: false, email: 'kimi@example.com' })
    act(() => authStateHandler?.({ state: 'success', email: 'kimi@example.com' }))
    await settleStatus()
    expect(container.textContent).toContain('Signed in with Kimi')
    expect(container.textContent).toContain('kimi@example.com')
  })

  it('shows restored identity and signs out', async () => {
    spies.getKimiAuthStatus.mockResolvedValue({ signed_in: true, mode: 'oauth', in_flight: false, email: 'restored@example.com' })
    renderSection()
    await settleStatus()
    expect(container.textContent).toContain('Signed in with Kimi')
    expect(container.textContent).toContain('restored@example.com')
    spies.signOutKimi.mockResolvedValue(undefined)
    await act(async () => {
      buttonByText('Sign out').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await spies.signOutKimi.mock.results[spies.signOutKimi.mock.results.length - 1]!.value
    })
    expect(spies.signOutKimi).toHaveBeenCalledTimes(1)
    expect(spies.getKimiAuthStatus.mock.calls.length).toBeGreaterThanOrEqual(2)
  })
})
