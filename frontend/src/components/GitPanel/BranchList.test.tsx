// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { TooltipProvider } from '@/components/ui/tooltip'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { BranchList, type BranchListProps } from './BranchList'
import { MANAGED_FOCUS_BLOCKED_REASON } from '@/hooks/useSessionWorktreePin'
import type { Branch } from '@/types/models'

let container: HTMLDivElement
let root: Root

function makeBranch(overrides: Partial<Branch> = {}): Branch {
  return { name: 'feature/x', is_current: false, kind: 'local', upstream: '', ...overrides }
}

function listProps(overrides: Partial<Omit<BranchListProps, 'variant'>> = {}): Omit<BranchListProps, 'variant'> {
  return {
    branches: [],
    onCheckout: vi.fn(),
    onRename: vi.fn(),
    onMerge: vi.fn(),
    onRebase: vi.fn(),
    onPush: vi.fn(),
    onDeleteLocal: vi.fn(),
    onCheckoutRemote: vi.fn(),
    onDeleteRemote: vi.fn(),
    ...overrides,
  }
}

beforeEach(() => {
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

/** Flush microtasks so portaled content (dropdown content) mounts. */
function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

/** Menu harness: BranchList must render inside DropdownMenuContent — the
 *  collapsible headers are registered Radix items. Tracks close requests so
 *  the "menu stays open" assertions are real (a lost preventDefault closes). */
function renderMenu(props: Omit<BranchListProps, 'variant'>, onCloseRequest?: () => void) {
  function Harness() {
    const [open, setOpen] = useState(true)
    return (
      <TooltipProvider>
        <DropdownMenu
          open={open}
          onOpenChange={(o) => {
            if (!o) onCloseRequest?.()
            setOpen(o)
          }}
        >
          <DropdownMenuTrigger asChild>
            <Button>trigger</Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-80">
            <BranchList {...props} variant="menu" />
          </DropdownMenuContent>
        </DropdownMenu>
      </TooltipProvider>
    )
  }
  act(() => {
    root.render(<Harness />)
  })
  return flush()
}

/** Dialog harness — no Radix menu context needed for the dialog variant. */
function renderDialog(props: Omit<BranchListProps, 'variant'>) {
  act(() => {
    root.render(
      <TooltipProvider>
        <div>
          <BranchList {...props} variant="dialog" />
        </div>
      </TooltipProvider>,
    )
  })
  return flush()
}

function isMenuOpen(): boolean {
  return body().querySelector('[data-slot="dropdown-menu-content"]') !== null
}

function click(el: Element): Promise<void> {
  act(() => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  return flush()
}

/** Leaf rows: div[role=button] minus the dir rows, which are role=button too
 *  (keyboard-reachable toggles) and carry `branch-dir-*` testids. */
function leafRows(): Element[] {
  return Array.from(
    body().querySelectorAll('div[role="button"]:not([data-testid^="branch-dir-"])'),
  )
}

/** A branch row is a div[role=button] whose accessible name is exactly
 *  `name` — always the FULL branch name/ref: in tree mode the visible label
 *  is the basename (`branchLeafLabel`), while `aria-label` keeps the full
 *  name so rows stay unambiguous (two dirs can hold same-named leaves). */
function rowByName(name: string): HTMLElement {
  const row = leafRows().find((el) => el.getAttribute('aria-label') === name)
  expect(row, `branch row "${name}"`).toBeTruthy()
  return row as HTMLElement
}

function hasRow(name: string): boolean {
  return leafRows().some((el) => el.getAttribute('aria-label') === name)
}

/** Menu-variant section header — a registered menu item with the exact label. */
function sectionHeader(label: string): HTMLElement {
  const item = Array.from(body().querySelectorAll('[role="menuitem"]')).find(
    (el) => (el.textContent ?? '').trim() === label,
  )
  expect(item, `section header "${label}"`).toBeTruthy()
  return item as HTMLElement
}

function dirRow(path: string): HTMLElement {
  const el = body().querySelector(`[data-testid="branch-dir-${path}"]`)
  expect(el, `dir row "${path}"`).toBeTruthy()
  return el as HTMLElement
}

function emptyState(): HTMLElement | null {
  return body().querySelector('[data-testid="branch-list-empty"]')
}

// ─────────────────────────── Variants (flat) ─────────────────────────────────

describe('BranchList — flat variants', () => {
  it('menu variant: collapsible Local/Remote headers with regions, separator before Remote', async () => {
    await renderMenu(
      listProps({ branches: [makeBranch({ name: 'main' }), makeBranch({ name: 'origin/main', kind: 'remote' })] }),
    )

    for (const label of ['Local', 'Remote']) {
      const header = sectionHeader(label)
      expect(header.getAttribute('aria-expanded')).toBe('true')
      const regionId = header.getAttribute('aria-controls')
      expect(regionId).toBeTruthy()
      expect(document.getElementById(regionId!)).toBeTruthy()
    }
    // Exactly one BranchList separator: it leads the Remote section.
    expect(body().querySelectorAll('[data-slot="dropdown-menu-separator"]').length).toBe(1)
    expect(rowByName('main')).toBeTruthy()
    expect(rowByName('origin/main')).toBeTruthy()
  })

  it('dialog variant: static label headers inside a ul, li-wrapped rows, no separators', async () => {
    await renderDialog(
      listProps({ branches: [makeBranch({ name: 'main' }), makeBranch({ name: 'origin/main', kind: 'remote' })] }),
    )

    const ul = body().querySelector('ul')
    expect(ul).toBeTruthy()
    // The headers are plain label li elements, NOT registered menu items.
    const labels = Array.from(body().querySelectorAll('li')).filter(
      (li) => ['Local', 'Remote'].includes((li.textContent ?? '').trim()),
    )
    expect(labels.length).toBe(2)
    expect(body().querySelectorAll('[role="menuitem"]').length).toBe(0)
    expect(body().querySelectorAll('[data-slot="dropdown-menu-separator"]').length).toBe(0)
    // Rows are li-wrapped.
    expect(rowByName('main').closest('li')).toBeTruthy()
  })

  it('flat filter hides rows, then whole sections when nothing matches', async () => {
    const props = listProps({
      branches: [makeBranch({ name: 'main' }), makeBranch({ name: 'feature/x' }), makeBranch({ name: 'origin/main', kind: 'remote' })],
    })

    // 'main' matches: local keeps only `main`, remote keeps `origin/main`;
    // the local section still renders its header (one surviving row).
    await renderMenu(listProps({ ...props, filter: (n) => n.includes('main') }))
    expect(rowByName('main')).toBeTruthy()
    expect(hasRow('feature/x')).toBe(false)
    expect(rowByName('origin/main')).toBeTruthy()
    expect(sectionHeader('Local')).toBeTruthy()
    expect(sectionHeader('Remote')).toBeTruthy()

    // Nothing matches: sections disappear, the no-match empty state shows.
    await renderMenu(listProps({ ...props, filter: () => false }))
    expect(body().querySelectorAll('[role="menuitem"]').length).toBe(0)
    expect(emptyState()?.textContent).toContain('No matching branches')
  })

  it('empty states: labels per variant, hideEmptyState suppresses', async () => {
    await renderMenu(listProps({ branches: [] }))
    expect(emptyState()?.textContent).toContain('No branches')

    await renderDialog(listProps({ branches: [], noBranchesLabel: 'No branches found' }))
    expect(emptyState()?.textContent).toContain('No branches found')

    await renderMenu(listProps({ branches: [makeBranch()], hideEmptyState: true, filter: () => false }))
    expect(emptyState()).toBeNull()
  })

  it('row props thread through: pinned lock, blocked reason, inFlight spinner, disabled dim', async () => {
    await renderMenu(
      listProps({
        branches: [makeBranch({ name: 'main' }), makeBranch({ name: 'feature/x' })],
        pinnedFor: (name) => name === 'main',
        checkoutBlockedReasonFor: (name) => (name === 'feature/x' ? MANAGED_FOCUS_BLOCKED_REASON : null),
        inFlightFor: (name) => (name === 'feature/x' ? 'checkout' : null),
        disabledFor: (name) => name === 'feature/x',
      }),
    )

    const pinned = rowByName('main')
    expect(pinned.querySelector('svg[aria-label="Pinned to this session"]')).toBeTruthy()
    expect(pinned.getAttribute('title')).toContain('Pinned')
    expect(pinned.className).not.toContain('opacity-50')

    const blocked = rowByName('feature/x')
    expect(blocked.getAttribute('title')).toBe(MANAGED_FOCUS_BLOCKED_REASON)
    expect(blocked.className).toContain('opacity-50')
    // In-flight checkout renders the row's own spinner.
    expect(blocked.querySelector('svg.animate-spin')).toBeTruthy()
  })
})

// ───────────────────────── Draft selection threading ─────────────────────────

describe('BranchList — draft selection threading', () => {
  it('local draft rows: selected marker, onSelect with the full name, hover actions hidden', async () => {
    const onSelect = vi.fn()
    await renderMenu(
      listProps({
        branches: [makeBranch({ name: 'main' }), makeBranch({ name: 'feature/x' })],
        draftSelect: {
          local: { isSelected: (name) => name === 'main', onSelect },
        },
      }),
    )

    expect(body().querySelectorAll('[data-testid="draft-branch-row"]').length).toBe(2)
    expect(rowByName('main').getAttribute('aria-current')).toBe('true')
    // Draft rows are pure selection targets — no hover action buttons.
    expect(rowByName('main').querySelectorAll('button').length).toBe(0)

    await click(rowByName('feature/x'))
    expect(onSelect).toHaveBeenCalledWith('feature/x')
  })

  it('remote draft rows: onSelect receives the full ref; blocked twin refuses the pick', async () => {
    const onSelect = vi.fn()
    await renderMenu(
      listProps({
        branches: [makeBranch({ name: 'origin/main', kind: 'remote' }), makeBranch({ name: 'origin/feature/x', kind: 'remote' })],
        draftSelect: {
          remote: {
            isSelected: () => false,
            onSelect,
            blockedReasonFor: (ref) => (ref === 'origin/main' ? 'the main checkout holds it' : null),
          },
        },
      }),
    )

    const blocked = rowByName('origin/main')
    expect(blocked.getAttribute('title')).toBe('the main checkout holds it')
    await click(blocked)
    expect(onSelect).not.toHaveBeenCalled()

    await click(rowByName('origin/feature/x'))
    expect(onSelect).toHaveBeenCalledWith('origin/feature/x')
  })
})

// ─────────────────────── Menu-variant open-keeping ───────────────────────────

describe('BranchList — menu stays open', () => {
  it('section header toggle keeps the menu open (Radix select is preventDefault-ed)', async () => {
    let closeRequests = 0
    await renderMenu(
      listProps({ branches: [makeBranch({ name: 'main' })] }),
      () => {
        closeRequests++
      },
    )

    await click(sectionHeader('Local'))
    expect(closeRequests).toBe(0)
    expect(isMenuOpen()).toBe(true)
    // Collapsed: the region unmounts, the header stays for re-expanding.
    expect(sectionHeader('Local').getAttribute('aria-expanded')).toBe('false')
    expect(document.getElementById('branch-region-local')).toBeNull()
    expect(hasRow('main')).toBe(false)

    await click(sectionHeader('Local'))
    expect(closeRequests).toBe(0)
    expect(isMenuOpen()).toBe(true)
    expect(rowByName('main')).toBeTruthy()
  })

  it('tree dir click keeps the menu open and toggles the children', async () => {
    let closeRequests = 0
    await renderMenu(
      listProps({
        branches: [makeBranch({ name: 'feature/login' })],
        tree: true,
      }),
      () => {
        closeRequests++
      },
    )

    // Dirs render expanded by default.
    expect(hasRow('feature/login')).toBe(true)
    await click(dirRow('feature'))
    expect(closeRequests).toBe(0)
    expect(isMenuOpen()).toBe(true)
    expect(dirRow('feature').getAttribute('aria-expanded')).toBe('false')
    expect(hasRow('feature/login')).toBe(false)

    await click(dirRow('feature'))
    expect(closeRequests).toBe(0)
    expect(isMenuOpen()).toBe(true)
    expect(rowByName('feature/login')).toBeTruthy()
  })
})

// ───────────────────────────────── Tree mode ─────────────────────────────────

describe('BranchList — tree mode', () => {
  it('dirs render expanded by default, collapse on click, indent children by depth*12px', async () => {
    await renderMenu(
      listProps({
        branches: [makeBranch({ name: 'main' }), makeBranch({ name: 'feature/login' }), makeBranch({ name: 'feature/auth/token' })],
        tree: true,
      }),
    )

    // Expanded by default: the whole structure is visible at once.
    const featureDir = dirRow('feature')
    expect(featureDir.getAttribute('aria-expanded')).toBe('true')
    expect(featureDir.style.paddingLeft).toBe('8px')
    expect(rowByName('feature/login').parentElement!.style.paddingLeft).toBe('12px')
    // Root-level leaf renders bare — no indent wrapper.
    expect(rowByName('main').style.paddingLeft).toBe('')
    expect(rowByName('main').parentElement?.getAttribute('role')).toBe('presentation')

    // Collapse `feature`: its subtree disappears, the dir row stays.
    await click(featureDir)
    expect(featureDir.getAttribute('aria-expanded')).toBe('false')
    expect(hasRow('feature/login')).toBe(false)

    // Re-expand: `auth` (dir) sorts before the `login` leaf inside `feature`.
    await click(featureDir)
    const authDir = dirRow('feature/auth')
    expect(authDir.getAttribute('aria-expanded')).toBe('true')
    expect(authDir.style.paddingLeft).toBe('20px')
    expect(rowByName('feature/login').parentElement!.style.paddingLeft).toBe('12px')
    expect(rowByName('feature/auth/token').parentElement!.style.paddingLeft).toBe('24px')
  })

  it('remote prefix becomes the top-level node of the Remote section tree', async () => {
    await renderMenu(
      listProps({
        branches: [makeBranch({ name: 'origin/main', kind: 'remote' }), makeBranch({ name: 'origin/feature/x', kind: 'remote' })],
        tree: true,
      }),
    )

    // `origin` is the tree root; nested `feature` dir; both leaves visible.
    const origin = dirRow('origin')
    expect(origin.getAttribute('aria-expanded')).toBe('true')
    expect(dirRow('origin/feature')).toBeTruthy()
    expect(rowByName('origin/main')).toBeTruthy()
    expect(rowByName('origin/feature/x')).toBeTruthy()

    // Collapsing the top node hides the whole remote subtree.
    await click(origin)
    expect(hasRow('origin/main')).toBe(false)
    expect(hasRow('origin/feature/x')).toBe(false)
    expect(body().querySelector('[data-testid="branch-dir-origin/feature"]')).toBeNull()

    await click(origin)
    expect(rowByName('origin/feature/x')).toBeTruthy()
  })

  it('tree leaves show the basename; the accessible name and flat mode stay full', async () => {
    await renderMenu(
      listProps({
        branches: [
          makeBranch({ name: 'origin/ui/branch-grouping', kind: 'remote' }),
        ],
        tree: true,
      }),
    )

    // The dirs above the leaf carry every ancestor segment; the leaf renders
    // only the last one.
    expect(dirRow('origin')).toBeTruthy()
    expect(dirRow('origin/ui')).toBeTruthy()
    const leaf = rowByName('origin/ui/branch-grouping')
    expect((leaf.textContent ?? '').trim()).toBe('branch-grouping')
    // The accessible name stays the full ref — the visible basename is
    // ambiguous across sibling dirs (`origin/ui/x` vs `origin/cli/x`).
    expect(leaf.getAttribute('aria-label')).toBe('origin/ui/branch-grouping')

    // Flat mode ignores the basename convention and shows the full ref.
    await renderMenu(
      listProps({
        branches: [
          makeBranch({ name: 'origin/ui/branch-grouping', kind: 'remote' }),
        ],
      }),
    )
    const flatLeaf = rowByName('origin/ui/branch-grouping')
    expect((flatLeaf.textContent ?? '').trim()).toBe('origin/ui/branch-grouping')
    expect(body().querySelector('[data-testid="branch-dir-origin"]')).toBeNull()
  })

  it('an active search force-expands a dir the user collapsed — and restores on clear', async () => {
    let setSearchActive: (v: boolean) => void = () => {}
    function Harness({ props }: { props: Omit<BranchListProps, 'variant'> }) {
      const [searchActive, setSearchActiveState] = useState(false)
      setSearchActive = setSearchActiveState
      return (
        <TooltipProvider>
          <DropdownMenu open onOpenChange={() => {}}>
            <DropdownMenuContent align="start" className="w-80">
              <BranchList {...props} searchActive={searchActive} variant="menu" />
            </DropdownMenuContent>
          </DropdownMenu>
        </TooltipProvider>
      )
    }
    const props = listProps({
      branches: [makeBranch({ name: 'feature/login' })],
      tree: true,
    })
    act(() => {
      root.render(<Harness props={props} />)
    })
    await flush()

    // The user collapses the dir…
    await click(dirRow('feature'))
    expect(dirRow('feature').getAttribute('aria-expanded')).toBe('false')

    // …a search flips on: the collapsed dir is force-expanded…
    act(() => {
      setSearchActive(true)
    })
    await flush()
    expect(dirRow('feature').getAttribute('aria-expanded')).toBe('true')
    expect(rowByName('feature/login')).toBeTruthy()

    // …and the user's collapsed state is restored when the search clears.
    act(() => {
      setSearchActive(false)
    })
    await flush()
    expect(dirRow('feature').getAttribute('aria-expanded')).toBe('false')
    expect(hasRow('feature/login')).toBe(false)
  })

  it('dirs whose branches all fail the filter are pruned', async () => {
    await renderMenu(
      listProps({
        branches: [makeBranch({ name: 'feature/login' }), makeBranch({ name: 'main' })],
        tree: true,
        filter: (n) => n.includes('main'),
      }),
    )

    expect(body().querySelector('[data-testid="branch-dir-feature"]')).toBeNull()
    expect(rowByName('main')).toBeTruthy()
  })

  it('leaf+directory collision renders once: the dir carries the branch inside', async () => {
    await renderMenu(
      listProps({
        branches: [makeBranch({ name: 'feature' }), makeBranch({ name: 'feature/login' })],
        tree: true,
      }),
    )

    // One dir node — no duplicate sibling leaf at the root.
    expect(body().querySelectorAll('[data-testid="branch-dir-feature"]').length).toBe(1)
    // Expanded by default: the dir's own branch row comes first, then the nested leaf.
    // (leafRows excludes the dir row itself, which also carries text "feature";
    // both leaves render the basename "feature", so order is asserted by the
    // full accessible name.)
    const rows = leafRows()
      .map((el) => el.getAttribute('aria-label'))
      .filter((t) => t === 'feature' || t === 'feature/login')
    expect(rows).toEqual(['feature', 'feature/login'])

    // Collapsing hides the carried leaf with the rest of the subtree.
    await click(dirRow('feature'))
    expect(hasRow('feature')).toBe(false)
    expect(hasRow('feature/login')).toBe(false)
  })

  it('dialog variant wraps tree rows in li elements too', async () => {
    await renderDialog(
      listProps({
        branches: [makeBranch({ name: 'feature/login' })],
        tree: true,
        searchActive: true,
      }),
    )

    expect(dirRow('feature').closest('li')).toBeTruthy()
    expect(rowByName('feature/login').closest('li')).toBeTruthy()
  })

  it('dir rows are keyboard-reachable: tabIndex=0, Enter/Space twin the click toggle', async () => {
    await renderMenu(
      listProps({
        branches: [makeBranch({ name: 'feature/login' })],
        tree: true,
      }),
    )

    const dir = dirRow('feature')
    expect(dir.getAttribute('role')).toBe('button')
    expect(dir.getAttribute('tabindex')).toBe('0')

    // Enter collapses the dir — its subtree unmounts, the row stays.
    act(() => {
      dir.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    })
    await flush()
    expect(dir.getAttribute('aria-expanded')).toBe('false')
    expect(hasRow('feature/login')).toBe(false)

    // Space re-expands (same row element — the toggle never remounts it).
    act(() => {
      dir.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true }))
    })
    await flush()
    expect(dir.getAttribute('aria-expanded')).toBe('true')
    expect(rowByName('feature/login')).toBeTruthy()
  })
})
