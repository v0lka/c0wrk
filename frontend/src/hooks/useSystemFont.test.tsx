// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// jsdom here exposes no window.localStorage; zustand's persist middleware in
// systemFontStore captures it at store-creation time, so polyfill before the
// store module is imported (same setup as uiScaleStore.test.ts).
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  const map = new Map<string, string>()
  win.localStorage = {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => { map.set(k, v) },
    removeItem: (k: string) => { map.delete(k) },
    clear: () => map.clear(),
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    get length() { return map.size },
  }
})

// --- Mock the backend boundary so tests never touch the Wails runtime ---
const { apiMocks, onGlobalEventMock, loggerMocks } = vi.hoisted(() => ({
  apiMocks: {
    getSystemUIFont: vi.fn(),
  },
  // Typed to the real onGlobalEvent() signature so mockImplementation below
  // type-checks under `tsc -b`.
  onGlobalEventMock: vi.fn((_name: string, _handler: () => void) => () => {}),
  loggerMocks: {
    error: vi.fn(),
    warn: vi.fn(),
    info: vi.fn(),
    debug: vi.fn(),
  },
}))

vi.mock('@/api/systemFont', () => ({ getSystemUIFont: apiMocks.getSystemUIFont }))
vi.mock('@/api/runtime', () => ({ onGlobalEvent: onGlobalEventMock }))
vi.mock('@/lib/logger', () => ({ logger: loggerMocks }))

import { useSystemFont } from './useSystemFont'
import { useSystemFontStore, SYSTEM_FONT_CSS_VAR } from '@/stores/systemFontStore'

/** A promise whose settlement the test controls (in-flight fetch races). */
function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void } {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

let root: Root | null = null
let container: HTMLDivElement | null = null

/** Handlers captured per event name. */
const capturedHandlers = new Map<string, () => void>()
onGlobalEventMock.mockImplementation((name: string, handler: () => void) => {
  capturedHandlers.set(name, handler)
  return () => {}
})

function Harness(): null {
  useSystemFont()
  return null
}

function renderHook(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(createElement(Harness))
  })
}

/** Let the fire-and-forget promise chain settle past the store writes. */
async function flushMicrotasks(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve()
  })
}

function fireBackendReady(): void {
  act(() => {
    capturedHandlers.get('backend:ready')?.()
  })
}

function cssVar(): string {
  return document.documentElement.style.getPropertyValue(SYSTEM_FONT_CSS_VAR)
}

beforeEach(() => {
  apiMocks.getSystemUIFont.mockReset()
  onGlobalEventMock.mockClear()
  loggerMocks.error.mockClear()
  capturedHandlers.clear()
  useSystemFontStore.setState({ followSystemFont: false, systemFontFamily: null })
  document.documentElement.style.removeProperty(SYSTEM_FONT_CSS_VAR)
})

afterEach(() => {
  if (root) {
    const r = root
    root = null
    act(() => {
      r.unmount()
    })
  }
  container?.remove()
  container = null
})

