// @vitest-environment jsdom
//
// The scoped-removal leaves and the guard-record rendering:
//   - EmbeddedLLMActions: the split Remove button (main click = "all"), the
//     dropdown's three scoped items and their disabled states, and the
//     no-button case of a machine with nothing on disk.
//   - EmbeddedLLMCleanupAction: the "data still on disk" row of a
//     not-installed machine with residue.
//   - EmbeddedLLMRemoveDialog: the scope-aware copy (what goes, what stays as
//     a cache).
//   - EmbeddedLLMInstallRecord's guard lines: an unapplied guard renders the
//     warning line with the issue link, an applied one a muted line.
//
// These are controlled leaves: the busy window, the RPC and the dialog state
// all live in the parent, so plain renders with explicit props suffice. The
// harness follows the project convention (createRoot + act, no
// testing-library).

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import { EmbeddedLLMActions, EmbeddedLLMCleanupAction } from './EmbeddedLLMActions'
import { EmbeddedLLMRemoveDialog } from './EmbeddedLLMRemoveDialog'
import { EmbeddedLLMInstallRecord } from './EmbeddedLLMInstallRecord'
import { guardIssueURL } from '@/lib/embeddedGuardView'
import type { EmbeddedLLMStatus } from '@/api/embedded'
import { makeEmbeddedStatus } from '@/test/embeddedStatusFixture'

let container: HTMLDivElement
let root: Root

function mount(node: React.ReactNode): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(node)
  })
}

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

function q(testId: string): HTMLElement | null {
  // Radix portals (dropdown menu, dialog) land in document.body.
  return document.querySelector(`[data-testid="${testId}"]`)
}

