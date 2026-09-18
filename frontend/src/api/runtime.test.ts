// @vitest-environment jsdom
//
// Tests for the batched-event transport dispatcher in api/runtime.ts.
//
// The desktop backend collapses high-frequency session events into a single
// `c0wrk:events:batch` Wails event per ~16ms flush (see
// desktop/event_batcher.go). These tests pin the frontend half of that
// contract: a batch envelope must fan back out to the exact per-event
// subscribers, preserving order and the per-event null-payload filtering, and
// individually-emitted (non-batched) events must keep working.

import { describe, it, expect, beforeEach, vi } from 'vitest'

// The malformed-envelope tests below feed the dispatcher garbage on purpose;
// the real logger would print "[events] dropped malformed …" warnings for
// each. That output is the expected behavior under test, not a regression —
// mock the logger to keep the suite output clean.
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

type Cb = (...data: unknown[]) => void

interface Harness {
  handlers: Map<string, Set<Cb>>
}

function installFakeRuntime(): Harness {
  const handlers = new Map<string, Set<Cb>>()
  const runtime = {
    EventsOn(name: string, cb: Cb): () => void {
      let set = handlers.get(name)
      if (!set) {
        set = new Set()
        handlers.set(name, set)
      }
      set.add(cb)
      return () => {
        handlers.get(name)?.delete(cb)
      }
    },
    EventsEmit(): void {},
    ClipboardSetText: async (): Promise<boolean> => true,
    BrowserOpenURL(): void {},
    WindowSetTitle(): void {},
    LogError(): void {},
  }
  ;(window as unknown as Record<string, unknown>).runtime = runtime
  return { handlers }
}

type RuntimeModule = typeof import('@/api/runtime')

async function freshRuntimeModule(): Promise<RuntimeModule> {
  vi.resetModules()
  return await import('@/api/runtime')
}

let harness: Harness
let runtime: RuntimeModule

beforeEach(async () => {
  harness = installFakeRuntime()
  runtime = await freshRuntimeModule()
})

/** Invoke the single envelope listener the module installed, if any. */
function fireEnvelope(envelope: unknown): void {
  const set = harness.handlers.get(runtime.EVENT_BATCH_NAME)
  if (!set || set.size === 0) throw new Error('batch listener was not installed')
  for (const cb of Array.from(set)) cb(envelope)
}

describe('batched event dispatch', () => {
  it('fans a batch envelope out to per-event session subscribers in order', () => {
    const received: string[] = []
    runtime.onSessionEvent('s1', 'assistant_chunk', (d) => {
      received.push(`chunk:${(d as { accumulated_content?: string } | undefined)?.accumulated_content}`)
    })
    runtime.onSessionEvent('s1', 'assistant_done', (d) => {
      received.push(`done:${(d as { content?: string } | undefined)?.content}`)
    })

    fireEnvelope({
      events: [
        { name: 'session:s1:assistant_chunk', args: [{ content: 'a', accumulated_content: 'a' }] },
        { name: 'session:s1:assistant_chunk', args: [{ content: 'b', accumulated_content: 'ab' }] },
        { name: 'session:s1:assistant_done', args: [{ content: 'ab' }] },
      ],
    })

    expect(received).toEqual(['chunk:a', 'chunk:ab', 'done:ab'])
  })

  it('preserves the null-payload filter for batched session events', () => {
    const seen: Array<unknown> = []
    runtime.onSessionEvent('s1', 'task_cancelled', (d) => seen.push(d))

    fireEnvelope({ events: [{ name: 'session:s1:task_cancelled', args: [null] }] })
    fireEnvelope({ events: [{ name: 'session:s1:task_cancelled', args: [] }] })

    expect(seen).toEqual([undefined, undefined])
  })

  it('delivers global events carried in a batch envelope', () => {
    const names: string[] = []
    runtime.onGlobalEvent('session:renamed', (d) => names.push((d as { name: string }).name))

    fireEnvelope({ events: [{ name: 'session:renamed', args: [{ id: 's1', name: 'Title' }] }] })

    expect(names).toEqual(['Title'])
  })

  it('ignores entries for unsubscribed event names', () => {
    const received: string[] = []
    runtime.onSessionEvent('s1', 'thought', () => received.push('thought'))

    fireEnvelope({
      events: [
        { name: 'session:s1:other', args: [{ x: 1 }] },
        { name: 'session:s1:thought', args: [{ content: 'x' }] },
      ],
    })

    expect(received).toEqual(['thought'])
  })

  it('still receives individually-emitted (non-batched) events', () => {
    const received: string[] = []
    runtime.onGlobalEvent('backend:ready', () => received.push('ready'))

    // A direct EventsEmit reaches the handler through the per-event listener.
    const direct = harness.handlers.get('backend:ready')
    expect(direct && direct.size).toBe(1)
    for (const cb of Array.from(direct!)) cb()

    expect(received).toEqual(['ready'])
  })

  it('stops delivering a batched event after unsubscribe', () => {
    const received: string[] = []
    const unsubscribe = runtime.onSessionEvent('s1', 'thought', () => received.push('thought'))

    fireEnvelope({ events: [{ name: 'session:s1:thought', args: [{ content: 'a' }] }] })
    unsubscribe()
    fireEnvelope({ events: [{ name: 'session:s1:thought', args: [{ content: 'b' }] }] })

    expect(received).toEqual(['thought'])
  })

  it('does not throw on a malformed envelope', () => {
    runtime.onSessionEvent('s1', 'thought', () => {})
    expect(() => fireEnvelope({ nope: true })).not.toThrow()
    expect(() => fireEnvelope(undefined)).not.toThrow()
    expect(() => fireEnvelope({ events: [{ name: 42 }] })).not.toThrow()
  })
})
