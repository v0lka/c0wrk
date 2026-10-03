// @vitest-environment jsdom
// Component tests for ChatGPTAuthSection: the api_key/oauth mode selector,
// the browser sign-in flow driven through the captured chatgpt_auth:state
// subscription, the signed-in identity snapshot, and Sign out.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ChatGPTAuthEventData } from '@/types/events'
import type { ChatGPTAuthStatusResponse } from '@/types/models'

const spies = vi.hoisted(() => ({
  getChatGPTAuthStatus: vi.fn<() => Promise<ChatGPTAuthStatusResponse>>(),
  startChatGPTSignIn: vi.fn(),
  cancelChatGPTSignIn: vi.fn(),
  signOutChatGPT: vi.fn(),
}))

// The component subscribes via onChatGPTAuthState; capture the registered
// handler so tests simulate backend transitions by invoking it directly.
// Declared with `var`-style hoisting semantics via function-scope assignment
// inside the mock factory below (called only after the component mounts).
let authStateHandler: ((data: ChatGPTAuthEventData) => void) | null = null

vi.mock('@/api/auth', () => ({
  getChatGPTAuthStatus: spies.getChatGPTAuthStatus,
  startChatGPTSignIn: spies.startChatGPTSignIn,
  cancelChatGPTSignIn: spies.cancelChatGPTSignIn,
  signOutChatGPT: spies.signOutChatGPT,
  onChatGPTAuthState: (cb: (data: ChatGPTAuthEventData) => void) => {
    authStateHandler = cb
    return () => {
      authStateHandler = null
    }
  },
}))

import { ChatGPTAuthSection } from './ChatGPTAuthSection'
import { formatAuthExpiry } from '@/lib/chatgptFormat'

let container: HTMLDivElement
let root: Root
let modeChanges: Array<'api_key' | 'oauth'>