function click(el: Element | null): void {
  if (!el) throw new Error('element to click not found')
  act(() => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** Flush microtasks so portaled content (dropdown menu) mounts. */
async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

/** Radix opens a dropdown on the trigger's POINTERDOWN, not click. */
async function openMenu(testId: string): Promise<void> {
  act(() => {
    q(testId)?.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
  })
  await flush()
}

const NO_BUSY = null
const ALL_PRESENT = { runtime: true, weights: true, projection: true }

function actionsNode(
  onRemove: (scope: 'all' | 'runtime' | 'weights' | 'projection') => void,
  leftovers = ALL_PRESENT,
  busy: 'install' | 'remove' | 'load' | 'unload' | 'cancel' | null = null,
): React.ReactNode {
  return (
    <EmbeddedLLMActions
      loaded={false}
      loading={false}
      busy={busy}
      onLoad={() => {}}
      onUnload={() => {}}
      onRemove={onRemove}
      leftovers={leftovers}
    />
  )
}

describe('EmbeddedLLMActions — the split Remove button', () => {
  it('renders the main Remove (= remove everything) and the three scoped items', async () => {
    mount(actionsNode(() => {}))
    expect(q('embedded-llm-remove')).not.toBeNull()
    await openMenu('embedded-llm-remove-menu-trigger')
    expect(q('embedded-llm-remove-runtime')).not.toBeNull()
    expect(q('embedded-llm-remove-weights')).not.toBeNull()
    expect(q('embedded-llm-remove-projection')).not.toBeNull()
  })

  it('routes the main click to scope "all" and the items to their scopes', async () => {
    const scopes: string[] = []
    mount(actionsNode((scope) => scopes.push(scope)))
    click(q('embedded-llm-remove'))
    await openMenu('embedded-llm-remove-menu-trigger')
    click(q('embedded-llm-remove-runtime'))
    await openMenu('embedded-llm-remove-menu-trigger')
    click(q('embedded-llm-remove-weights'))
    await openMenu('embedded-llm-remove-menu-trigger')
    click(q('embedded-llm-remove-projection'))
    expect(scopes).toEqual(['all', 'runtime', 'weights', 'projection'])
  })

  it('disables a scope whose bytes are not on disk', async () => {
    mount(
      actionsNode(
        () => {},
        { runtime: false, weights: true, projection: false },
      ),
    )
    await openMenu('embedded-llm-remove-menu-trigger')
    expect((q('embedded-llm-remove-runtime') as HTMLElement).getAttribute('data-disabled')).not.toBeNull()
    expect((q('embedded-llm-remove-weights') as HTMLElement).getAttribute('data-disabled')).toBeNull()
    expect((q('embedded-llm-remove-projection') as HTMLElement).getAttribute('data-disabled')).not.toBeNull()
  })

  it('renders no Remove control when nothing is on disk', () => {
    mount(
      actionsNode(() => {}, { runtime: false, weights: false, projection: false }),
    )
    expect(q('embedded-llm-remove')).toBeNull()
    expect(q('embedded-llm-remove-group')).toBeNull()
  })

  it('disables every Remove control while an action is busy', () => {
    mount(actionsNode(() => {}, ALL_PRESENT, 'remove'))
    expect((q('embedded-llm-remove') as HTMLButtonElement).disabled).toBe(true)
    // A menu opened before the busy window stays non-clickable: Radix keeps
    // its items mounted, so the disabled ATTRIBUTE is what guards them.
    expect((q('embedded-llm-remove-menu-trigger') as HTMLButtonElement).disabled).toBe(true)
  })
})

describe('EmbeddedLLMCleanupAction — the residue row', () => {
  it('names the surviving groups and offers the scoped removal', async () => {
    const scopes: string[] = []
    mount(
      <EmbeddedLLMCleanupAction
        busy={NO_BUSY}
        leftovers={{ runtime: false, weights: true, projection: true }}
        onRemove={(scope) => scopes.push(scope)}
      />,
    )
    const row = q('embedded-llm-leftovers')
    expect(row?.textContent).toContain('weights')
    expect(row?.textContent).toContain('vision projector')
    expect(row?.textContent).not.toContain('runtime')
    click(q('embedded-llm-leftover-remove'))
    expect(scopes).toEqual(['all'])
  })

  it('omits the residue line when nothing survives (the parent gates the row)', () => {
    mount(
      <EmbeddedLLMCleanupAction
        busy={NO_BUSY}
        leftovers={{ runtime: false, weights: false, projection: false }}
        onRemove={() => {}}
      />,
    )
    // The residue sentence is gone; whether the whole component renders at all
    // is EmbeddedLLMSettings' call (it gates on anyLeftover).
    expect(q('embedded-llm-leftovers')?.textContent).not.toContain('Data still on disk')
  })
})

describe('EmbeddedLLMRemoveDialog — the scope-aware copy', () => {
  function dialogNode(scope: 'all' | 'runtime' | 'weights' | 'projection'): React.ReactNode {
    return (
      <EmbeddedLLMRemoveDialog open busy={false} scope={scope} onCancel={() => {}} onConfirm={() => {}} />
    )
  }

  it('warns that the full removal re-downloads everything', () => {
    mount(dialogNode('all'))
    expect(q('embedded-llm-remove-title')?.textContent).toContain('Remove the embedded model?')
    expect(q('embedded-llm-remove-description')?.textContent).toContain(
      'downloads everything again',
    )
  })

  it('tells the runtime scope that the weights survive as a cache', () => {
    mount(dialogNode('runtime'))
    expect(q('embedded-llm-remove-title')?.textContent).toContain('Remove the runtime?')
    expect(q('embedded-llm-remove-description')?.textContent).toContain(
      'instead of downloading the model again',
    )
  })

  it('tells the weights scope that only the weights are re-downloaded', () => {
    mount(dialogNode('weights'))
    expect(q('embedded-llm-remove-description')?.textContent).toContain(
      're-downloads only the weights',
    )
  })

  it('tells the projection scope that image input stops', () => {
    mount(dialogNode('projection'))
    expect(q('embedded-llm-remove-description')?.textContent).toContain('image input stops working')
  })
})

describe('the install record guard lines', () => {
  const base = makeEmbeddedStatus({
    installed: true,
    state: 'installed',
    backend: 'cuda-13.3',
  }) as EmbeddedLLMStatus

  it('renders an unapplied guard as a warning with the issue link', () => {
    const status = {
      ...base,
      guards: [
        {
          guard: 'cuda-13.3-crash',
          action: 'prefer_backend',
          reason: 'crash_on_load',
          severity: 'critical',
          issue: 'PrismML-Eng/llama.cpp#222',
          applied: false,
          backend: 'cuda-12.8',
          packing: '',
          guidance:
            'the CUDA 13.3 build is documented to segfault on some Linux systems; install the CUDA 12.x runtime libraries and reinstall',
        },
      ],
    } as EmbeddedLLMStatus
    mount(<EmbeddedLLMInstallRecord status={status} />)
    const warning = q('embedded-llm-guard-warning')
    expect(warning?.textContent).toContain('CUDA 12.x runtime libraries')
    const link = q('embedded-llm-guard-issue-link') as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('https://github.com/PrismML-Eng/llama.cpp/issues/222')
    expect(link.textContent).toBe('PrismML-Eng/llama.cpp#222')
  })

  it('renders an applied guard as a muted line without a warning testid', () => {
    const status = {
      ...base,
      guards: [
        {
          guard: 'cuda-13.3-crash',
          action: 'prefer_backend',
          reason: 'crash_on_load',
          severity: 'critical',
          issue: 'PrismML-Eng/llama.cpp#222',
          applied: true,
          backend: 'cuda-12.8',
          packing: '',
          guidance: 'this install uses the CUDA 12.8 build instead',
        },
      ],
    } as EmbeddedLLMStatus
    mount(<EmbeddedLLMInstallRecord status={status} />)
    expect(q('embedded-llm-guard-applied')?.textContent).toContain('12.8')
    expect(q('embedded-llm-guard-warning')).toBeNull()
  })

  it('renders nothing when no guard covers the machine', () => {
    mount(<EmbeddedLLMInstallRecord status={base} />)
    expect(q('embedded-llm-guard-records')).toBeNull()
  })

  it('maps issue citations to URLs and refuses a malformed one', () => {
    expect(guardIssueURL('PrismML-Eng/llama.cpp#222')).toBe(
      'https://github.com/PrismML-Eng/llama.cpp/issues/222',
    )
    expect(guardIssueURL('not-an-issue')).toBeNull()
    expect(guardIssueURL('repo/name#abc')).toBeNull()
  })
})
