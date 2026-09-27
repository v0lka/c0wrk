// @vitest-environment node
//
// embeddedLLMSync — the RE-ENTRANCY contract of the two disable windows.
//
// `embeddedLLMStore.test.ts` pins the store's reducers and one action at a
// time; this file pins what happens when two overlap, which is reachable from
// a single physical click inside Settings → LLM → Embedded LLM: a Radix menu
// opened before the first action keeps its portaled items clickable (`disabled`
// only reaches the TRIGGER), and clicking one of them while a dirty NumberField
// still holds focus produces TWO commits — the field's `focusout` flushes
// synchronously during `mousedown`, then the item's own `click` commits again.
//
// Both windows are therefore COUNTED, and each one closes at zero only:
//   - `busy` — the mutating runner's window. An early clear re-enables every
//     embedded-LLM control while another write is still pending.
//   - `tuningLoading` — the tuning read window. An early clear re-enables the
//     knobs against a snapshot the sibling write predates, so the next commit
//     would persist a patch derived from values the user never saw.
//
// Every test here drives the overlap by holding a read-back open with a
// deferred promise: without control over WHEN each read lands, the two windows
// cannot be observed separately.

import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  getEmbeddedLLMStatus: vi.fn(),
  getEmbeddedLLMTuning: vi.fn(),
  onState: vi.fn(),
  onProgress: vi.fn(),
  invalidateConfigCache: vi.fn(),
}))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

// The sync module invalidates the shared model cache on an install transition;
// stub it so the node env never pulls the real cache in.
vi.mock('@/hooks/useConfigData', () => ({
  invalidateConfigCache: mocks.invalidateConfigCache,
}))

vi.mock('@/api/embedded', () => ({
  getEmbeddedLLMStatus: mocks.getEmbeddedLLMStatus,
  onEmbeddedLLMState: mocks.onState,
  onEmbeddedLLMInstallProgress: mocks.onProgress,
}))

vi.mock('@/api/embeddedTuning', () => ({
  getEmbeddedLLMTuning: mocks.getEmbeddedLLMTuning,
}))

import { refreshEmbeddedLLMTuning, runEmbeddedLLMAction } from './embeddedLLMSync'
import { useEmbeddedLLMStore } from './embeddedLLMStore'
import type { EmbeddedLLMTuning } from '@/api/embeddedTuning'
import { makeTuning } from '@/test/embeddedTuningFixture'

const state = () => useEmbeddedLLMStore.getState()

/** A promise whose settlement the test controls. */
interface Deferred<T> {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (reason: unknown) => void
}

function deferred<T = void>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

/** Let the microtask queue run so a settled read-back reaches its `finally`. */
async function tick(): Promise<void> {
  for (let i = 0; i < 4; i += 1) await Promise.resolve()
}

beforeEach(() => {
  vi.clearAllMocks()
  // `clearAllMocks` keeps implementations, so re-arm the default read here
  // instead of relying on whatever the previous test queued.
  mocks.getEmbeddedLLMTuning.mockResolvedValue(makeTuning())
  state().reset()
})

