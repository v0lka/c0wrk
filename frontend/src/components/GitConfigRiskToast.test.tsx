// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { GitConfigRiskData } from '@/types/events'

// --- Mock the event API so tests never touch the Wails backend ---

const { apiMocks } = vi.hoisted(() => ({
  apiMocks: {
    onGitConfigRisk: vi.fn<(cb: (data: GitConfigRiskData) => void) => (() => void)>(),
  },
}))

vi.mock('@/api/gitConfigRisk', () => ({
  onGitConfigRisk: apiMocks.onGitConfigRisk,
}))

import { GitConfigRiskToast } from './GitConfigRiskToast'

const DANGEROUS: GitConfigRiskData = {
  path: '/tmp/untrusted-repo',
  source: 'project',
  notice: 'Repository-defined git hooks do not run inside c0wrk.',
  findings: [
    { key: 'core.fsmonitor', description: 'runs a filesystem-monitor command' },
    { key: 'filter.lfs.process', description: 'runs an external filter' },
  ],
}

describe('GitConfigRiskToast', () => {
  let container: HTMLDivElement
  let root: Root
  let fire: ((data: GitConfigRiskData) => void) | null

  const render = () => {
    act(() => {
      root.render(<GitConfigRiskToast />)
    })
  }

  beforeEach(() => {
    fire = null
    apiMocks.onGitConfigRisk.mockImplementation((cb: (data: GitConfigRiskData) => void) => {
      fire = cb
      return () => {}
    })
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  const toast = () => container.querySelector('[data-testid="git-config-risk-toast"]')

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

  it('dismisses and stays hidden afterwards', () => {
    render()
    act(() => fire?.(DANGEROUS))
    const dismiss = container.querySelector<HTMLButtonElement>('[aria-label="Dismiss"]')
    expect(dismiss).not.toBeNull()
    act(() => dismiss?.click())
    expect(toast()).toBeNull()
    // A stale event must not resurrect a dismissed toast — only a new event may.
    expect(toast()).toBeNull()
  })
})
