// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { apiMocks, onGlobalEventMock, capturedHandlers } = vi.hoisted(() => ({
  apiMocks: {
    getHardenGitRepos: vi.fn<() => Promise<string[]>>(),
    removeHardenGitRepo: vi.fn<(path: string) => Promise<void>>(),
  },
  // Capture onGlobalEvent handlers by event name so tests can emit
  // config:updated (the backend's post-persist signal) at the dialog.
  capturedHandlers: new Map<string, () => void>(),
  onGlobalEventMock: vi.fn((name: string, handler: () => void) => {
    capturedHandlers.set(name, handler)
    return () => {
      capturedHandlers.delete(name)
    }
  }),
}))

vi.mock('@/api/gitConfigRisk', () => ({
  getHardenGitRepos: apiMocks.getHardenGitRepos,
  removeHardenGitRepo: apiMocks.removeHardenGitRepo,
}))

vi.mock('@/api/runtime', () => ({ onGlobalEvent: onGlobalEventMock }))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import { HardenReposDialog } from './HardenReposDialog'

// Radix portals the dialog content to document.body, so queries target the
// whole document rather than the render container.
const dialog = () => document.querySelector('[data-testid="harden-repos-dialog"]')

const renderDialog = (root: Root, open: boolean, onOpenChange: (v: boolean) => void = () => {}) =>
  act(async () => {
    root.render(<HardenReposDialog open={open} onOpenChange={onOpenChange} />)
  })

describe('HardenReposDialog', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    apiMocks.getHardenGitRepos.mockReset()
    apiMocks.removeHardenGitRepo.mockReset()
    apiMocks.getHardenGitRepos.mockResolvedValue([])
    onGlobalEventMock.mockClear()
    capturedHandlers.clear()
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  it('renders nothing while closed and loads nothing', async () => {
    await renderDialog(root, false)
    expect(dialog()).toBeNull()
    expect(apiMocks.getHardenGitRepos).not.toHaveBeenCalled()
  })

  it('lists the hardened repositories on open', async () => {
    apiMocks.getHardenGitRepos.mockResolvedValue(['/Users/dev/repo-a', '/srv/work/repo-b'])
    await renderDialog(root, true)
    const el = dialog()
    expect(el).not.toBeNull()
    expect(el?.textContent).toContain('/Users/dev/repo-a')
    expect(el?.textContent).toContain('/srv/work/repo-b')
  })

  it('shows the empty state when nothing is hardened', async () => {
    await renderDialog(root, true)
    const el = dialog()
    expect(el?.textContent).toContain('No hardened repositories')
  })

  it('removes an entry via the per-row button and drops it from the list', async () => {
    apiMocks.getHardenGitRepos.mockResolvedValue(['/Users/dev/repo-a', '/srv/work/repo-b'])
    apiMocks.removeHardenGitRepo.mockResolvedValue(undefined)
    await renderDialog(root, true)

    const removeBtn = document.querySelector<HTMLButtonElement>(
      '[data-testid="harden-repo-remove-0"]',
    )
    expect(removeBtn).not.toBeNull()
    await act(async () => {
      removeBtn?.click()
    })

    expect(apiMocks.removeHardenGitRepo).toHaveBeenCalledWith('/Users/dev/repo-a')
    const el = dialog()
    expect(el?.textContent).not.toContain('/Users/dev/repo-a')
    expect(el?.textContent).toContain('/srv/work/repo-b')
  })

  it('keeps the entry and surfaces the error when removal fails', async () => {
    apiMocks.getHardenGitRepos.mockResolvedValue(['/Users/dev/repo-a'])
    apiMocks.removeHardenGitRepo.mockRejectedValue(new Error('config not initialized'))
    await renderDialog(root, true)

    const removeBtn = document.querySelector<HTMLButtonElement>(
      '[data-testid="harden-repo-remove-0"]',
    )
    await act(async () => {
      removeBtn?.click()
    })

    const el = dialog()
    expect(el?.textContent).toContain('/Users/dev/repo-a')
    expect(document.querySelector('[data-testid="harden-repos-error"]')?.textContent).toContain(
      'config not initialized',
    )
  })

  it('surfaces a load failure with the empty list', async () => {
    apiMocks.getHardenGitRepos.mockRejectedValue(new Error('rpc unavailable'))
    await renderDialog(root, true)
    const el = dialog()
    expect(el?.textContent).toContain('No hardened repositories')
    expect(document.querySelector('[data-testid="harden-repos-error"]')?.textContent).toContain(
      'rpc unavailable',
    )
  })

  it('reloads the list on config:updated while open (a repo hardened via the toast mid-dialog)', async () => {
    apiMocks.getHardenGitRepos
      .mockResolvedValueOnce([]) // initial load on open: nothing hardened
      .mockResolvedValueOnce(['/srv/newly-hardened']) // reload after the toast's persist
    await renderDialog(root, true)
    expect(dialog()?.textContent).toContain('No hardened repositories')

    await act(async () => {
      capturedHandlers.get('config:updated')?.()
    })

    expect(apiMocks.getHardenGitRepos).toHaveBeenCalledTimes(2)
    expect(dialog()?.textContent).toContain('/srv/newly-hardened')
  })

  it('does not subscribe to config:updated while closed', async () => {
    await renderDialog(root, false)
    expect(capturedHandlers.has('config:updated')).toBe(false)
  })
})
