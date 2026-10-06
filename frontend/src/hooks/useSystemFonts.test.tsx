// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// jsdom here exposes no window.localStorage; zustand's persist middleware in
// fontStore captures it at store-creation time, so polyfill before the store
// module is imported (same setup as uiScaleStore.test.ts).
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
    getSystemFonts: vi.fn(),
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

vi.mock('@/api/fonts', () => ({ getSystemFonts: apiMocks.getSystemFonts }))
vi.mock('@/api/runtime', () => ({ onGlobalEvent: onGlobalEventMock }))
vi.mock('@/lib/logger', () => ({ logger: loggerMocks }))

import { useSystemFonts } from './useSystemFonts'
import { useFontStore, FONT_SANS_CSS_VAR, FONT_MONO_CSS_VAR } from '@/stores/fontStore'
import { FONT_SANS_STACK, FONT_MONO_STACK } from '@/lib/fonts'

/** Shape of the getSystemFonts() answer. */
interface FontsAnswer {
  uiFamily: string | null
  monoFamily: string | null
}

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
  useSystemFonts()
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

function sansVar(): string {
  return document.documentElement.style.getPropertyValue(FONT_SANS_CSS_VAR)
}

function monoVar(): string {
  return document.documentElement.style.getPropertyValue(FONT_MONO_CSS_VAR)
}

