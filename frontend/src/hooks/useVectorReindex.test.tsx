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
const emitSpy = vi.hoisted(() => vi.fn())

vi.mock('@/api/vector', () => ({
  reindexVectorIndex: reindexSpy,
}))

// The hook's other runtime touchpoints: `subscribe` (the missed-status
// release) as a no-op — these tests drive the store directly — and `emit`
// spied, because a rejected reindex RPC must surface through the app-wide
// runtime_error toast instead of a console-only log.
vi.mock('@/api/runtime', () => ({
  emit: emitSpy,
  subscribe: () => () => {},
}))

import { useVectorReindex } from '@/hooks/useVectorReindex'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { useProjectStore } from '@/stores/projectStore'
import type { VectorIndexErrorKind, VectorIndexStatus } from '@/types/models'

const statusWith = (state: VectorIndexStatus['state']): VectorIndexStatus => ({
  state,
  progress: 0,
  files_indexed: 0,
  total_files: 0,
})

const unavailableWith = (kind?: VectorIndexErrorKind): VectorIndexStatus => ({
  ...statusWith('unavailable'),
  last_error: kind ? `the backend's own cause (${kind})` : undefined,
  error_kind: kind,
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

/** Flushes the reindex RPC's promise settlement inside act, so the latch's
 *  release-on-resolve/reject (a React state update in a microtask) never
 *  lands outside act and warn. */
async function settleRpc() {
  await act(async () => {
    await Promise.resolve()
  })
}

let container: HTMLElement
let root: Root

beforeEach(() => {
  reindexSpy.mockReset()
  reindexSpy.mockResolvedValue(undefined)
  emitSpy.mockClear()
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

  it('labels the suspended kind as a resume, not a rebuild', () => {
    // ADR-124: with the breaker tripped the collection is live and the same
    // manager-level Reindex runs a validation-based reconcile that resumes
    // indexing (the breaker's own reset path) — the title must not promise
    // a rebuild-from-source it does not perform.
    act(() => {
      useVectorIndexStore.setState({
        status: { ...statusWith('unavailable'), error_kind: 'suspended' },
      })
    })
    act(() => {
      root.render(<Harness />)
    })
    expect(hook().reindexDisabled).toBe(false)
    expect(hook().reindexActionTitle).toBe(
      'Resume indexing — rerun the pass and re-arm file changes',
    )
  })

  it('keeps the Resume label on a READY status carrying the suspended verdict', () => {
    // The pair-persistence corner: a failing branch-switch pass after the
    // trip ends in a terminal ready report, and the suspended kind rides it —
    // the state is ready (search answers) while file-change indexing stays
    // suspended. The action is the same breaker reset, so the label must
    // stay Resume instead of falling back to the plain force-reindex title.
    act(() => {
      useVectorIndexStore.setState({
        status: {
          ...statusWith('ready'),
          error_kind: 'suspended',
          last_error: 'automatic incremental indexing suspended after 5 consecutive failed passes',
        },
      })
    })
    act(() => {
      root.render(<Harness />)
    })
    expect(hook().reindexDisabled).toBe(false)
    expect(hook().reindexActionTitle).toBe(
      'Resume indexing — rerun the pass and re-arm file changes',
    )
  })

  it('fires the reindex RPC while the index is unavailable (the backend escalates to a rebuild)', async () => {
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
    await settleRpc()
  })

  it('releases a pending optimistic latch when the index turns unavailable', async () => {
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
    await settleRpc()
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

  it('releases a pending optimistic latch when the index enters pre-work', async () => {
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
    await settleRpc()
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

  it('reports a plain click as busy during the optimistic latch', async () => {
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
    await settleRpc()
  })
})

describe('useVectorReindex — the repair gate (error_kind)', () => {
  // The kinds the repair escalation actually addresses: a proven store
  // defect, an OS fault (the re-attach retries the open once it clears), the
  // ADR-124 circuit breaker (the manager-level Reindex is the breaker's own
  // reset path), the defensive unknown, and a missing kind (never dead-end
  // the user).
  it.each([
    ['repairable'],
    ['transient'],
    ['suspended'],
    ['unknown'],
    [undefined],
  ] as const)('keeps the repair action enabled for an unavailable index of kind %s', async (kind) => {
    act(() => {
      useVectorIndexStore.setState({ status: unavailableWith(kind) })
    })
    act(() => {
      root.render(<Harness />)
    })
    expect(hook().reindexDisabled).toBe(false)
    expect(hook().reindexBusy).toBe(false)
    // The ADR-124 suspension is the resume-shaped exception: the collection
    // is live and the same RPC runs the reconcile that re-arms file changes.
    expect(hook().reindexActionTitle).toBe(
      kind === 'suspended'
        ? 'Resume indexing — rerun the pass and re-arm file changes'
        : 'Rebuild index — recreate the broken index from source',
    )
    act(() => {
      hook().handleReindex()
    })
    expect(reindexSpy).toHaveBeenCalledTimes(1)
    await settleRpc()
  })

  // The registry-level kinds deterministically reject the RPC before any
  // repair runs (a dead embedder, no focus root, the shutdown window), so the
  // action must not promise a fix it cannot deliver — and must not fire the
  // doomed request either.
  it.each([
    ['fatal', 'A rebuild cannot fix this failure — see the status cause below'],
    ['no_target', 'No focused workspace to reindex'],
    ['shutting_down', 'App is shutting down'],
  ] as const)(
    'disables the action with a reason-specific title for kind %s and never fires the RPC',
    (kind, title) => {
      act(() => {
        useVectorIndexStore.setState({ status: unavailableWith(kind) })
      })
      act(() => {
        root.render(<Harness />)
      })
      expect(hook().reindexDisabled).toBe(true)
      // Not busy: nothing is running, so no spinner — the disabled state is
      // the kind gate, exactly like the terminal `disabled` state.
      expect(hook().reindexBusy).toBe(false)
      expect(hook().reindexDisabledTitle).toBe(title)
      act(() => {
        hook().handleReindex()
      })
      expect(reindexSpy).not.toHaveBeenCalled()
      expect(emitSpy).not.toHaveBeenCalled()
    },
  )
})

describe('useVectorReindex — the RPC settlement', () => {
  it('releases the optimistic latch when the RPC resolves (the pass is scheduled)', async () => {
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
    // The RPC resolves once the pass is scheduled: the latch releases (the
    // first busy status event owns the disabled state from then on; until it
    // lands a duplicate click merely restarts the cancel-and-restart pass).
    await act(async () => {
      await Promise.resolve()
    })
    expect(hook().reindexBusy).toBe(false)
    expect(hook().reindexDisabled).toBe(false)
  })

  it('releases the latch and surfaces a runtime_error toast when the RPC rejects', async () => {
    reindexSpy.mockRejectedValueOnce(
      new Error('vector index not available: vector search not ready'),
    )
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
    await act(async () => {
      await Promise.resolve()
    })
    // The refusal is never a silent no-op: the latch releases for a retry
    // and the app-wide toast carries the backend's routing cause.
    expect(hook().reindexBusy).toBe(false)
    expect(emitSpy).toHaveBeenCalledTimes(1)
    expect(emitSpy).toHaveBeenCalledWith(
      'runtime_error',
      expect.objectContaining({
        id: expect.any(String),
        message: 'Index repair failed: vector index not available: vector search not ready',
      }),
    )
  })
})
