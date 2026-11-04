import { describe, it, expect, vi, beforeEach } from 'vitest'

const getSpecsStatusMock = vi.hoisted(() => vi.fn())

vi.mock('@/api/specs', () => ({
  getSpecsStatus: getSpecsStatusMock,
}))

import { useSpecsStore } from './specsStore'
import type { SpecsStatus } from '@/types/models'

function statusOf(present: boolean, projectId: string): SpecsStatus {
  return {
    present,
    root: present ? `/ws/${projectId}/specs` : '',
    files: [],
    index_tasks: [],
    adrs: [],
    findings: [],
    counts: { files: 0, adrs: 0, findings: 0, findings_by_kind: {} },
  }
}

/** A controllable pending fetch: resolves only when the test releases it. */
interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (reason: unknown) => void
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

/** Let pending microtasks (fetch continuations) settle. */
async function flush(): Promise<void> {
  for (let i = 0; i < 10; i++) await Promise.resolve()
}

beforeEach(() => {
  getSpecsStatusMock.mockReset()
  useSpecsStore.getState().reset()
})

describe('specsStore — fetchStatus', () => {
  it('stores a successful status stamped with its projectId', async () => {
    const status = statusOf(true, 'p1')
    getSpecsStatusMock.mockResolvedValue(status)

    await useSpecsStore.getState().fetchStatus('p1')

    const s = useSpecsStore.getState()
    expect(getSpecsStatusMock).toHaveBeenCalledWith('p1')
    expect(s.status).toBe(status)
    expect(s.projectId).toBe('p1')
    expect(s.isLoading).toBe(false)
    expect(s.error).toBeNull()
  })

  it('stores the neutral present=false state as data, not an error', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(false, 'p1'))

    await useSpecsStore.getState().fetchStatus('p1')

    const s = useSpecsStore.getState()
    expect(s.status?.present).toBe(false)
    expect(s.error).toBeNull()
    expect(s.isLoading).toBe(false)
  })

  it('stores the error message and keeps the previous status on failure', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')

    getSpecsStatusMock.mockRejectedValue(new Error('parse exploded'))
    await useSpecsStore.getState().fetchStatus('p1')

    const s = useSpecsStore.getState()
    expect(s.error).toBe('parse exploded')
    expect(s.isLoading).toBe(false)
    // The stale-but-useful previous status stays rendered alongside.
    expect(s.status?.present).toBe(true)
    expect(s.projectId).toBe('p1')
  })

  it('marks isLoading for the whole flight and clears it at the end', async () => {
    const d = deferred<SpecsStatus>()
    getSpecsStatusMock.mockReturnValue(d.promise)

    const pending = useSpecsStore.getState().fetchStatus('p1')
    expect(useSpecsStore.getState().isLoading).toBe(true)

    d.resolve(statusOf(true, 'p1'))
    await pending

    expect(useSpecsStore.getState().isLoading).toBe(false)
  })

  it('discards a superseded response (last-write-wins by initiation order)', async () => {
    const first = deferred<SpecsStatus>()
    const second = deferred<SpecsStatus>()
    getSpecsStatusMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    const p1 = useSpecsStore.getState().fetchStatus('p1')
    const p2 = useSpecsStore.getState().fetchStatus('p2')

    // The OLDER fetch resolves last — it must not overwrite the newer one.
    second.resolve(statusOf(true, 'p2'))
    await p2
    first.resolve(statusOf(true, 'p1'))
    await p1
    await flush()

    const s = useSpecsStore.getState()
    expect(s.projectId).toBe('p2')
    expect(s.status?.root).toBe('/ws/p2/specs')
    expect(s.isLoading).toBe(false)
    expect(s.error).toBeNull()
  })

  it('discards a superseded error response too', async () => {
    const first = deferred<SpecsStatus>()
    const second = deferred<SpecsStatus>()
    getSpecsStatusMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)

    const p1 = useSpecsStore.getState().fetchStatus('p1')
    const p2 = useSpecsStore.getState().fetchStatus('p2')

    second.resolve(statusOf(true, 'p2'))
    await p2
    first.reject(new Error('stale failure'))
    await p1
    await flush()

    const s = useSpecsStore.getState()
    expect(s.projectId).toBe('p2')
    expect(s.status?.root).toBe('/ws/p2/specs')
    expect(s.error).toBeNull()
  })
})

describe('specsStore — reset', () => {
  it('clears status, projectId, error and isLoading', async () => {
    getSpecsStatusMock.mockResolvedValue(statusOf(true, 'p1'))
    await useSpecsStore.getState().fetchStatus('p1')

    useSpecsStore.getState().reset()

    const s = useSpecsStore.getState()
    expect(s.status).toBeNull()
    expect(s.projectId).toBeNull()
    expect(s.isLoading).toBe(false)
    expect(s.error).toBeNull()
  })
})
