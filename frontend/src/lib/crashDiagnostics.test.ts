// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

const { logErrorMock } = vi.hoisted(() => ({ logErrorMock: vi.fn<(message: string) => void>() }))

vi.mock('@/api/runtime', () => ({
  logError: logErrorMock,
}))

import { reportCrash } from './crashDiagnostics'

interface Dump {
  kind: string
  boundary: string
  error: { name: string; message: string; stack: string | null }
  componentStack: string | null
  context: Record<string, unknown>
}

function readRing(): Dump[] {
  const raw = localStorage.getItem('c0wrk-crash-dump')
  return raw ? (JSON.parse(raw) as Dump[]) : []
}

function lastEntry(): Dump {
  const { calls } = logErrorMock.mock
  const last = calls[calls.length - 1]
  if (!last) throw new Error('logError was not called')
  return JSON.parse(last[0].replace('[ui-crash] ', '')) as Dump
}

beforeEach(() => {
  logErrorMock.mockReset()
  localStorage.clear()
  vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  vi.restoreAllMocks()
  localStorage.clear()
})

describe('reportCrash', () => {
  it('forwards a structured dump to the Go log channel', () => {
    reportCrash(new Error('boom'), { componentStack: 'at Boom' })

    expect(logErrorMock).toHaveBeenCalledTimes(1)
    const entry = lastEntry()
    expect(entry.kind).toBe('ui-crash')
    expect(entry.error.name).toBe('Error')
    expect(entry.error.message).toBe('boom')
    expect(entry.componentStack).toBe('at Boom')
  })

  it('serializes non-Error thrown values without throwing', () => {
    expect(() => reportCrash('just a string')).not.toThrow()
    expect(lastEntry().error.message).toBe('just a string')
  })

  it('rings the localStorage dumps, keeping only the last 5', () => {
    for (let i = 0; i < 7; i++) reportCrash(new Error(`e${i}`))

    expect(readRing().map(d => d.error.message)).toEqual(['e2', 'e3', 'e4', 'e5', 'e6'])
  })

  it('never throws when the Go log channel throws, still ringing the dump', () => {
    logErrorMock.mockImplementation(() => {
      throw new Error('runtime bridge dead')
    })

    expect(() => reportCrash(new Error('boom'))).not.toThrow()
    expect(readRing()).toHaveLength(1)
  })

  it('never throws when localStorage is unavailable', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('quota exceeded')
    })

    expect(() => reportCrash(new Error('boom'))).not.toThrow()
    // The Go log channel still received the dump.
    expect(logErrorMock).toHaveBeenCalledTimes(1)
  })
})
