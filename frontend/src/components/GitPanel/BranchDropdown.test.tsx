// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { TooltipProvider } from '@/components/ui/tooltip'
import { withVirtualTimers } from '@/test/virtualTime'

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
    deleteWorktree: vi.fn(),
  },
}))

vi.mock('@/api/git', () => gitMocks)
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn() } }))

// Focus actions (worktree switching) are mocked so tests never touch the backend.
const { focusMocks } = vi.hoisted(() => ({
  focusMocks: {
    focusWorktree: vi.fn(),
    refreshGitFocus: vi.fn(),
  },
}))

vi.mock('@/lib/gitFocus', () => focusMocks)

import { BranchDropdown } from './BranchDropdown'
import { focusWorktree } from '@/lib/gitFocus'
import { MANAGED_FOCUS_BLOCKED_REASON } from '@/hooks/useSessionWorktreePin'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
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
  gitMocks.deleteWorktree.mockResolvedValue(undefined)

  useGitPanelStore.getState().reset()
  // Branch operations record their outcome against the ACTIVE project.
  useProjectStore.setState({ activeProjectId: 'p1' })
  // The session pin (managed worktree) starts unset — no active session.
  useSessionStore.setState({ sessions: null, activeSessionId: null })
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

/** A branch row is a div[role=button] whose accessible name is exactly
 *  `name` — always the FULL branch name/ref: in tree mode the visible
 *  label is the basename (`branchLeafLabel`), while `aria-label` keeps
 *  the full name. */