describe('useSystemFont', () => {
  it('stores the detected family on mount and subscribes to backend:ready', async () => {
    apiMocks.getSystemUIFont.mockResolvedValue({ family: 'Noto Sans' })

    renderHook()
    await flushMicrotasks()

    expect(useSystemFontStore.getState().systemFontFamily).toBe('Noto Sans')
    expect(apiMocks.getSystemUIFont).toHaveBeenCalledTimes(1)
    expect(onGlobalEventMock).toHaveBeenCalledWith('backend:ready', expect.any(Function))
  })

  it('applies the family to <html> when following is already on', async () => {
    useSystemFontStore.setState({ followSystemFont: true })
    apiMocks.getSystemUIFont.mockResolvedValue({ family: 'DejaVu Sans' })

    renderHook()
    await flushMicrotasks()

    expect(cssVar()).toBe('"DejaVu Sans"')
  })

  it('leaves <html> untouched while following is off (family cached only)', async () => {
    apiMocks.getSystemUIFont.mockResolvedValue({ family: 'Noto Sans' })

    renderHook()
    await flushMicrotasks()

    expect(useSystemFontStore.getState().systemFontFamily).toBe('Noto Sans')
    expect(cssVar()).toBe('')
  })

  it('maps available=false to a null family (definitive answer, latches)', async () => {
    apiMocks.getSystemUIFont.mockResolvedValue(null)

    renderHook()
    await flushMicrotasks()

    expect(useSystemFontStore.getState().systemFontFamily).toBeNull()
    expect(loggerMocks.error).not.toHaveBeenCalled()

    // The answer is definitive: backend:ready must not re-fetch it.
    fireBackendReady()
    await flushMicrotasks()
    expect(apiMocks.getSystemUIFont).toHaveBeenCalledTimes(1)
  })

  it('survives a rejected mount fetch and recovers via the backend:ready retry', async () => {
    // Mount request can arrive before the backend is ready — getApp() throws
    // or the RPC rejects. Nothing latches, nothing breaks.
    apiMocks.getSystemUIFont
      .mockRejectedValueOnce(new Error('Wails App bindings are not available'))
      .mockResolvedValueOnce({ family: 'Cantarell' })

    renderHook()
    await flushMicrotasks()

    expect(loggerMocks.error).toHaveBeenCalledTimes(1)
    expect(useSystemFontStore.getState().systemFontFamily).toBeNull()

    fireBackendReady()
    await flushMicrotasks()

    expect(useSystemFontStore.getState().systemFontFamily).toBe('Cantarell')
    expect(apiMocks.getSystemUIFont).toHaveBeenCalledTimes(2)
    expect(loggerMocks.error).toHaveBeenCalledTimes(1) // no new failures
  })

  it('stays retryable when the post-ready fetch also fails', async () => {
    apiMocks.getSystemUIFont.mockRejectedValue(new Error('still not ready'))

    renderHook()
    await flushMicrotasks()

    fireBackendReady()
    await flushMicrotasks()

    expect(apiMocks.getSystemUIFont).toHaveBeenCalledTimes(2)
    expect(useSystemFontStore.getState().systemFontFamily).toBeNull()
  })

  it('does not re-fetch after a successful load', async () => {
    apiMocks.getSystemUIFont.mockResolvedValue({ family: 'Noto Sans' })

    renderHook()
    await flushMicrotasks()

    fireBackendReady()
    await flushMicrotasks()

    expect(apiMocks.getSystemUIFont).toHaveBeenCalledTimes(1)
  })

  it('replays a backend:ready that arrived while a fetch was in flight', async () => {
    // backend:ready fires between the mount call and its settlement; the
    // one-shot emission must not be lost when that attempt then FAILS (the
    // only state where a replay is needed — a settled definitive answer
    // latches and makes the retry moot).
    const first = deferred<{ family: string } | null>()
    apiMocks.getSystemUIFont
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce({ family: 'Cantarell' })

    renderHook()
    fireBackendReady() // in flight: remembered, not consumed
    await flushMicrotasks()
    expect(apiMocks.getSystemUIFont).toHaveBeenCalledTimes(1)

    first.reject(new Error('transient')) // settles as a failure: no latch
    await flushMicrotasks()

    // The remembered retry replays now that the failed attempt settled.
    expect(apiMocks.getSystemUIFont).toHaveBeenCalledTimes(2)
    expect(useSystemFontStore.getState().systemFontFamily).toBe('Cantarell')
  })

  it('ignores a late-arriving answer after unmount', async () => {
    const d = deferred<{ family: string } | null>()
    apiMocks.getSystemUIFont.mockReturnValueOnce(d.promise)

    renderHook()
    const r = root
    root = null
    act(() => {
      r?.unmount()
    })

    d.resolve({ family: 'Noto Sans' })
    await flushMicrotasks()

    expect(useSystemFontStore.getState().systemFontFamily).toBeNull()
  })
})