beforeEach(() => {
  apiMocks.getSystemFonts.mockReset()
  onGlobalEventMock.mockClear()
  loggerMocks.error.mockClear()
  capturedHandlers.clear()
  useFontStore.setState({
    uiFontFamily: null,
    monoFontFamily: null,
    detectedUIFamily: null,
    detectedMonoFamily: null,
  })
  document.documentElement.style.removeProperty(FONT_SANS_CSS_VAR)
  document.documentElement.style.removeProperty(FONT_MONO_CSS_VAR)
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

describe('useSystemFonts', () => {
  // --- the three detection outcomes: both families, one, none ---

  it('stores both detected families on mount and subscribes to backend:ready', async () => {
    apiMocks.getSystemFonts.mockResolvedValue({ uiFamily: 'Noto Sans', monoFamily: 'JetBrains Mono' })

    renderHook()
    await flushMicrotasks()

    expect(useFontStore.getState().detectedUIFamily).toBe('Noto Sans')
    expect(useFontStore.getState().detectedMonoFamily).toBe('JetBrains Mono')
    expect(apiMocks.getSystemFonts).toHaveBeenCalledTimes(1)
    expect(onGlobalEventMock).toHaveBeenCalledWith('backend:ready', expect.any(Function))
  })

  it('records only the UI family when the desktop reports no mono font', async () => {
    apiMocks.getSystemFonts.mockResolvedValue({ uiFamily: 'Noto Sans', monoFamily: null })

    renderHook()
    await flushMicrotasks()

    expect(useFontStore.getState().detectedUIFamily).toBe('Noto Sans')
    expect(useFontStore.getState().detectedMonoFamily).toBeNull()
  })

  it('records only the mono family when the desktop reports no UI font', async () => {
    apiMocks.getSystemFonts.mockResolvedValue({ uiFamily: null, monoFamily: 'JetBrains Mono' })

    renderHook()
    await flushMicrotasks()

    expect(useFontStore.getState().detectedUIFamily).toBeNull()
    expect(useFontStore.getState().detectedMonoFamily).toBe('JetBrains Mono')
  })

  it('maps an empty detection to null candidates (definitive answer, latches)', async () => {
    apiMocks.getSystemFonts.mockResolvedValue({ uiFamily: null, monoFamily: null })

    renderHook()
    await flushMicrotasks()

    expect(useFontStore.getState().detectedUIFamily).toBeNull()
    expect(useFontStore.getState().detectedMonoFamily).toBeNull()
    expect(loggerMocks.error).not.toHaveBeenCalled()

    // The answer is definitive: backend:ready must not re-fetch it.
    fireBackendReady()
    await flushMicrotasks()
    expect(apiMocks.getSystemFonts).toHaveBeenCalledTimes(1)
  })

  // --- detection never paints ---

  it('detection records the candidates but never touches <html>', async () => {
    apiMocks.getSystemFonts.mockResolvedValue({ uiFamily: 'Noto Sans', monoFamily: 'JetBrains Mono' })

    renderHook()
    await flushMicrotasks()

    expect(useFontStore.getState().detectedUIFamily).toBe('Noto Sans')
    expect(useFontStore.getState().detectedMonoFamily).toBe('JetBrains Mono')
    // What is rendered is decided solely by the persisted chosen families —
    // a detection arriving must not repaint text on its own.
    expect(sansVar()).toBe('')
    expect(monoVar()).toBe('')
  })

  it('a late detection does not repaint an already-chosen family', async () => {
    // The user (or a persisted choice) already applied families; the hook
    // only refreshes the session candidates.
    useFontStore.getState().setUIFontFamily('DejaVu Sans')
    useFontStore.getState().setMonoFontFamily('Cascadia Code')
    apiMocks.getSystemFonts.mockResolvedValue({ uiFamily: 'Noto Sans', monoFamily: 'JetBrains Mono' })

    renderHook()
    await flushMicrotasks()

    expect(useFontStore.getState().detectedUIFamily).toBe('Noto Sans')
    expect(useFontStore.getState().detectedMonoFamily).toBe('JetBrains Mono')
    expect(sansVar()).toBe(`"DejaVu Sans", ${FONT_SANS_STACK}`)
    expect(monoVar()).toBe(`"Cascadia Code", ${FONT_MONO_STACK}`)
  })

  // --- retry backend:ready scheme (in-flight / pendingRetry / latch) ---

  it('survives a rejected mount fetch and recovers via the backend:ready retry', async () => {
    // Mount request can arrive before the backend is ready — getApp() throws
    // or the RPC rejects. Nothing latches, nothing breaks.
    apiMocks.getSystemFonts
      .mockRejectedValueOnce(new Error('Wails App bindings are not available'))
      .mockResolvedValueOnce({ uiFamily: 'Cantarell', monoFamily: 'JetBrains Mono' })

    renderHook()
    await flushMicrotasks()

    expect(loggerMocks.error).toHaveBeenCalledTimes(1)
    expect(useFontStore.getState().detectedUIFamily).toBeNull()
    expect(useFontStore.getState().detectedMonoFamily).toBeNull()

    fireBackendReady()
    await flushMicrotasks()

    expect(useFontStore.getState().detectedUIFamily).toBe('Cantarell')
    expect(useFontStore.getState().detectedMonoFamily).toBe('JetBrains Mono')
    expect(apiMocks.getSystemFonts).toHaveBeenCalledTimes(2)
    expect(loggerMocks.error).toHaveBeenCalledTimes(1) // no new failures
  })

  it('stays retryable when the post-ready fetch also fails', async () => {
    apiMocks.getSystemFonts.mockRejectedValue(new Error('still not ready'))

    renderHook()
    await flushMicrotasks()

    fireBackendReady()
    await flushMicrotasks()

    expect(apiMocks.getSystemFonts).toHaveBeenCalledTimes(2)
    expect(useFontStore.getState().detectedUIFamily).toBeNull()
    expect(useFontStore.getState().detectedMonoFamily).toBeNull()
  })

  it('does not re-fetch after a successful load', async () => {
    apiMocks.getSystemFonts.mockResolvedValue({ uiFamily: 'Noto Sans', monoFamily: 'JetBrains Mono' })

    renderHook()
    await flushMicrotasks()

    fireBackendReady()
    await flushMicrotasks()

    expect(apiMocks.getSystemFonts).toHaveBeenCalledTimes(1)
  })

  it('replays a backend:ready that arrived while a fetch was in flight', async () => {
    // backend:ready fires between the mount call and its settlement; the
    // one-shot emission must not be lost when that attempt then FAILS (the
    // only state where a replay is needed — a settled definitive answer
    // latches and makes the retry moot).
    const first = deferred<FontsAnswer>()
    apiMocks.getSystemFonts
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce({ uiFamily: 'Cantarell', monoFamily: 'JetBrains Mono' })

    renderHook()
    fireBackendReady() // in flight: remembered, not consumed
    await flushMicrotasks()
    expect(apiMocks.getSystemFonts).toHaveBeenCalledTimes(1)

    first.reject(new Error('transient')) // settles as a failure: no latch
    await flushMicrotasks()

    // The remembered retry replays now that the failed attempt settled.
    expect(apiMocks.getSystemFonts).toHaveBeenCalledTimes(2)
    expect(useFontStore.getState().detectedUIFamily).toBe('Cantarell')
    expect(useFontStore.getState().detectedMonoFamily).toBe('JetBrains Mono')
  })

  it('ignores a late-arriving answer after unmount', async () => {
    const d = deferred<FontsAnswer>()
    apiMocks.getSystemFonts.mockReturnValueOnce(d.promise)

    renderHook()
    const r = root
    root = null
    act(() => {
      r?.unmount()
    })

    d.resolve({ uiFamily: 'Noto Sans', monoFamily: 'JetBrains Mono' })
    await flushMicrotasks()

    expect(useFontStore.getState().detectedUIFamily).toBeNull()
    expect(useFontStore.getState().detectedMonoFamily).toBeNull()
  })
})
