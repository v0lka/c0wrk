// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { GoalToggle } from './GoalToggle'
import { GOAL_BLOCKED_BY_MODEL_PROFILES_REASON } from '@/lib/goalGate'
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

  it('is disabled with the ModelProfiles reason when blocked by the Model Profiles gate', () => {
    act(() => {
      root.render(<GoalToggle blocked />)
    })
    const btn = trigger()
    expect(btn.disabled).toBe(true)
    expect(btn.getAttribute('title')).toBe(GOAL_BLOCKED_BY_MODEL_PROFILES_REASON)

    // Clicking a blocked trigger must not arm goal mode.
    act(() => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useInputModeStore.getState().goalEnabled).toBe(false)
  })

  it('prefers the ModelProfiles reason over the session-lock title when both apply', () => {
    act(() => {
      root.render(<GoalToggle disabled blocked />)
    })
    const btn = trigger()
    expect(btn.disabled).toBe(true)
    expect(btn.getAttribute('title')).toBe(GOAL_BLOCKED_BY_MODEL_PROFILES_REASON)
  })

  it('surfaces the task-state lockReason in the title while disabled', () => {
    act(() => {
      root.render(
        <GoalToggle disabled lockReason="Locked while the task is paused — resume or cancel it first" />,
      )
    })
    const btn = trigger()
    expect(btn.disabled).toBe(true)
    expect(btn.getAttribute('title')).toBe('Locked while the task is paused — resume or cancel it first')

    // Clicking a locked trigger must not arm goal mode.
    act(() => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useInputModeStore.getState().goalEnabled).toBe(false)
  })

  it('falls back to the session-running title when lockReason is empty', () => {
    act(() => {
      root.render(<GoalToggle disabled lockReason="" />)
    })
    expect(trigger().getAttribute('title')).toBe('Locked while the session is running')
  })
})