describe('embeddedLLMSync — runEmbeddedLLMAction with two actions in flight', () => {
  it('keeps `busy` set until BOTH overlapping read-backs have landed', async () => {
    const first = deferred<void>()
    const second = deferred<void>()

    const a = runEmbeddedLLMAction('tuning', async () => {}, () => first.promise)
    await tick()
    const b = runEmbeddedLLMAction('tuning', async () => {}, () => second.promise)
    await tick()
    expect(state().busy).toBe('tuning')

    // The FIRST read-back lands while the sibling write is still pending. This
    // is the exact early clear the window exists to prevent: without the tally
    // this `finally` re-enabled every tuning control on the stale snapshot.
    first.resolve()
    await tick()
    expect(state().busy).toBe('tuning')

    second.resolve()
    await Promise.all([a, b])
    expect(state().busy).toBeNull()
  })

  it('lets the FIRST action name the window and keeps that name while it runs', async () => {
    const load = deferred<void>()
    const tuning = deferred<void>()

    const a = runEmbeddedLLMAction('load', async () => {}, () => load.promise)
    await tick()
    const b = runEmbeddedLLMAction('tuning', async () => {}, () => tuning.promise)
    await tick()
    // A quick write overlapping a long `load` must not rename the spinner.
    expect(state().busy).toBe('load')

    tuning.resolve()
    await b
    expect(state().busy).toBe('load')

    load.resolve()
    await a
    expect(state().busy).toBeNull()
  })

  it('releases the window once when one of two overlapping read-backs throws', async () => {
    const good = deferred<void>()

    const throwing = runEmbeddedLLMAction('tuning', async () => {}, () =>
      Promise.reject(new Error('read-back drift')),
    )
    const sibling = runEmbeddedLLMAction('tuning', async () => {}, () => good.promise)
    await tick()

    // A throwing read-back still must not wedge the window shut — but it also
    // must not close it for its sibling.
    await throwing
    expect(state().busy).toBe('tuning')

    good.resolve()
    await sibling
    expect(state().busy).toBeNull()
  })

  it('returns the tally to zero, so a later single action arms and closes its own window', async () => {
    await runEmbeddedLLMAction('tuning', async () => {}, async () => {})
    expect(state().busy).toBeNull()

    const read = deferred<void>()
    const run = runEmbeddedLLMAction('unload', async () => {}, () => read.promise)
    await tick()
    expect(state().busy).toBe('unload')

    read.resolve()
    await run
    expect(state().busy).toBeNull()
  })

  it('still arms the window and captures a rejection when nothing overlaps', async () => {
    const read = deferred<void>()
    const run = runEmbeddedLLMAction(
      'tuning',
      async () => {
        throw new Error('embedded_llm.tuning.parallel 0 is not valid')
      },
      () => read.promise,
    )
    await tick()
    expect(state().busy).toBe('tuning')

    read.resolve()
    await run
    expect(state().error).toBe('embedded_llm.tuning.parallel 0 is not valid')
    expect(state().busy).toBeNull()
  })
})

describe('embeddedLLMSync — refreshEmbeddedLLMTuning with two reads in flight', () => {
  it('keeps `tuningLoading` set until BOTH overlapping reads have landed', async () => {
    const first = deferred<EmbeddedLLMTuning>()
    const second = deferred<EmbeddedLLMTuning>()
    mocks.getEmbeddedLLMTuning
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)

    const a = refreshEmbeddedLLMTuning()
    const b = refreshEmbeddedLLMTuning()
    expect(state().tuningLoading).toBe(true)

    const stale = makeTuning({ parallel: 2 })
    first.resolve(stale)
    await a
    expect(state().tuning).toBe(stale)
    // `setTuning` clears the flag as part of applying a snapshot: the sibling
    // read has to re-arm it, or a commit in this window folds a patch derived
    // from values the user never saw rendered.
    expect(state().tuningLoading).toBe(true)

    const fresh = makeTuning({ parallel: 4 })
    second.resolve(fresh)
    await b
    expect(state().tuning).toBe(fresh)
    expect(state().tuningLoading).toBe(false)
  })

  it('keeps the window open when a FAILING read has a pending sibling', async () => {
    const good = deferred<EmbeddedLLMTuning>()
    mocks.getEmbeddedLLMTuning
      .mockRejectedValueOnce(new Error('not ready'))
      .mockReturnValueOnce(good.promise)

    const failing = refreshEmbeddedLLMTuning()
    const sibling = refreshEmbeddedLLMTuning()

    await expect(failing).resolves.toBe(false)
    expect(state().tuningLoading).toBe(true)

    good.resolve(makeTuning({ parallel: 8 }))
    await expect(sibling).resolves.toBe(true)
    expect(state().tuningLoading).toBe(false)
  })

  it('closes the window on a lone failed read — the clear lives in the finally', async () => {
    mocks.getEmbeddedLLMTuning.mockRejectedValue(new Error('not ready'))

    await expect(refreshEmbeddedLLMTuning()).resolves.toBe(false)

    expect(state().tuningLoading).toBe(false)
    expect(state().error).toBeNull()
  })

  it('returns the tally to zero, so a later lone read arms and clears the flag', async () => {
    await refreshEmbeddedLLMTuning()
    expect(state().tuningLoading).toBe(false)

    const pending = refreshEmbeddedLLMTuning()
    expect(state().tuningLoading).toBe(true)
    await pending
    expect(state().tuningLoading).toBe(false)
  })
})
