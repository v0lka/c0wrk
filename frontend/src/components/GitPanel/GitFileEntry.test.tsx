// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { GitFileEntry } from './GitFileEntry'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

const LONG_PATH =
  '/repo/src/deeply/nested/directory/structure/with-a-very-long-file-name.ts'

function makeEntry(overrides: Partial<GitPanelEntry> = {}): GitPanelEntry {
  return {
    path: LONG_PATH,
    status: 'M',
    staged: false,
    diffStat: null,
    indexStatus: ' ',
    worktreeStatus: 'M',
    ...overrides,
  }
}

function render(ui: React.ReactNode): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => {
    root.render(ui)
  })
  return container
}

/** The truncating span that renders the file name. */
function nameSpan(container: HTMLElement): HTMLSpanElement {
  return container.querySelector('span.truncate') as HTMLSpanElement
}

/** The status badge span — the only element on the row carrying `py-px`. */
function badgeSpan(container: HTMLElement): HTMLSpanElement {
  return container.querySelector('span.py-px') as HTMLSpanElement
}

/** The staging checkbox input for this row. */
function checkbox(container: HTMLElement): HTMLInputElement {
  return container.querySelector('input[type="checkbox"]') as HTMLInputElement
}

/** A promise whose resolution the test controls (for in-flight assertions). */
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

/** Drain pending microtasks (and the macrotask queue) so async handlers settle. */
function flush(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0))
}

const noop = () => Promise.resolve(true)

describe('GitFileEntry', () => {
  it('carries the workspace-relative display path as the name tooltip', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry()}
        side="worktree"
        workspaceRoot="/repo"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(nameSpan(container).getAttribute('title')).toBe(
      'src/deeply/nested/directory/structure/with-a-very-long-file-name.ts',
    )
  })

  it('falls back to the raw path in the tooltip when no workspace root is known', () => {
    const container = render(
      <GitFileEntry entry={makeEntry()} side="worktree" onToggle={noop} onOpenDiff={noop} />,
    )
    expect(nameSpan(container).getAttribute('title')).toBe(LONG_PATH)
  })

  it('keeps the raw path in the tooltip when it lies outside the workspace root', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry()}
        side="worktree"
        workspaceRoot="/other-repo"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(nameSpan(container).getAttribute('title')).toBe(LONG_PATH)
  })
})

// ─────────────────────────── Axis-aware row state ────────────────────────────

describe('GitFileEntry — staging axis', () => {
  it('renders a checked checkbox and dispatches unstage on the index axis', () => {
    const onToggle = vi.fn()
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: ' ' })}
        side="index"
        onToggle={onToggle}
        onOpenDiff={noop}
      />,
    )

    const box = checkbox(container)
    expect(box.checked).toBe(true)

    act(() => {
      box.click()
    })
    expect(onToggle).toHaveBeenCalledTimes(1)
    expect(onToggle).toHaveBeenCalledWith(LONG_PATH, 'unstage')
  })

  it('renders an unchecked checkbox and dispatches stage on the worktree axis', () => {
    const onToggle = vi.fn()
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: ' ', worktreeStatus: 'M' })}
        side="worktree"
        onToggle={onToggle}
        onOpenDiff={noop}
      />,
    )

    const box = checkbox(container)
    expect(box.checked).toBe(false)

    act(() => {
      box.click()
    })
    expect(onToggle).toHaveBeenCalledTimes(1)
    expect(onToggle).toHaveBeenCalledWith(LONG_PATH, 'stage')
  })

  it('gives the two rows of an MM entry opposite checkbox states and actions', () => {
    // The same file modified on both axes: two independent rows. The index
    // row is checked and unstages; the worktree row is unchecked and stages.
    const entry = makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: 'M' })

    const indexToggle = vi.fn()
    const indexRow = render(
      <GitFileEntry entry={entry} side="index" onToggle={indexToggle} onOpenDiff={noop} />,
    )
    expect(checkbox(indexRow).checked).toBe(true)
    act(() => {
      checkbox(indexRow).click()
    })
    expect(indexToggle).toHaveBeenCalledWith(LONG_PATH, 'unstage')

    const worktreeToggle = vi.fn()
    const worktreeRow = render(
      <GitFileEntry entry={entry} side="worktree" onToggle={worktreeToggle} onOpenDiff={noop} />,
    )
    expect(checkbox(worktreeRow).checked).toBe(false)
    act(() => {
      checkbox(worktreeRow).click()
    })
    expect(worktreeToggle).toHaveBeenCalledWith(LONG_PATH, 'stage')
  })
})

