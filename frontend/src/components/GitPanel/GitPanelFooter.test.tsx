// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// vi.mock factories are hoisted, so the mock objects must be created via
// vi.hoisted() to be accessible inside the factory.
const { gitMocks, loggerMock } = vi.hoisted(() => ({
  gitMocks: {
    pull: vi.fn(),
    push: vi.fn(),
    fetch: vi.fn(),
  },
  loggerMock: { error: vi.fn(), info: vi.fn(), warn: vi.fn(), debug: vi.fn() },
}))

vi.mock('@/api/git', () => gitMocks)
vi.mock('@/lib/logger', () => ({ logger: loggerMock }))

import { GitPanelFooter } from './GitPanelFooter'
import { useGitPanelStore, selectLastOperation } from '@/stores/gitPanelStore'
import type { GitOperationRecord } from '@/stores/gitPanelStore'
import { useProjectStore } from '@/stores/projectStore'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  vi.clearAllMocks()
  useGitPanelStore.getState().reset()
  useProjectStore.setState({ activeProjectId: 'proj-a' })
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

function render(): void {
  act(() => {
    root.render(<GitPanelFooter />)
  })
}

/** Flush pending microtasks inside act() so async state updates settle. */
async function flush(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 5; i += 1) await Promise.resolve()
  })
}

function logButton(): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>('button[aria-label="Git operation log"]')
  expect(el).not.toBeNull()
  return el!
}

function opButton(label: 'Pull' | 'Push' | 'Fetch'): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>(`button[title="${label}"]`)
  expect(el).not.toBeNull()
  return el!
}

function panel(): HTMLElement | null {
  // The log panel is portaled to document.body (out of this container), so a
  // container-scoped query would miss it.
  return document.querySelector<HTMLElement>('[role="dialog"]')
}

function storeRecord(): GitOperationRecord | undefined {
  return selectLastOperation(useGitPanelStore.getState(), 'proj-a')
}

/** Seed a per-project operation record directly (bypassing a real op). */
function seed(overrides: Partial<GitOperationRecord> = {}): void {
  act(() => {
    useGitPanelStore.getState().recordGitOperation('proj-a', {
      kind: 'fetch',
      label: 'Fetch',
      ok: true,
      output: 'seeded output',
      error: null,
      at: 1,
      acknowledged: false,
      ...overrides,
    })
  })
}

describe('GitPanelFooter — operation log button', () => {
  it('is neutral with no record and no panel', () => {
    render()
    expect(logButton().className).toContain('text-muted-foreground')
    expect(logButton().className).not.toContain('text-success')
    expect(panel()).toBeNull()
  })

  it('records a successful pull, tints green, and opening acknowledges + shows the output', async () => {
    gitMocks.pull.mockResolvedValue('Already up to date.')
    render()

    act(() => {
      opButton('Pull').click()
    })
    await flush()

    const rec = storeRecord()
    expect(rec).toMatchObject({ kind: 'pull', label: 'Pull', ok: true, output: 'Already up to date.' })
    expect(logButton().className).toContain('text-success')

    // Opening acknowledges the record (neutralises the tint) and mounts the
    // panel with the captured output.
    act(() => {
      logButton().click()
    })
    expect(storeRecord()?.acknowledged).toBe(true)
    expect(logButton().className).toContain('text-muted-foreground')
    expect(logButton().className).not.toContain('text-success')
    expect(panel()).not.toBeNull()
    expect(panel()!.textContent).toContain('Already up to date.')
  })

  it('records a failed push, tints red, and shows the failure message', async () => {
    gitMocks.push.mockRejectedValue(new Error('no upstream configured'))
    render()

    act(() => {
      opButton('Push').click()
    })
    await flush()

    const rec = storeRecord()
    expect(rec).toMatchObject({ kind: 'push', label: 'Push', ok: false, error: 'no upstream configured' })
    expect(logButton().className).toContain('text-destructive')

    act(() => {
      logButton().click()
    })
    expect(panel()!.textContent).toContain('no upstream configured')
    expect(panel()!.querySelector('.text-destructive')).not.toBeNull()
  })

  it('falls back to the empty remote-op output label when git prints nothing', async () => {
    gitMocks.fetch.mockResolvedValue('')
    render()

    act(() => {
      opButton('Fetch').click()
    })
    await flush()

    expect(storeRecord()).toMatchObject({ kind: 'fetch', ok: true, output: 'Fetch completed.' })
  })
})

describe('GitPanelFooter — log popover dismissal', () => {
  it('closes on Escape', () => {
    seed()
    render()
    act(() => {
      logButton().click()
    })
    expect(panel()).not.toBeNull()

    act(() => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    })
    expect(panel()).toBeNull()
  })

  it('closes on an outside click', () => {
    seed()
    render()
    act(() => {
      logButton().click()
    })
    expect(panel()).not.toBeNull()

    act(() => {
      document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    })
    expect(panel()).toBeNull()
  })

  it('stays open on a click inside the panel', () => {
    seed()
    render()
    act(() => {
      logButton().click()
    })
    const p = panel()
    expect(p).not.toBeNull()

    act(() => {
      p!.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    })
    expect(panel()).not.toBeNull()
  })

  it('closes again when the trigger is clicked while open', () => {
    seed()
    render()
    act(() => {
      logButton().click()
    })
    expect(panel()).not.toBeNull()

    act(() => {
      logButton().click()
    })
    expect(panel()).toBeNull()
  })
})

describe('GitPanelFooter — remote-op gate', () => {
  it('spins the log button (still clickable) and disables the op buttons while a remote op is in flight', () => {
    useGitPanelStore.setState({ remoteOperationInProgress: true })
    render()

    // The log stays readable mid-operation, so its button is not disabled…
    expect(logButton().disabled).toBe(false)
    expect(container.querySelector('.animate-spin')).not.toBeNull()
    expect(logButton().className).toContain('text-muted-foreground')
    // …while the remote op buttons share the busy gate.
    expect(opButton('Pull').disabled).toBe(true)
    expect(opButton('Push').disabled).toBe(true)
    expect(opButton('Fetch').disabled).toBe(true)
  })
})
