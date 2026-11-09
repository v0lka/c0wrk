// Behavior of the force-full-reindex action's state machine (useVectorReindex):
// the ADR-064 `loading` open and the ADR-104 stage-2 pre-work states
// (`preparing` view-model attach window / `migration-rebuild` store promotion)
// read as busy with their own tooltips; the terminal `unavailable` state (a
// failed open/attach) keeps the action ENABLED — it is the user's repair path:
// the backend escalates a request with no live collection into a re-attach
// that rebuilds an unopenable corpus from source. It therefore renders without
// the busy spinner, labels itself a rebuild, and clicking it DOES fire the RPC.
// Uses the real stores; only the RPC boundary (@/api/vector) is mocked.

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const reindexSpy = vi.hoisted(() => vi.fn())

vi.mock('@/api/vector', () => ({
  reindexVectorIndex: reindexSpy,
}))

import { useVectorReindex } from '@/hooks/useVectorReindex'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { useProjectStore } from '@/stores/projectStore'
import type { VectorIndexStatus } from '@/types/models'

const statusWith = (state: VectorIndexStatus['state']): VectorIndexStatus => ({
  state,
  progress: 0,
  files_indexed: 0,
  total_files: 0,
})

// Harness: capture the latest hook return so tests can assert and click.
let latest: ReturnType<typeof useVectorReindex> | null = null
function Harness() {
  latest = useVectorReindex()
  return null
}

/** The captured hook return, failing loudly if never rendered. */
function hook(): ReturnType<typeof useVectorReindex> {
  if (!latest) throw new Error('harness did not capture the hook return')
  return latest
}

let container: HTMLElement
let root: Root

beforeEach(() => {
  reindexSpy.mockReset()
  reindexSpy.mockResolvedValue(undefined)
  act(() => {
    useVectorIndexStore.getState().reset()
    useProjectStore.setState({ activeProjectId: 'proj-1' })
  })
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  document.body.replaceChildren()
  act(() => {
    useProjectStore.setState({ activeProjectId: null })
  })
})

describe('useVectorReindex — the terminal unavailable state', () => {
  it('keeps the repair action enabled, without the busy spinner, labelled as a rebuild', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('unavailable') })
    })
    act(() => {
      root.render(<Harness />)
    })
    // The repair path must stay reachable from the UI: no dead end.
    expect(hook().reindexDisabled).toBe(false)
    // Nothing is running: unavailable is terminal, not busy — no spinner.
    expect(hook().reindexBusy).toBe(false)
    // The enabled label promises the repair, not an incremental pass.
    expect(hook().reindexActionTitle).toBe(
      'Rebuild index — recreate the broken index from source',
    )
  })

  it('fires the reindex RPC while the index is unavailable (the backend escalates to a rebuild)', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('unavailable') })
    })
    act(() => {
      root.render(<Harness />)
    })
    act(() => {
      hook().handleReindex()
    })
    expect(reindexSpy).toHaveBeenCalledTimes(1)
    // The optimistic latch owns the busy state until a status event lands, and
    // its label names the repair rather than an incremental pass.
    expect(hook().reindexBusy).toBe(true)
    expect(hook().reindexBusyTitle).toBe('Repairing index…')
  })

  it('releases a pending optimistic latch when the index turns unavailable', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('ready') })
    })
    act(() => {
      root.render(<Harness />)
    })
    act(() => {
      hook().handleReindex()
    })
    // The optimistic latch owns the busy state until a status event lands.
    expect(hook().reindexBusy).toBe(true)
    // The backend reports the failed repair instead of a busy state: the latch
    // must release, leaving the action available for another attempt.
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('unavailable') })
    })
    expect(hook().reindexBusy).toBe(false)
    expect(hook().reindexDisabled).toBe(false)
  })
})

describe('useVectorReindex — the ADR-104 stage-2 pre-work states', () => {
  it.each([
    ['preparing', 'Preparing index…'],
    ['migration-rebuild', 'Migrating store…'],
  ] as const)(
    'counts %s as busy with its own honest tooltip and never fires the RPC',
    (state, title) => {
      act(() => {
        useVectorIndexStore.setState({ status: statusWith(state) })
      })
      act(() => {
        root.render(<Harness />)
      })
      // Pre-work, not a pass — but busy: a reindex fired now would race the
      // attach/migration. Disabled WITH the spinner, never the plain
      // 'Reindexing...' label.
      expect(hook().reindexBusy).toBe(true)
      expect(hook().reindexDisabled).toBe(true)
      expect(hook().reindexDisabledTitle).toBe(title)
      act(() => {
        hook().handleReindex()
      })
      expect(reindexSpy).not.toHaveBeenCalled()
    },
  )

  it('releases a pending optimistic latch when the index enters pre-work', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('ready') })
    })
    act(() => {
      root.render(<Harness />)
    })
    act(() => {
      hook().handleReindex()
    })
    expect(hook().reindexBusy).toBe(true)
    // The store flips to the attach window instead of a pass: the state owns
    // the disabled state from here (isBusy), so the latch must release —
    // otherwise it would re-arm on every subsequent status event and mask
    // the transition out of pre-work.
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('preparing') })
    })
    expect(hook().reindexBusy).toBe(true)
    expect(hook().reindexDisabledTitle).toBe('Preparing index…')
  })
})

describe('useVectorReindex — busy states (unchanged)', () => {
  it('counts the ADR-064 DB open as busy with its own tooltip', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('loading') })
    })
    act(() => {
      root.render(<Harness />)
    })
    expect(hook().reindexDisabled).toBe(true)
    expect(hook().reindexBusy).toBe(true)
    expect(hook().reindexDisabledTitle).toBe('Opening the index…')
  })

  it('reports a plain click as busy during the optimistic latch', () => {
    act(() => {
      useVectorIndexStore.setState({ status: statusWith('ready') })
    })
    act(() => {
      root.render(<Harness />)
    })
    expect(hook().reindexDisabled).toBe(false)
    act(() => {
      hook().handleReindex()
    })
    expect(reindexSpy).toHaveBeenCalledTimes(1)
    // The optimistic latch owns the disabled state until a status event lands.
    expect(hook().reindexBusy).toBe(true)
    expect(hook().reindexDisabled).toBe(true)
    expect(hook().reindexDisabledTitle).toBe('Reindexing...')
  })
})
