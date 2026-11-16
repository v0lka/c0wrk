// @vitest-environment jsdom
// SpecsQuickActions — one button per PANEL-LEVEL action the framework
// declares: the label and the skill come from the payload, enablement is the
// backend's `available` flag (its evaluation of the declared requirements), and
// the reserved findings gesture plus node-scoped gestures never appear here.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SpecsQuickActions } from './SpecsQuickActions'
import { REQUIREMENT_UNMET_REASON } from './specsActions'
import { makeAction } from '@/test/sddFixture'
import { useSpecsStore } from '@/stores/specsStore'
import type { SDDAction } from '@/types/models'

let container: HTMLElement | null = null
let root: Root | null = null

// The gesture goes to the onDispatch PROP (SpecsPanel's preview gate) — this
// surface never sends into the chat itself, so the sender is not mocked here.
const onDispatch = vi.fn()

async function render(actions: SDDAction[]): Promise<void> {
  onDispatch.mockClear()
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
  await act(async () => {
    root!.render(<SpecsQuickActions actions={actions} onDispatch={onDispatch} />)
  })
}

function buttons(): HTMLElement[] {
  return Array.from(container?.querySelectorAll<HTMLElement>('[data-testid="specs-quick-action"]') ?? [])
}

afterEach(() => {
  act(() => root?.unmount())
  root = null
  container = null
  useSpecsStore.setState({ error: null })
})

const ACTIONS: SDDAction[] = [
  makeAction({ kind: 'init', label: 'Init', skill: 'spec-init', requires: [] }),
  makeAction({ kind: 'check', label: 'Check drift', skill: 'spec-check' }),
  makeAction({ kind: 'apply', label: 'Apply', concept: 'change', skill: 'openspec-apply-change' }),
  makeAction({ kind: 'fix-findings', label: 'Fix all', skill: 'spec-update', requires: ['root', 'findings'] }),
]

describe('SpecsQuickActions', () => {
  it('renders one button per declared panel-level action, in declaration order', async () => {
    await render(ACTIONS)
    expect(buttons().map((b) => b.textContent)).toEqual(['Init', 'Check drift'])
    expect(buttons().map((b) => b.getAttribute('data-skill'))).toEqual(['spec-init', 'spec-check'])
  })

  it('renders nothing when the framework declares no panel-level gesture', async () => {
    await render([ACTIONS[2]!, ACTIONS[3]!])
    expect(container?.childElementCount).toBe(0)
  })

  it('disables a gesture whose declared requirement is unmet, with the reason', async () => {
    await render([makeAction({ kind: 'update', label: 'Update', available: false })])
    const [button] = buttons()
    expect(button?.hasAttribute('disabled')).toBe(true)
    expect(button?.getAttribute('title')).toBe(REQUIREMENT_UNMET_REASON)
  })

  it('names the missing skill — not the tree — for a skillless declared action', async () => {
    await render([makeAction({ kind: 'init', label: 'Init', skill: '', available: true })])
    const [button] = buttons()
    expect(button?.hasAttribute('disabled')).toBe(true)
    expect(button?.getAttribute('title')).toBe(
      'The framework declares this action with no skill to run',
    )
  })

  it('hands the declared skill and prompt to the panel dispatch gate', async () => {
    await render([ACTIONS[1]!])
    act(() => {
      buttons()[0]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onDispatch).toHaveBeenCalledTimes(1)
    const [pending, newSession] = onDispatch.mock.calls[0] as [
      { key: string; skill: string; prompt: string },
      boolean,
    ]
    expect(pending.skill).toBe('spec-check')
    expect(pending.prompt).toBe('Check the specifications for drift.')
    expect(pending.key).toBe('check')
    expect(newSession).toBe(false)
  })

  it('routes a Shift-click into a new session', async () => {
    await render([ACTIONS[1]!])
    act(() => {
      buttons()[0]?.dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }))
    })
    expect((onDispatch.mock.calls[0] as unknown[])[1]).toBe(true)
  })

  it('does not dispatch a disabled gesture', async () => {
    await render([makeAction({ kind: 'create', label: 'Create', available: false })])
    act(() => {
      buttons()[0]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onDispatch).not.toHaveBeenCalled()
  })

  // A plain record lookup also reads INHERITED names: `constructor` is a
  // VALID operator action slug, and without an own-property guard the icon
  // lookup resolves to Object.prototype.constructor, which React tries to
  // render as a component — crashing the whole app on panel open. (Review
  // 28, MUST FIX.) An unknown slug takes the generic icon and must render
  // the same way.
  it('renders a `constructor` action kind without crashing (own-property icon lookup)', async () => {
    await render([
      makeAction({ kind: 'constructor', label: 'Construct specifications', skill: 'spec-init', requires: [] }),
      makeAction({ kind: 'totally-unknown-kind', label: 'Mystery', skill: 'spec-check', requires: [] }),
    ])
    expect(buttons().map((b) => b.textContent)).toEqual(['Construct specifications', 'Mystery'])
    expect(container?.querySelector('[data-testid="specs-quick-actions"]')).not.toBeNull()
    expect(onDispatch).not.toHaveBeenCalled()
  })
})
