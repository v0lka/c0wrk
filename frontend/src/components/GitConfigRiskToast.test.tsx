// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { GitConfigRiskData } from '@/types/events'

// --- Mock the API layer so tests never touch the Wails backend ---

const { apiMocks } = vi.hoisted(() => ({
  apiMocks: {
    onGitConfigRisk: vi.fn<(cb: (data: GitConfigRiskData) => void) => (() => void)>(),
    trustGitRepo: vi.fn<(path: string) => Promise<void>>(),
    hardenGitRepo: vi.fn<(path: string) => Promise<void>>(),
    subscribe: vi.fn<(event: string, cb: (data: unknown) => void) => (() => void)>(),
  },
}))

vi.mock('@/api/gitConfigRisk', () => ({
  onGitConfigRisk: apiMocks.onGitConfigRisk,
  trustGitRepo: apiMocks.trustGitRepo,
  hardenGitRepo: apiMocks.hardenGitRepo,
}))

vi.mock('@/api/runtime', () => ({
  subscribe: apiMocks.subscribe,
}))

vi.mock('@/lib/logger', () => ({
  logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

import { GitConfigRiskToast } from './GitConfigRiskToast'
import { useProjectStore } from '@/stores/projectStore'

const DANGEROUS: GitConfigRiskData = {
  path: '/tmp/untrusted-repo',
  source: 'project',
  notice: 'Repository-defined git hooks do not run inside c0wrk.',
  findings: [
    { key: 'core.fsmonitor', description: 'runs a filesystem-monitor command' },
    { key: 'filter.lfs.process', description: 'runs an external filter' },
  ],
}

const DRIFTED: GitConfigRiskData = {
  ...DANGEROUS,
  reason: 'This repository was previously trusted, but its git configuration changed since you trusted it.',
  diff: '--- trusted\n+++ current\n@@ -1 +1 @@\n-core.fsmonitor = /safe/bin\n+core.fsmonitor = /evil/bin',
}

describe('GitConfigRiskToast', () => {
  let container: HTMLDivElement
  let root: Root
  let fire: ((data: GitConfigRiskData) => void) | null
  let fireSwitched: ((data: unknown) => void) | null

  const render = () => {
    act(() => {
      root.render(<GitConfigRiskToast />)
    })
  }

  beforeEach(() => {
    fire = null
    fireSwitched = null
    apiMocks.onGitConfigRisk.mockReset()
    apiMocks.trustGitRepo.mockReset()
    apiMocks.hardenGitRepo.mockReset()
    apiMocks.subscribe.mockReset()
    apiMocks.onGitConfigRisk.mockImplementation((cb: (data: GitConfigRiskData) => void) => {
      fire = cb
      return () => {}
    })
    apiMocks.subscribe.mockImplementation((event: string, cb: (data: unknown) => void) => {
      if (event === 'project:switched') fireSwitched = cb
      return () => {}
    })
    useProjectStore.getState().setActiveProjectId(null)
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  const toast = () => container.querySelector('[data-testid="git-config-risk-toast"]')

  const click = async (testid: string) => {
    const btn = container.querySelector<HTMLButtonElement>(`[data-testid="${testid}"]`)
    expect(btn).not.toBeNull()
    await act(async () => {
      btn?.click()
    })
  }

  it('renders nothing until an event arrives (clean repos never warn)', () => {
    render()
    expect(fire).not.toBeNull()
    expect(toast()).toBeNull()
  })

  it('shows the notice, the scanned path and every detected key', () => {
    render()
    act(() => fire?.(DANGEROUS))
    const el = toast()
    expect(el).not.toBeNull()
    expect(el?.textContent).toContain('Repository-defined git hooks do not run inside c0wrk.')
    expect(el?.textContent).toContain('/tmp/untrusted-repo')
    expect(el?.textContent).toContain('core.fsmonitor')
    expect(el?.textContent).toContain('filter.lfs.process')
    expect(el?.getAttribute('role')).toBe('alert')
  })

  it('offers exactly Trust, Harden and close — no Ignore/Fix', () => {
    render()
    act(() => fire?.(DANGEROUS))
    expect(container.querySelector('[data-testid="git-config-risk-trust"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="git-config-risk-harden"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="git-config-risk-close"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="git-config-risk-ignore"]')).toBeNull()
    expect(container.querySelector('[data-testid="git-config-risk-fix"]')).toBeNull()
  })

  it('replaces the visible warning when a newer event arrives', () => {
    render()
    act(() => fire?.(DANGEROUS))
    act(() =>
      fire?.({
        path: '/tmp/added-workdir',
        source: 'workdir',
        notice: 'notice-two',
        findings: [{ key: 'merge.evil.driver', description: 'custom merge driver' }],
      }),
    )
    const el = toast()
    expect(el?.textContent).toContain('Added working directory')
    expect(el?.textContent).toContain('/tmp/added-workdir')
    expect(el?.textContent).not.toContain('filter.lfs.process')
  })

  it('renders every finding even when keys repeat (duplicate config keys, several include markers)', () => {
    render()
    act(() =>
      fire?.({
        path: '/tmp/dup',
        source: 'project',
        notice: 'notice',
        findings: [
          { key: '(include directive)', description: 'first include' },
          { key: '(include directive)', description: 'second include' },
          { key: 'filter.lfs.process', description: 'first occurrence' },
          { key: 'filter.lfs.process', description: 'second occurrence' },
        ],
      }),
    )
    const items = container.querySelectorAll('li')
    expect(items).toHaveLength(4)
    expect(items[0]?.textContent).toContain('first include')
    expect(items[1]?.textContent).toContain('second include')
    expect(items[2]?.textContent).toContain('first occurrence')
    expect(items[3]?.textContent).toContain('second occurrence')
  })

  it('does not render reason or diff for ordinary first-time warnings', () => {
    render()
    act(() => fire?.(DANGEROUS))
    expect(container.querySelector('[data-testid="git-config-risk-reason"]')).toBeNull()
    expect(container.querySelector('[data-testid="git-config-risk-diff"]')).toBeNull()
  })

  it('renders the re-confirmation reason and diff when a trusted repo drifted', () => {
    render()
    act(() => fire?.(DRIFTED))
    expect(container.querySelector('[data-testid="git-config-risk-reason"]')?.textContent).toContain(
      'its git configuration changed',
    )
    const diff = container.querySelector('[data-testid="git-config-risk-diff"]')
    expect(diff?.textContent).toContain('core.fsmonitor = /evil/bin')
  })

  it('the close (×) dismisses the warning without deciding (repo stays pending)', async () => {
    render()
    act(() => fire?.(DANGEROUS))
    await click('git-config-risk-close')
    expect(toast()).toBeNull()
    expect(apiMocks.trustGitRepo).not.toHaveBeenCalled()
    expect(apiMocks.hardenGitRepo).not.toHaveBeenCalled()
  })

  it('Trust this repo persists the scanned path and dismisses the toast', async () => {
    apiMocks.trustGitRepo.mockResolvedValue(undefined)
    render()
    act(() => fire?.(DANGEROUS))
    await click('git-config-risk-trust')
    expect(apiMocks.trustGitRepo).toHaveBeenCalledWith('/tmp/untrusted-repo')
    expect(toast()).toBeNull()
    expect(apiMocks.hardenGitRepo).not.toHaveBeenCalled()
  })

  it('a failed trust keeps the toast open and surfaces the error', async () => {
    apiMocks.trustGitRepo.mockRejectedValue(new Error('config not initialized'))
    render()
    act(() => fire?.(DANGEROUS))
    await click('git-config-risk-trust')
    expect(toast()).not.toBeNull()
    expect(container.querySelector('[data-testid="git-config-risk-error"]')?.textContent).toContain(
      'config not initialized',
    )
  })

  it('Harden persists the scanned path and dismisses the toast', async () => {
    apiMocks.hardenGitRepo.mockResolvedValue(undefined)
    render()
    act(() => fire?.(DANGEROUS))
    await click('git-config-risk-harden')
    expect(apiMocks.hardenGitRepo).toHaveBeenCalledWith('/tmp/untrusted-repo')
    expect(toast()).toBeNull()
    expect(apiMocks.trustGitRepo).not.toHaveBeenCalled()
  })

  it('a failed harden keeps the toast open and surfaces the error', async () => {
    apiMocks.hardenGitRepo.mockRejectedValue(new Error('config not initialized'))
    render()
    act(() => fire?.(DANGEROUS))
    await click('git-config-risk-harden')
    expect(toast()).not.toBeNull()
    expect(container.querySelector('[data-testid="git-config-risk-error"]')?.textContent).toContain(
      'config not initialized',
    )
  })

  // --- Project-switch reset: a decision must never land in another project ---

  it('dismisses the warning when the user switches to a different project', () => {
    render()
    act(() => useProjectStore.getState().setActiveProjectId('proj-a'))
    act(() => fire?.(DANGEROUS))
    expect(toast()).not.toBeNull()
    act(() => fireSwitched?.({ id: 'proj-b', name: 'B', workspace_path: '/tmp/b' }))
    expect(toast()).toBeNull()
  })

  it('keeps the warning on a same-project switched re-emit (reconciliation)', () => {
    render()
    act(() => useProjectStore.getState().setActiveProjectId('proj-a'))
    act(() => fire?.(DANGEROUS))
    act(() => fireSwitched?.({ id: 'proj-a', name: 'A', workspace_path: '/tmp/a' }))
    expect(toast()).not.toBeNull()
  })

  // --- Race guard: a newer event during an in-flight action wins ---

  it('a late trust resolution does not close a newer warning', async () => {
    let resolveTrust: (() => void) | null = null
    apiMocks.trustGitRepo.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveTrust = resolve
        }),
    )
    render()
    act(() => fire?.(DANGEROUS))
    await click('git-config-risk-trust')
    act(() => fire?.({ ...DANGEROUS, path: '/tmp/second-repo', notice: 'notice-two' }))
    await act(async () => {
      resolveTrust?.()
    })
    const el = toast()
    expect(el).not.toBeNull()
    expect(el?.textContent).toContain('/tmp/second-repo')
  })

  it('a late trust failure does not annotate a newer warning', async () => {
    let rejectTrust: ((err: Error) => void) | null = null
    apiMocks.trustGitRepo.mockImplementation(
      () =>
        new Promise<void>((_, reject) => {
          rejectTrust = reject
        }),
    )
    render()
    act(() => fire?.(DANGEROUS))
    await click('git-config-risk-trust')
    act(() => fire?.({ ...DANGEROUS, path: '/tmp/second-repo' }))
    await act(async () => {
      rejectTrust?.(new Error('config not initialized'))
    })
    // The failure belongs to the replaced warning: no error line, and the
    // new warning's actions are usable (pending flags were reset by the event).
    expect(toast()).not.toBeNull()
    expect(container.querySelector('[data-testid="git-config-risk-error"]')).toBeNull()
    const trustBtn = container.querySelector<HTMLButtonElement>('[data-testid="git-config-risk-trust"]')
    expect(trustBtn?.disabled).toBe(false)
  })

  it('a late harden resolution does not close a newer warning', async () => {
    let resolveHarden: (() => void) | null = null
    apiMocks.hardenGitRepo.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveHarden = resolve
        }),
    )
    render()
    act(() => fire?.(DANGEROUS))
    await click('git-config-risk-harden')
    expect(apiMocks.hardenGitRepo.mock.calls[0]?.[0]).toBe('/tmp/untrusted-repo')
    act(() => fire?.({ ...DANGEROUS, path: '/tmp/second-repo', notice: 'notice-two' }))
    await act(async () => {
      resolveHarden?.()
    })
    const el = toast()
    expect(el).not.toBeNull()
    expect(el?.textContent).toContain('/tmp/second-repo')
  })
})
