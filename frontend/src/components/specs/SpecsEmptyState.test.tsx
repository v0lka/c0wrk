// @vitest-environment jsdom
// SpecsEmptyState — a DETECTED framework whose declared roots are absent. The
// empty state names the framework (its own label + description), points at the
// declared root, and offers the framework's bootstrap gesture only when it
// declares one (an action with no requirement at all) that is DISPATCHABLE —
// a declared-but-unavailable gesture (skill not installed) stays CTA-less.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SpecsEmptyState } from './SpecsEmptyState'
import { makeAction, makeFramework, makeSpecsStatus } from '@/test/sddFixture'
import type { SpecsStatus } from '@/types/models'

// The gesture goes to the onDispatch PROP (SpecsPanel's preview gate) — this
// surface never sends into the chat itself, so the sender is not mocked here.
const onDispatch = vi.fn()

let container: HTMLElement | null = null
let root: Root | null = null

async function render(status: SpecsStatus, reason?: 'absent' | 'empty-tree'): Promise<void> {
  onDispatch.mockClear()
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
  await act(async () => {
    root!.render(<SpecsEmptyState status={status} onDispatch={onDispatch} reason={reason} />)
  })
}

function query(testId: string): HTMLElement | null {
  return container?.querySelector(`[data-testid="${testId}"]`) ?? null
}

afterEach(() => {
  act(() => root?.unmount())
  root = null
  container = null
})

/** A detected framework whose roots are missing — the panel's empty state. */
function emptyStatus(actions: SpecsStatus['actions']): SpecsStatus {
  return makeSpecsStatus({
    tree_present: false,
    root: '',
    detected: [
      makeFramework('openspec', 'OpenSpec', {
        description: 'Delta-driven spec-driven development.',
        tree_present: false,
        root_present: false,
        marker_present: false,
      }),
    ],
    documents: [],
    nodes: [],
    views: [],
    actions,
  })
}

describe('SpecsEmptyState', () => {
  it('names the detected framework and its declared root', async () => {
    await render(emptyStatus([makeAction({ kind: 'sync', label: 'Sync', requires: ['root'] })]))
    expect(container?.textContent).toContain('No OpenSpec tree yet')
    expect(container?.textContent).toContain('Delta-driven spec-driven development.')
    expect(container?.textContent).toContain('specs')
  })

  it('offers the declared bootstrap gesture and dispatches it', async () => {
    await render(
      emptyStatus([
        makeAction({
          kind: 'init',
          label: 'Init',
          skill: 'spec-init',
          prompt: 'Initialize the specification tree.',
          requires: [],
        }),
      ]),
    )
    const cta = query('specs-initialize-cta')
    expect(cta?.getAttribute('data-skill')).toBe('spec-init')

    act(() => {
      cta?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onDispatch).toHaveBeenCalledTimes(1)
    const [pending, newSession] = onDispatch.mock.calls[0] as [
      { key: string; skill: string; prompt: string },
      boolean,
    ]
    expect(pending.prompt).toBe('Initialize the specification tree.')
    expect(pending.skill).toBe('spec-init')
    expect(newSession).toBe(false)
  })

  it('offers no CTA when every declared gesture requires a tree', async () => {
    await render(emptyStatus([makeAction({ kind: 'check', label: 'Check', requires: ['root'] })]))
    expect(query('specs-initialize-cta')).toBeNull()
    expect(container?.textContent).toContain('No OpenSpec tree yet')
  })

  it('offers no CTA when the declared bootstrap gesture is not dispatchable', async () => {
    // The backend reports the action unavailable (its skill is not installed
    // in any spelling); the empty state must not wire a live-looking gesture
    // whose dispatch would degrade to an unresolvable skill mention.
    await render(
      emptyStatus([
        makeAction({
          kind: 'init',
          label: 'Init',
          skill: 'spec-init',
          prompt: 'Initialize the specification tree.',
          requires: [],
          available: false,
        }),
      ]),
    )
    expect(query('specs-initialize-cta')).toBeNull()
    expect(onDispatch).not.toHaveBeenCalled()
    expect(container?.textContent).toContain('No OpenSpec tree yet')
  })

  it('names the present-but-empty tree instead of a missing one', async () => {
    // `tree_present` is a filesystem predicate: the root directory can exist
    // before the first document. The copy must say that, not claim the tree is
    // absent, and the bootstrap gesture stays available.
    const status = makeSpecsStatus({
      tree_present: true,
      root: '/ws/specs',
      detected: [makeFramework('openspec', 'OpenSpec', { tree_present: true, root_present: true })],
      documents: [],
      nodes: [],
      views: [],
      counts: { documents: 0, findings: 0, by_concept: {}, findings_by_kind: {} },
      actions: [makeAction({ kind: 'init', label: 'Init', skill: 'spec-init', requires: [] })],
    })
    await render(status, 'empty-tree')

    expect(query('specs-empty-state')?.getAttribute('data-reason')).toBe('empty-tree')
    expect(container?.textContent).toContain('No OpenSpec documents yet')
    expect(container?.textContent).not.toContain('No OpenSpec tree yet')
    expect(container?.textContent).toContain('/ws/specs')
    expect(query('specs-initialize-cta')?.getAttribute('data-skill')).toBe('spec-init')
  })
})
