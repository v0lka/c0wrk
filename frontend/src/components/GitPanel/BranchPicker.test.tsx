// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// jsdom in this environment does not expose `window.localStorage`, which
// zustand's `persist` middleware captures at store-creation time (via
// createJSONStorage(() => window.localStorage)). Install an in-memory
// polyfill before any store module is imported so gitPanelStore works.
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  g.IS_REACT_ACT_ENVIRONMENT = true
  win.IS_REACT_ACT_ENVIRONMENT = true
  const map = new Map<string, string>()
  win.localStorage = {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => { map.set(k, v) },
    removeItem: (k: string) => { map.delete(k) },
    clear: () => map.clear(),
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    get length() { return map.size },
  }
})

// --- Mock the git API wrappers so tests never touch the Wails backend ---
// vi.mock factories are hoisted, so the mock objects must be created via
// vi.hoisted() to be accessible inside the factory.
const { gitMocks } = vi.hoisted(() => ({
  gitMocks: {
    getBranches: vi.fn(),
    checkoutBranch: vi.fn(),
    createBranch: vi.fn(),
    getBranchBases: vi.fn(),
  },
}))

vi.mock('@/api/git', () => gitMocks)

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn() },
}))

import { BranchPicker } from './BranchPicker'
import { useGitPanelStore } from '@/stores/gitPanelStore'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  // Reset call history without removing the mock function identities.
  gitMocks.getBranches.mockReset()
  gitMocks.checkoutBranch.mockReset()
  gitMocks.createBranch.mockReset()
  gitMocks.getBranchBases.mockReset()
  // Default: resolve to an empty branch list so the effect always has a
  // thenable. Individual tests override this as needed.
  gitMocks.getBranches.mockResolvedValue([])

  useGitPanelStore.getState().reset()
  useGitPanelStore.getState().openBranchPicker()
  useGitPanelStore.setState({
    branch: { name: 'main', upstream: '', ahead: 0, behind: 0 },
  })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  // Radix Dialog portals content into document.body — clear any leftovers.
  document.body.innerHTML = ''
})

/**
 * The Radix Dialog renders its content through a portal into document.body,
 * so assertions must target document.body rather than the mount container.
 */
function body(): HTMLElement {
  return document.body
}

/**
 * Set a controlled input's value in a way React detects. React tracks the
 * last value via a hidden descriptor, so directly assigning `.value` then
 * dispatching `input` does NOT trigger onChange in jsdom. We must use the
 * native HTMLInputElement value setter, then dispatch the event.
 */
function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype,
    'value',
  )!.set!
  setter.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

/** Flush microtasks so async effects (getBranches) resolve. */
function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

/** All buttons currently in the DOM (inside the portaled dialog). */
function allButtons(): HTMLButtonElement[] {
  return Array.from(body().querySelectorAll('button'))
}

function renderPicker() {
  act(() => {
    root.render(<BranchPicker />)
  })
}

