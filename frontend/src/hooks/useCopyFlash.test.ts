// @vitest-environment jsdom
// useCopyFlash — the shared copy-flash contract (chat/CopyButton + the specs
// CopyIconButton): native-Wails clipboard write, the 'copied'/'failed'
// verdict flashing, the bounded re-armed revert timer, the unmount cleanup,
// and the component-specific flash kinds riding the same timer. The consumer
// components pin their own render mapping (icons/labels) in their suites;
// this suite pins the hook contract itself.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { useCopyFlash, COPY_FLASH_MS } from './useCopyFlash'
import { clipboardSetText } from '@/api/runtime'

// The hook must go through the native Wails runtime, NOT navigator.clipboard
// (the boundary module is mocked so no real window.runtime binding is
// touched; the mock carries exactly the one export the hook imports).
vi.mock('@/api/runtime', () => ({
  clipboardSetText: vi.fn().mockResolvedValue(true),
}))

let root: Root | null = null
let container: HTMLDivElement | null = null

type CopyFlashResult = ReturnType<typeof useCopyFlash>
let last: CopyFlashResult | null = null

/** The copy-only harness (default generic — no extension kinds). */
function Harness(): null {
  last = useCopyFlash()
  return null
}

type SaveFlashResult = ReturnType<typeof useCopyFlash<'saved' | 'download-started'>>
let saveLast: SaveFlashResult | null = null

/** The extended harness — chat/CopyButton's save-kind shape. */
function SaveHarness(): null {
  saveLast = useCopyFlash<'saved' | 'download-started'>()
  return null
}

function renderHook(which: 'copy' | 'save' = 'copy'): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(createElement(which === 'copy' ? Harness : SaveHarness))
  })
}

function unmountHook(): void {
  if (root) {
    const r = root
    root = null
    act(() => {
      r.unmount()
    })
  }
  container?.remove()
  container = null
}

/** Settle the copy promise's microtasks under fake timers. */
async function settle(): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0)
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  // Pin the success verdict explicitly: the failure tests below override it,
  // and this per-test setup keeps that override from leaking across tests.
  vi.mocked(clipboardSetText).mockResolvedValue(true)
  last = null
  saveLast = null
})

afterEach(() => {
  unmountHook()
  vi.clearAllMocks()
  vi.useRealTimers()
})

describe('useCopyFlash — the copy verdict flash', () => {
  it('starts idle and never touches the clipboard until copy is called', () => {
    renderHook()
    expect(last!.feedback).toBeNull()
    expect(clipboardSetText).not.toHaveBeenCalled()
  })

  it('flashes "copied" on a successful write and reverts after COPY_FLASH_MS', async () => {
    renderHook()
    act(() => {
      last!.copy('answer text')
    })
    await settle()

    expect(clipboardSetText).toHaveBeenCalledTimes(1)
    expect(clipboardSetText).toHaveBeenCalledWith('answer text')
    expect(last!.feedback).toBe('copied')

    await act(async () => {
      await vi.advanceTimersByTimeAsync(COPY_FLASH_MS)
    })
    expect(last!.feedback).toBeNull()
  })

  it('flashes "failed" — never "copied" — when the write reports false, with the same bounded window', async () => {
    vi.mocked(clipboardSetText).mockResolvedValue(false)
    renderHook()
    act(() => {
      last!.copy('nope')
    })
    await settle()

    expect(last!.feedback).toBe('failed')

    await act(async () => {
      await vi.advanceTimersByTimeAsync(COPY_FLASH_MS)
    })
    expect(last!.feedback).toBeNull()
  })

  it('flashes "failed" when the write rejects (the defensive catch path)', async () => {
    vi.mocked(clipboardSetText).mockRejectedValue(new Error('transport exploded'))
    renderHook()
    act(() => {
      last!.copy('nope')
    })
    await settle()

    expect(last!.feedback).toBe('failed')
  })

  it('re-arms the revert timer on every flash — a late second flash outlives the first window', async () => {
    renderHook()
    act(() => {
      last!.copy('first')
    })
    await settle()
    expect(last!.feedback).toBe('copied')

    // One tick before the first window closes, flash again: the re-arm must
    // restart the FULL window, not leave the original (almost-expired) timer.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(COPY_FLASH_MS - 1)
    })
    act(() => {
      last!.copy('second')
    })
    await settle()
    expect(clipboardSetText).toHaveBeenCalledWith('second')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1)
    })
    // The first window's deadline has passed — the re-armed flash must hold.
    expect(last!.feedback).toBe('copied')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(COPY_FLASH_MS - 1)
    })
    expect(last!.feedback).toBeNull()
  })

  it('clears the revert timer on unmount — nothing fires into the unmounted hook', async () => {
    renderHook()
    act(() => {
      last!.copy('countdown')
    })
    await settle()
    // The revert timer is the only pending timer while mounted.
    expect(vi.getTimerCount()).toBe(1)

    unmountHook()
    // Unmount cleanup cleared it; advancing past the window must not re-add it.
    expect(vi.getTimerCount()).toBe(0)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(COPY_FLASH_MS)
    })
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('useCopyFlash — component-specific kinds ride the same timer', () => {
  it('flashes an extended kind via flash and reverts it on the shared window', async () => {
    renderHook('save')
    act(() => {
      saveLast!.flash('saved')
    })
    expect(saveLast!.feedback).toBe('saved')
    // The extended kind never leaks into a copy-only consumer's union.

    await act(async () => {
      await vi.advanceTimersByTimeAsync(COPY_FLASH_MS)
    })
    expect(saveLast!.feedback).toBeNull()
  })

  it('a copy verdict overrides an in-flight extended flash', async () => {
    renderHook('save')
    act(() => {
      saveLast!.flash('download-started')
    })
    act(() => {
      saveLast!.copy('text')
    })
    await settle()

    expect(saveLast!.feedback).toBe('copied')
  })
})
