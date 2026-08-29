// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { ImageErrorBanner } from './ImageErrorBanner'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useSessionStore } from '@/stores/sessionStore'
import { NULL_SESSION_KEY } from '@/stores/chatInputStore'

// Enable React's act() flushing in this jsdom environment.

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

function resetStore() {
  useAttachmentsStore.setState({ attachmentsBySession: {}, uploadsBySession: {}, namesById: {}, imageErrorBySession: {} })
  useSessionStore.setState({ activeSessionId: 's1' })
}

describe('ImageErrorBanner', () => {
  beforeEach(() => {
    resetStore()
    document.body.innerHTML = ''
    root = null
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
  })

  it('renders nothing when the active session has no error', () => {
    const container = render(<ImageErrorBanner />)
    expect(container.innerHTML).toBe('')
  })

  it('renders the error message set for the active session', () => {
    useAttachmentsStore.getState().setImageError('s1', 'Model gpt-4 does not support images.')
    const container = render(<ImageErrorBanner />)
    expect(container.textContent).toContain('Model gpt-4 does not support images.')
    expect(container.querySelector('button[aria-label="Dismiss image error"]')).toBeTruthy()
  })

  it('renders nothing when only ANOTHER session has an error', () => {
    useAttachmentsStore.getState().setImageError('s2', 'Model gpt-4 does not support images.')
    const container = render(<ImageErrorBanner />)
    expect(container.innerHTML).toBe('')
  })

  it('follows the active session: switching hides the other session error', () => {
    useAttachmentsStore.getState().setImageError('s1', 'no vision on s1 model')
    const container = render(<ImageErrorBanner />)
    expect(container.textContent).toContain('no vision on s1 model')

    act(() => {
      useSessionStore.setState({ activeSessionId: 's2' })
    })
    expect(container.innerHTML).toBe('')
  })

  it('renders the NULL-sentinel error when no session is active', () => {
    // An image-only picker rejection before any session exists is keyed under
    // the NULL_SESSION_KEY sentinel — it must still surface in the input.
    useSessionStore.setState({ activeSessionId: null })
    useAttachmentsStore.getState().setImageError(NULL_SESSION_KEY, 'no session, no vision')
    const container = render(<ImageErrorBanner />)
    expect(container.textContent).toContain('no session, no vision')
  })

  it('dismiss button clears the active session error', () => {
    useAttachmentsStore.getState().setImageError('s1', 'Some error')
    useAttachmentsStore.getState().setImageError('s2', 'Other session error')
    const container = render(<ImageErrorBanner />)
    const btn = container.querySelector('button[aria-label="Dismiss image error"]') as HTMLButtonElement
    act(() => {
      btn.click()
    })
    // Only the active session's error is cleared.
    expect('s1' in useAttachmentsStore.getState().imageErrorBySession).toBe(false)
    expect(useAttachmentsStore.getState().imageErrorBySession['s2']).toBe('Other session error')
  })
})
