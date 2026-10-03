// @vitest-environment jsdom
// Race regressions for useChatGPTAuthFlow: out-of-order status snapshots
// (the epoch guard) and the Cancel-failure recovery path for the waiting
// posture. The happy-path flow is covered through ChatGPTAuthSection.test.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ChatGPTAuthEventData } from '@/types/events'
import type { ChatGPTAuthStatusResponse } from '@/types/models'

const spies = vi.hoisted(() => ({
  getChatGPTAuthStatus: vi.fn(),
  startChatGPTSignIn: vi.fn(),
  cancelChatGPTSignIn: vi.fn(),
  signOutChatGPT: vi.fn(),
}))

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

import { useChatGPTAuthFlow } from '@/hooks/useChatGPTAuthFlow'

let container: HTMLDivElement
let root: Root
let harnessOutput: { busy: boolean; error: string | null }
let lastHandlers: {
  signIn: () => Promise<void>
  cancel: () => Promise<void>
} | null = null

function Harness(): React.ReactElement {
  const { signInBusy, authError, handleSignIn, handleCancel } = useChatGPTAuthFlow()
  harnessOutput = { busy: signInBusy, error: authError }
  lastHandlers = { signIn: handleSignIn, cancel: handleCancel }
  return <div data-testid="harness">{signInBusy ? 'waiting' : 'idle'}</div>
}

function renderHarness(): void {
  act(() => {
    root.render(<Harness />)
  })
}

function snapshot(
  overrides: Partial<ChatGPTAuthStatusResponse> = {},
): Promise<ChatGPTAuthStatusResponse> {
  return Promise.resolve({ signed_in: false, mode: 'oauth', in_flight: false, ...overrides })
}

/** A status read whose resolution the test controls (arrival-order races). */
function deferredSnapshot(): {
  promise: Promise<ChatGPTAuthStatusResponse>
  resolve: (snapshot: ChatGPTAuthStatusResponse) => void
} {
  let resolve!: (snapshot: ChatGPTAuthStatusResponse) => void
  const promise = new Promise<ChatGPTAuthStatusResponse>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

/** Await the specific status RPC triggered by mount/event/cancel. */
async function settleStatus(): Promise<void> {
  const result = spies.getChatGPTAuthStatus.mock.results[spies.getChatGPTAuthStatus.mock.results.length - 1]
  if (!result || result.type !== 'return') throw new Error('no status RPC to settle')
  await act(async () => { await result.value })
}

function emitAuthState(data: ChatGPTAuthEventData): void {
  if (!authStateHandler) throw new Error('no auth state handler subscribed')
  act(() => {
    authStateHandler!(data)
  })
}

function handlers(): { signIn: () => Promise<void>; cancel: () => Promise<void> } {
  if (!lastHandlers) throw new Error('harness not mounted')
  return lastHandlers
}

beforeEach(() => {
  vi.clearAllMocks()
  authStateHandler = null
  harnessOutput = { busy: false, error: null }
  lastHandlers = null
  spies.getChatGPTAuthStatus.mockImplementation(() => snapshot())
  spies.startChatGPTSignIn.mockResolvedValue({ auth_url: 'https://auth.example/abc' })
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

describe('useChatGPTAuthFlow snapshot ordering (epoch guard)', () => {
  it('drops an older in_flight=true read resolving after the terminal event — no wedge', async () => {
    // The mount read is slow (its server-side in_flight was captured while
    // a flow still ran)...
    const stale = deferredSnapshot()
    spies.getChatGPTAuthStatus.mockImplementationOnce(() => stale.promise)
    renderHarness()

    // ...the flow concludes while it is in flight: the terminal event
    // clears the busy flag and starts a NEWER read, which resolves first
    // with the post-conclusion state.
    spies.getChatGPTAuthStatus.mockResolvedValueOnce({ signed_in: true, mode: 'oauth', in_flight: false })
    emitAuthState({ state: 'success', email: 'dev@example.com' })
    await settleStatus()
    expect(harnessOutput.busy).toBe(false)

    // The STALE older read resolves LAST with in_flight=true: the epoch
    // guard must drop it — without the guard it would re-arm the waiting
    // posture with no flow left to cancel (Cancel cannot recover).
    await act(async () => {
      stale.resolve({ signed_in: false, mode: 'oauth', in_flight: true })
      await stale.promise
    })
    expect(harnessOutput.busy).toBe(false)
  })

  it('drops an older in_flight=false read resolving after a newer true read — no busy flicker-off', async () => {
    // The mount read is slow and captured the PRE-flow state
    // (in_flight=false) before a flow started...
    const stale = deferredSnapshot()
    spies.getChatGPTAuthStatus.mockImplementationOnce(() => stale.promise)
    renderHarness()

    // ...while it is still in flight, a terminal event (a stale cancelled
    // from an older flow) starts a NEWER read, which resolves FIRST with
    // in_flight=true — a flow IS running — and restores the waiting
    // posture from the authoritative snapshot.
    spies.getChatGPTAuthStatus.mockResolvedValueOnce({ signed_in: false, mode: 'oauth', in_flight: true })
    emitAuthState({ state: 'cancelled' })
    await settleStatus()
    expect(harnessOutput.busy).toBe(true)

    // The stale PRE-flow read (in_flight=false) resolves LAST: the epoch
    // guard must drop it — without the guard it would extinguish the
    // restored waiting posture while the flow still runs.
    await act(async () => {
      stale.resolve({ signed_in: false, mode: 'oauth', in_flight: false })
      await stale.promise
    })
    expect(harnessOutput.busy).toBe(true)
  })
})

describe('useChatGPTAuthFlow cancel recovery', () => {
  it('releases the waiting posture when the cancel RPC fails and nothing runs', async () => {
    renderHarness()
    await settleStatus()

    await act(async () => {
      await handlers().signIn()
    })
    expect(harnessOutput.busy).toBe(true)

    // The cancel RPC fails (nothing is actually running) — no cancelled
    // event will arrive; the hook must release the posture itself.
    spies.cancelChatGPTSignIn.mockRejectedValue(new Error('no sign-in in progress'))
    spies.getChatGPTAuthStatus.mockResolvedValueOnce({ signed_in: false, mode: 'oauth', in_flight: false })
    await act(async () => {
      await handlers().cancel()
    })
    await settleStatus()

    expect(harnessOutput.busy).toBe(false)
    expect(harnessOutput.error).toBe('no sign-in in progress')
  })

  it('re-arms the waiting posture after a failed cancel when a flow still runs', async () => {
    renderHarness()
    await settleStatus()

    await act(async () => {
      await handlers().signIn()
    })
    expect(harnessOutput.busy).toBe(true)

    // The cancel RPC fails, but the authoritative re-read shows the flow
    // is genuinely still running — the waiting posture comes back.
    spies.cancelChatGPTSignIn.mockRejectedValue(new Error('transient RPC failure'))
    spies.getChatGPTAuthStatus.mockResolvedValueOnce({ signed_in: false, mode: 'oauth', in_flight: true })
    await act(async () => {
      await handlers().cancel()
    })
    await settleStatus()

    expect(harnessOutput.busy).toBe(true)
    expect(harnessOutput.error).toBe('transient RPC failure')
  })
})
