// The status bar pill and the vector search panel both render off
// `vectorIndexStore.status`. Push events alone left it on its `idle` default at
// startup (nothing emitted for an already-built index), so the bar was empty
// and the search panel claimed "Select a project to search" while a project was
// selected. These tests pin the seed/refresh contract of useVectorIndexStatus.

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const runtime = vi.hoisted(() => {
  const subs = new Map<string, Array<(data: unknown) => void>>()
  return {
    subs,
    subscribe: (event: string, cb: (data: unknown) => void) => {
      const list = subs.get(event) ?? []
      list.push(cb)
      subs.set(event, list)
      return () => {
        const current = subs.get(event)
        if (!current) return
        const i = current.indexOf(cb)
        if (i >= 0) current.splice(i, 1)
      }
    },
  }
})

const getStatusSpy = vi.hoisted(() => vi.fn())

vi.mock('@/api/runtime', () => ({ subscribe: runtime.subscribe }))
vi.mock('@/api/vector', () => ({ getVectorIndexStatus: getStatusSpy }))

import { useVectorIndexStatus } from '@/hooks/useVectorIndexStatus'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { useProjectStore } from '@/stores/projectStore'
import type { VectorIndexStatus } from '@/types/models'

function statusOf(state: VectorIndexStatus['state']): VectorIndexStatus {
  return { state, progress: 0, files_indexed: 0, total_files: 0 }
}

function emit(event: string, data: unknown): void {
  for (const cb of [...(runtime.subs.get(event) ?? [])]) cb(data)
}

function Harness() {
  useVectorIndexStatus()
  return null
}

let container: HTMLElement
let root: Root

async function render(): Promise<void> {
  await act(async () => {
    root.render(<Harness />)
  })
}

beforeEach(() => {
  runtime.subs.clear()
  getStatusSpy.mockReset()
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
})

describe('useVectorIndexStatus — initial seed', () => {
  it('seeds the store from GetVectorIndexStatus on mount, replacing the idle default', async () => {
    getStatusSpy.mockResolvedValue(statusOf('ready'))
    await render()

    expect(getStatusSpy).toHaveBeenCalledTimes(1)
    expect(useVectorIndexStore.getState().status.state).toBe('ready')
  })

  it('re-seeds when the active project changes', async () => {
    getStatusSpy.mockResolvedValue(statusOf('indexing'))
    await render()
    expect(useVectorIndexStore.getState().status.state).toBe('indexing')

    getStatusSpy.mockResolvedValue(statusOf('unavailable'))
    await act(async () => {
      useProjectStore.setState({ activeProjectId: 'proj-2' })
    })

    expect(getStatusSpy).toHaveBeenCalledTimes(2)
    expect(useVectorIndexStore.getState().status.state).toBe('unavailable')
  })

  it('keeps the idle default when the fetch rejects (backend not ready)', async () => {
    getStatusSpy.mockRejectedValue(new Error('not ready'))
    await render()

    expect(useVectorIndexStore.getState().status.state).toBe('idle')
  })

  it('ignores a malformed getter payload', async () => {
    getStatusSpy.mockResolvedValue({ bogus: true } as unknown as VectorIndexStatus)
    await render()

    expect(useVectorIndexStore.getState().status.state).toBe('idle')
  })
})

describe('useVectorIndexStatus — push + backend:ready', () => {
  it('applies pushed vector_index:status events', async () => {
    getStatusSpy.mockResolvedValue(statusOf('idle'))
    await render()

    await act(async () => {
      emit('vector_index:status', { ...statusOf('reindexing'), files_indexed: 3, total_files: 9 })
    })

    expect(useVectorIndexStore.getState().status.state).toBe('reindexing')
    expect(useVectorIndexStore.getState().status.files_indexed).toBe(3)
  })

  it('re-seeds once the backend signals readiness', async () => {
    getStatusSpy.mockResolvedValue(statusOf('idle'))
    await render()
    expect(getStatusSpy).toHaveBeenCalledTimes(1)

    getStatusSpy.mockResolvedValue(statusOf('ready'))
    await act(async () => {
      emit('backend:ready', undefined)
    })

    expect(getStatusSpy).toHaveBeenCalledTimes(2)
    expect(useVectorIndexStore.getState().status.state).toBe('ready')
  })

  it('drops a pushed payload that fails the type guard', async () => {
    getStatusSpy.mockResolvedValue(statusOf('ready'))
    await render()

    await act(async () => {
      emit('vector_index:status', { state: 'bogus' })
    })

    expect(useVectorIndexStore.getState().status.state).toBe('ready')
  })
})
