// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Spies created via vi.hoisted so they exist before vi.mock factories run.
// Only the functions the component actually calls are mocked; the rest of
// each module surface stays absent per the project's partial-mock
// convention (see ModelConfigDialog.test).
const mocks = vi.hoisted(() => ({
  checkNotificationAuthorization: vi.fn(),
  initSystemNotifications: vi.fn(),
  showTestNotification: vi.fn(),
  isWailsReady: vi.fn(),
  send: vi.fn(),
}))

vi.mock('@/api/notifications', async (importOriginal) => {
  const original =
    await importOriginal<typeof import('@/api/notifications')>()
  return {
    ...original,
    checkNotificationAuthorization: mocks.checkNotificationAuthorization,
    initSystemNotifications: mocks.initSystemNotifications,
    showTestNotification: mocks.showTestNotification,
  }
})

// lib/systemNotifications is mocked wholesale: the component must not
// depend on its store gate or transport internals for the preview send —
// only on the exported sendSystemNotification signature.
vi.mock('@/lib/systemNotifications', () => ({
  sendSystemNotification: mocks.send,
}))

vi.mock('@/api/runtime', () => ({
  isWailsReady: mocks.isWailsReady,
}))

import { SystemNotificationSettings } from './SystemNotificationSettings'
import { useSystemNotificationStore } from '@/stores/systemNotificationStore'

let container: HTMLDivElement
let root: Root

function render(): void {
  act(() => {
    root.render(<SystemNotificationSettings />)
  })
}

/** Click the master toggle's hidden checkbox (the Toggle primitive's input). */
function toggleInput(): HTMLInputElement {
  const input = container.querySelector<HTMLInputElement>('input[type="checkbox"]')
  if (!input) throw new Error('toggle input not found')
  return input
}

function testButton(): HTMLButtonElement | null {
  return container.querySelector<HTMLButtonElement>('button')
}

function hintText(): string | null {
  const el = container.querySelector('[data-testid="notification-permission-hint"]')
  return el?.textContent ?? null
}

async function flush(): Promise<void> {
  await act(async () => {})
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.isWailsReady.mockReturnValue(true)
  mocks.checkNotificationAuthorization.mockResolvedValue(true)
  mocks.initSystemNotifications.mockResolvedValue(undefined)
  mocks.showTestNotification.mockResolvedValue(undefined)
  mocks.send.mockResolvedValue(undefined)
  useSystemNotificationStore.setState({ enabled: true })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  useSystemNotificationStore.setState({ enabled: true })
})

describe('SystemNotificationSettings', () => {
  it('renders the toggle reflecting the store state', async () => {
    useSystemNotificationStore.setState({ enabled: false })
    render()
    await flush()
    expect(toggleInput().checked).toBe(false)
    expect(container.textContent).toContain('System Notifications')
    expect(container.textContent).toContain('Disabled')
  })

  it('probes authorization once on mount and renders no hint when granted', async () => {
    render()
    expect(mocks.checkNotificationAuthorization).toHaveBeenCalledTimes(1)
    await flush()
    expect(hintText()).toBeNull()
    expect(testButton()).not.toBeNull()
  })

  it('skips the probe entirely when the Wails runtime is absent', async () => {
    mocks.isWailsReady.mockReturnValue(false)
    render()
    await flush()
    expect(mocks.checkNotificationAuthorization).not.toHaveBeenCalled()
    expect(hintText()).toBeNull()
  })

  it('renders the muted macOS hint instead of the test button when denied', async () => {
    mocks.checkNotificationAuthorization.mockResolvedValue(false)
    render()
    await flush()
    expect(hintText()).toBe(
      'Notifications are disabled for c0wrk in macOS Settings — enable them to receive alerts',
    )
    expect(testButton()).toBeNull()
  })

  it('turning on initializes the bridge and previews a banner', async () => {
    useSystemNotificationStore.setState({ enabled: false })
    render()
    await flush()

    await act(async () => {
      toggleInput().click()
    })

    expect(useSystemNotificationStore.getState().enabled).toBe(true)
    expect(mocks.initSystemNotifications).toHaveBeenCalledTimes(1)
    expect(mocks.send).toHaveBeenCalledTimes(1)
    const [content] = mocks.send.mock.calls[0] as [
      { title: string; body: string; kind: string },
    ]
    expect(content.title).toBe('c0wrk — System notifications enabled')
    expect(content.body).toBe(
      'You will see notifications like this when tasks finish or need your input.',
    )
  })

  it('turning off sends nothing and tears nothing down', async () => {
    render()
    await flush()

    await act(async () => {
      toggleInput().click()
    })

    expect(useSystemNotificationStore.getState().enabled).toBe(false)
    expect(mocks.initSystemNotifications).not.toHaveBeenCalled()
    expect(mocks.send).not.toHaveBeenCalled()
  })

  it('suppresses the enable preview while authorization is denied', async () => {
    mocks.checkNotificationAuthorization.mockResolvedValue(false)
    useSystemNotificationStore.setState({ enabled: false })
    render()
    await flush()

    await act(async () => {
      toggleInput().click()
    })

    // Init still runs (the bridge is real even with banners hidden); only
    // the preview banner is skipped — sending into a denied channel would
    // confuse the "did it work?" signal.
    expect(mocks.initSystemNotifications).toHaveBeenCalledTimes(1)
    expect(mocks.send).not.toHaveBeenCalled()
  })

  it('the test button sends the Go-authored banner', async () => {
    render()
    await flush()

    await act(async () => {
      testButton()?.click()
    })

    expect(mocks.showTestNotification).toHaveBeenCalledTimes(1)
    expect(mocks.send).not.toHaveBeenCalled()
  })

  it('hides the test button while the toggle is off', async () => {
    useSystemNotificationStore.setState({ enabled: false })
    render()
    await flush()
    expect(testButton()).toBeNull()
  })
})
