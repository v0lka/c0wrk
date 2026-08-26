// Global vitest setup, wired via `setupFiles` in vitest.config.ts. Runs once
// per test file, before any test-file import — so it can prepare the
// environment ahead of store/component module evaluation.
//
// 1. IS_REACT_ACT_ENVIRONMENT: React 19 requires this flag whenever act() is
//    used, otherwise it warns "The current testing environment is not
//    configured to support act(...)". This repo calls act() directly from
//    'react' (no @testing-library/react), so the flag is set globally here
//    instead of via per-file vi.hoisted blocks.
//
// 2. localStorage polyfill: Node >= 25 defines an experimental localStorage
//    accessor on globalThis (activated via --localstorage-file). In test
//    environments it shadows jsdom's window.localStorage, returns undefined,
//    and emits an ExperimentalWarning on every read. zustand's persist
//    middleware reads localStorage at store-creation time, so the broken
//    accessor is replaced with an in-memory Storage before any store module
//    is imported. defineProperty is used (not assignment) so Node's
//    warning-emitting getter/setter is never invoked.
//
// 3. Range geometry polyfill (jsdom): jsdom lacks Range.getClientRects /
//    getBoundingClientRect, which CodeMirror's measure cycle calls from
//    requestAnimationFrame — the async TypeError otherwise shows up in stderr
//    while the tests stay green. Installed only when a DOM is present.

const g = globalThis as Record<string, unknown>

// --- 1. React act() environment flag -----------------------------------------
g.IS_REACT_ACT_ENVIRONMENT = true
const win = g.window as Record<string, unknown> | undefined
if (win !== undefined) {
  win.IS_REACT_ACT_ENVIRONMENT = true
}

// --- 2. In-memory localStorage over Node's broken experimental accessor -----
const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
const isBrokenAccessor =
  descriptor !== undefined && descriptor.get !== undefined && !('value' in descriptor)
if (isBrokenAccessor) {
  const map = new Map<string, string>()
  const storage: Storage = {
    get length(): number {
      return map.size
    },
    clear: (): void => {
      map.clear()
    },
    getItem: (key: string): string | null => map.get(key) ?? null,
    key: (index: number): string | null => Array.from(map.keys())[index] ?? null,
    removeItem: (key: string): void => {
      map.delete(key)
    },
    setItem: (key: string, value: string): void => {
      map.set(key, value)
    },
  }
  Object.defineProperty(globalThis, 'localStorage', {
    value: storage,
    writable: true,
    configurable: true,
  })
}

// --- 3. Range geometry polyfill for jsdom ------------------------------------
// jsdom implements no text-layout geometry on Range: `getClientRects` and
// `getBoundingClientRect` are missing entirely (Element has them, Range does
// not). CodeMirror's measure cycle runs in requestAnimationFrame and calls
// them via EditorView.coordsAtPos (tooltip/cursor positioning); the resulting
// async TypeError lands in stderr after the assertions already passed, so the
// suite stays green but noisy. Empty rect lists are a safe substitute:
// CodeMirror treats "no rects" as "no geometry" and falls back gracefully.
if (typeof document !== 'undefined' && typeof document.createRange === 'function') {
  const rangeProto = Object.getPrototypeOf(document.createRange()) as Range
  if (typeof rangeProto.getClientRects !== 'function') {
    rangeProto.getClientRects = (): DOMRectList =>
      ({ length: 0, item: () => null }) as unknown as DOMRectList
  }
  if (typeof rangeProto.getBoundingClientRect !== 'function') {
    rangeProto.getBoundingClientRect = (): DOMRect =>
      ({
        x: 0,
        y: 0,
        width: 0,
        height: 0,
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        toJSON: () => ({}),
      }) as unknown as DOMRect
  }
}
