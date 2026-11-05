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
    commands: { listCommands: vi.fn().mockResolvedValue([]) },
    mcp: {
      getMCPMentionableServers: vi.fn().mockResolvedValue([]),
      // Pure helper mirrored verbatim (production behavior under test): the
      // session-kind-aware filter — only the ACTIVE context's column decides.
      mentionableMCPNames: (servers: Array<{ name: string; chat_mode: string; code_mode: string }>, context: 'chat' | 'code') =>
        servers.filter((s) => (context === 'chat' ? s.chat_mode : s.code_mode) !== 'disabled').map((s) => s.name),
    },
    capabilities: { getCapabilityModes: vi.fn().mockResolvedValue({ skills: {}, agents: {}, mcp: {} }) },
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
vi.mock('@/api/commands', () => apiMocks.commands)
vi.mock('@/api/mcp', () => apiMocks.mcp)
vi.mock('@/api/capabilities', () => apiMocks.capabilities)
vi.mock('@/api/runtime', () => apiMocks.runtime)
vi.mock('@/api/attachments', () => apiMocks.attachments)
// The send RPC flow is useMessageSender's own tested concern; here we need a
// send() whose rejection we control, to drive the controller's catch path.
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: sendMock, cancel: vi.fn(), isProcessing: false }),
}))
// The controller mounts the real usePasteHandler → useConfigData chain, whose
// getConfig() RPC resolves in a microtask AFTER act() returns — its state
// update then trips the act() warning in tests with no post-render await
// (mount-only scenarios). Vision/model data is the paste flow's own tested
// concern; stub it (same convention as usePasteHandler/useStageAttachments tests).
vi.mock('@/hooks/useConfigData', () => ({
  useConfigData: () => ({ allModels: [], defaultModel: '', loaded: true }),
  invalidateConfigCache: vi.fn(),
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
import { usePromptSuggestionsStore } from '@/stores/promptSuggestionsStore'
import { logger } from '@/lib/logger'
import { NO_PROJECT_ID, type SessionInfo } from '@/types/models'
import type { PromptSuggestionRow } from '@/types/events'

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
  useInputModeStore.setState({ mode: 'chat', pendingInsertion: null, selectedModel: null })
  useThemeStore.setState({ themeId: 'default-dark', themeCss: '', customThemes: [] })
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

// ─────────────────────────────────────────────────────────────────────────────
// Resume on a paused session: a typed message must not be dropped
// ─────────────────────────────────────────────────────────────────────────────

describe('handleResume nudge-resume', () => {
  it('sends the typed message through the send flow instead of dropping it', async () => {
    render()
    await act(async () => {
      useChatStore.setState({ paused: { 'sess-a': true } })
    })
    await type('nudge text')

    sendMock.mockResolvedValue(undefined)
    await act(async () => {
      await controllerRef.current!.handleResume()
    })

    // The message went through the normal send flow — the backend's
    // SendMessage routes a send into a paused session to a nudge-resume
    // (persisting it with is_nudge, exactly like pressing Enter)…
    expect(sendMock).toHaveBeenCalledOnce()
    expect(sendMock).toHaveBeenCalledWith('nudge text', [], [], [], 'sess-a')
    // …the editor was cleared (the text lives on as the sent message)…
    expect(editorText()).toBe('')
    expect(slice('sess-a')?.draft).toBe('')
    // …and the plain-resume RPC was never fired.
    expect(apiMocks.chat.resumeSession).not.toHaveBeenCalled()
  })

  it('resumes without a nudge when the input is empty', async () => {
    render()
    await act(async () => {
      useChatStore.setState({ paused: { 'sess-a': true } })
    })

    await act(async () => {
      await controllerRef.current!.handleResume()
    })

    expect(apiMocks.chat.resumeSession).toHaveBeenCalledOnce()
    expect(apiMocks.chat.resumeSession).toHaveBeenCalledWith('sess-a', '', '', '')
    expect(sendMock).not.toHaveBeenCalled()
    // Optimistic paused→active flip (the session leaves the paused state —
    // "not paused" is encoded as an absent key in the store).
    expect(useChatStore.getState().paused['sess-a']).toBeUndefined()
    expect(useChatStore.getState().taskActive['sess-a']).toBe(true)
  })

  it('treats a whitespace-only draft as empty (plain resume)', async () => {
    render()
    await act(async () => {
      useChatStore.setState({ paused: { 'sess-a': true } })
    })
    await type('   ')

    await act(async () => {
      await controllerRef.current!.handleResume()
    })

    expect(sendMock).not.toHaveBeenCalled()
    expect(apiMocks.chat.resumeSession).toHaveBeenCalledOnce()
  })

  it('does not send the hidden chat draft when resuming from terminal mode', async () => {
    render()
    await act(async () => {
      useChatStore.setState({ paused: { 'sess-a': true } })
    })
    await type('leftover draft')
    await act(async () => {
      useInputModeStore.setState({ mode: 'terminal' })
    })

    await act(async () => {
      await controllerRef.current!.handleResume()
    })

    // Terminal mode hides the chat editor: Resume is a plain resume and the
    // (invisible) draft stays untouched for the next chat-mode visit.
    expect(sendMock).not.toHaveBeenCalled()
    expect(apiMocks.chat.resumeSession).toHaveBeenCalledOnce()
    expect(slice('sess-a')?.draft).toBe('leftover draft')
  })

  it('restores the paused state when the plain resume RPC fails', async () => {
    render()
    await act(async () => {
      useChatStore.setState({ paused: { 'sess-a': true }, unfinishedTaskStatus: { 'sess-a': 'paused' } })
    })
    apiMocks.chat.resumeSession.mockRejectedValueOnce(new Error('resume rpc down'))

    await act(async () => {
      await controllerRef.current!.handleResume()
    })

    expect(useChatStore.getState().paused['sess-a']).toBe(true)
    expect(useChatStore.getState().taskActive['sess-a']).toBe(false)
    // The optimistic activation pinned the overlay to ''; the rollback must put
    // the pre-resume value back so status surfaces stay correct.
    expect(useChatStore.getState().unfinishedTaskStatus['sess-a']).toBe('paused')
  })

  it('ignores a second Resume activation while the first is still in flight', async () => {
    // Double-clicking Resume is a common gesture: the second activation
    // finds the editor cleared (the first cleared it synchronously — here
    // the plain branch, an empty editor) and would fire a duplicate plain
    // resume that the backend rejects, transiently restoring paused=true /
    // taskActive=false against the first activation's optimistic state. The
    // in-flight guard (resumingRef) must swallow it.
    render()
    await act(async () => {
      useChatStore.setState({ paused: { 'sess-a': true } })
    })
    const d = deferred<void>()
    apiMocks.chat.resumeSession.mockReturnValueOnce(d.promise)

    await act(async () => {
      const first = controllerRef.current!.handleResume()
      // Fires while the first RPC is still pending: a synchronous no-op.
      const second = controllerRef.current!.handleResume()
      expect(apiMocks.chat.resumeSession).toHaveBeenCalledOnce()
      d.resolve(undefined)
      await first
      await second
    })

    expect(apiMocks.chat.resumeSession).toHaveBeenCalledOnce()
    // The optimistic state of the winning activation is never contradicted.
    expect(useChatStore.getState().paused['sess-a']).toBeUndefined()
    expect(useChatStore.getState().taskActive['sess-a']).toBe(true)

    // The guard resets once the RPC settles: a later Resume works normally.
    apiMocks.chat.resumeSession.mockResolvedValueOnce(undefined)
    await act(async () => {
      await controllerRef.current!.handleResume()
    })
    expect(apiMocks.chat.resumeSession).toHaveBeenCalledTimes(2)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// Unified /-refs on the send path (issue #110): partition plain refs against
// both catalogs, thread qualified refs verbatim without a fetch, and turn a
// colliding plain ref into a no-op + hint.
// ─────────────────────────────────────────────────────────────────────────────
describe('handleSend /-ref partitioning', () => {
  beforeEach(() => {
    apiMocks.agents.listAgents.mockResolvedValue([])
    apiMocks.skills.listSkills.mockResolvedValue([])
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([])
    sendMock.mockResolvedValue(undefined)
  })

  it('partitions plain refs by catalog: agents → activeAgents, skills → activeSkills', async () => {
    render()
    apiMocks.agents.listAgents.mockResolvedValue([{ name: 'code-reviewer', description: 'Reviews code' }])
    apiMocks.skills.listSkills.mockResolvedValue([{ name: 'commit', description: 'Commit helper' }])
    await type('/code-reviewer please /commit it')

    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    expect(sendMock).toHaveBeenCalledOnce()
    expect(sendMock).toHaveBeenCalledWith(
      '/code-reviewer please /commit it',
      ['commit'],
      ['code-reviewer'],
      [],
      'sess-a',
    )
  })

  it('passes a plain ref unknown to both catalogs through as a skill (permissive)', async () => {
    render()
    await type('use /mystery-ref now')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledWith('use /mystery-ref now', ['mystery-ref'], [], [], 'sess-a')
  })

  // Cross-boundary regression (ADR-106): the plain goal token absent from
  // every catalog is the built-in /goal command, never a permissive skill
  // passthrough. Threading ["goal"] here would have the backend's
  // stripMentionRefs eat the leading command prefix before
  // DetectAndStripGoalMode runs — the bare form must leave the client with
  // empty mention arrays so the backend detector arms goal mode.
  it('sends a bare /goal command with no mention threading (the built-in survives to the backend detector)', async () => {
    render()
    await type('/goal ship it')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledOnce()
    expect(sendMock).toHaveBeenCalledWith('/goal ship it', [], [], [], 'sess-a')
  })

  it('sends a bare /goal as the whole message with no threading', async () => {
    render()
    await type('/goal')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledWith('/goal', [], [], [], 'sess-a')
  })

  it('threads only the real mention when it exposes a goal prefix behind it', async () => {
    render()
    apiMocks.skills.listSkills.mockResolvedValue([{ name: 'explore', description: 'Explore' }])
    await type('/explore /goal ship it')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    // /explore threads and is backend-stripped; the exposed /goal prefix
    // then arms goal mode post-preprocessing (ADR-106 D1). goal must NOT
    // ride the threading or the strip would eat the exposed prefix too.
    expect(sendMock).toHaveBeenCalledWith('/explore /goal ship it', ['explore'], [], [], 'sess-a')
  })

  it('threads goal as a mention when a real skill named goal exists (shadowing preserved)', async () => {
    render()
    apiMocks.skills.listSkills.mockResolvedValue([{ name: 'goal', description: 'Goal skill' }])
    await type('/goal ship it')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    // ADR-106 D5: the catalog entry wins the plain spelling — the mention
    // threads and the backend strip resolves it to the entry, disarming the
    // command (recovery: rename, the toggle, or /goal:N).
    expect(sendMock).toHaveBeenCalledWith('/goal ship it', ['goal'], [], [], 'sess-a')
  })

  it('threads qualified refs verbatim without fetching the catalogs', async () => {
    render()
    await type('/agent: reviewer and /skill: study-paper plus /skill:glued')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledWith(
      '/agent: reviewer and /skill: study-paper plus /skill:glued',
      ['study-paper', 'glued'],
      ['reviewer'],
      [],
      'sess-a',
    )
    // Qualified refs carry an explicit kind — no catalog round-trip needed.
    expect(apiMocks.agents.listAgents).not.toHaveBeenCalled()
    expect(apiMocks.skills.listSkills).not.toHaveBeenCalled()
    expect(apiMocks.mcp.getMCPMentionableServers).not.toHaveBeenCalled()
  })

  it('blocks the send for a colliding plain ref: no-op, text preserved, hint shown', async () => {
    render()
    apiMocks.agents.listAgents.mockResolvedValue([{ name: 'review', description: 'Agent review' }])
    apiMocks.skills.listSkills.mockResolvedValue([{ name: 'review', description: 'Skill review' }])
    await type('run /review please')

    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    // Nothing was activated or requested — the message never left the client.
    expect(sendMock).not.toHaveBeenCalled()
    // The text is preserved verbatim in the editor (and the draft).
    expect(editorText()).toBe('run /review please')
    expect(slice('sess-a')?.draft).toBe('run /review please')
    // A user-visible hint points at the qualified syntax.
    const hint = slice('sess-a')?.sendError
    expect(hint).toContain('Ambiguous')
    expect(hint).toContain('/review')
    expect(hint).toContain('/agent: review')
    expect(hint).toContain('/skill: review')
  })

  it('sends the non-ambiguous refs alongside a colliding one only after it is qualified', async () => {
    // Regression shape: after the collision no-op, qualifying the ref and
    // re-sending goes through with BOTH refs partitioned.
    render()
    apiMocks.agents.listAgents.mockResolvedValue([
      { name: 'review', description: 'Agent review' },
      { name: 'reviewer', description: 'Reviews code' },
    ])
    apiMocks.skills.listSkills.mockResolvedValue([{ name: 'review', description: 'Skill review' }])
    await type('run /review please')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).not.toHaveBeenCalled()

    await type('run /agent: review please')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    // The qualified colliding ref threads as an agent; the message is sent.
    expect(sendMock).toHaveBeenCalledOnce()
    expect(sendMock).toHaveBeenCalledWith(
      'run /agent: review please',
      [],
      ['review'],
      [],
      'sess-a',
    )
  })

  it('degrades to permissive skill threading when the catalog fetch fails', async () => {
    render()
    apiMocks.agents.listAgents.mockRejectedValue(new Error('rpc down'))
    apiMocks.skills.listSkills.mockRejectedValue(new Error('rpc down'))
    apiMocks.mcp.getMCPMentionableServers.mockRejectedValue(new Error('rpc down'))
    await type('use /commit now')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    // Catalog failure must not drop the user's text: unknown plain refs pass
    // through as skills (server-side resolution), exactly like the historical
    // extractSkillRefs behavior.
    expect(sendMock).toHaveBeenCalledWith('use /commit now', ['commit'], [], [], 'sess-a')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// Custom command invocation on the send path (ADR-096): a leading /name that
// resolves to a command REPLACES the message with the expanded template
// (frontmatter agent → an extra requested subagent, model → the fallback
// model override); a collision with a mention catalog stays the ADR-076
// visible no-op; an unknown leading name keeps the permissive passthrough.
// ─────────────────────────────────────────────────────────────────────────────
describe('handleSend custom command invocation', () => {
  beforeEach(() => {
    apiMocks.agents.listAgents.mockResolvedValue([])
    apiMocks.skills.listSkills.mockResolvedValue([])
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([])
    apiMocks.commands.listCommands.mockResolvedValue([])
    sendMock.mockResolvedValue(undefined)
  })

  it('expands a leading command and sends the template with $ARGUMENTS bound', async () => {
    render()
    apiMocks.commands.listCommands.mockResolvedValue([
      { name: 'deploy', description: 'Deploy', agent: '', model: '', template: 'Deploy $ARGUMENTS with care.' },
    ])
    await type('/deploy prod')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledOnce()
    expect(sendMock).toHaveBeenCalledWith('Deploy prod with care.', [], [], [], 'sess-a')
    // The invocation was consumed: the editor is empty after the send.
    expect(editorText()).toBe('')
  })

  it('threads the frontmatter agent and model binding (agent validated against the full catalog)', async () => {
    render()
    apiMocks.agents.listAgents.mockResolvedValue([{ name: 'code-reviewer', description: 'Reviews code' }])
    apiMocks.commands.listCommands.mockResolvedValue([
      { name: 'deploy', description: '', agent: 'code-reviewer', model: 'gpt-x', template: 'Go.' },
    ])
    await type('/deploy')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledWith('Go.', [], ['code-reviewer'], [], 'sess-a', { commandModel: 'gpt-x' })
  })

  it('a capability-disabled agent still binds: validation runs against the FULL catalog, not the mentionable set', async () => {
    render()
    apiMocks.agents.listAgents.mockResolvedValue([{ name: 'quiet-agent', description: 'Disabled profile' }])
    apiMocks.capabilities.getCapabilityModes.mockResolvedValue({
      skills: {},
      agents: { 'quiet-agent': { chat_mode: 'disabled', code_mode: 'disabled' } },
      mcp: {},
    })
    apiMocks.commands.listCommands.mockResolvedValue([
      { name: 'deploy', description: '', agent: 'quiet-agent', model: '', template: 'Go.' },
    ])
    await type('/deploy')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    // A binding is explicit — it skips the kind-filtered mentionable set a
    // plain /agent mention resolves through.
    expect(sendMock).toHaveBeenCalledWith('Go.', [], ['quiet-agent'], [], 'sess-a')
    // Per-fixture-override convention: restore the shared caps mock.
    apiMocks.capabilities.getCapabilityModes.mockResolvedValue({ skills: {}, agents: {}, mcp: {} })
  })

  it('an unknown command agent is dropped with a warning; the send proceeds without the binding', async () => {
    render()
    apiMocks.commands.listCommands.mockResolvedValue([
      { name: 'deploy', description: '', agent: 'ghost', model: 'gpt-x', template: 'Go.' },
    ])
    await type('/deploy')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledWith('Go.', [], [], [], 'sess-a', { commandModel: 'gpt-x' })
    // The expected diagnostic is intercepted at its source and asserted:
    // exactly one warn naming the unknown profile.
    expect(logger.warn).toHaveBeenCalledTimes(1)
    expect(logger.warn).toHaveBeenCalledWith(expect.stringContaining('ghost'))
  })

  it('the user-selected model wins over the command binding', async () => {
    render()
    // Store updates after render() must run inside act(): the mounted Harness
    // subscribes to the input-mode store, and an unwrapped update re-renders
    // it asynchronously (React act(...) warnings).
    act(() => {
      useInputModeStore.getState().setSelectedModel('user-model')
    })
    apiMocks.commands.listCommands.mockResolvedValue([
      { name: 'deploy', description: '', agent: '', model: 'gpt-x', template: 'Go.' },
    ])
    await type('/deploy')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledWith('Go.', [], [], [], 'sess-a', { commandModel: 'gpt-x' })
    // useMessageSender (stubbed here) owns the fallback precedence; the
    // binding travels so the real implementation can apply it.
    expect(apiMocks.commands.listCommands).toHaveBeenCalled()
  })

  it('a skill+command collision is a no-op with the four-catalog hint', async () => {
    render()
    apiMocks.skills.listSkills.mockResolvedValue([{ name: 'test', description: 'Skill test' }])
    apiMocks.commands.listCommands.mockResolvedValue([
      { name: 'test', description: 'Command test', agent: '', model: '', template: 'T' },
    ])
    await type('/test run everything')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).not.toHaveBeenCalled()
    expect(editorText()).toBe('/test run everything')
    const hint = slice('sess-a')?.sendError
    expect(hint).toContain('Ambiguous')
    expect(hint).toContain('/command: test')
  })

  it('the qualified /command: form resolves a colliding name', async () => {
    render()
    apiMocks.skills.listSkills.mockResolvedValue([{ name: 'test', description: 'Skill test' }])
    apiMocks.commands.listCommands.mockResolvedValue([
      { name: 'test', description: 'Command test', agent: '', model: '', template: 'Run [$ARGUMENTS]' },
    ])
    await type('/command: test run everything')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledOnce()
    expect(sendMock).toHaveBeenCalledWith('Run [run everything]', [], [], [], 'sess-a')
  })

  it('an unknown leading name keeps the permissive skill passthrough', async () => {
    render()
    await type('/ghost args')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledWith('/ghost args', ['ghost'], [], [], 'sess-a')
  })

  it('an empty expansion is a no-op with a hint', async () => {
    render()
    apiMocks.commands.listCommands.mockResolvedValue([
      { name: 'blank', description: '', agent: '', model: '', template: '   ' },
    ])
    await type('/blank')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).not.toHaveBeenCalled()
    expect(editorText()).toBe('/blank')
    expect(slice('sess-a')?.sendError).toContain('expanded to an empty prompt')
  })

  it('a failed send restores the ORIGINAL invocation text', async () => {
    render()
    apiMocks.commands.listCommands.mockResolvedValue([
      { name: 'deploy', description: '', agent: '', model: '', template: 'Expanded.' },
    ])
    sendMock.mockRejectedValue(new Error('rpc down'))
    await type('/deploy prod')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(editorText()).toBe('/deploy prod')
    expect(slice('sess-a')?.sendError).toContain('rpc down')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// MCP server mentions on the send path: plain /server refs partition into
// activeMCPServers against the mentionable catalog (auto + manual only — a
// disabled server is inert), /mcp: qualified refs thread verbatim, and an
// mcp-catalog collision is the same no-op + hint as an agent/skill one.
// ─────────────────────────────────────────────────────────────────────────────
describe('handleSend MCP server mentions', () => {
  beforeEach(() => {
    apiMocks.agents.listAgents.mockResolvedValue([])
    apiMocks.skills.listSkills.mockResolvedValue([])
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([])
    apiMocks.capabilities.getCapabilityModes.mockResolvedValue({ skills: {}, agents: {}, mcp: {} })
    sendMock.mockResolvedValue(undefined)
  })

  it('partitions a plain /server ref into activeMCPServers (position 4 of send)', async () => {
    render()
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([
      { name: 'context7', chat_mode: 'manual', code_mode: 'auto' },
      { name: 'github', chat_mode: 'auto', code_mode: 'auto' },
    ])
    await type('/context7 look up /github repos')

    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    expect(sendMock).toHaveBeenCalledOnce()
    expect(sendMock).toHaveBeenCalledWith(
      '/context7 look up /github repos',
      [],
      [],
      ['context7', 'github'],
      'sess-a',
    )
  })

  it('never threads a disabled server as a mention', async () => {
    render()
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([
      { name: 'off-srv', chat_mode: 'disabled', code_mode: 'disabled' },
    ])
    await type('use /off-srv now')

    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    // A disabled server is filtered out of the mentionable catalog, so the
    // plain ref falls through to the permissive skill path — never threaded
    // as an MCP mention.
    expect(sendMock).toHaveBeenCalledWith('use /off-srv now', ['off-srv'], [], [], 'sess-a')
  })

  it('threads /mcp: qualified refs verbatim without fetching the catalogs', async () => {
    render()
    await type('/mcp: context7 and /mcp:glued')

    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    expect(sendMock).toHaveBeenCalledWith(
      '/mcp: context7 and /mcp:glued',
      [],
      [],
      ['context7', 'glued'],
      'sess-a',
    )
    expect(apiMocks.mcp.getMCPMentionableServers).not.toHaveBeenCalled()
    expect(apiMocks.agents.listAgents).not.toHaveBeenCalled()
    expect(apiMocks.skills.listSkills).not.toHaveBeenCalled()
  })

  it('blocks the send for a plain ref colliding with the mcp catalog', async () => {
    render()
    apiMocks.skills.listSkills.mockResolvedValue([{ name: 'deploy', description: 'Deploy skill' }])
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([{ name: 'deploy', chat_mode: 'manual', code_mode: 'auto' }])
    await type('run /deploy please')

    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    expect(sendMock).not.toHaveBeenCalled()
    expect(editorText()).toBe('run /deploy please')
    const hint = slice('sess-a')?.sendError
    expect(hint).toContain('Ambiguous')
    expect(hint).toContain('/mcp: deploy')
  })

  it('sends after the mcp collision is qualified', async () => {
    render()
    apiMocks.skills.listSkills.mockResolvedValue([{ name: 'deploy', description: 'Deploy skill' }])
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([{ name: 'deploy', chat_mode: 'manual', code_mode: 'auto' }])
    await type('run /deploy please')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).not.toHaveBeenCalled()

    await type('run /mcp: deploy please')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })
    expect(sendMock).toHaveBeenCalledOnce()
    expect(sendMock).toHaveBeenCalledWith(
      'run /mcp: deploy please',
      [],
      [],
      ['deploy'],
      'sess-a',
    )
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// Session-kind-aware mention filtering on the send path: the ORIGIN session's
// kind (SessionInfo.project_id — NO_PROJECT_ID → chat, a real project → code)
// picks the capabilities column, so a plain ref never partitions into a
// mention it could not enable for that session. Mirrors the completion's
// kind-aware offers; the backend drops disabled mentions anyway.
// ─────────────────────────────────────────────────────────────────────────────
describe('handleSend session-kind-aware mention filtering', () => {
  /** Make the origin session known to the store with an explicit kind. */
  function activateOrigin(projectId: string): void {
    const session: SessionInfo = {
      id: 'sess-a',
      project_id: projectId,
      name: 'S',
      created_at: '2026-01-01T00:00:00Z',
      last_active_at: '2026-01-01T00:00:00Z',
      archived: false,
      pinned: false,
      active: false,
      total_input_tokens: 0,
      total_output_tokens: 0,
      model: 'test-model',
      family: 'test',
      has_unfinished_task: false,
    }
    useSessionStore.setState({ sessions: [session], activeSessionId: 'sess-a' })
  }

  beforeEach(() => {
    apiMocks.agents.listAgents.mockResolvedValue([])
    apiMocks.skills.listSkills.mockResolvedValue([])
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([])
    apiMocks.capabilities.getCapabilityModes.mockResolvedValue({ skills: {}, agents: {}, mcp: {} })
    sendMock.mockResolvedValue(undefined)
  })

  it('never threads a chat-disabled server as an MCP mention from a CHAT session', async () => {
    activateOrigin(NO_PROJECT_ID)
    render()
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([
      { name: 'srv', chat_mode: 'disabled', code_mode: 'auto' },
    ])
    await type('use /srv now')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    // Filtered out of the chat-kind MCP catalog, the plain ref falls through
    // to the permissive skill path — it can never become the mention the
    // server's tools could not honor in this session anyway.
    expect(sendMock).toHaveBeenCalledWith('use /srv now', ['srv'], [], [], 'sess-a')
  })

  it('threads the same server as an MCP mention from a CODE session', async () => {
    activateOrigin('proj-1')
    render()
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([
      { name: 'srv', chat_mode: 'disabled', code_mode: 'auto' },
    ])
    await type('use /srv now')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    // The code column keeps the server mentionable — the plain ref partitions
    // into activeMCPServers (position 4 of send).
    expect(sendMock).toHaveBeenCalledWith('use /srv now', [], [], ['srv'], 'sess-a')
  })

  it('threads a manual-in-chat server as an MCP mention from a CHAT session (manual is mentionable)', async () => {
    activateOrigin(NO_PROJECT_ID)
    render()
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([
      { name: 'srv', chat_mode: 'manual', code_mode: 'auto' },
    ])
    await type('use /srv now')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    // manual is exactly the mention-triggered mode — the mention must pass.
    expect(sendMock).toHaveBeenCalledWith('use /srv now', [], [], ['srv'], 'sess-a')
  })

  it('partitions a plain ref away from an agent disabled for the active kind', async () => {
    activateOrigin(NO_PROJECT_ID)
    render()
    apiMocks.agents.listAgents.mockResolvedValue([{ name: 'reviewer', description: 'Reviews code' }])
    apiMocks.capabilities.getCapabilityModes.mockResolvedValue({
      skills: {},
      agents: { reviewer: { chat_mode: 'disabled', code_mode: 'auto' } },
      mcp: {},
    })
    await type('run /reviewer please')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    // Dropped from the chat-kind agent catalog, the plain ref degrades to the
    // permissive skill path (the backend drops the unknown/disabled skill) —
    // never into an agent delegation this session cannot use.
    expect(sendMock).toHaveBeenCalledWith('run /reviewer please', ['reviewer'], [], [], 'sess-a')
  })

  it('resolves a collision only against the kind-visible catalogs (no spurious ambiguity)', async () => {
    // 'deploy' is BOTH a skill and an MCP server — but the server is disabled
    // for CHAT. In a chat session the kind-aware catalogs leave exactly one
    // visible home, so the plain ref resolves instead of no-oping as
    // ambiguous (the old both-contexts catalog would have blocked it).
    activateOrigin(NO_PROJECT_ID)
    render()
    apiMocks.skills.listSkills.mockResolvedValue([{ name: 'deploy', description: 'Deploy skill' }])
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([{ name: 'deploy', chat_mode: 'disabled', code_mode: 'auto' }])
    await type('run /deploy please')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    expect(sendMock).toHaveBeenCalledWith('run /deploy please', ['deploy'], [], [], 'sess-a')
  })

  it('fails open when the capabilities fetch rejects: catalogs stay whole', async () => {
    activateOrigin('proj-1')
    render()
    apiMocks.mcp.getMCPMentionableServers.mockResolvedValue([
      { name: 'srv', chat_mode: 'disabled', code_mode: 'manual' },
    ])
    apiMocks.capabilities.getCapabilityModes.mockRejectedValue(new Error('caps down'))
    await type('use /srv now')
    await act(async () => {
      await controllerRef.current!.handleSend()
    })

    // No filtering information → nothing is hidden on the client; the
    // backend's own disabled-mention drop remains the safety net.
    expect(sendMock).toHaveBeenCalledWith('use /srv now', [], [], ['srv'], 'sess-a')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// Prompt suggestions (ADR-088): the placeholder hint + arrow navigation.
// The store is armed directly (the refetch triggers live in the sync hook's
// own suite); here we prove the controller WIRING: the idle placeholder
// becomes the hint surface (the previewed row, a blank line, the italic
// usage hints), ↑↓ drive the store's clamped selection,
// and → inserts the previewed row as real content without a send.
// ─────────────────────────────────────────────────────────────────────────────

const SUGGESTION_ROWS: PromptSuggestionRow[] = [
  { id: 's1', session_id: 'sess-a', task_id: 't1', kind: 'goal_advance', text: 'Follow up on the failing test', status: 'active', created_at: '2026-01-01T00:00:00Z' },
  { id: 's2', session_id: 'sess-a', task_id: 't1', kind: 'goal_advance', text: 'Write release notes', status: 'active', created_at: '2026-01-01T00:00:01Z' },
]

const EMPTY_SUGGESTIONS_STATE = {
  sessionId: null,
  projectId: '',
  source: 'none' as const,
  suggestions: [] as PromptSuggestionRow[],
  selectedIndex: -1,
  loading: false,
}

describe('prompt-suggestions: placeholder hint + arrow navigation', () => {
  function armSuggestions(sessionId: string | null, rows: PromptSuggestionRow[] = SUGGESTION_ROWS) {
    usePromptSuggestionsStore.setState({
      ...EMPTY_SUGGESTIONS_STATE,
      sessionId,
      projectId: 'proj-1',
      source: 'continuation',
      suggestions: rows,
      // Mirror the real load() rest: a fresh non-empty set rests ON the top row.
      selectedIndex: rows.length > 0 ? 0 : -1,
    })
  }

  function suggestionState() {
    return usePromptSuggestionsStore.getState()
  }

  function placeholderWrap(): HTMLElement | null {
    return container.querySelector<HTMLElement>('.cm-placeholder')
  }

  function placeholderText(): string {
    return placeholderWrap()?.textContent ?? ''
  }

  function placeholderPreview(): string {
    return placeholderWrap()?.querySelector('.cm-suggestion-preview')?.textContent ?? ''
  }

  function placeholderHints(): string {
    return placeholderWrap()?.querySelector('.cm-suggestion-hints')?.textContent ?? ''
  }

  function placeholderHintsItalic(): string {
    return placeholderWrap()?.querySelector<HTMLElement>('.cm-suggestion-hints')?.style.fontStyle ?? ''
  }

  /** The blank-line separator as rendered: the two explicit <br> elements. */
  function placeholderBlankLine(): number {
    return placeholderWrap()?.querySelectorAll('.cm-suggestion-placeholder > br').length ?? 0
  }

  async function pressKey(key: string): Promise<boolean> {
    const content = container.querySelector('.cm-content')
    expect(content).not.toBeNull()
    const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })
    // Async act: CodeMirror may defer its key handlers to a microtask
    // (InputState.handleEvent → Promise.resolve().then(runHandlers)); the
    // store write and the resulting React re-render must land inside act.
    // defaultPrevented is read after act settles for the same reason.
    await act(async () => {
      content!.dispatchEvent(event)
    })
    return event.defaultPrevented
  }

  beforeEach(() => {
    usePromptSuggestionsStore.setState(EMPTY_SUGGESTIONS_STATE)
  })

  afterEach(() => {
    // Unmount BEFORE resetting the store: the reset is a real snapshot change
    // and would re-render a still-mounted Harness outside act.
    act(() => {
      root.unmount()
    })
    container.remove()
    document.body.innerHTML = ''
    usePromptSuggestionsStore.setState(EMPTY_SUGGESTIONS_STATE)
  })

  it('idle + non-empty set: the placeholder previews the top pick over a BLANK line above the italic usage hints', () => {
    armSuggestions('sess-a')
    render()
    expect(placeholderPreview()).toBe('Follow up on the failing test')
    expect(placeholderBlankLine()).toBe(2)
    expect(placeholderHints()).toBe(
      '↑↓ browse · → insert\nOr just type a message... (Enter to send, Shift+Enter for new line)',
    )
    expect(placeholderHintsItalic()).toBe('italic')
  })

  it('the hint arms and disarms live with the store (placeholder compartment reconfigure)', () => {
    render()
    expect(placeholderText()).toBe('Type a message... (Enter to send, Shift+Enter for new line)')
    act(() => {
      armSuggestions('sess-a')
    })
    expect(placeholderPreview()).toBe('Follow up on the failing test')
    expect(placeholderHints()).toContain('↑↓ browse · → insert')
    act(() => {
      armSuggestions(null, [])
    })
    expect(placeholderText()).toBe('Type a message... (Enter to send, Shift+Enter for new line)')
  })

  it('a non-idle session keeps its own placeholder (the hint never buries a state)', () => {
    armSuggestions('sess-a')
    useChatStore.setState({ taskActive: { 'sess-a': true } })
    render()
    expect(placeholderText()).toBe('Working — your message joins the next request to the model')
  })

  it('a foreign-session snapshot renders nothing (the selectors key on the active session)', () => {
    armSuggestions('sess-other')
    render()
    expect(placeholderText()).toBe('Type a message... (Enter to send, Shift+Enter for new line)')
  })

  it('ArrowDown/ArrowUp browse the selection and the placeholder follows the browsed row', async () => {
    armSuggestions('sess-a')
    render()

    // Resting ON the top row: the placeholder already previews it, so the
    // first ↓ must land on a VISIBLY different row (regression: the old
    // unselected -1 rest rendered identically to row 0 and the first press
    // looked dead — switching only started on the second press).
    expect(placeholderPreview()).toBe('Follow up on the failing test')
    expect(await pressKey('ArrowDown')).toBe(true)
    expect(suggestionState().selectedIndex).toBe(1)
    expect(placeholderPreview()).toBe('Write release notes')

    expect(await pressKey('ArrowUp')).toBe(true)
    expect(suggestionState().selectedIndex).toBe(0)
    expect(placeholderPreview()).toBe('Follow up on the failing test')

    // Clamped at the top: consumed, no further change (and no dead-looking
    // hop to a hidden state — the top row IS the resting state).
    expect(await pressKey('ArrowUp')).toBe(true)
    expect(suggestionState().selectedIndex).toBe(0)
  })

  it('navigation clamps at both ends (no wrap-around)', async () => {
    armSuggestions('sess-a')
    render()
    expect(await pressKey('ArrowUp')).toBe(true)
    expect(suggestionState().selectedIndex).toBe(0)
    expect(await pressKey('ArrowDown')).toBe(true)
    expect(await pressKey('ArrowDown')).toBe(true)
    expect(await pressKey('ArrowDown')).toBe(true)
    expect(suggestionState().selectedIndex).toBe(1)
  })

  it('ArrowRight inserts the previewed row as real content — draft persisted, nothing sent, selection rests back on the top row', async () => {
    armSuggestions('sess-a')
    render()
    expect(await pressKey('ArrowDown')).toBe(true)

    expect(await pressKey('ArrowRight')).toBe(true)
    expect(editorText()).toBe('Write release notes')
    expect(slice('sess-a')?.draft).toBe('Write release notes')
    expect(suggestionState().selectedIndex).toBe(0)
    expect(sendMock).not.toHaveBeenCalled()
  })

  it('ArrowRight at rest inserts exactly the top row the placeholder displays', async () => {
    armSuggestions('sess-a')
    render()
    expect(await pressKey('ArrowRight')).toBe(true)
    expect(editorText()).toBe('Follow up on the failing test')
    expect(suggestionState().selectedIndex).toBe(0)
    expect(sendMock).not.toHaveBeenCalled()
  })

  it('with content in the editor, the arrows are never intercepted', async () => {
    armSuggestions('sess-a')
    render()
    await type('draft text')

    await pressKey('ArrowDown')
    await pressKey('ArrowRight')
    expect(editorText()).toBe('draft text')
    expect(suggestionState().selectedIndex).toBe(0)
  })

  it('a foreign-session snapshot: the arrows fall through (the controller handler declines)', async () => {
    armSuggestions('sess-other')
    render()
    await pressKey('ArrowDown')
    await pressKey('ArrowRight')
    // The sync layer has already swapped the foreign snapshot for the harness
    // session's own (empty) set — the sentinel stands; the arrows declined,
    // nothing was browsed or inserted.
    expect(suggestionState().selectedIndex).toBe(-1)
    expect(editorText()).toBe('')
    expect(sendMock).not.toHaveBeenCalled()
  })
})
