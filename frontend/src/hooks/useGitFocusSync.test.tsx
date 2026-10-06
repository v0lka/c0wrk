// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the focus actions so tests never touch the Wails backend ---
const { focusMocks } = vi.hoisted(() => ({
  focusMocks: {
    focusSessionWorkspace: vi.fn(),
    clearGitFocusState: vi.fn(),
  },
}))

vi.mock('@/lib/gitFocus', () => focusMocks)

import { useGitFocusSync } from './useGitFocusSync'
import { focusSessionWorkspace, clearGitFocusState } from '@/lib/gitFocus'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'

/** Minimal probe component mounting the side-effect hook. */
function Probe() {
  useGitFocusSync()
  return null
}

let container: HTMLDivElement | null = null
let root: Root | null = null

function renderProbe(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(<Probe />)
  })
}

beforeEach(() => {
  focusMocks.focusSessionWorkspace.mockReset()
  focusMocks.focusSessionWorkspace.mockResolvedValue(true)
  focusMocks.clearGitFocusState.mockReset()
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

describe('useGitFocusSync', () => {
  it('applies the session-default focus on mount for an active project', () => {
    useProjectStore.setState({ activeProjectId: 'p1' })
    useSessionStore.setState({ activeSessionId: null })

    renderProbe()

    expect(focusSessionWorkspace).toHaveBeenCalledTimes(1)
    expect(clearGitFocusState).not.toHaveBeenCalled()
  })

  it('re-applies the focus when the active session changes', () => {
    useProjectStore.setState({ activeProjectId: 'p1' })
    useSessionStore.setState({ activeSessionId: 'sess-1' })
    renderProbe()

    expect(focusSessionWorkspace).toHaveBeenCalledTimes(1)

    act(() => {
      useSessionStore.setState({ activeSessionId: 'sess-2' })
    })

    expect(focusSessionWorkspace).toHaveBeenCalledTimes(2)
  })

  it('re-applies the focus when the active project changes', () => {
    useProjectStore.setState({ activeProjectId: 'p1' })
    renderProbe()

    act(() => {
      useProjectStore.setState({ activeProjectId: 'p2' })
    })

    expect(focusSessionWorkspace).toHaveBeenCalledTimes(2)
  })

  it('clears the mirrored focus state when no project is active', () => {
    useProjectStore.setState({ activeProjectId: null })

    renderProbe()

    expect(focusSessionWorkspace).not.toHaveBeenCalled()
    expect(clearGitFocusState).toHaveBeenCalledTimes(1)
  })
})
