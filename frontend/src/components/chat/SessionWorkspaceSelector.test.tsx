// Tests for SessionWorkspaceSelector — the chat toolbar's draft/pinned
// workspace selector (ADR-080 draft UX).
//
// Covers: hidden in CHAT mode / no project; hidden while the experimental
// gate is off; interactive draft state (`local`
// default, branch display, `branch…` opens the picker in draft mode and arms
// the draft when missing, non-git projects hide `branch…`, choosing local
// resets a branch draft); permanently disabled pinned button for existing
// managed and local sessions — the pin ignores the transient selector lock,
// so it stays disabled across chat continuations; draft retirement when a
// session becomes active.
// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { TooltipProvider } from '@/components/ui/tooltip'

import { SessionWorkspaceSelector } from './SessionWorkspaceSelector'
import { useSessionDraftStore } from '@/stores/sessionDraftStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useExperimentalStore } from '@/stores/experimentalStore'
import type { ProjectInfo, SessionInfo } from '@/types/models'

let container: HTMLDivElement | null = null
let root: Root | null = null

function makeProject(overrides: Partial<ProjectInfo> = {}): ProjectInfo {
  return {
    id: 'proj-1',
    name: 'Proj',
    workspace_path: '/w/proj',
    is_external: false,
    is_no_project: false,
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

function makeSession(overrides: Partial<SessionInfo> = {}): SessionInfo {
  return {
    id: 's-1',
    project_id: 'proj-1',
    name: 'Session',
    created_at: '2026-01-01T00:00:00Z',
    last_active_at: '2026-01-01T00:00:00Z',
    archived: false,
    pinned: false,
    active: false,
    total_input_tokens: 0,
    total_output_tokens: 0,
    model: 'm',
    family: 'f',
    has_unfinished_task: false,
    ...overrides,
  }
}

function renderSelector(props: { disabled?: boolean } = {}) {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(
      <TooltipProvider>
        <SessionWorkspaceSelector {...props} />
      </TooltipProvider>,
    )
  })
}

function body(): HTMLElement {
  return document.body
}

function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

function trigger(): HTMLButtonElement | null {
  return body().querySelector<HTMLButtonElement>('[data-testid="session-workspace-selector"]')
}

function readOnly(): HTMLButtonElement | null {
  return body().querySelector<HTMLButtonElement>('[data-testid="session-workspace-readonly"]')
}

function menuItem(testId: string): HTMLElement | null {
  return body().querySelector<HTMLElement>(`[data-testid="${testId}"]`)
}

async function openMenu() {
  act(() => {
    trigger()!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
  })
  await flush()
}

function resetStores(opts: { noProject?: boolean; gitRepo?: boolean } = {}) {
  // Default the experimental gate ON so the existing cases exercise the
  // selector itself; the gate-off case flips it off explicitly.
  useExperimentalStore.setState({ enabled: true })
  useSessionDraftStore.getState().clearDraft()
  useSessionDraftStore.getState().setCommitting(false)
  useSessionStore.setState({ sessions: null, activeSessionId: null })
  useGitPanelStore.getState().reset()
  const project = opts.noProject
    ? makeProject({ id: '__no_project__', is_no_project: true })
    : makeProject()
  useProjectStore.setState({
    activeProjectId: opts.noProject ? '__no_project__' : 'proj-1',
    projects: [project],
  })
  if (opts.gitRepo) {
    useGitPanelStore.getState().setGitRepo(true, 'proj-1')
  }
}

