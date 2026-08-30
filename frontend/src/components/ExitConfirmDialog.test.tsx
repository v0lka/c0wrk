// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the RPC boundary so tests never touch the Wails backend ---

const { runtimeMocks } = vi.hoisted(() => ({
  runtimeMocks: {
    confirmExit: vi.fn(),
  },
}))

vi.mock('@/api/runtime', () => ({
  confirmExit: runtimeMocks.confirmExit,
}))

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn() },
}))

import { ExitConfirmDialog } from './ExitConfirmDialog'
import { useExitGuardStore } from '@/stores/exitGuardStore'
import { logger } from '@/lib/logger'

// Radix dialog primitives observe layout via ResizeObserver, which jsdom
// does not provide.
vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

let root: Root
let container: HTMLDivElement

const sessions = [
  { id: 'sess-1', name: 'Refactor', compacting: false },
  { id: 'sess-2', name: 'Docs pass', compacting: true },
]

function renderDialog() {
  act(() => {
    root.render(<ExitConfirmDialog />)
  })
}

function bodyText(): string {
  return document.body.textContent ?? ''
}

function clickButton(label: string) {
  const buttons = [...document.querySelectorAll('button')]
  const btn = buttons.find((b) => b.textContent === label)
  if (!btn) throw new Error(`button "${label}" not found; got: ${buttons.map((b) => b.textContent).join(', ')}`)
  act(() => {
    btn.click()
  })
}

beforeEach(() => {
  runtimeMocks.confirmExit.mockReset()
  runtimeMocks.confirmExit.mockResolvedValue(undefined)
  vi.mocked(logger.error).mockClear()
  useExitGuardStore.getState().clear()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

describe('ExitConfirmDialog — plain quit', () => {
  it('renders the session list and quit wording', () => {
    renderDialog()
    act(() => {
      useExitGuardStore.getState().present(sessions, false)
    })

    expect(bodyText()).toContain('Quit c0wrk?')
    expect(bodyText()).toContain('2 sessions are still working')
    expect(bodyText()).toContain('Refactor')
    expect(bodyText()).toContain('running task')
    expect(bodyText()).toContain('compacting context')
    expect(bodyText()).toContain('Quit anyway')
  })

  it('renders the generic list-less variant for an untrusted payload', () => {
    renderDialog()
    act(() => {
      useExitGuardStore.getState().presentUnknown()
    })

    expect(bodyText()).toContain('Quit c0wrk?')
    expect(bodyText()).toContain('Some sessions are still working')
    expect(document.querySelector('ul')).toBeNull()
    expect(bodyText()).toContain('Quit anyway')
  })

  it('renders nothing while closed', () => {
    renderDialog()

    expect(document.querySelector('[role="dialog"]')).toBeNull()
  })
})

describe('ExitConfirmDialog — update context', () => {
  it('presents restart wording when the quit belongs to a pending update', () => {
    renderDialog()
    act(() => {
      useExitGuardStore.getState().present(sessions, true)
    })

    expect(bodyText()).toContain('Restart & Update?')
    expect(bodyText()).toContain('staged update will be installed')
    expect(bodyText()).toContain('Restart & Update')
    expect(bodyText()).not.toContain('Quit anyway')
  })
})

describe('ExitConfirmDialog — decisions', () => {
  it('cancel clears the modal state', () => {
    renderDialog()
    act(() => {
      useExitGuardStore.getState().present(sessions, false)
    })

    clickButton('Cancel')

    expect(useExitGuardStore.getState().open).toBe(false)
  })

  it('confirm calls the ConfirmExit RPC and keeps the modal open on success', async () => {
    renderDialog()
    act(() => {
      useExitGuardStore.getState().present(sessions, false)
    })

    await act(async () => {
      clickButton('Quit anyway')
    })

    expect(runtimeMocks.confirmExit).toHaveBeenCalledTimes(1)
    // On success the app quits — the modal deliberately stays open so the
    // window never flashes empty during teardown.
    expect(useExitGuardStore.getState().open).toBe(true)
  })

  it('confirm surfaces RPC failures inline and keeps the modal answerable', async () => {
    runtimeMocks.confirmExit.mockRejectedValue(new Error('context gone'))
    renderDialog()
    act(() => {
      useExitGuardStore.getState().present(sessions, false)
    })

    await expect(act(async () => {
      clickButton('Quit anyway')
    })).resolves.toBeUndefined()

    expect(runtimeMocks.confirmExit).toHaveBeenCalledTimes(1)
    expect(logger.error).toHaveBeenCalled()
    // The failure is visible in the modal (not only the invisible log), so
    // the quit dialog is never a silent dead end.
    const alertEl = document.querySelector('[role="alert"]')
    expect(alertEl?.textContent).toContain('context gone')
    expect(useExitGuardStore.getState().open).toBe(true)
  })

  it('a double-click fires exactly one ConfirmExit RPC', async () => {
    let release: (() => void) | undefined
    runtimeMocks.confirmExit.mockImplementation(
      () => new Promise<void>((resolve) => { release = resolve }),
    )
    renderDialog()
    act(() => {
      useExitGuardStore.getState().present(sessions, false)
    })

    await act(async () => {
      clickButton('Quit anyway')
      clickButton('Quit anyway')
    })

    expect(runtimeMocks.confirmExit).toHaveBeenCalledTimes(1)
    release?.()
  })
})
