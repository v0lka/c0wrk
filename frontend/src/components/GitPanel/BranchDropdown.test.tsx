// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { TooltipProvider } from '@/components/ui/tooltip'

// --- Mock the git API wrappers so tests never touch the Wails backend ---
const { gitMocks } = vi.hoisted(() => ({
  gitMocks: {
    checkoutBranch: vi.fn(),
    renameBranch: vi.fn(),
    deleteBranch: vi.fn(),
    merge: vi.fn(),
    rebase: vi.fn(),
    pushBranch: vi.fn(),
    checkoutRemoteBranch: vi.fn(),
    deleteRemoteBranch: vi.fn(),
  },
}))

vi.mock('@/api/git', () => gitMocks)
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn() } }))

import { BranchDropdown } from './BranchDropdown'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import type { Branch } from '@/types/models'

let container: HTMLDivElement
let root: Root

function makeBranch(overrides: Partial<Branch> = {}): Branch {
  return { name: 'feature/x', is_current: false, kind: 'local', upstream: '', ...overrides }
}

beforeEach(() => {
  Object.values(gitMocks).forEach((m) => m.mockReset())
  gitMocks.checkoutBranch.mockResolvedValue(undefined)
  gitMocks.renameBranch.mockResolvedValue(undefined)
  gitMocks.deleteBranch.mockResolvedValue(undefined)
  gitMocks.merge.mockResolvedValue(undefined)
  gitMocks.rebase.mockResolvedValue(undefined)
  gitMocks.pushBranch.mockResolvedValue('pushed')
  gitMocks.checkoutRemoteBranch.mockResolvedValue(undefined)
  gitMocks.deleteRemoteBranch.mockResolvedValue('deleted')

  useGitPanelStore.getState().reset()
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
  document.body.innerHTML = ''
})

function body(): HTMLElement {
  return document.body
}

/** The trigger button lives in the mount container (before open it is the only button). */
function trigger(): HTMLButtonElement {
  return container.querySelector('button') as HTMLButtonElement
}

function renderDropdown() {
  act(() => {
    root.render(
      <TooltipProvider>
        <BranchDropdown />
      </TooltipProvider>,
    )
  })
}

/** Flush microtasks so portaled content (dropdown / dialog) mounts and async actions settle. */
function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

async function openDropdown() {
  act(() => {
    trigger().dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
  })
  await flush()
}

function isMenuOpen(): boolean {
  return body().querySelector('[data-slot="dropdown-menu-content"]') !== null
}

/** A branch row is a div[role=button] containing the given name. */
function rowByName(name: string): HTMLElement {
  return Array.from(body().querySelectorAll('div[role="button"]')).find((el) =>
    (el.textContent ?? '').includes(name),
  ) as HTMLElement
}

/** The action buttons inside a row (hover mini-icons). */
function rowButtons(row: HTMLElement): HTMLButtonElement[] {
  return Array.from(row.querySelectorAll('button'))
}

function menuItem(text: string): HTMLElement {
  return Array.from(body().querySelectorAll('[data-slot="dropdown-menu-item"]')).find((el) =>
    (el.textContent ?? '').includes(text),
  ) as HTMLElement
}

function dialogButton(text: string): HTMLButtonElement {
  return Array.from(body().querySelectorAll('button')).find(
    (b) => (b.textContent ?? '').trim() === text,
  ) as HTMLButtonElement
}