beforeEach(() => {
  vi.clearAllMocks()
  authStateHandler = null
  modeChanges = []
  spies.getChatGPTAuthStatus.mockResolvedValue({ signed_in: false, mode: 'api_key', in_flight: false })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(async () => {
  await act(async () => {
    root.unmount()
  })
  expect(authStateHandler).toBeNull()
  container.remove()
  document.body.innerHTML = ''
})

type Mode = 'api_key' | 'oauth'

function renderSection(authMode: Mode): void {
  act(() => {
    root.render(
      <ChatGPTAuthSection
        authMode={authMode}
        onAuthModeChange={(m) => modeChanges.push(m)}
      />,
    )
  })
}

/** Await the status RPC actually issued by the mounted component. */
async function settleStatus(): Promise<void> {
 const result = spies.getChatGPTAuthStatus.mock.results[spies.getChatGPTAuthStatus.mock.results.length - 1]
 if (!result || result.type !== 'return') throw new Error('no status RPC to settle')
 await act(async () => { await result.value })
}

function buttonByText(text: string): HTMLButtonElement {
  const btn = Array.from(container.querySelectorAll('button')).find((b) =>
    b.textContent?.includes(text),
  )
  if (!btn) throw new Error(`button "${text}" not found`)
  return btn
}

async function pickOption(ariaLabel: string, optionLabel: string): Promise<void> {
  const trigger = container.querySelector(`button[aria-label="${ariaLabel}"]`)
  if (!trigger) throw new Error(`${ariaLabel} trigger not found`)
  await act(async () => {
    trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
  })
  const option = Array.from(document.body.querySelectorAll<HTMLElement>('[role="menuitem"]')).find(
    (o) => o.textContent?.includes(optionLabel),
  )
  if (!option) throw new Error(`Option "${optionLabel}" not found`)
  await act(async () => {
    option.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** Emit one chatgpt_auth:state transition to the mounted component. */
function emitAuthState(data: ChatGPTAuthEventData): void {
  if (!authStateHandler) throw new Error('no auth state handler subscribed')
  act(() => {
    authStateHandler!(data)
  })
}

describe('formatAuthExpiry', () => {
  it('renders an RFC3339 timestamp as a local string', () => {
    expect(formatAuthExpiry('2026-10-01T10:00:00Z')).toBe(
      new Date('2026-10-01T10:00:00Z').toLocaleString(),
    )
  })

  it('shows an unparseable value verbatim instead of dropping it', () => {
    expect(formatAuthExpiry('not-a-date')).toBe('not-a-date')
  })
})

describe('ChatGPTAuthSection mode selector', () => {
  it('renders the selector without subscription UI in api_key mode', async () => {
    renderSection('api_key')
    await settleStatus()
    expect(container.querySelector('button[aria-label="ChatGPT authentication mode"]')).not.toBeNull()
    expect(container.textContent).toContain('API key')
    expect(container.textContent).not.toContain('Sign in with ChatGPT')
    // The status snapshot is fetched once on mount regardless of mode, so a
    // mode flip without remount already knows the identity.
    expect(spies.getChatGPTAuthStatus).toHaveBeenCalledTimes(1)
  })

  it('reports oauth when the subscription option is picked', async () => {
    renderSection('api_key')
    await settleStatus()
    await pickOption('ChatGPT authentication mode', 'ChatGPT subscription')
    expect(modeChanges).toEqual(['oauth'])
  })

  it('reports api_key when the API key option is re-picked from oauth', async () => {
    renderSection('oauth')
    await settleStatus()
    await pickOption('ChatGPT authentication mode', 'API key')
    expect(modeChanges).toEqual(['api_key'])
  })
})

describe('ChatGPTAuthSection sign-in flow', () => {
  it('shows the identity snapshot and signs out when already signed in', async () => {
    spies.getChatGPTAuthStatus.mockResolvedValue({
      signed_in: true,
      in_flight: false,
      email: 'dev@example.com',
      account_id: 'acct-7',
      expires_at: '2026-10-01T10:00:00Z',
      mode: 'oauth',
    })
    renderSection('oauth')
    await settleStatus()

    expect(container.textContent).toContain('Signed in with ChatGPT')
    expect(container.textContent).toContain('dev@example.com')
    expect(container.textContent).toContain('acct-7')
    expect(container.textContent).toContain(new Date('2026-10-01T10:00:00Z').toLocaleString())

    spies.signOutChatGPT.mockResolvedValue(undefined)
    await act(async () => {
      buttonByText('Sign out').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await spies.signOutChatGPT.mock.results[spies.signOutChatGPT.mock.results.length - 1]!.value.catch(() => undefined)
    })
    expect(spies.signOutChatGPT).toHaveBeenCalledTimes(1)
    // The snapshot is re-read after the mutation settles.
    expect(spies.getChatGPTAuthStatus.mock.calls.length).toBeGreaterThanOrEqual(2)
  })

  it('starts the flow, shows the busy state, and clears it on success', async () => {
    renderSection('oauth')
    await settleStatus()

    spies.startChatGPTSignIn.mockResolvedValue({ auth_url: 'https://auth.example/abc' })
    await act(async () => {
      buttonByText('Sign in with ChatGPT').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await spies.startChatGPTSignIn.mock.results[spies.startChatGPTSignIn.mock.results.length - 1]!.value.catch(() => undefined)
    })
    expect(spies.startChatGPTSignIn).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain('Waiting for browser')

    // The flow completes in the browser; the success transition re-reads the
    // authoritative snapshot and clears the busy state.
    spies.getChatGPTAuthStatus.mockResolvedValue({
      signed_in: true,
      in_flight: false,
      email: 'dev@example.com',
      mode: 'oauth',
    })
    emitAuthState({ state: 'success', email: 'dev@example.com' })
    await settleStatus()
    expect(container.textContent).not.toContain('Waiting for browser')
    expect(container.textContent).toContain('Signed in with ChatGPT')
  })

  it('surfaces the error transition and keeps the panel usable', async () => {
    renderSection('oauth')
    await settleStatus()

    spies.startChatGPTSignIn.mockResolvedValue({ auth_url: 'https://auth.example/abc' })
    await act(async () => {
      buttonByText('Sign in with ChatGPT').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await spies.startChatGPTSignIn.mock.results[spies.startChatGPTSignIn.mock.results.length - 1]!.value.catch(() => undefined)
    })

    emitAuthState({ state: 'error', error: 'the keychain refused the persist' })
    await settleStatus()
    expect(container.textContent).toContain('the keychain refused the persist')
    expect(container.textContent).not.toContain('Waiting for browser')
    // The Sign in button is back — a retry is one click away.
    expect(() => buttonByText('Sign in with ChatGPT')).not.toThrow()
  })

  it('keeps the refusal error when the RPC itself fails (a flow already runs)', async () => {
    renderSection('oauth')
    await settleStatus()

    spies.startChatGPTSignIn.mockRejectedValue(
      new Error('a ChatGPT sign-in is already in progress — cancel it before starting another'),
    )
    await act(async () => {
      buttonByText('Sign in with ChatGPT').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await spies.startChatGPTSignIn.mock.results[spies.startChatGPTSignIn.mock.results.length - 1]!.value.catch(() => undefined)
    })
    expect(container.textContent).toContain('already in progress')
    expect(container.textContent).not.toContain('Waiting for browser')
  })

  it('clears the busy state on the quiet cancelled transition', async () => {
    renderSection('oauth')
    await settleStatus()

    spies.startChatGPTSignIn.mockResolvedValue({ auth_url: 'https://auth.example/abc' })
    await act(async () => {
      buttonByText('Sign in with ChatGPT').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await spies.startChatGPTSignIn.mock.results[spies.startChatGPTSignIn.mock.results.length - 1]!.value.catch(() => undefined)
    })
    emitAuthState({ state: 'cancelled' })
    await settleStatus()
    expect(container.textContent).not.toContain('Waiting for browser')
  })

  it('cancel click calls CancelChatGPTSignIn while the flow is in flight', async () => {
    renderSection('oauth')
    await settleStatus()

    spies.startChatGPTSignIn.mockResolvedValue({ auth_url: 'https://auth.example/abc' })
    spies.cancelChatGPTSignIn.mockResolvedValue(undefined)
    await act(async () => {
      buttonByText('Sign in with ChatGPT').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await spies.startChatGPTSignIn.mock.results[spies.startChatGPTSignIn.mock.results.length - 1]!.value.catch(() => undefined)
    })
    await act(async () => {
      buttonByText('Cancel').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await spies.cancelChatGPTSignIn.mock.results[spies.cancelChatGPTSignIn.mock.results.length - 1]!.value.catch(() => undefined)
    })
    expect(spies.cancelChatGPTSignIn).toHaveBeenCalledTimes(1)
  })

  it('restores the busy posture from status.in_flight after a remount (a pending event was missed)', async () => {
    // Start the flow in one panel instance...
    spies.startChatGPTSignIn.mockResolvedValue({ auth_url: 'https://auth.example/abc' })
    renderSection('oauth')
    await settleStatus()
    await act(async () => {
      buttonByText('Sign in with ChatGPT').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await spies.startChatGPTSignIn.mock.results[spies.startChatGPTSignIn.mock.results.length - 1]!.value.catch(() => undefined)
    })
    expect(container.textContent).toContain('Waiting for browser')

    // ...unmount it (collapsed accordion / closed settings) BEFORE any
    // terminal event, then remount: the fresh mount never sees the pending
    // event, so only the authoritative status snapshot can restore the
    // Waiting/Cancel posture.
    act(() => {
      root.unmount()
    })
    spies.getChatGPTAuthStatus.mockResolvedValue({ signed_in: false, mode: 'oauth', in_flight: true })
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    renderSection('oauth')
    await settleStatus()

    expect(container.textContent).toContain('Waiting for browser')
    expect(container.textContent).toContain('Cancel')
    expect(container.textContent).not.toContain('Sign in with ChatGPT')

    // The terminal transition still clears it.
    spies.getChatGPTAuthStatus.mockResolvedValue({ signed_in: false, mode: 'oauth', in_flight: false })
    emitAuthState({ state: 'cancelled' })
    await settleStatus()
    expect(container.textContent).not.toContain('Waiting for browser')
  })

  it('ignores malformed chatgpt_auth:state payloads (guarded upstream)', async () => {
    renderSection('oauth')
    await settleStatus()
    // The api module's guard drops malformed payloads before they reach this
    // component; a handler is only ever invoked with a valid state enum.
    emitAuthState({ state: 'pending' })
    expect(container.textContent).toContain('Not signed in')
  })
})
