// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// The experimental gate is a module-level hook dependency of E2SToggle —
// mock it so each test can flip the gate without touching the config fetch
// machinery inside the real hook.
let experimentalOn = true
vi.mock('@/hooks/useExperimentalFeatures', () => ({
  useExperimentalFeatures: () => experimentalOn,
}))

import { E2SToggle } from './E2SToggle'
import { useInputModeStore } from '@/stores/inputModeStore'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  experimentalOn = true
  useInputModeStore.setState({ goalEnabled: false, goalBudget: '', e2sEnabled: false })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<E2SToggle />)
  })
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

function trigger(): HTMLButtonElement {
  const btn = container.querySelector('button[aria-label="Toggle E2S mode"]')
  expect(btn).not.toBeNull()
  return btn as HTMLButtonElement
}

describe('E2SToggle', () => {
  it('toggles E2S mode on click when enabled', () => {
    expect(trigger().getAttribute('aria-pressed')).toBe('false')
    act(() => {
      trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useInputModeStore.getState().e2sEnabled).toBe(true)
    expect(trigger().getAttribute('aria-pressed')).toBe('true')
  })

  it('enabling E2S disables goal mode (mutual exclusion in the UI)', () => {
    useInputModeStore.setState({ goalEnabled: true })
    act(() => {
      trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useInputModeStore.getState().e2sEnabled).toBe(true)
    expect(useInputModeStore.getState().goalEnabled).toBe(false)
  })

  it('is hidden when the experimental gate is off', () => {
    const withGateOff = document.createElement('div')
    document.body.appendChild(withGateOff)
    const offRoot = createRoot(withGateOff)
    try {
      experimentalOn = false
      act(() => {
        offRoot.render(<E2SToggle />)
      })
      expect(withGateOff.querySelector('button[aria-label="Toggle E2S mode"]')).toBeNull()
    } finally {
      act(() => {
        offRoot.unmount()
      })
      withGateOff.remove()
    }
  })

  it('is visible when the experimental gate is on', () => {
    expect(trigger()).toBeDefined()
  })

  it('is disabled while the session is running (session-pinning lock)', () => {
    act(() => {
      root.render(<E2SToggle disabled />)
    })
    const btn = trigger()
    expect(btn.disabled).toBe(true)
    expect(btn.getAttribute('title')).toBe('Locked while the session is running')

    // Clicking a disabled trigger must not arm E2S mode.
    act(() => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useInputModeStore.getState().e2sEnabled).toBe(false)
  })

  it('surfaces the task-state lockReason in the title while disabled', () => {
    act(() => {
      root.render(<E2SToggle disabled lockReason="Locked while a failed task awaits resume or cancel" />)
    })
    const btn = trigger()
    expect(btn.disabled).toBe(true)
    expect(btn.getAttribute('title')).toBe('Locked while a failed task awaits resume or cancel')

    // Clicking a locked trigger must not arm E2S mode.
    act(() => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useInputModeStore.getState().e2sEnabled).toBe(false)
  })
})
