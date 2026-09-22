// Unit tests for runGitOperation — the single wrapper that runs a git
// operation and records its outcome in the per-project git-panel slice.
//
// jsdom environment: importing gitPanelStore pulls in zustand's persist
// middleware, which resolves `window.localStorage` at import time (the store's
// own test opts into jsdom for the same reason).
// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { runGitOperation, gitOperationErrorMessage } from '@/lib/gitOperation'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { logger } from '@/lib/logger'

/** Reset the store to initial state before each test. */
function resetStore() {
  useGitPanelStore.getState().reset()
}

/** The stored operation record for a project, if any. */
function recordFor(projectId: string) {
  return useGitPanelStore.getState().operationByProject[projectId]
}

/** A promise whose resolution is controlled by the test. */
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

describe('runGitOperation', () => {
  beforeEach(() => {
    resetStore()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  // ── Success path ──

  it('records a successful operation and returns its result', async () => {
    const outcome = await runGitOperation({
      projectId: 'proj-a',
      kind: 'push',
      label: 'Pushed to origin',
      fn: async () => 'Everything up-to-date',
    })

    expect(outcome).toEqual({ ok: true, result: 'Everything up-to-date', error: null })
    const rec = recordFor('proj-a')!
    expect(rec).toMatchObject({
      kind: 'push',
      label: 'Pushed to origin',
      ok: true,
      output: 'Everything up-to-date',
      error: null,
      acknowledged: false,
    })
    expect(typeof rec.at).toBe('number')
  })

  it('uses extractOutput to project a structured result onto the record output', async () => {
    const outcome = await runGitOperation({
      projectId: 'proj-a',
      kind: 'commit',
      label: 'Committed',
      fn: async () => ({ sha: 'abc123', output: 'hook output' }),
      extractOutput: (r) => r.output,
    })

    expect(outcome).toEqual({
      ok: true,
      result: { sha: 'abc123', output: 'hook output' },
      error: null,
    })
    expect(recordFor('proj-a')!.output).toBe('hook output')
    expect(recordFor('proj-a')!.ok).toBe(true)
  })

  it('defaults the output to empty for a non-string result', async () => {
    const outcome = await runGitOperation({
      projectId: 'proj-a',
      kind: 'stage',
      label: 'Staged',
      fn: async () => 42,
    })

    expect(outcome.ok).toBe(true)
    expect(recordFor('proj-a')!.output).toBe('')
    expect(recordFor('proj-a')!.ok).toBe(true)
  })

  // ── Failure path ──

  it('records a failure with the error message and ok=false', async () => {
    const outcome = await runGitOperation({
      projectId: 'proj-a',
      kind: 'pull',
      label: 'Pull from origin',
      fn: async () => {
        throw new Error('CONFLICT (content): Merge conflict in src/app.ts')
      },
    })

    expect(outcome).toEqual({
      ok: false,
      result: undefined,
      error: 'CONFLICT (content): Merge conflict in src/app.ts',
    })
    expect(recordFor('proj-a')).toMatchObject({
      kind: 'pull',
      label: 'Pull from origin',
      ok: false,
      output: '',
      error: 'CONFLICT (content): Merge conflict in src/app.ts',
      acknowledged: false,
    })
    expect(typeof recordFor('proj-a')!.at).toBe('number')
  })

  it('captures errors instead of rethrowing (the promise never rejects)', async () => {
    await expect(
      runGitOperation({
        projectId: 'proj-a',
        kind: 'fetch',
        label: 'Fetched',
        fn: async () => {
          throw new Error('boom')
        },
      }),
    ).resolves.toEqual({ ok: false, result: undefined, error: 'boom' })
  })

  it('logs failures but not successes', async () => {
    const errorSpy = vi.spyOn(logger, 'error')

    await runGitOperation({
      projectId: 'proj-a',
      kind: 'stage',
      label: 'Staged',
      fn: async () => 'ok',
    })
    expect(errorSpy).not.toHaveBeenCalled()

    await runGitOperation({
      projectId: 'proj-a',
      kind: 'pull',
      label: 'Pull',
      fn: async () => {
        throw new Error('nope')
      },
    })
    expect(errorSpy).toHaveBeenCalledTimes(1)
  })

  it('logs a failure at warn when the call site opts into warn severity', async () => {
    const errorSpy = vi.spyOn(logger, 'error')
    const warnSpy = vi.spyOn(logger, 'warn')

    await runGitOperation({
      projectId: 'proj-a',
      kind: 'discard',
      label: 'Discarded changes in a.ts',
      recordSuccess: false,
      logLevel: 'warn',
      fn: async () => {
        throw new Error('nothing to discard')
      },
    })

    expect(warnSpy).toHaveBeenCalledTimes(1)
    expect(errorSpy).not.toHaveBeenCalled()
  })

  it('keeps ERROR severity independent of recordSuccess', async () => {
    const errorSpy = vi.spyOn(logger, 'error')
    const warnSpy = vi.spyOn(logger, 'warn')

    // `recordSuccess: false` alone must NOT downgrade the log: severity is the
    // call site's explicit `logLevel` choice, not implied by the recording flag.
    await runGitOperation({
      projectId: 'proj-a',
      kind: 'stage',
      label: 'Staged a.ts',
      recordSuccess: false,
      fn: async () => {
        throw new Error('boom')
      },
    })

    expect(errorSpy).toHaveBeenCalledTimes(1)
    expect(warnSpy).not.toHaveBeenCalled()
  })

  // ── Outcome / record lifecycle ──

  it('replaces the project record with the latest operation', async () => {
    await runGitOperation({
      projectId: 'proj-a',
      kind: 'push',
      label: 'Pushed',
      fn: async () => 'first',
    })
    await runGitOperation({
      projectId: 'proj-a',
      kind: 'commit',
      label: 'Committed',
      fn: async () => {
        throw new Error('second failed')
      },
    })

    const rec = recordFor('proj-a')!
    expect(rec.kind).toBe('commit')
    expect(rec.ok).toBe(false)
    expect(rec.error).toBe('second failed')
    expect(rec.output).toBe('')
  })

  it('resets acknowledgement when a new operation is recorded', async () => {
    await runGitOperation({
      projectId: 'proj-a',
      kind: 'push',
      label: 'Push',
      fn: async () => 'ok',
    })
    useGitPanelStore.getState().acknowledgeOperation('proj-a')
    expect(recordFor('proj-a')!.acknowledged).toBe(true)

    await runGitOperation({
      projectId: 'proj-a',
      kind: 'fetch',
      label: 'Fetch',
      fn: async () => 'ok',
    })
    expect(recordFor('proj-a')!.acknowledged).toBe(false)
  })

  // ── Per-project isolation ──

  it('keeps records isolated per project', async () => {
    await runGitOperation({
      projectId: 'proj-a',
      kind: 'push',
      label: 'A push',
      fn: async () => 'A output',
    })
    await runGitOperation({
      projectId: 'proj-b',
      kind: 'pull',
      label: 'B pull',
      fn: async () => {
        throw new Error('B failed')
      },
    })

    expect(recordFor('proj-a')).toMatchObject({
      kind: 'push',
      ok: true,
      output: 'A output',
      error: null,
    })
    expect(recordFor('proj-b')).toMatchObject({
      kind: 'pull',
      ok: false,
      output: '',
      error: 'B failed',
    })
  })

  it('attributes a result to the project captured at call time under a concurrent switch', async () => {
    // Start A but leave it pending; complete B in the meantime. A's eventual
    // record must still land under proj-a — the id was captured at call time.
    const a = deferred<string>()
    const aPromise = runGitOperation({
      projectId: 'proj-a',
      kind: 'push',
      label: 'A push',
      fn: () => a.promise,
    })

    await runGitOperation({
      projectId: 'proj-b',
      kind: 'fetch',
      label: 'B fetch',
      fn: async () => 'B done',
    })
    expect(recordFor('proj-b')!.kind).toBe('fetch')
    expect(recordFor('proj-a')).toBeUndefined()

    a.resolve('A done')
    const outcome = await aPromise
    expect(outcome).toEqual({ ok: true, result: 'A done', error: null })
    expect(recordFor('proj-a')!.output).toBe('A done')
    // B's record is untouched.
    expect(recordFor('proj-b')!.kind).toBe('fetch')
  })
})

describe('gitOperationErrorMessage', () => {
  it('returns an Error message as-is', () => {
    expect(gitOperationErrorMessage(new Error('boom'))).toBe('boom')
  })

  it('stringifies a non-Error thrown value', () => {
    expect(gitOperationErrorMessage('plain string')).toBe('plain string')
    expect(gitOperationErrorMessage(42)).toBe('42')
  })
})

describe('runGitOperation — recordSuccess: false', () => {
  beforeEach(() => {
    resetStore()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('does not record a successful operation (success is silent)', async () => {
    const outcome = await runGitOperation({
      projectId: 'proj-a',
      kind: 'stage',
      label: 'Staged foo.ts',
      fn: async () => 'staged',
      recordSuccess: false,
    })

    expect(outcome).toEqual({ ok: true, result: 'staged', error: null })
    expect(recordFor('proj-a')).toBeUndefined()
  })

  it('still records a failure when recordSuccess is false', async () => {
    const outcome = await runGitOperation({
      projectId: 'proj-a',
      kind: 'discard',
      label: 'Discarded foo.ts',
      fn: async () => {
        throw new Error('discard failed')
      },
      recordSuccess: false,
    })

    expect(outcome).toEqual({ ok: false, result: undefined, error: 'discard failed' })
    expect(recordFor('proj-a')).toMatchObject({
      kind: 'discard',
      ok: false,
      error: 'discard failed',
    })
  })
})
