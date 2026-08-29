// Controller-level regression tests for per-session chat-input state.
//
// chatInputStore gives every session its own draft / optimize-in-flight flag
// / optimize error. These tests mount the REAL controller + CodeMirror editor
// (jsdom) and prove the invariants:
//  - a draft survives an A→B→A session switch (and project/CHAT↔CODE
//    switches, which are session-id changes under the hood);
//  - an Optimize round-trip completing AFTER a session switch lands its
//    result (or restores its original text on failure) in the session
//    captured at click time — never in the other session's editor;
//  - a send failure restores the text to the session the send originated from;
//  - a send failure with no origin session keys its restored text and error
//    to the no-session scratch (NULL_SESSION_KEY) — never to a session the
//    user switched to mid-flight;
//  - the NULL_SESSION_KEY image-error banner is retired once a real session
//    becomes active (it cannot resurface on a later no-session state);
//  - the toolbar's isOptimizing reflects the ACTIVE session's flag only.
//
// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { createElement } from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// vi.mock factories are hoisted; the mock objects must come from vi.hoisted().
const { apiMocks, sendMock } = vi.hoisted(() => ({
  apiMocks: {
    prompt: { optimizePrompt: vi.fn() },
    chat: { sendMessage: vi.fn(), cancelTask: vi.fn(), pauseSession: vi.fn(), resumeSession: vi.fn() },
    sessions: { createSession: vi.fn(), listSessions: vi.fn() },
    agents: { listAgents: vi.fn().mockResolvedValue([]) },
    workspace: { listDirectory: vi.fn().mockResolvedValue([]) },
    skills: { listSkills: vi.fn().mockResolvedValue([]) },
    runtime: {
      subscribe: vi.fn(() => () => {}),
      emit: vi.fn(),
      onGlobalEvent: vi.fn(() => () => {}),
      onSessionEvent: vi.fn(() => () => {}),
      reportDroppedEvent: vi.fn(),
    },
    attachments: { pasteFromClipboard: vi.fn(), isImagePath: vi.fn(), attachFiles: vi.fn() },
  },
  sendMock: vi.fn(),
}))

vi.mock('@/api/prompt', () => apiMocks.prompt)
vi.mock('@/api/chat', () => apiMocks.chat)
vi.mock('@/api/sessions', () => apiMocks.sessions)
vi.mock('@/api/agents', () => apiMocks.agents)
vi.mock('@/api/workspace', () => apiMocks.workspace)
vi.mock('@/api/skills', () => apiMocks.skills)
vi.mock('@/api/runtime', () => apiMocks.runtime)
vi.mock('@/api/attachments', () => apiMocks.attachments)
// The send RPC flow is useMessageSender's own tested concern; here we need a
// send() whose rejection we control, to drive the controller's catch path.
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: sendMock, cancel: vi.fn(), isProcessing: false }),
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { useChatInputController, type ChatInputController } from '@/hooks/useChatInputController'
import { useChatInputStore, NULL_SESSION_KEY } from '@/stores/chatInputStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useChatStore } from '@/stores/chatStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useThemeStore } from '@/stores/themeStore'

/** Create a promise whose settlement is controlled by the test. */
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

let container: HTMLDivElement
let root: Root
let controllerRef: { current: ChatInputController | null } = { current: null }

function Harness() {
  const controller = useChatInputController()
  controllerRef.current = controller
  return createElement('div', { ref: controller.editor.containerRef })
}

function render() {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(createElement(Harness))
  })
}

/** Switch the active session (mirrors a session/project/mode switch). */
async function switchSession(id: string | null) {
  await act(async () => {
    useSessionStore.setState({ activeSessionId: id })
  })
}

/** Type text into the editor as if the user did (persists the draft). */
async function type(text: string) {
  await act(async () => {
    controllerRef.current!.editor.setText(text)
  })
}

function editorText(): string {
  return controllerRef.current!.editor.getText()
}

function slice(sessionId: string) {
  return useChatInputStore.getState().inputs[sessionId]
}