beforeEach(() => {
  resetStores()
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

describe('SessionWorkspaceSelector', () => {
  it('renders nothing in CHAT (No Project) mode', () => {
    resetStores({ noProject: true })
    renderSelector()
    expect(trigger()).toBeNull()
    expect(readOnly()).toBeNull()
  })

  it('renders nothing while no project is active', () => {
    useProjectStore.setState({ activeProjectId: null, projects: [] })
    renderSelector()
    expect(trigger()).toBeNull()
  })

  it('renders nothing while the experimental gate is off (visibility-only gate)', () => {
    // The picker/display is an experimental surface: with the master switch
    // off it must render nothing, though the underlying functionality and the
    // hooks (draft retirement) stay intact.
    useExperimentalStore.setState({ enabled: false })
    useGitPanelStore.getState().setGitRepo(true, 'proj-1')
    renderSelector()
    expect(trigger()).toBeNull()
    expect(readOnly()).toBeNull()

    // The pinned disabled button for an existing session is gated the same way.
    act(() => {
      useSessionStore.setState({
        activeSessionId: 's-m',
        sessions: [
          makeSession({
            id: 's-m',
            workspace_binding: {
              kind: 'managed_worktree',
              workspace_path: '/w/proj/.worktrees/s-abcd1234',
              worktree_name: 's-abcd1234',
              branch: 'feat/pinned',
            },
          }),
        ],
      })
    })
    expect(readOnly()).toBeNull()
    expect(trigger()).toBeNull()
  })

  it('draft state (no session): shows local and offers local + branch… in a git repo', async () => {
    useGitPanelStore.getState().setGitRepo(true, 'proj-1')
    renderSelector()
    await openMenu()

    expect(trigger()!.textContent).toContain('local')
    expect(menuItem('workspace-option-local')).not.toBeNull()
    expect(menuItem('workspace-option-branch')).not.toBeNull()
  })

  it('branch draft: the trigger shows the drafted branch name', () => {
    useSessionDraftStore.getState().startDraft('proj-1')
    useSessionDraftStore
      .getState()
      .setDraftWorkspace({ kind: 'branch', branch: 'feat/x', createBranch: false, startPoint: '' })
    renderSelector()

    expect(trigger()!.textContent).toContain('feat/x')
  })

  it('picking branch… arms the draft when missing and opens the picker in draft mode', async () => {
    useGitPanelStore.getState().setGitRepo(true, 'proj-1')
    renderSelector()
    await openMenu()

    act(() => {
      menuItem('workspace-option-branch')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(true)
    expect(useGitPanelStore.getState().branchPickerMode).toBe('draft')
    expect(useSessionDraftStore.getState().draft).toEqual({
      projectId: 'proj-1',
      workspace: { kind: 'local' },
    })
  })

  it('picking local resets an armed branch draft back to local', async () => {
    useSessionDraftStore.getState().startDraft('proj-1')
    useSessionDraftStore
      .getState()
      .setDraftWorkspace({ kind: 'branch', branch: 'feat/x', createBranch: false, startPoint: '' })
    renderSelector()
    await openMenu()

    act(() => {
      menuItem('workspace-option-local')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(useSessionDraftStore.getState().draft?.workspace).toEqual({ kind: 'local' })
  })

  it('hides branch… when the project is not a git repository', async () => {
    renderSelector()
    await openMenu()

    expect(menuItem('workspace-option-local')).not.toBeNull()
    expect(menuItem('workspace-option-branch')).toBeNull()
  })

  it('managed session: shows the pinned branch as a permanently disabled button', () => {
    useSessionStore.setState({
      activeSessionId: 's-m',
      sessions: [
        makeSession({
          id: 's-m',
          workspace_binding: {
            kind: 'managed_worktree',
            workspace_path: '/w/proj/.worktrees/s-abcd1234',
            worktree_name: 's-abcd1234',
            branch: 'feat/pinned',
          },
        }),
      ],
    })
    renderSelector()

    const chip = readOnly()
    expect(chip).not.toBeNull()
    // A real disabled button, not an enabled-looking passive element: the
    // disabled state must be visible up front, not discovered by clicks that
    // go nowhere. Expressed as aria-disabled (not the native attribute) so
    // the chip stays focusable and its reason is announced (see the
    // accessibility test below).
    expect(chip!.tagName).toBe('BUTTON')
    expect(chip!.getAttribute('aria-disabled')).toBe('true')
    expect(chip!.disabled).toBe(false)
    expect(chip!.className).toContain('cursor-not-allowed')
    expect(chip!.textContent).toContain('feat/pinned')
    expect(trigger()).toBeNull()
  })

  it('local session: shows local as a permanently disabled button', () => {
    useSessionStore.setState({
      activeSessionId: 's-l',
      sessions: [makeSession({ id: 's-l', workspace_binding: { kind: 'local', workspace_path: '/w/proj' } })],
    })
    renderSelector()

    const chip = readOnly()
    expect(chip).not.toBeNull()
    expect(chip!.tagName).toBe('BUTTON')
    expect(chip!.getAttribute('aria-disabled')).toBe('true')
    expect(chip!.disabled).toBe(false)
    expect(chip!.textContent).toContain('local')
  })

  it('pinned chip is focusable and announces its pin reason to assistive tech', () => {
    // A natively `disabled` button is removed from the tab order and
    // suppresses pointer events, so the `title`-only reason was unreachable
    // by keyboard/screen-reader (and the tooltip itself unreliable in the
    // WebKit webview). aria-disabled keeps the chip in the tab order and
    // aria-describedby carries the reason in a visually-hidden element.
    useSessionStore.setState({
      activeSessionId: 's-m',
      sessions: [
        makeSession({
          id: 's-m',
          workspace_binding: {
            kind: 'managed_worktree',
            workspace_path: '/w/proj/.worktrees/s-abcd1234',
            worktree_name: 's-abcd1234',
            branch: 'feat/pinned',
          },
        }),
      ],
    })
    renderSelector()

    const chip = readOnly()
    expect(chip).not.toBeNull()
    // Focusable (no native disabled) — the reason is reachable by keyboard.
    expect(chip!.disabled).toBe(false)
    // The description reference resolves to a non-empty element that names
    // the reason: fixed at creation.
    const describedBy = chip!.getAttribute('aria-describedby')
    expect(describedBy).toBeTruthy()
    const description = document.getElementById(describedBy!)
    expect(description).not.toBeNull()
    expect(description!.textContent).toContain('fixed when the session was created')
    // The visually-hidden description is hidden visually but exposed to AT.
    expect(description!.className).toContain('sr-only')
    // The mouse tooltip mirrors the same reason (works again: no native
    // disabled suppressing pointer events).
    expect(chip!.getAttribute('title')).toContain('fixed when the session was created')
  })

  it('pinned button stays disabled across chat continuations (ignores the transient selector lock)', () => {
    // The mid-task selector lock (the `disabled` prop) lifts when the task
    // settles — that is exactly when a chat continuation becomes possible.
    // The pin must not lift with it: the workspace binding is immutable for
    // the session's whole lifetime, so the button stays disabled even though
    // the transient lock is gone.
    useSessionStore.setState({
      activeSessionId: 's-m',
      sessions: [
        makeSession({
          id: 's-m',
          workspace_binding: {
            kind: 'managed_worktree',
            workspace_path: '/w/proj/.worktrees/s-abcd1234',
            worktree_name: 's-abcd1234',
            branch: 'feat/pinned',
          },
        }),
      ],
    })
    renderSelector({ disabled: false })

    const chip = readOnly()
    expect(chip).not.toBeNull()
    expect(chip!.tagName).toBe('BUTTON')
    expect(chip!.getAttribute('aria-disabled')).toBe('true')
  })

  it('selecting a session retires an armed draft', async () => {
    useSessionDraftStore.getState().startDraft('proj-1')
    renderSelector()
    expect(useSessionDraftStore.getState().draft).not.toBeNull()

    act(() => {
      useSessionStore.getState().setActiveSessionId('s-other')
    })
    await flush()

    expect(useSessionDraftStore.getState().draft).toBeNull()
  })

  it('switching to another project retires the draft', async () => {
    useSessionDraftStore.getState().startDraft('proj-1')
    renderSelector()

    act(() => {
      useProjectStore.setState({
        activeProjectId: 'proj-2',
        projects: [makeProject(), makeProject({ id: 'proj-2', name: 'Two' })],
      })
    })
    await flush()

    expect(useSessionDraftStore.getState().draft).toBeNull()
  })
})
