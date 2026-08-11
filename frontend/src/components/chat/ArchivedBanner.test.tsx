// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Enable React's act() flushing in this jsdom environment.
;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

// --- Mock the session API wrapper so the test never touches the Wails backend ---
const { archiveSessionMock } = vi.hoisted(() => ({
  archiveSessionMock: vi.fn(),
}))
vi.mock('@/api/sessions', () => ({
  archiveSession: archiveSessionMock,
}))
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn() } }))

// --- Mock the session store: ArchivedBanner only needs `updateSession` ---
const { updateSessionMock } = vi.hoisted(() => ({
  updateSessionMock: vi.fn(),
}))
vi.mock('@/stores/sessionStore', () => ({
  useSessionStore: (selector: (s: { updateSession: typeof updateSessionMock }) => unknown) =>
    selector({ updateSession: updateSessionMock }),
}))

import { ArchivedBanner } from './ArchivedBanner'

let root: Root | null = null

function render(el: React.ReactElement): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(el)
  })
  return container
}

describe('ArchivedBanner', () => {
  beforeEach(() => {
    archiveSessionMock.mockReset()
    updateSessionMock.mockReset()
    document.body.innerHTML = ''
    root = null
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
  })

  it('renders the archived message and a Restore button', () => {
    const container = render(<ArchivedBanner sessionId="s1" />)
    expect(container.textContent).toContain('archived')
    expect(container.querySelector('button')).not.toBeNull()
    expect(container.querySelector('button')!.textContent).toContain('Restore')
  })

  it('calls archiveSession (toggle) and updates the store on Restore click', async () => {
    archiveSessionMock.mockResolvedValueOnce(undefined)
    const container = render(<ArchivedBanner sessionId="s1" />)
    const btn = container.querySelector('button') as HTMLButtonElement

    await act(async () => {
      btn.click()
    })

    expect(archiveSessionMock).toHaveBeenCalledWith('s1')
    expect(updateSessionMock).toHaveBeenCalledWith('s1', { archived: false })
  })

  it('disables the Restore button while the request is in flight', async () => {
    let resolveArchive: () => void = () => {}
    archiveSessionMock.mockReturnValueOnce(new Promise<void>((resolve) => { resolveArchive = resolve }))
    const container = render(<ArchivedBanner sessionId="s1" />)
    const btn = container.querySelector('button') as HTMLButtonElement

    act(() => {
      btn.click()
    })

    // While the promise is pending the button is disabled.
    expect(btn.disabled).toBe(true)

    await act(async () => {
      resolveArchive()
      await Promise.resolve()
    })

    // After the request settles the button is re-enabled.
    expect(btn.disabled).toBe(false)
  })

  it('logs and recovers when archiveSession rejects', async () => {
    archiveSessionMock.mockRejectedValueOnce(new Error('boom'))
    const { logger } = await import('@/lib/logger')
    const container = render(<ArchivedBanner sessionId="s1" />)
    const btn = container.querySelector('button') as HTMLButtonElement

    await act(async () => {
      btn.click()
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(logger.error).toHaveBeenCalled()
    // The store must NOT be updated on failure.
    expect(updateSessionMock).not.toHaveBeenCalled()
  })
})