/** Set a controlled input's value in a way React detects (native setter + event). */
function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  setter.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('BranchDropdown', () => {
  it('renders the current branch name in the trigger', () => {
    renderDropdown()
    expect(container.textContent).toContain('main')
  })

  it('opens the dropdown on pointerdown and lists local + remote groups', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feature/x' }),
      makeBranch({ name: 'origin/feature/x', kind: 'remote' }),
    ])
    renderDropdown()
    expect(isMenuOpen()).toBe(false)

    await openDropdown()

    expect(isMenuOpen()).toBe(true)
    expect(body().textContent).toContain('Local')
    expect(body().textContent).toContain('Remote')
    expect(body().textContent).toContain('feature/x')
    expect(body().textContent).toContain('origin/feature/x')
  })

  it('hides the search input when there are fewer than 5 branches', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'a' }),
      makeBranch({ name: 'b' }),
      makeBranch({ name: 'c' }),
    ])
    renderDropdown()
    await openDropdown()
    expect(body().querySelector('input[placeholder="Search branches..."]')).toBeNull()
  })

  it('shows the search input when there are 5+ branches', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'a' }),
      makeBranch({ name: 'b' }),
      makeBranch({ name: 'c' }),
      makeBranch({ name: 'd' }),
    ])
    renderDropdown()
    await openDropdown()
    expect(body().querySelector('input[placeholder="Search branches..."]')).not.toBeNull()
  })

  it('filters branches by the search input', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feature/auth' }),
      makeBranch({ name: 'feature/api' }),
      makeBranch({ name: 'bugfix/1' }),
      makeBranch({ name: 'bugfix/2' }),
    ])
    renderDropdown()
    await openDropdown()

    const input = body().querySelector('input[placeholder="Search branches..."]') as HTMLInputElement
    act(() => setInputValue(input, 'feature'))

    expect(body().textContent).toContain('feature/auth')
    expect(body().textContent).toContain('feature/api')
    expect(body().textContent).not.toContain('bugfix/1')
  })

  it('checks out a local branch on click and closes the dropdown', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feature/x' }),
    ])
    renderDropdown()
    await openDropdown()

    act(() => {
      rowByName('feature/x').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.checkoutBranch).toHaveBeenCalledWith('feature/x')
    expect(isMenuOpen()).toBe(false)
  })

  it('does not checkout the current branch', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
    ])
    renderDropdown()
    await openDropdown()

    act(() => {
      rowByName('main').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(gitMocks.checkoutBranch).not.toHaveBeenCalled()
  })

  it('wires the push action', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feature/x' }),
    ])
    renderDropdown()
    await openDropdown()

    const row = rowByName('feature/x')
    act(() => {
      rowButtons(row)[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.pushBranch).toHaveBeenCalledWith('feature/x')
    expect(gitMocks.checkoutBranch).not.toHaveBeenCalled()
  })

  it('wires the merge action', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feature/x' }),
    ])
    renderDropdown()
    await openDropdown()

    const row = rowByName('feature/x')
    act(() => {
      rowButtons(row)[1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.merge).toHaveBeenCalledWith('feature/x')
  })

  it('wires the rebase action', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feature/x' }),
    ])
    renderDropdown()
    await openDropdown()

    const row = rowByName('feature/x')
    act(() => {
      rowButtons(row)[2]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.rebase).toHaveBeenCalledWith('feature/x')
  })

  it('switches to an inline rename input', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feature/x' }),
    ])
    renderDropdown()
    await openDropdown()

    const row = rowByName('feature/x')
    act(() => {
      rowButtons(row)[3]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(isMenuOpen()).toBe(false)
    const input = container.querySelector('input') as HTMLInputElement
    expect(input).not.toBeNull()
    expect(input.value).toBe('feature/x')
  })

  it('opens the delete confirm dialog for a local branch', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feature/x' }),
    ])
    renderDropdown()
    await openDropdown()

    const row = rowByName('feature/x')
    act(() => {
      rowButtons(row)[4]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(body().textContent).toContain('Delete branch?')

    act(() => {
      dialogButton('Delete').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.deleteBranch).toHaveBeenCalledWith('feature/x', false)
  })

  it('checks out a remote branch as local on click', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'origin/feature/x', kind: 'remote' }),
    ])
    renderDropdown()
    await openDropdown()

    act(() => {
      rowByName('origin/feature/x').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.checkoutRemoteBranch).toHaveBeenCalledWith('origin/feature/x')
    expect(isMenuOpen()).toBe(false)
  })

  it('opens the delete confirm dialog for a remote branch', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'origin/feature/x', kind: 'remote' }),
    ])
    renderDropdown()
    await openDropdown()

    const row = rowByName('origin/feature/x')
    act(() => {
      rowButtons(row)[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(body().textContent).toContain('Delete remote branch?')

    act(() => {
      dialogButton('Delete').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.deleteRemoteBranch).toHaveBeenCalledWith('feature/x', 'origin')
  })

  it('New branch... opens the branch picker', async () => {
    renderDropdown()
    await openDropdown()

    act(() => {
      menuItem('New branch...').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(true)
    expect(isMenuOpen()).toBe(false)
  })

  it('Manage branches... opens the branch picker', async () => {
    renderDropdown()
    await openDropdown()

    act(() => {
      menuItem('Manage branches...').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(true)
    expect(isMenuOpen()).toBe(false)
  })
})
