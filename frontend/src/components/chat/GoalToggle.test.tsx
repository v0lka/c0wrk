// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { GoalToggle } from './GoalToggle'
import { useInputModeStore } from '@/stores/inputModeStore'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  useInputModeStore.setState({ goalEnabled: false })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<GoalToggle />)
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
  const btn = container.querySelector('button[aria-label="Toggle goal mode"]')
  expect(btn).not.toBeNull()
  return btn as HTMLButtonElement
}

describe('GoalToggle', () => {
  it('toggles goal mode on click when enabled', () => {
    expect(trigger().getAttribute('aria-pressed')).toBe('false')
    act(() => {
      trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useInputModeStore.getState().goalEnabled).toBe(true)
    expect(trigger().getAttribute('aria-pressed')).toBe('true')
  })

  it('is disabled while the session is running (session-pinning lock)', () => {
    act(() => {
      root.render(<GoalToggle disabled />)
    })
    const btn = trigger()
    expect(btn.disabled).toBe(true)
    expect(btn.getAttribute('title')).toBe('Locked while the session is running')

    // Clicking a disabled trigger must not arm goal mode.
    act(() => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useInputModeStore.getState().goalEnabled).toBe(false)
  })
})
