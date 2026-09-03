// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { apiMocks } = vi.hoisted(() => ({
  apiMocks: {
    getTrustedGitRepos: vi.fn<() => Promise<string[]>>(),
    removeTrustedGitRepo: vi.fn<(path: string) => Promise<void>>(),
  },
}))

vi.mock('@/api/gitConfigRisk', () => ({
  getTrustedGitRepos: apiMocks.getTrustedGitRepos,
  removeTrustedGitRepo: apiMocks.removeTrustedGitRepo,
}))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import { TrustedReposDialog } from './TrustedReposDialog'

// Radix portals the dialog content to document.body, so queries target the
// whole document rather than the render container.
const dialog = () => document.querySelector('[data-testid="trusted-repos-dialog"]')

const renderDialog = (root: Root, open: boolean, onOpenChange: (v: boolean) => void = () => {}) =>
  act(async () => {
    root.render(<TrustedReposDialog open={open} onOpenChange={onOpenChange} />)
  })

describe('TrustedReposDialog', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    apiMocks.getTrustedGitRepos.mockReset()
    apiMocks.removeTrustedGitRepo.mockReset()
    apiMocks.getTrustedGitRepos.mockResolvedValue([])
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  it('renders nothing while closed and loads nothing', async () => {
    await renderDialog(root, false)
    expect(dialog()).toBeNull()
    expect(apiMocks.getTrustedGitRepos).not.toHaveBeenCalled()
  })

  it('lists the trusted repositories on open', async () => {
    apiMocks.getTrustedGitRepos.mockResolvedValue(['/Users/dev/repo-a', '/srv/work/repo-b'])
    await renderDialog(root, true)
    const el = dialog()
    expect(el).not.toBeNull()
    expect(el?.textContent).toContain('/Users/dev/repo-a')
    expect(el?.textContent).toContain('/srv/work/repo-b')
  })

  it('shows the empty state when nothing is trusted', async () => {
    await renderDialog(root, true)
    const el = dialog()
    expect(el?.textContent).toContain('No trusted repositories')
  })

  it('removes an entry via the per-row button and drops it from the list', async () => {
    apiMocks.getTrustedGitRepos.mockResolvedValue(['/Users/dev/repo-a', '/srv/work/repo-b'])
    apiMocks.removeTrustedGitRepo.mockResolvedValue(undefined)
    await renderDialog(root, true)

    const removeBtn = document.querySelector<HTMLButtonElement>(
      '[data-testid="trusted-repo-remove-0"]',
    )
    expect(removeBtn).not.toBeNull()
    await act(async () => {
      removeBtn?.click()
    })

    expect(apiMocks.removeTrustedGitRepo).toHaveBeenCalledWith('/Users/dev/repo-a')
    const el = dialog()
    expect(el?.textContent).not.toContain('/Users/dev/repo-a')
    expect(el?.textContent).toContain('/srv/work/repo-b')
  })

  it('keeps the entry and surfaces the error when removal fails', async () => {
    apiMocks.getTrustedGitRepos.mockResolvedValue(['/Users/dev/repo-a'])
    apiMocks.removeTrustedGitRepo.mockRejectedValue(new Error('config not initialized'))
    await renderDialog(root, true)

    const removeBtn = document.querySelector<HTMLButtonElement>(
      '[data-testid="trusted-repo-remove-0"]',
    )
    await act(async () => {
      removeBtn?.click()
    })

    const el = dialog()
    expect(el?.textContent).toContain('/Users/dev/repo-a')
    expect(document.querySelector('[data-testid="trusted-repos-error"]')?.textContent).toContain(
      'config not initialized',
    )
  })

  it('surfaces a load failure with the empty list', async () => {
    apiMocks.getTrustedGitRepos.mockRejectedValue(new Error('rpc unavailable'))
    await renderDialog(root, true)
    const el = dialog()
    expect(el?.textContent).toContain('No trusted repositories')
    expect(document.querySelector('[data-testid="trusted-repos-error"]')?.textContent).toContain(
      'rpc unavailable',
    )
  })
})
