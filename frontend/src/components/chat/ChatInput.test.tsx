// @vitest-environment jsdom
//
// Regression guard for input-shell resilience.
//
// A crash inside a single input sub-pane (the terminal wraps third-party
// xterm.js and is the most likely offender) must NOT replace the whole chat
// input with the "Input error" fallback. The pane has its own error boundary,
// so the mode toolbar stays mounted and the user can switch back to chat.
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest'
import { createElement } from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { runtimeMock } = vi.hoisted(() => ({
  runtimeMock: {
    subscribe: vi.fn(() => () => {}),
    emit: vi.fn(),
    onGlobalEvent: vi.fn(() => () => {}),
    onSessionEvent: vi.fn(() => () => {}),
    reportDroppedEvent: vi.fn(),
    getApp: vi.fn(),
  },
}))

vi.mock('@/api/runtime', () => runtimeMock)
vi.mock('@/api/config', () => ({
  getConfig: vi.fn().mockResolvedValue({
    loaded: true,
    llm: { all_models: [], default_model: '', models_ready: true },
  }),
}))
vi.mock('@/api/prompt', () => ({ optimizePrompt: vi.fn() }))
vi.mock('@/api/chat', () => ({
  sendMessage: vi.fn(),
  cancelTask: vi.fn(),
  pauseSession: vi.fn(),
  resumeSession: vi.fn(),
}))
vi.mock('@/api/sessions', () => ({ createSession: vi.fn(), listSessions: vi.fn() }))
vi.mock('@/api/agents', () => ({ listAgents: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/workspace', () => ({ listDirectory: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/skills', () => ({ listSkills: vi.fn().mockResolvedValue([]) }))
vi.mock('@/api/attachments', () => ({
  pasteFromClipboard: vi.fn(),
  isImagePath: vi.fn(),
  attachFiles: vi.fn(),
  pickAttachmentFiles: vi.fn(),
}))
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: vi.fn(), cancel: vi.fn(), isProcessing: false }),
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))
// The terminal pane crashes on mount — this is the failure under test.
vi.mock('@/components/terminal/TerminalPanel', () => ({
  TerminalPanel: () => {
    throw new Error('terminal pane boom')
  },
}))

import { ErrorBoundary } from '@/components/ErrorBoundary'
import { ChatInput } from '@/components/chat/ChatInput'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useThemeStore } from '@/stores/themeStore'
import { useUiScaleStore } from '@/stores/uiScaleStore'

beforeAll(() => {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: (query: string) => ({
      matches: false, media: query, onchange: null,
      addListener: () => {}, removeListener: () => {},
      addEventListener: () => {}, removeEventListener: () => {},
      dispatchEvent: () => false,
    }),
  })
  class RO {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  globalThis.ResizeObserver = RO as unknown as typeof ResizeObserver
})

// The terminal pane throws ON PURPOSE in every test here (see the
// TerminalPanel mock above); react-dom reports each caught crash through
// console.error ("The above error occurred in the <TerminalPanel>
// component…"). That report is the expected behavior under test, not a
// regression — silence it to keep the suite output clean.
beforeEach(() => {
  vi.spyOn(console, 'error').mockImplementation(() => {})
})

let container: HTMLDivElement
let root: Root
let outerBoundaryTripped = false

afterEach(() => {
  vi.restoreAllMocks()
  act(() => root.unmount())
  container.remove()
  document.body.innerHTML = ''
})

describe('ChatInput resilience to a pane crash', () => {
  it('keeps the input shell mounted when the terminal pane throws', async () => {
    outerBoundaryTripped = false
    useSessionStore.setState({ sessions: [], activeSessionId: 'sess-a' })
    useProjectStore.setState({ activeProjectId: 'proj-1' })
    useInputModeStore.setState({ mode: 'terminal', pendingInsertion: null, pendingTerminalDir: null })
    useThemeStore.setState({ themeId: 'default-dark', themeCss: '', themeType: 'dark', customThemes: [] })
    useUiScaleStore.setState({ scale: 100 })

    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)

    await act(async () => {
      root.render(
        createElement(ErrorBoundary, {
          fallback: () => {
            outerBoundaryTripped = true
            return createElement('div', null, 'INPUT GONE')
          },
          children: createElement(ChatInput),
        }),
      )
    })

    // The whole-input fallback never fires…
    expect(outerBoundaryTripped).toBe(false)
    expect(container.textContent).not.toContain('INPUT GONE')
    // …the toolbar (mode toggles) is still there…
    expect(container.querySelector('[aria-label="Switch to chat mode"]')).not.toBeNull()
    expect(container.querySelector('[aria-label="Switch to terminal mode"]')).not.toBeNull()
    // …and the terminal pane shows its own inline fallback.
    expect(container.textContent).toContain('The terminal failed to start.')

    // Recovering: the in-pane "Back to chat" switches modes without losing the shell.
    const back = Array.from(container.querySelectorAll('button'))
      .find((b) => b.textContent?.includes('Back to chat'))
    expect(back).toBeDefined()
    await act(async () => {
      back!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useInputModeStore.getState().mode).toBe('chat')
    expect(outerBoundaryTripped).toBe(false)
  })
})
