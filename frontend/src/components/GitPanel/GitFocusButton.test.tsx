// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the focus actions so tests never touch the Wails backend ---
const { focusMocks } = vi.hoisted(() => ({
  focusMocks: {
    focusSessionWorkspace: vi.fn(),
  },
}))

vi.mock('@/lib/gitFocus', () => focusMocks)

import { GitFocusButton } from './GitFocusButton'
import { focusSessionWorkspace } from '@/lib/gitFocus'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useExperimentalStore } from '@/stores/experimentalStore'
import type { GitPanelFocus } from '@/types/models'

let container: HTMLDivElement | null = null
let root: Root | null = null

function renderButton(): HTMLButtonElement {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(<GitFocusButton />)
  })
  return container.querySelector<HTMLButtonElement>('[data-testid="git-focus-button"]')!
}

function makeFocus(overrides: Partial<GitPanelFocus> = {}): GitPanelFocus {
  return {
    path: '/repo/.worktrees/s-abc12345',
    name: 's-abc12345',
    kind: 'managed',
    branch: 'sess/s-abc12345',
    managed: true,
    pinned: true,
    ...overrides,
  }
}

beforeEach(() => {
  focusMocks.focusSessionWorkspace.mockReset()
  focusMocks.focusSessionWorkspace.mockResolvedValue(true)
  useGitPanelStore.getState().reset()
  // Default the experimental gate ON so the existing cases exercise the
  // button itself; the gate-off case flips it off explicitly.
  useExperimentalStore.setState({ enabled: true })
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

describe('GitFocusButton', () => {
  it('renders the crosshair button with the session-focus title before any focus resolves', () => {
    const button = renderButton()

    expect(button).not.toBeNull()
    expect(button.getAttribute('aria-pressed')).toBe('false')
    expect(button.getAttribute('title')).toBe('Focus the Git panel on the current session')
  })

  it('shows the focused worktree in the title and stays un-highlighted while focused on the session worktree', () => {
    const focus = makeFocus()
    act(() => {
      useGitPanelStore.getState().setFocus(focus)
      useGitPanelStore.getState().setFocusSessionPath(focus.path)
    })

    const button = renderButton()

    expect(button.getAttribute('title')).toContain('s-abc12345')
    expect(button.getAttribute('title')).toContain('sess/s-abc12345')
    expect(button.getAttribute('aria-pressed')).toBe('false')
    expect(button.hasAttribute('data-diverged')).toBe(false)
  })

  it('is highlighted (aria-pressed) while the focus diverged from the session worktree', () => {
    act(() => {
      useGitPanelStore.getState().setFocus(makeFocus({ path: '/repo', name: 'repo', kind: 'main', managed: false, pinned: false, branch: 'main' }))
      useGitPanelStore.getState().setFocusSessionPath('/repo/.worktrees/s-abc12345')
    })

    const button = renderButton()

    expect(button.getAttribute('aria-pressed')).toBe('true')
    expect(button.getAttribute('data-diverged')).toBe('true')
    expect(button.getAttribute('title')).toContain('click to focus the current session')
  })

  it('clicking focuses the panel on the current session worktree', () => {
    act(() => {
      useGitPanelStore.getState().setFocus(makeFocus({ path: '/repo', name: 'repo', kind: 'main', managed: false, pinned: false }))
      useGitPanelStore.getState().setFocusSessionPath('/repo/.worktrees/s-abc12345')
    })

    const button = renderButton()
    act(() => {
      button.click()
    })

    expect(focusSessionWorkspace).toHaveBeenCalledTimes(1)
  })

  it('renders nothing while the experimental gate is off (visibility-only gate)', () => {
    // The crosshair is an experimental surface: with the master switch off it
    // must render nothing, though the focus functionality stays intact.
    useExperimentalStore.setState({ enabled: false })
    container = document.createElement('div')
    document.body.appendChild(container)
    const r = createRoot(container)
    root = r
    act(() => {
      r.render(<GitFocusButton />)
    })

    expect(container.querySelector('[data-testid="git-focus-button"]')).toBeNull()
  })
})