describe('GitFileEntry — status badge', () => {
  it('shows the index code on the index axis', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: 'D' })}
        side="index"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(badgeSpan(container).textContent).toBe('M')
  })

  it('shows the worktree code on the worktree axis', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: 'D' })}
        side="worktree"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(badgeSpan(container).textContent).toBe('D')
  })

  it('shows A for an untracked row rather than the raw ? marker', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'A', indexStatus: '?', worktreeStatus: '?' })}
        side="worktree"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(badgeSpan(container).textContent).toBe('A')
  })
})

describe('GitFileEntry — merge conflict', () => {
  it('marks a conflict row with the conflict icon', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'U', indexStatus: 'U', worktreeStatus: 'U' })}
        side="index"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(container.querySelector('svg[aria-label="Merge conflict"]')).not.toBeNull()
  })

  it('does not mark a plain modification with the conflict icon', () => {
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: ' ' })}
        side="index"
        onToggle={noop}
        onOpenDiff={noop}
      />,
    )
    expect(container.querySelector('svg[aria-label="Merge conflict"]')).toBeNull()
  })
})

// ─────────────────────── Optimistic checkbox state ──────────────────────────

describe('GitFileEntry — optimistic checkbox', () => {
  it('holds the flipped value while the toggle is in flight, then reverts on failure', async () => {
    let resolveToggle!: (value: boolean) => void
    const onToggle = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          resolveToggle = resolve
        }),
    )
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: ' ' })}
        side="index"
        onToggle={onToggle}
        onOpenDiff={noop}
      />,
    )
    const box = checkbox(container)
    expect(box.checked).toBe(true)

    await act(async () => {
      box.click()
    })
    // The box holds the intended (flipped) state instead of snapping back to
    // the stale server-derived value while the RPC is still in flight.
    expect(box.checked).toBe(false)

    await act(async () => {
      resolveToggle(false)
      await flush()
    })
    // A failed toggle reverts the optimistic flip.
    expect(box.checked).toBe(true)
  })

  it('keeps the flipped value and dispatches stage on a successful worktree toggle', async () => {
    const onToggle = vi.fn(() => Promise.resolve(true))
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'A', indexStatus: '?', worktreeStatus: '?' })}
        side="worktree"
        onToggle={onToggle}
        onOpenDiff={noop}
      />,
    )
    const box = checkbox(container)
    expect(box.checked).toBe(false)

    await act(async () => {
      box.click()
    })
    expect(box.checked).toBe(true)
    expect(onToggle).toHaveBeenCalledWith(LONG_PATH, 'stage')
  })

  it('coalesces rapid clicks into a single in-flight request, then reconciles to the last click', async () => {
    const first = deferred<boolean>()
    const onToggle = vi.fn()
    onToggle.mockReturnValueOnce(first.promise).mockResolvedValue(true)
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: ' ' })}
        side="index"
        onToggle={onToggle}
        onOpenDiff={noop}
      />,
    )
    const box = checkbox(container)

    await act(async () => {
      box.click()
    })
    expect(box.checked).toBe(false)
    expect(onToggle).toHaveBeenNthCalledWith(1, LONG_PATH, 'unstage')

    // A second click while the first request is in flight re-checks the box but
    // must NOT fire a second, overlapping request.
    await act(async () => {
      box.click()
    })
    expect(box.checked).toBe(true)
    expect(onToggle).toHaveBeenCalledTimes(1)

    // Settling the first request drains the queued (last-click) intent.
    await act(async () => {
      first.resolve(true)
      await flush()
    })
    expect(onToggle).toHaveBeenNthCalledWith(2, LONG_PATH, 'stage')
    expect(box.checked).toBe(true)
  })

  it('honours the last click when an earlier in-flight request fails', async () => {
    const first = deferred<boolean>()
    const onToggle = vi.fn()
    onToggle.mockReturnValueOnce(first.promise).mockResolvedValue(true)
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: ' ' })}
        side="index"
        onToggle={onToggle}
        onOpenDiff={noop}
      />,
    )
    const box = checkbox(container)

    await act(async () => {
      box.click() // unstage
    })
    expect(box.checked).toBe(false)

    await act(async () => {
      box.click() // re-check, queued while the first request is in flight
    })
    expect(box.checked).toBe(true)
    expect(onToggle).toHaveBeenCalledTimes(1)

    // The first (unstage) request fails, but the queued re-check is honoured so
    // the row still reflects the user's last click.
    await act(async () => {
      first.resolve(false)
      await flush()
    })
    expect(onToggle).toHaveBeenNthCalledWith(2, LONG_PATH, 'stage')
    expect(box.checked).toBe(true)
  })

  it('reverts the flip when the toggle handler rejects', async () => {
    const onToggle = vi.fn(() => Promise.reject(new Error('index.lock exists')))
    const container = render(
      <GitFileEntry
        entry={makeEntry({ status: 'M', indexStatus: 'M', worktreeStatus: ' ' })}
        side="index"
        onToggle={onToggle}
        onOpenDiff={noop}
      />,
    )
    const box = checkbox(container)
    expect(box.checked).toBe(true)

    await act(async () => {
      box.click()
      await flush()
    })
    // A rejection is treated like a `false` return: the optimistic flip reverts.
    expect(box.checked).toBe(true)
  })

  it('does not re-apply a stale override when the same status recurs', async () => {
    const onToggle = vi.fn(() => new Promise<boolean>(() => {})) // never settles
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    const path = '/repo/a.ts'
    const entryM = makeEntry({ path, status: 'M', indexStatus: ' ', worktreeStatus: 'M' })
    const renderRow = (entry: GitPanelEntry) =>
      root.render(
        <GitFileEntry entry={entry} side="worktree" onToggle={onToggle} onOpenDiff={noop} />,
      )

    act(() => renderRow(entryM))
    const box = checkbox(container)
    expect(box.checked).toBe(false)

    await act(async () => {
      box.click()
    })
    expect(box.checked).toBe(true)

    // The server status changes → the override is dropped…
    act(() =>
      renderRow(makeEntry({ path, status: 'D', indexStatus: ' ', worktreeStatus: 'D' })),
    )
    expect(box.checked).toBe(false)

    // …so when the original status recurs the stale override must not re-apply.
    act(() => renderRow(entryM))
    expect(box.checked).toBe(false)
  })

  it('drops the override once the server state it was applied against changes', async () => {
    const onToggle = vi.fn(() => new Promise<boolean>(() => {}))
    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    const path = '/repo/a.ts'

    act(() => {
      root.render(
        <GitFileEntry
          entry={makeEntry({ path, status: 'M', indexStatus: ' ', worktreeStatus: 'M' })}
          side="worktree"
          onToggle={onToggle}
          onOpenDiff={noop}
        />,
      )
    })
    const box = checkbox(container)
    expect(box.checked).toBe(false)

    await act(async () => {
      box.click()
    })
    expect(box.checked).toBe(true)

    // Same path, new server status → serverKey changes → the override no longer
    // applies and the box re-derives from the (now unmodified) server state.
    act(() => {
      root.render(
        <GitFileEntry
          entry={makeEntry({ path, status: 'D', indexStatus: ' ', worktreeStatus: 'D' })}
          side="worktree"
          onToggle={onToggle}
          onOpenDiff={noop}
        />,
      )
    })
    expect(box.checked).toBe(false)
  })
})