beforeEach(() => {
  vi.clearAllMocks()
  apiMocks.agents.listAgents.mockResolvedValue([])
  useChatInputStore.setState({ inputs: {} })
  useAttachmentsStore.setState({ attachmentsBySession: {}, uploadsBySession: {}, namesById: {}, imageErrorBySession: {} })
  useSessionStore.setState({ sessions: [], activeSessionId: 'sess-a' })
  useProjectStore.setState({ activeProjectId: 'proj-1' })
  useChatStore.setState({ messages: {}, messageOrder: {}, taskActive: {}, paused: {}, pausing: {}, compacting: {} })
  useInputModeStore.setState({ mode: 'chat', pendingInsertion: null })
  useThemeStore.setState({ theme: 'dark' })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  controllerRef = { current: null }
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

// ─────────────────────────────────────────────────────────────────────────────
// Draft survives session switches
// ─────────────────────────────────────────────────────────────────────────────

describe('per-session drafts', () => {
  it('persists keystrokes to the active session and survives A→B→A', async () => {
    render()
    await type('draft A')
    expect(slice('sess-a')?.draft).toBe('draft A')
    expect(controllerRef.current!.hasContent).toBe(true)

    await switchSession('sess-b')
    // B starts empty; nothing leaked from A.
    expect(editorText()).toBe('')
    expect(controllerRef.current!.hasContent).toBe(false)
    expect(slice('sess-b')).toBeUndefined()

    await type('draft B')
    await switchSession('sess-a')
    expect(editorText()).toBe('draft A')
    expect(controllerRef.current!.hasContent).toBe(true)

    await switchSession('sess-b')
    expect(editorText()).toBe('draft B')
  })

  it('preserves the draft across a transient null-session state (project switch)', async () => {
    render()
    await type('draft A')
    // Project switches pass through activeSessionId = null before the new
    // project's sessions load; the draft must survive that transient state.
    await switchSession(null)
    expect(slice('sess-a')?.draft).toBe('draft A')
    await switchSession('sess-b')
    await switchSession('sess-a')
    expect(editorText()).toBe('draft A')
  })

  it('keeps text typed with no active session under the sentinel slot', async () => {
    await switchSession(null)
    render()
    await type('scratch')
    expect(controllerRef.current!.hasContent).toBe(true)
    await switchSession('sess-b')
    expect(editorText()).toBe('')
    await switchSession(null)
    expect(editorText()).toBe('scratch')
  })

  it('loads the existing draft into the editor on remount (CHAT↔CODE switch)', async () => {
    render()
    await type('draft A')
    // A CHAT↔CODE mode switch unmounts the chat input; switching back
    // remounts it with the same session active. The stored draft must
    // appear in the editor without any keystroke.
    act(() => {
      root.unmount()
    })
    container.remove()
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    render()
    expect(editorText()).toBe('draft A')
    expect(controllerRef.current!.hasContent).toBe(true)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// Optimize: mid-flight session-switch safety
// ─────────────────────────────────────────────────────────────────────────────

describe('handleOptimize origin-session targeting', () => {
  it('result lands in the click-time session draft, not the other session editor', async () => {
    render()
    await type('orig A')

    const { promise: optimizePromise, resolve } = deferred<{
      optimized_prompt: string
      keywords: string[]
      used_context: boolean
    }>()
    apiMocks.prompt.optimizePrompt.mockReturnValueOnce(optimizePromise)

    let inflight!: Promise<void>
    act(() => {
      inflight = controllerRef.current!.handleOptimize()
    })
    expect(apiMocks.prompt.optimizePrompt).toHaveBeenCalledWith('orig A')
    expect(controllerRef.current!.isOptimizing).toBe(true)
    expect(slice('sess-a')?.isOptimizing).toBe(true)

    // Switch away while the request is in flight: the spinner must reflect
    // the ACTIVE session (B: no optimization running) — not A's in-flight one.
    await switchSession('sess-b')
    expect(controllerRef.current!.isOptimizing).toBe(false)

    resolve({ optimized_prompt: 'OPTIMIZED', keywords: [], used_context: false })
    await act(async () => {
      await inflight
    })

    // B's editor is untouched; nothing leaked into B's slice.
    expect(editorText()).toBe('')
    expect(slice('sess-b')).toBeUndefined()
    // The result landed in A's draft; the flag cleared.
    expect(slice('sess-a')?.draft).toBe('OPTIMIZED')
    expect(slice('sess-a')?.isOptimizing).toBe(false)

    // Returning to A shows the optimized draft.
    await switchSession('sess-a')
    expect(editorText()).toBe('OPTIMIZED')
  })

  it('applies the result live when the origin session is still active after A→B→A', async () => {
    render()
    await type('orig A')
    const { promise: optimizePromise, resolve } = deferred<{
      optimized_prompt: string
      keywords: string[]
      used_context: boolean
    }>()
    apiMocks.prompt.optimizePrompt.mockReturnValueOnce(optimizePromise)

    let inflight!: Promise<void>
    act(() => {
      inflight = controllerRef.current!.handleOptimize()
    })
    await switchSession('sess-b')
    await switchSession('sess-a')
    // Back on the origin session while still in flight: spinner shows A's flag.
    expect(controllerRef.current!.isOptimizing).toBe(true)

    resolve({ optimized_prompt: 'OPTIMIZED', keywords: [], used_context: false })
    await act(async () => {
      await inflight
    })
    expect(editorText()).toBe('OPTIMIZED')
    expect(slice('sess-b')).toBeUndefined()
  })

  it('failure restores the original into the origin session and errors only there', async () => {
    render()
    await type('orig A')
    const { promise: optimizePromise, reject } = deferred<never>()
    apiMocks.prompt.optimizePrompt.mockReturnValueOnce(optimizePromise)

    let inflight!: Promise<void>
    act(() => {
      inflight = controllerRef.current!.handleOptimize()
    })
    await switchSession('sess-b')

    reject(new Error('boom'))
    await act(async () => {
      await inflight.catch(() => undefined)
    })

    // Original restored into A's draft; error recorded on A only.
    expect(slice('sess-a')?.draft).toBe('orig A')
    expect(slice('sess-a')?.optimizeError).toBe('Optimization failed: boom')
    expect(slice('sess-b')).toBeUndefined()
    // The active session (B) shows no error and no text in its editor.
    expect(controllerRef.current!.optimizeError).toBeNull()
    expect(editorText()).toBe('')

    await switchSession('sess-a')
    expect(editorText()).toBe('orig A')
    expect(controllerRef.current!.optimizeError).toBe('Optimization failed: boom')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// Send failure: restore targets the originating session
// ─────────────────────────────────────────────────────────────────────────────

describe('handleSend failure restore', () => {
  it('restores the text into the editor when the origin session is still active', async () => {
    render()
    await type('msg A')
    sendMock.mockRejectedValueOnce(new Error('rpc down'))

    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(editorText()).toBe('msg A')
    expect(slice('sess-a')?.draft).toBe('msg A')
    expect(slice('sess-a')?.sendError).toBe('rpc down')
    expect(controllerRef.current!.sendError).toBe('rpc down')
  })

  it('restores the text into the ORIGIN session draft after a mid-flight switch', async () => {
    render()
    await type('msg A')
    const { promise: sendPromise, reject } = deferred<never>()
    sendMock.mockReturnValueOnce(sendPromise)

    let inflight!: Promise<void>
    act(() => {
      inflight = controllerRef.current!.handleSend()
    })
    // Editor is cleared synchronously on send; user switches to B.
    expect(editorText()).toBe('')
    await switchSession('sess-b')

    reject(new Error('rpc down'))
    await act(async () => {
      await inflight.catch(() => undefined)
    })

    // B's editor stays empty; the failed message and error are back in A.
    expect(editorText()).toBe('')
    expect(slice('sess-a')?.draft).toBe('msg A')
    expect(slice('sess-a')?.sendError).toBe('rpc down')
    expect(slice('sess-b')).toBeUndefined()
    expect(controllerRef.current!.sendError).toBeNull()
    await switchSession('sess-a')
    expect(editorText()).toBe('msg A')
  })

  it('keys the restore and the error to the scratch slot when no session existed', async () => {
    await switchSession(null)
    render()
    await type('scratch msg')
    const { promise: sendPromise, reject } = deferred<never>()
    sendMock.mockReturnValueOnce(sendPromise)

    let inflight!: Promise<void>
    act(() => {
      inflight = controllerRef.current!.handleSend()
    })
    expect(editorText()).toBe('')
    // The user switches to an existing session while the (failing) send is
    // in flight — the failed message must NOT land in that session.
    await switchSession('sess-b')

    reject(new Error('rpc down'))
    await act(async () => {
      await inflight.catch(() => undefined)
    })

    expect(editorText()).toBe('')
    expect(slice('sess-b')).toBeUndefined()
    // The failed message and its error are keyed to the no-session scratch.
    const scratch = useChatInputStore.getState().inputs[NULL_SESSION_KEY]
    expect(scratch?.draft).toBe('scratch msg')
    expect(scratch?.sendError).toBe('rpc down')
    expect(controllerRef.current!.sendError).toBeNull()

    await switchSession(null)
    expect(editorText()).toBe('scratch msg')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// NULL_SESSION_KEY image-error retirement on session activation
// ─────────────────────────────────────────────────────────────────────────────

describe('sentinel image-error retirement', () => {
  it('clears the NULL_SESSION_KEY image error when a session becomes active', async () => {
    useAttachmentsStore.getState().setImageError(NULL_SESSION_KEY, 'no vision')
    await switchSession(null)
    render()
    // No session is active — the banner is still live.
    expect(useAttachmentsStore.getState().imageErrorBySession[NULL_SESSION_KEY]).toBe('no vision')

    await switchSession('sess-b')
    expect(NULL_SESSION_KEY in useAttachmentsStore.getState().imageErrorBySession).toBe(false)

    // Returning to the no-session state does not resurrect the stale banner.
    await switchSession(null)
    expect(NULL_SESSION_KEY in useAttachmentsStore.getState().imageErrorBySession).toBe(false)
  })

  it('retires the sentinel on mount when a session is already active', async () => {
    useAttachmentsStore.getState().setImageError(NULL_SESSION_KEY, 'no vision')
    render()
    expect(NULL_SESSION_KEY in useAttachmentsStore.getState().imageErrorBySession).toBe(false)
  })

  it('leaves real-session image errors untouched', async () => {
    render()
    useAttachmentsStore.getState().setImageError('sess-b', 'no vision')
    await switchSession('sess-a')
    await switchSession('sess-b')
    expect(useAttachmentsStore.getState().imageErrorBySession['sess-b']).toBe('no vision')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// Send lock while attachment uploads are in flight
// ─────────────────────────────────────────────────────────────────────────────

describe('send locked during attachment uploads', () => {
  it('exposes attachmentsUploading and blocks handleSend while the active session has in-flight uploads', async () => {
    render()
    await type('msg with attachment')

    // Seed one in-flight upload placeholder for the ACTIVE session.
    await act(async () => {
      useAttachmentsStore.setState({
        uploadsBySession: {
          'sess-a': [{ id: 'u1', fileName: 'doc.pdf', path: '/p/doc.pdf', isImage: false }],
        },
      })
    })
    expect(controllerRef.current!.attachmentsUploading).toBe(true)

    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    // The send never left the client and the draft stays in the editor…
    expect(sendMock).not.toHaveBeenCalled()
    expect(editorText()).toBe('msg with attachment')
    // …and the Enter path surfaces WHY it is blocked instead of failing silently.
    expect(slice('sess-a')?.sendError).toBe(
      'Processing attachments — send unlocks when they finish',
    )

    // Uploads settle → the flag flips and the send goes through.
    sendMock.mockResolvedValue(undefined)
    await act(async () => {
      useAttachmentsStore.setState({ uploadsBySession: {} })
    })
    expect(controllerRef.current!.attachmentsUploading).toBe(false)

    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledOnce()
  })

  it('keeps the flag false for uploads of OTHER sessions', async () => {
    render()
    await act(async () => {
      useAttachmentsStore.setState({
        uploadsBySession: {
          'sess-b': [{ id: 'u1', fileName: 'doc.pdf', path: '/p/doc.pdf', isImage: false }],
        },
      })
    })
    expect(controllerRef.current!.attachmentsUploading).toBe(false)
  })
})