function rowByName(name: string): HTMLElement {
  const row = Array.from(body().querySelectorAll('div[role="button"]')).find(
    (el) => el.getAttribute('aria-label') === name,
  )
  expect(row, `branch row "${name}"`).toBeTruthy()
  return row as HTMLElement
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

  it('keeps the search input focused when a keystroke matches a branch row', async () => {
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
    // The dispatch sits INSIDE the virtual-time window: Radix's typeahead
    // focuses its match from a setTimeout(0), which must land on the fake
    // clock and fire deterministically during the advance.
    await withVirtualTimers(() => {
      input.focus()
      input.dispatchEvent(new KeyboardEvent('keydown', { key: 'm', bubbles: true }))
    })

    // Branch rows are plain role=button divs (the menu holds no Radix items
    // today), so the typeahead has nothing to steal with — but the input is a
    // MenuSearchInput so this holds even if menu items are ever added.
    expect(document.activeElement).toBe(input)
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
    // The outcome is recorded in the operation console slice.
    expect(useGitPanelStore.getState().operationByProject['p1']).toMatchObject({
      kind: 'checkout',
      ok: true,
    })
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

  it('does not render the New branch.../Manage branches... footer items', async () => {
    // The footer entries duplicated the git panel toolbar's "Manage branches"
    // button (the only picker opener) and were removed.
    renderDropdown()
    await openDropdown()

    expect(menuItem('New branch...')).toBeUndefined()
    expect(menuItem('Manage branches...')).toBeUndefined()
    // Branch creation lives in the BranchPicker modal — the dropdown never
    // opens it itself.
    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(false)
  })
})

describe('BranchDropdown — tree/flat display-mode toggle', () => {
  // 5 branches: enough for the search input to render alongside the toggle.
  const treeFixture = [
    makeBranch({ name: 'main', is_current: true }),
    makeBranch({ name: 'feature/auth' }),
    makeBranch({ name: 'feature/api' }),
    makeBranch({ name: 'origin/feature/auth', kind: 'remote' }),
    makeBranch({ name: 'origin/feature/api', kind: 'remote' }),
  ]

  /** The mode toggle — the only registered menu checkbox, found by its
   *  title. */
  function modeToggle(): HTMLElement {
    const el = body().querySelector<HTMLElement>(
      '[role="menuitemcheckbox"][title="Show branch tree"], [role="menuitemcheckbox"][title="Show flat list"]',
    )
    expect(el, 'display-mode toggle').toBeTruthy()
    return el!
  }

  /** Click the toggle (Radix item click → onSelect). */
  async function clickToggle(): Promise<void> {
    act(() => {
      modeToggle().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()
  }

  /** Dir rows inside one section's content region, in document order. */
  function dirIds(regionId: string): string[] {
    return Array.from(
      body().querySelectorAll(`#${regionId} [data-testid^="branch-dir-"]`),
    ).map((el) => el.getAttribute('data-testid')!)
  }

  it('renders the toggle below the 5-branch threshold — with the search input still hidden', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feature/x' }),
    ])
    renderDropdown()
    await openDropdown()

    // Default mode is flat, so the icon advertises the switch TO the tree;
    // aria-checked announces the CURRENT mode (flat = unchecked).
    const toggle = body().querySelector(
      '[role="menuitemcheckbox"][title="Show branch tree"]',
    )
    expect(toggle).not.toBeNull()
    expect(toggle!.getAttribute('aria-checked')).toBe('false')
    // The toggle renders even though the search input stays hidden (<5).
    expect(body().querySelector('input[placeholder="Search branches..."]')).toBeNull()
    // The flat list itself is untouched.
    expect(rowByName('feature/x')).toBeTruthy()
  })

  it('flips the display mode without closing the menu — and back', async () => {
    useGitPanelStore.getState().setBranches(treeFixture)
    renderDropdown()
    await openDropdown()

    await clickToggle()

    // The onSelect preventDefault keeps the menu open…
    expect(isMenuOpen()).toBe(true)
    // …the toggle now advertises the way back, with the state announced…
    const treeToggle = body().querySelector(
      '[role="menuitemcheckbox"][title="Show flat list"]',
    )
    expect(treeToggle).not.toBeNull()
    expect(treeToggle!.getAttribute('aria-checked')).toBe('true')
    expect(body().querySelector('[role="menuitemcheckbox"][title="Show branch tree"]')).toBeNull()
    // …and Local renders its `feature` directory instead of the flat list.
    expect(body().querySelector('[data-testid="branch-dir-feature"]')).not.toBeNull()
    expect(rowByName('feature/auth')).toBeTruthy()

    await clickToggle()

    // Flipping back restores the flat view; the menu is STILL open and the
    // checkbox is unchecked again.
    expect(isMenuOpen()).toBe(true)
    const flatToggle = body().querySelector(
      '[role="menuitemcheckbox"][title="Show branch tree"]',
    )
    expect(flatToggle).not.toBeNull()
    expect(flatToggle!.getAttribute('aria-checked')).toBe('false')
    expect(body().querySelector('[data-testid="branch-dir-feature"]')).toBeNull()
    expect(rowByName('feature/auth')).toBeTruthy()
  })

  it('tree mode builds directories in BOTH sections — local dirs and the remote-prefix top node', async () => {
    useGitPanelStore.getState().setBranches(treeFixture)
    renderDropdown()
    await openDropdown()

    await clickToggle()

    // Local: the `feature` dir, expanded by default, holding its leaves.
    expect(dirIds('branch-region-local')).toEqual(['branch-dir-feature'])
    // Remote: the `origin` prefix emerges as the top-level dir, nesting `feature`.
    expect(dirIds('branch-region-remote')).toEqual([
      'branch-dir-origin',
      'branch-dir-origin/feature',
    ])
    // Every leaf stays reachable — the accessible names are the full
    // names; the visible labels are basenames indented under their dirs.
    expect(rowByName('feature/auth')).toBeTruthy()
    expect(rowByName('origin/feature/api')).toBeTruthy()
    expect((rowByName('feature/auth').textContent ?? '').trim()).toBe('auth')
  })

  it('a search that matches nothing shows the empty state — with every tree dir pruned', async () => {
    useGitPanelStore.getState().setBranches(treeFixture)
    renderDropdown()
    await openDropdown()

    await clickToggle()

    const input = body().querySelector(
      'input[placeholder="Search branches..."]',
    ) as HTMLInputElement
    expect(input).not.toBeNull()
    act(() => setInputValue(input, 'zzz'))
    await flush()

    expect(body().querySelector('[data-testid="branch-list-empty"]')).not.toBeNull()
    expect(body().textContent).toContain('No matching branches')
    // Both sections collapsed away entirely — no stray dir rows remain.
    expect(dirIds('branch-region-local')).toEqual([])
    expect(dirIds('branch-region-remote')).toEqual([])
    // The flip itself never closed the menu.
    expect(isMenuOpen()).toBe(true)
  })
})