describe('BranchPicker', () => {
  it('does not render anything when closed', () => {
    useGitPanelStore.getState().closeBranchPicker()
    renderPicker()
    expect(body().textContent).not.toContain('Switch Branch')
  })

  it('renders the title and loads branches when open', async () => {
    gitMocks.getBranches.mockResolvedValue([
      { name: 'main', is_current: true },
      { name: 'feature/x', is_current: false },
      { name: 'bugfix/123', is_current: false },
    ])
    renderPicker()

    await flush()

    expect(body().textContent).toContain('Switch Branch')
    expect(gitMocks.getBranches).toHaveBeenCalled()
    // All branches rendered.
    expect(body().textContent).toContain('main')
    expect(body().textContent).toContain('feature/x')
    expect(body().textContent).toContain('bugfix/123')
  })

  it('marks the current branch with a check and disables its button', async () => {
    gitMocks.getBranches.mockResolvedValue([
      { name: 'main', is_current: true },
      { name: 'dev', is_current: false },
    ])
    renderPicker()
    await flush()

    // Find the branch buttons (the ones containing the branch name text).
    const mainBtn = allButtons().find((b) => b.textContent?.includes('main'))!
    const devBtn = allButtons().find((b) => b.textContent?.includes('dev'))!

    expect(mainBtn.disabled).toBe(true)
    // The current branch shows a check icon (lucide renders an <svg>).
    expect(mainBtn.querySelector('svg')).not.toBeNull()
    expect(devBtn.disabled).toBe(false)
  })

  it('calls checkoutBranch and closes the picker when a branch is clicked', async () => {
    gitMocks.getBranches.mockResolvedValue([
      { name: 'main', is_current: true },
      { name: 'dev', is_current: false },
    ])
    gitMocks.checkoutBranch.mockResolvedValue(undefined)
    renderPicker()
    await flush()

    const devBtn = allButtons().find((b) => b.textContent?.includes('dev'))!
    await act(async () => {
      devBtn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(gitMocks.checkoutBranch).toHaveBeenCalledWith('dev')
    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(false)
  })

  it('shows an error when checkoutBranch fails and stays open', async () => {
    gitMocks.getBranches.mockResolvedValue([
      { name: 'main', is_current: true },
      { name: 'dev', is_current: false },
    ])
    gitMocks.checkoutBranch.mockRejectedValue(
      new Error('local changes would be overwritten'),
    )
    renderPicker()
    await flush()

    const devBtn = allButtons().find((b) => b.textContent?.includes('dev'))!
    await act(async () => {
      devBtn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(body().textContent).toContain('local changes would be overwritten')
    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(true)
  })

  it('filters branches by the filter input', async () => {
    gitMocks.getBranches.mockResolvedValue([
      { name: 'main', is_current: true },
      { name: 'feature/auth', is_current: false },
      { name: 'feature/api', is_current: false },
      { name: 'bugfix/123', is_current: false },
    ])
    renderPicker()
    await flush()

    const filterInput = Array.from(body().querySelectorAll('input')).find(
      (i) => i.getAttribute('placeholder') === 'Filter branches...',
    )!
    await act(async () => {
      setInputValue(filterInput, 'feature')
    })

    const text = body().textContent ?? ''
    expect(text).toContain('feature/auth')
    expect(text).toContain('feature/api')
    expect(text).not.toContain('main')
    expect(text).not.toContain('bugfix/123')
  })

  it('creates a new branch via the New button', async () => {
    gitMocks.getBranches.mockResolvedValue([
      { name: 'main', is_current: true },
    ])
    gitMocks.createBranch.mockResolvedValue(undefined)
    renderPicker()
    await flush()

    const newBranchInput = Array.from(body().querySelectorAll('input')).find(
      (i) => i.getAttribute('placeholder') === 'branch-name',
    )!
    const newBtn = allButtons().find((b) => b.textContent?.includes('New'))!

    await act(async () => {
      setInputValue(newBranchInput, 'feature/new')
    })
    await act(async () => {
      newBtn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(gitMocks.createBranch).toHaveBeenCalledWith('feature/new', '')
    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(false)
  })

  it('creates a new branch from a selected base', async () => {
    gitMocks.getBranches.mockResolvedValue([
      { name: 'main', is_current: true },
    ])
    gitMocks.getBranchBases.mockResolvedValue([
      { ref: 'develop', label: 'develop', type: 'local', detail: '' },
      { ref: 'origin/main', label: 'origin/main', type: 'remote', detail: '' },
    ])
    gitMocks.createBranch.mockResolvedValue(undefined)
    renderPicker()
    await flush()

    // Type the new branch name.
    const newBranchInput = Array.from(body().querySelectorAll('input')).find(
      (i) => i.getAttribute('placeholder') === 'branch-name',
    )!
    await act(async () => {
      setInputValue(newBranchInput, 'feature/from-dev')
    })

    // Expand the "Choose base" collapsible.
    const baseTrigger = allButtons().find((b) =>
      b.textContent?.includes('Choose base'),
    )!
    await act(async () => {
      baseTrigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()
    })

    // Select the 'develop' base.
    const devBaseBtn = allButtons().find((b) =>
      b.textContent?.includes('develop'),
    )!
    await act(async () => {
      devBaseBtn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
    })

    // Click New.
    const newBtn = allButtons().find((b) => b.textContent?.includes('New'))!
    await act(async () => {
      newBtn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(gitMocks.createBranch).toHaveBeenCalledWith('feature/from-dev', 'develop')
  })

  it('creates a new branch via Enter key in the new-branch input', async () => {
    gitMocks.getBranches.mockResolvedValue([
      { name: 'main', is_current: true },
    ])
    gitMocks.createBranch.mockResolvedValue(undefined)
    renderPicker()
    await flush()

    const newBranchInput = Array.from(body().querySelectorAll('input')).find(
      (i) => i.getAttribute('placeholder') === 'branch-name',
    )!

    await act(async () => {
      setInputValue(newBranchInput, 'release/v2')
    })
    await act(async () => {
      newBranchInput.dispatchEvent(
        new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
      )
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(gitMocks.createBranch).toHaveBeenCalledWith('release/v2', '')
  })

  it('shows an error when createBranch fails', async () => {
    gitMocks.getBranches.mockResolvedValue([
      { name: 'main', is_current: true },
    ])
    gitMocks.createBranch.mockRejectedValue(
      new Error('branch "dup" already exists'),
    )
    renderPicker()
    await flush()

    const newBranchInput = Array.from(body().querySelectorAll('input')).find(
      (i) => i.getAttribute('placeholder') === 'branch-name',
    )!
    const newBtn = allButtons().find((b) => b.textContent?.includes('New'))!

    await act(async () => {
      setInputValue(newBranchInput, 'dup')
    })
    await act(async () => {
      newBtn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(body().textContent).toContain('already exists')
    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(true)
  })

  it('shows a loading state and no branch list while branches load', () => {
    // Never-resolving promise keeps loadingBranches=true.
    gitMocks.getBranches.mockReturnValue(new Promise(() => {}))
    renderPicker()

    expect(gitMocks.getBranches).toHaveBeenCalled()
    // No branch names rendered yet.
    expect(body().textContent).not.toContain('main')
  })

  it('captures a pending branch base when the picker transitions closed→open', async () => {
    // Close the picker first (beforeEach opens it) and mount — mirroring the
    // real initial state where BranchPicker is always mounted but closed.
    useGitPanelStore.getState().closeBranchPicker()
    renderPicker()
    expect(body().textContent).not.toContain('Switch Branch')

    // Simulate the commit context menu's "Create › Branch" handler: set the
    // base ref and open the picker synchronously in the same tick.
    gitMocks.getBranchBases.mockResolvedValue([
      { ref: 'develop', label: 'develop', type: 'local', detail: '' },
    ])
    act(() => {
      useGitPanelStore.getState().setPendingBranchBase('abc1234deadbeef')
      useGitPanelStore.getState().openBranchPicker()
    })
    await flush()

    // The "Choose base" collapsible should auto-expand and the preselected
    // base should be visible — proving NewBranchSection captured it despite
    // Radix Dialog's deferred Presence mount.
    expect(body().textContent).toContain('Base:')
    expect(body().textContent).toContain('abc1234deadbeef')
  })
})