describe('BranchDropdown — worktree focus switcher', () => {
  beforeEach(() => {
    focusMocks.focusWorktree.mockReset()
    focusMocks.focusWorktree.mockResolvedValue(undefined)
    focusMocks.refreshGitFocus.mockReset()
    focusMocks.refreshGitFocus.mockResolvedValue(undefined)
  })

  it('hides the Worktrees section when the project has no worktrees', async () => {
    useGitPanelStore.getState().setWorktrees([])
    renderDropdown()
    await openDropdown()

    expect(Array.from(body().querySelectorAll('[data-testid="worktree-row"]'))).toHaveLength(0)
    expect(body().textContent).not.toContain('Worktrees')
  })

  it('lists every worktree and switches the panel focus on selection', async () => {
    useGitPanelStore.getState().setWorktrees([
      {
        path: '/repo', name: 'repo', kind: 'main', branch: 'main', head: 'a',
        managed: false, pinned: false, is_focus: true,
      },
      {
        path: '/repo/.worktrees/s-abc12345', name: 's-abc12345', kind: 'managed',
        branch: 'sess/s-abc12345', head: 'b', managed: true, pinned: false,
        is_focus: false, session_id: 'sess-1', session_name: 'Refactor loop',
      },
    ])
    renderDropdown()
    await openDropdown()

    const rows = Array.from(body().querySelectorAll('[data-testid="worktree-row"]'))
    expect(rows).toHaveLength(2)
    expect(body().textContent).toContain('Worktrees')
    expect(body().textContent).toContain('Local checkout')
    expect(body().textContent).toContain('Refactor loop')

    act(() => {
      rows[1]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(focusWorktree).toHaveBeenCalledTimes(1)
    expect(focusWorktree).toHaveBeenCalledWith('/repo/.worktrees/s-abc12345')
    // Selecting a worktree closes the dropdown.
    expect(isMenuOpen()).toBe(false)
  })

  it('exposes the delete action on the linked tree but never on the checkout', async () => {
    useGitPanelStore.getState().setWorktrees([
      {
        path: '/repo', name: 'repo', kind: 'main', branch: 'main', head: 'a',
        managed: false, pinned: false, is_focus: false,
      },
      {
        path: '/repo/.worktrees/s-abc12345', name: 's-abc12345', kind: 'managed',
        branch: 'sess/s-abc12345', head: 'b', managed: true, pinned: false,
        is_focus: false,
      },
    ])
    renderDropdown()
    await openDropdown()

    const rows = Array.from(body().querySelectorAll('[data-testid="worktree-row"]'))
    expect(rows).toHaveLength(2)
    // The checkout row has no hover actions; the linked tree has one.
    expect(rowButtons(rows[0] as HTMLElement)).toHaveLength(0)
    expect(rowButtons(rows[1] as HTMLElement)).toHaveLength(1)
  })

  it('opens the delete confirm dialog on the row action without switching the focus', async () => {
    useGitPanelStore.getState().setWorktrees([
      {
        path: '/repo/.worktrees/s-abc12345', name: 's-abc12345', kind: 'managed',
        branch: 'sess/s-abc12345', head: 'b', managed: true, pinned: false,
        is_focus: false,
      },
    ])
    renderDropdown()
    await openDropdown()

    const row = body().querySelector('[data-testid="worktree-row"]') as HTMLElement
    act(() => {
      rowButtons(row)[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    // The delete click must never double as a focus switch.
    expect(focusWorktree).not.toHaveBeenCalled()
    expect(body().textContent).toContain('Delete worktree?')

    act(() => {
      dialogButton('Delete').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.deleteWorktree).toHaveBeenCalledWith('/repo/.worktrees/s-abc12345', {
      force: false,
      unlock: false,
    })
    // Success refreshes the worktree list + focus mirror.
    expect(focusMocks.refreshGitFocus).toHaveBeenCalledTimes(1)
  })

  it('shows the unlock consent in the dialog for a locked tree', async () => {
    useGitPanelStore.getState().setWorktrees([
      {
        path: '/repo/.worktrees/s-abc12345', name: 's-abc12345', kind: 'managed',
        branch: 'sess/s-abc12345', head: 'b', managed: true, pinned: false,
        is_focus: false, locked: true,
      },
    ])
    renderDropdown()
    await openDropdown()

    const row = body().querySelector('[data-testid="worktree-row"]') as HTMLElement
    act(() => {
      rowButtons(row)[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    const box = body().querySelector('[data-testid="worktree-unlock-checkbox"]') as HTMLInputElement
    expect(box).toBeTruthy()
    expect(box.checked).toBe(false)

    act(() => {
      box.click()
    })
    act(() => {
      dialogButton('Delete').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.deleteWorktree).toHaveBeenCalledWith('/repo/.worktrees/s-abc12345', {
      force: false,
      unlock: true,
    })
  })

  it('shows the backend refusal in the dialog for a failed deletion', async () => {
    gitMocks.deleteWorktree.mockRejectedValue(
      new Error('worktree has uncommitted changes: M frontend/src/api/git.ts'),
    )
    // The failure is recorded against the ACTIVE project's console.
    useGitPanelStore.getState().setWorktrees([
      {
        path: '/repo/.worktrees/s-abc12345', name: 's-abc12345', kind: 'managed',
        branch: 'sess/s-abc12345', head: 'b', managed: true, pinned: false,
        is_focus: false,
      },
    ])
    renderDropdown()
    await openDropdown()

    const row = body().querySelector('[data-testid="worktree-row"]') as HTMLElement
    act(() => {
      rowButtons(row)[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    act(() => {
      dialogButton('Delete').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    // The refusal re-opens the dialog with the verbatim reason + force hint;
    // the outcome is recorded in the operation console.
    expect(body().textContent).toContain('worktree has uncommitted changes')
    expect(body().textContent).toContain('Force delete discards the uncommitted changes.')
    expect(focusMocks.refreshGitFocus).not.toHaveBeenCalled()
    expect(useGitPanelStore.getState().operationByProject['p1']).toMatchObject({
      kind: 'worktree-delete',
      ok: false,
    })

    // Retrying with Force discards the dirty tree.
    gitMocks.deleteWorktree.mockResolvedValue(undefined)
    act(() => {
      dialogButton('Force delete').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.deleteWorktree).toHaveBeenLastCalledWith('/repo/.worktrees/s-abc12345', {
      force: true,
      unlock: false,
    })
    expect(body().textContent).not.toContain('Delete worktree?')
    expect(focusMocks.refreshGitFocus).toHaveBeenCalledTimes(1)
  })
})

describe('BranchDropdown — collapsible sections', () => {
  /** Section header item — a registered menu item, found by its exact label. */
  function sectionHeader(label: string): HTMLElement {
    const item = Array.from(body().querySelectorAll('[role="menuitem"]')).find(
      (el) => (el.textContent ?? '').trim() === label,
    )
    expect(item, `section header "${label}"`).toBeTruthy()
    return item as HTMLElement
  }

  /**
   * A real bubbling keydown (the dispatch path Radix's React handlers listen
   * on). Roving-focus moves apply from a setTimeout, so the dispatch sits
   * inside the virtual-time window.
   */
  function pressKey(el: Element, key: string): Promise<void> {
    return withVirtualTimers(() => {
      el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
    }, 10)
  }

  function clickHeader(label: string) {
    act(() => {
      sectionHeader(label).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    return flush()
  }

  // Local and remote names must not be substrings of each other — the
  // section tests assert on absence via textContent.
  const localAndRemote = () =>
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feature/x' }),
      makeBranch({ name: 'origin/feature/y', kind: 'remote' }),
    ])

  it('renders section headers in the combo style with a chevron and hover affordance', async () => {
    localAndRemote()
    renderDropdown()
    await openDropdown()

    for (const label of ['Local', 'Remote']) {
      const btn = sectionHeader(label)
      expect(btn.className).toContain('uppercase')
      expect(btn.className).toContain('font-semibold')
      expect(btn.className).toContain('text-xs')
      expect(btn.className).toContain('hover:bg-muted')
      expect(btn.querySelector('svg.lucide-chevron-down')).not.toBeNull()
    }
  })

  it('section headers expose aria-expanded and name their live content region', async () => {
    localAndRemote()
    renderDropdown()
    await openDropdown()

    const local = sectionHeader('Local')
    expect(local.getAttribute('aria-expanded')).toBe('true')
    const regionId = local.getAttribute('aria-controls')!
    expect(regionId).toBeTruthy()
    expect(document.getElementById(regionId)?.textContent).toContain('feature/x')

    // Collapsing flips the state and unmounts the region it named.
    await clickHeader('Local')
    expect(sectionHeader('Local').getAttribute('aria-expanded')).toBe('false')
    expect(document.getElementById(regionId)).toBeNull()
  })

  it('collapses the Local section without closing the menu; the Remote section is untouched', async () => {
    localAndRemote()
    renderDropdown()
    await openDropdown()

    await clickHeader('Local')
    expect(isMenuOpen()).toBe(true)
    expect(body().textContent).not.toContain('feature/x')
    expect(body().textContent).toContain('origin/feature/y')
    // The collapsed section keeps its header so it can be re-expanded.
    expect(body().textContent).toContain('Local')

    await clickHeader('Local')
    expect(body().textContent).toContain('feature/x')
  })

  it('collapses the Remote section and re-expands it', async () => {
    localAndRemote()
    renderDropdown()
    await openDropdown()

    await clickHeader('Remote')
    expect(isMenuOpen()).toBe(true)
    expect(body().textContent).not.toContain('origin/feature/y')
    expect(body().textContent).toContain('feature/x')

    await clickHeader('Remote')
    expect(body().textContent).toContain('origin/feature/y')
  })

  it('collapses the Worktrees section and re-expands it', async () => {
    localAndRemote()
    useGitPanelStore.getState().setWorktrees([
      { path: '/repo', name: 'repo', kind: 'main', branch: 'main', head: 'a', managed: false, pinned: false, is_focus: true },
      {
        path: '/repo/.worktrees/s-abc', name: 's-abc', kind: 'managed',
        branch: 'sess/s-abc', head: 'b', managed: true, pinned: false, is_focus: false,
      },
    ])
    renderDropdown()
    await openDropdown()

    expect(body().querySelector('[data-testid="worktree-row"]')).not.toBeNull()

    await clickHeader('Worktrees')
    expect(isMenuOpen()).toBe(true)
    expect(body().querySelector('[data-testid="worktree-row"]')).toBeNull()
    expect(body().textContent).toContain('Worktrees')

    await clickHeader('Worktrees')
    expect(body().querySelector('[data-testid="worktree-row"]')).not.toBeNull()
  })

  it('an active search force-expands a collapsed section; clearing restores it', async () => {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feature/x' }),
      makeBranch({ name: 'feature/api' }),
      makeBranch({ name: 'bugfix/1' }),
      makeBranch({ name: 'bugfix/2' }),
    ])
    renderDropdown()
    await openDropdown()

    await clickHeader('Local')
    expect(body().textContent).not.toContain('feature/x')

    const input = body().querySelector('input[placeholder="Search branches..."]') as HTMLInputElement
    act(() => setInputValue(input, 'feature'))
    await flush()
    expect(body().textContent).toContain('feature/x')
    expect(body().textContent).not.toContain('bugfix/1')

    act(() => setInputValue(input, ''))
    await flush()
    expect(body().textContent).not.toContain('feature/x')
  })

  it('section headers are arrow-reachable and Enter toggles them without closing the menu', async () => {
    localAndRemote()
    renderDropdown()
    await openDropdown()

    // Pointer-open leaves focus on the menu content; the FIRST item of the
    // roving collection is the tree/flat display-mode toggle (it renders even
    // below the 5-branch threshold, where the search box stays hidden). The
    // Local header follows it — as a plain button it was invisible to the
    // arrows before the toggle existed, and Tab is prevented inside the menu,
    // so the arrows remain its only keyboard path.
    const menu = body().querySelector('[data-slot="dropdown-menu-content"]') as HTMLElement
    await pressKey(menu, 'ArrowDown')
    expect(document.activeElement).toBe(
      body().querySelector('[role="menuitemcheckbox"][title="Show branch tree"]'),
    )

    await pressKey(document.activeElement!, 'ArrowDown')
    expect(document.activeElement).toBe(sectionHeader('Local'))

    // Enter collapses the section — the menu stays open.
    await pressKey(document.activeElement!, 'Enter')
    expect(isMenuOpen()).toBe(true)
    expect(sectionHeader('Local').getAttribute('aria-expanded')).toBe('false')
    expect(body().textContent).not.toContain('feature/x')

    // Enter again re-expands (focus persisted through the header re-render).
    await pressKey(document.activeElement!, 'Enter')
    expect(sectionHeader('Local').getAttribute('aria-expanded')).toBe('true')
    expect(body().textContent).toContain('feature/x')
  })
})

describe('BranchDropdown — managed session pin', () => {
  /** Arm an active session bound to a managed worktree on feat/x (tree s-1). */
  function armManagedSession(): void {
    useSessionStore.setState({
      sessions: [
        {
          id: 's-1',
          project_id: 'p1',
          name: 'Session',
          created_at: '',
          last_active_at: '',
          archived: false,
          pinned: false,
          active: false,
          total_input_tokens: 0,
          total_output_tokens: 0,
          model: 'm',
          family: 'f',
          has_unfinished_task: false,
          workspace_binding: {
            kind: 'managed_worktree',
            workspace_path: '/repo/.worktrees/s-1',
            worktree_name: 's-1',
            branch: 'feat/x',
          },
        },
      ],
      activeSessionId: 's-1',
    })
  }

  it('renders a lock without a delete action on the pinned branch row', async () => {
    armManagedSession()
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feat/x' }),
    ])
    renderDropdown()
    await openDropdown()

    const pinnedRow = rowByName('feat/x')
    expect(pinnedRow.querySelector('svg.lucide-lock')).not.toBeNull()
    expect(pinnedRow.querySelector('svg.lucide-check')).toBeNull()
    // push | merge | rebase | rename — the delete action is gone entirely
    // (the pinned branch is not the focused tree's current one, so
    // merge/rebase stay, exactly as for any non-current row).
    expect(rowButtons(pinnedRow)).toHaveLength(4)
    expect(rowButtons(pinnedRow).some((b) => b.querySelector('svg.lucide-trash2'))).toBe(false)

    // An unpinned branch keeps the check and the full action set.
    const mainRow = rowByName('main')
    expect(mainRow.querySelector('svg.lucide-check')).not.toBeNull()
    expect(mainRow.querySelector('svg.lucide-lock')).toBeNull()
    expect(rowButtons(mainRow)).toHaveLength(3)
  })

  it('never checks out the pinned branch from the dropdown', async () => {
    armManagedSession()
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feat/x' }),
    ])
    renderDropdown()
    await openDropdown()

    act(() => {
      rowByName('feat/x').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.checkoutBranch).not.toHaveBeenCalled()
    expect(isMenuOpen()).toBe(true)
  })

  it('renders a lock without a delete action on the session worktree row', async () => {
    armManagedSession()
    useGitPanelStore.getState().setWorktrees([
      {
        path: '/repo', name: 'repo', kind: 'main', branch: 'main', head: 'a',
        managed: false, pinned: false, is_focus: false,
      },
      {
        path: '/repo/.worktrees/s-1', name: 's-1', kind: 'managed',
        branch: 'feat/x', head: 'b', managed: true, pinned: true,
        is_focus: true, session_id: 's-1', session_name: 'Session',
      },
      {
        path: '/repo/.worktrees/s-2', name: 's-2', kind: 'managed',
        branch: 'feat/y', head: 'c', managed: true, pinned: true,
        is_focus: false, session_id: 's-2', session_name: 'Other',
      },
    ])
    renderDropdown()
    await openDropdown()

    const rows = Array.from(body().querySelectorAll('[data-testid="worktree-row"]')) as HTMLElement[]
    const ownRow = rows.find((r) => r.getAttribute('data-path') === '/repo/.worktrees/s-1')!
    const otherRow = rows.find((r) => r.getAttribute('data-path') === '/repo/.worktrees/s-2')!

    expect(ownRow.querySelector('svg.lucide-lock')).not.toBeNull()
    expect(ownRow.querySelector('svg.lucide-check')).toBeNull()
    expect(rowButtons(ownRow)).toHaveLength(0)

    // Another session's managed tree keeps its delete affordance.
    expect(otherRow.querySelector('svg.lucide-lock')).toBeNull()
    expect(rowButtons(otherRow)).toHaveLength(1)
  })

  it('shows no locks for a local session', async () => {
    useSessionStore.setState({
      sessions: [
        {
          id: 's-local',
          project_id: 'p1',
          name: 'Local',
          created_at: '',
          last_active_at: '',
          archived: false,
          pinned: false,
          active: false,
          total_input_tokens: 0,
          total_output_tokens: 0,
          model: 'm',
          family: 'f',
          has_unfinished_task: false,
          workspace_binding: { kind: 'local', workspace_path: '/repo' },
        },
      ],
      activeSessionId: 's-local',
    })
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main', is_current: true }),
      makeBranch({ name: 'feat/x' }),
    ])
    useGitPanelStore.getState().setWorktrees([
      {
        path: '/repo', name: 'repo', kind: 'main', branch: 'main', head: 'a',
        managed: false, pinned: false, is_focus: true,
      },
    ])
    renderDropdown()
    await openDropdown()

    expect(body().querySelector('svg.lucide-lock')).toBeNull()
    expect(rowButtons(rowByName('main'))).toHaveLength(3)
  })
})

describe('BranchDropdown — managed-focus gate (ADR-080)', () => {
  /**
   * Branches + worktrees with a managed session tree IN FOCUS on
   * feat/focused (another session's tree — the gate follows the focus, not
   * the active session, so no session needs arming): the backend refuses
   * CheckoutBranch / CheckoutRemoteBranch there for every branch except the
   * focused tree's own, and the dropdown dims those rows up front.
   */
  function armManagedFocus(): void {
    useGitPanelStore.getState().setBranches([
      makeBranch({ name: 'main' }),
      makeBranch({ name: 'feat/focused', is_current: true }),
      makeBranch({ name: 'origin/feature/x', kind: 'remote' }),
    ])
    useGitPanelStore.getState().setWorktrees([
      {
        path: '/repo', name: 'repo', kind: 'main', branch: 'main', head: 'a',
        managed: false, pinned: false, is_focus: false,
      },
      {
        path: '/repo/.worktrees/s-9', name: 's-9', kind: 'managed',
        branch: 'feat/focused', head: 'b', managed: true, pinned: true,
        is_focus: true, session_id: 's-9', session_name: 'Other session',
      },
    ])
  }

  it('dims off-pin local rows with the reason and refuses their checkout in place', async () => {
    armManagedFocus()
    renderDropdown()
    await openDropdown()

    const offPin = rowByName('main')
    expect(offPin.getAttribute('aria-disabled')).toBe('true')
    expect(offPin.title).toBe(MANAGED_FOCUS_BLOCKED_REASON)
    expect(offPin.className).toContain('opacity-50')

    act(() => {
      offPin.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    // The refusal never reaches the server, and the dropdown stays open so
    // the user can pick a reachable branch instead.
    expect(gitMocks.checkoutBranch).not.toHaveBeenCalled()
    expect(isMenuOpen()).toBe(true)
  })

  it('keeps the focused tree\u2019s own branch and the hover actions live', async () => {
    armManagedFocus()
    renderDropdown()
    await openDropdown()

    // The focused tree's own branch is not gated: a same-branch checkout
    // cannot move the tree off its pin. (It is the focused tree's current
    // branch, so a click stays a no-op for the ordinary current-branch
    // reason — but the row carries no gate reason.)
    const own = rowByName('feat/focused')
    expect(own.getAttribute('aria-disabled')).toBeNull()
    expect(own.title).toBe('')
    act(() => {
      own.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(gitMocks.checkoutBranch).not.toHaveBeenCalled()

    // Hover actions on a gated row stay enabled (push is not focus-scoped).
    const push = rowButtons(rowByName('main'))[0]!
    expect(push.disabled).toBe(false)
  })

  it('gates remote rows too, but keeps delete-remote live', async () => {
    armManagedFocus()
    renderDropdown()
    await openDropdown()

    const remote = rowByName('origin/feature/x')
    expect(remote.getAttribute('aria-disabled')).toBe('true')
    expect(remote.title).toBe(MANAGED_FOCUS_BLOCKED_REASON)

    act(() => {
      remote.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.checkoutRemoteBranch).not.toHaveBeenCalled()
    expect(isMenuOpen()).toBe(true)
    // Deleting the remote branch does not touch the focused tree — the
    // action stays available under the gate.
    const del = rowButtons(remote)[0]!
    expect(del.disabled).toBe(false)
  })

  it('gates nothing once the focus returns to the main checkout', async () => {
    armManagedFocus()
    useGitPanelStore.getState().setWorktrees([
      {
        path: '/repo', name: 'repo', kind: 'main', branch: 'main', head: 'a',
        managed: false, pinned: false, is_focus: true,
      },
      {
        path: '/repo/.worktrees/s-9', name: 's-9', kind: 'managed',
        branch: 'feat/focused', head: 'b', managed: true, pinned: true,
        is_focus: false, session_id: 's-9', session_name: 'Other session',
      },
    ])
    renderDropdown()
    await openDropdown()

    const row = rowByName('main')
    expect(row.getAttribute('aria-disabled')).toBeNull()
    expect(row.title).toBe('')
    act(() => {
      row.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(gitMocks.checkoutBranch).toHaveBeenCalledWith('main')
    expect(isMenuOpen()).toBe(false)
  })
})
