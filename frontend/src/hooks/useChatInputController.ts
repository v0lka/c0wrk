import { useCallback, useEffect, useLayoutEffect } from 'react'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useChatStore } from '@/stores/chatStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useChatInputStore, getInputState, NULL_SESSION_KEY } from '@/stores/chatInputStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useChatEditor, type ChatEditorAPI } from '@/hooks/useChatEditor'
import { usePasteHandler } from '@/hooks/usePasteHandler'
import { extractSkillRefs, extractAgentRefs, filterKnownAgentRefs } from '@/lib/parseReferences'
import { optimizePrompt } from '@/api/prompt'
import { pauseSession, resumeSession } from '@/api/chat'
import { computeChatInputDisabled, computeChatPlaceholder } from '@/lib/chatInputLock'
import { createSession } from '@/api/sessions'
import { listAgents } from '@/api/agents'
import { logger } from '@/lib/logger'

// useChatInputController owns the editor lifecycle, send/optimize state and
// auto-dismiss for the chat input. The component (ChatInput.tsx) becomes a
// thin composition layer over this hook plus the toolbar/pane subcomponents.
//
// Returned shape exposes only what the view needs; internal refs/effects are
// hidden inside the hook.
export interface ChatInputController {
  // Editor instance returned by useChatEditor (containerRef + commands).
  editor: ChatEditorAPI

  // Status flags for rendering
  hasContent: boolean
  isOptimizing: boolean
  optimizeError: string | null
  sendError: string | null
  showCancel: boolean
  isInputDisabled: boolean
  isNoProject: boolean
  taskActive: boolean
  paused: boolean
  pausing: boolean
  compacting: boolean

  // Mode
  mode: 'chat' | 'terminal'
  setMode: (m: 'chat' | 'terminal') => void

  // Resize and expand
  height: number
  setHeight: (h: number) => void
  isExpanded: boolean
  toggleExpanded: () => void

  // Active session id (used by terminal panel)
  activeSessionId: string | null

  // Actions (async actions return their promise for callers that need to
  // await settlement; UI onClick handlers may ignore it).
  handleSend: () => Promise<void>
  handleOptimize: () => Promise<void>
  handlePause: () => void
  handleResume: () => void
  cancel: () => void
}

/**
 * useChatInputController encapsulates the chat input's stateful logic:
 * editor lifecycle, send-flow, optimize-flow with transient error UX, mode
 * toggles and resize. Splitting the concern out of the JSX keeps the
 * presentation component slim and unit-testable.
 */
export function useChatInputController(): ChatInputController {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const taskActive = useChatStore((s) => (activeSessionId ? s.taskActive[activeSessionId] ?? false : false))
  const paused = useChatStore((s) => (activeSessionId ? s.paused[activeSessionId] ?? false : false))
  const pausing = useChatStore((s) => (activeSessionId ? s.pausing[activeSessionId] ?? false : false))
  const compacting = useChatStore((s) => (activeSessionId ? s.compacting[activeSessionId] ?? false : false))

  // Per-session input state (draft / optimize flag / optimize + send errors)
  // lives in chatInputStore so it survives session switches and async results
  // always target the session captured at action time. All selectors return
  // primitives — referentially stable by construction.
  const draft = useChatInputStore((s) => getInputState(s.inputs, activeSessionId).draft)
  const isOptimizing = useChatInputStore((s) => getInputState(s.inputs, activeSessionId).isOptimizing)
  const optimizeError = useChatInputStore((s) => getInputState(s.inputs, activeSessionId).optimizeError)
  const sendError = useChatInputStore((s) => getInputState(s.inputs, activeSessionId).sendError)
  const hasContent = draft.length > 0

  const mode = useInputModeStore((s) => s.mode)
  const height = useInputModeStore((s) => s.height)
  const isExpanded = useInputModeStore((s) => s.isExpanded)
  const pendingInsertion = useInputModeStore((s) => s.pendingInsertion)
  const storeSetMode = useInputModeStore((s) => s.setMode)
  const setHeight = useInputModeStore((s) => s.setHeight)
  const toggleExpanded = useInputModeStore((s) => s.toggleExpanded)
  const clearPendingInsertion = useInputModeStore((s) => s.clearPendingInsertion)

  // Wrapped setMode that implicitly creates a session when switching to
  // terminal mode so the user never sees "Start a conversation…".
  const setMode = useCallback(async (newMode: 'chat' | 'terminal') => {
    if (newMode === 'terminal') {
      const sid = useSessionStore.getState().activeSessionId
      if (!sid) {
        try {
          const newSession = await createSession()
          useSessionStore.getState().addSession(newSession)
          useSessionStore.getState().setActiveSessionId(newSession.id)
        } catch (err) {
          logger.error('Failed to implicitly create session for terminal:', err)
          // Keyed to the origin context (no session was active — that is why
          // creation was attempted), never to whatever becomes active later.
          useChatInputStore
            .getState()
            .setSendError(sid ?? NULL_SESSION_KEY, 'Failed to create session — please create one first.')
          return // stay in current mode
        }
      }
    }
    storeSetMode(newMode)
  }, [storeSetMode])

  const { send, cancel, isProcessing } = useMessageSender()

  const isNoProject = !activeProjectId
  // Input lock policy + placeholder live in the pure helpers (chatInputLock)
  // so the matrix is unit-testable without the editor harness:
  //  - taskActive alone does NOT lock the input: a message sent while a task
  //    runs is live-delivered to the running request's next LLM call.
  //  - The pausing window (Pause clicked → session_paused received) DOES
  //    lock it: a send in that window races the pause→paused transition, so
  //    the backend rejects it and the UI mirrors that by disabling input.
  //  - A cooperatively paused task sets taskActive=false, so the input is
  //    unlocked on pause — letting the user send a nudge-resume.
  //  - Manual compaction locks the ENTIRE input area (editor + toolbar +
  //    action buttons): the flow owns the session from pause-wait through
  //    auto-resume, and the backend rejects sends with ErrSessionCompacting.
  const lockInput = { taskActive, paused, pausing, isNoProject, compacting }
  const isInputDisabled = computeChatInputDisabled(lockInput)
  // Stop (cancel) is available whenever a task is running, paused, or a send
  // is in flight; Pause/Resume flank it depending on the active vs paused
  // state. Also shown while compacting: CancelTask carries no compacting
  // guard, and terminating the in-flight request is the one user action that
  // helps the flow's pause-wait land (the request goroutine exits → its done
  // channel closes → the flow proceeds; a no-op for an idle compaction).
  // The toolbar hides the Pause/Resume flank for that window — the compaction
  // flow owns the pause signal, and the compact button's cancel affordance
  // owns aborting the compaction itself.
  const showCancel = taskActive || paused || isProcessing || compacting

  const placeholderText = computeChatPlaceholder(lockInput)

  // The editor needs a stable onSend reference, but handleSend captures the
  // editor itself. We resolve the cycle by holding the latest handleSend in a
  // closure that the editor invokes via a ref. The forward reference is set
  // immediately after handleSend is defined below.
  const handleSendHolder: { current: () => void } = { current: () => {} }

  // Same forward-reference trick for onPaste: usePasteHandler needs the editor,
  // and useChatEditor accepts onPaste. We hand the editor a holder that is
  // populated right after both are created, so the (mount-once) CM paste
  // extension always invokes the latest handler via its internal ref.
  const onPasteHolder: { current: (data: DataTransfer) => Promise<void> } = {
    current: async () => {},
  }

  const editor = useChatEditor({
    disabled: isInputDisabled,
    placeholder: placeholderText,
    // Mount-only initial document: the stored draft of the (freshly) active
    // session. The draft-swap layout effect covers subsequent switches, but
    // on mount it runs before the editor view exists — initialText makes the
    // remount case (CHAT↔CODE) independent of effect ordering.
    initialText: draft,
    onSend: () => handleSendHolder.current(),
    // Every keystroke (or programmatic doc change) persists the full text as
    // the active session's draft. `editor.setText` calls echo the same value
    // back through here — setDraft bails on no-op writes, so there is no
    // state churn.
    onContentChange: (text: string) => {
      useChatInputStore.getState().setDraft(activeSessionId ?? NULL_SESSION_KEY, text)
    },
    onPaste: (data: DataTransfer) => onPasteHolder.current(data),
  })

  // Per-session draft swap: when the active session changes, the editor's
  // current text is saved under the session we are LEAVING and the newly
  // active session's draft is loaded into the editor. The cleanup closure
  // still sees the previous activeSessionId (React runs it before the next
  // effect body), so the save lands under the correct key even though the
  // store's active id has already moved on. Runs on unmount too, persisting
  // the last session's draft (e.g. CHAT↔CODE mode switches).
  //
  // useLayoutEffect (not useEffect): the swap must complete synchronously
  // inside the commit that switched the session. With a passive effect, an
  // async result (optimize/send) settling between that commit and the
  // passive-effects flush would write the leaving session's slice via
  // writeTextToSession — and this cleanup would then clobber it with the
  // editor's pre-swap text. Synchronous execution closes that window (and
  // removes the one-frame flash of the leaving session's text).
  useLayoutEffect(() => {
    // A real session becoming active retires any NULL_SESSION_KEY
    // image-error banner: the rejection was raised against the no-session
    // scratch input, and the sentinel slot must not resurface it the next
    // time no session is active.
    if (activeSessionId !== null) {
      useAttachmentsStore.getState().setImageError(NULL_SESSION_KEY, null)
    }
    const inputs = useChatInputStore.getState().inputs
    editor.setText(getInputState(inputs, activeSessionId).draft)
    return () => {
      useChatInputStore
        .getState()
        .setDraft(activeSessionId ?? NULL_SESSION_KEY, editor.getText())
    }
    // editor is a stable useMemo'd API object, so this effect only re-runs
    // when the active session actually changes.
  }, [activeSessionId, editor])

  // Non-fast-path paste routing (images / copied files). The editor takes the
  // fast path for pure text; everything else flows through here.
  const { onPaste } = usePasteHandler(editor)
  onPasteHolder.current = onPaste

  // Programmatic text insertion: each pendingInsertion is consumed once.
  useEffect(() => {
    if (pendingInsertion === null) return
    editor.insertAtCursor(pendingInsertion)
    clearPendingInsertion()
  }, [pendingInsertion, editor, clearPendingInsertion])

  // Write text to the session an action ORIGINATED from, regardless of what
  // is active when the async action settles. When the user still views the
  // origin session, the editor is updated in place (its onContentChange then
  // persists the draft); otherwise the draft is written straight into the
  // origin session's store slice, so it shows up when the user returns — and
  // can never clobber the editor of an unrelated session.
  const writeTextToSession = useCallback(
    (originSessionId: string | null, text: string) => {
      const originKey = originSessionId ?? NULL_SESSION_KEY
      const activeNow = useSessionStore.getState().activeSessionId
      if (originKey === (activeNow ?? NULL_SESSION_KEY)) {
        editor.setText(text)
      } else {
        useChatInputStore.getState().setDraft(originKey, text)
      }
    },
    [editor],
  )

  const handleSend = useCallback(async () => {
    const originSessionId = activeSessionId
    const messageText = editor.getText().trim()
    if (!messageText) return
    const skills = extractSkillRefs(messageText)
    const rawAgentRefs = extractAgentRefs(messageText)
    // Clear the editor SYNCHRONOUSLY before any async work. An #mention
    // message awaits listAgents() below; clearing first means a second Enter
    // press during that fetch reads an empty editor and returns early instead
    // of duplicating the message/task (the catch restores text on failure).
    editor.clear()
    useChatInputStore.getState().setSendError(originSessionId ?? NULL_SESSION_KEY, null)
    // Only #mentions of real Subagent Profiles are threaded/stripped, so
    // extraction stays consistent with the (catalog-filtered) #-autocomplete.
    // Without this, common coding-domain prose like "#42" (issue/PR numbers)
    // would be stripped from the message text (data loss) and injected as a
    // delegation directive for a nonexistent agent (prompt noise). The fetch
    // is gated on a non-empty candidate list so the common no-mention send
    // adds no round-trip; listAgents() is backed by a server-side cache.
    let agents = rawAgentRefs
    if (rawAgentRefs.length > 0) {
      let knownNames: string[] = []
      try {
        knownNames = (await listAgents()).map((a) => a.name)
      } catch (err) {
        logger.warn('Could not load agent catalog for ref validation; no agents threaded:', err)
      }
      agents = filterKnownAgentRefs(rawAgentRefs, knownNames)
    }
    try {
      await send(messageText, skills, agents)
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      // Both the error and the restored text are keyed to the session the
      // send ORIGINATED from (the no-session scratch when origin is null:
      // the only failure that escapes send() is the auto-create RPC, and
      // when it fails no session was created to own the message). Keying by
      // origin keeps the failure out of an unrelated session the user may
      // have switched to while the request was in flight.
      useChatInputStore.getState().setSendError(originSessionId ?? NULL_SESSION_KEY, message)
      writeTextToSession(originSessionId, messageText)
    }
  }, [editor, send, activeSessionId, writeTextToSession])
  handleSendHolder.current = handleSend

  // Cooperatively pause the running task. The executor stops at the next step
  // boundary, so the pause is NOT instantaneous — the ReAct loop keeps
  // emitting events until it lands. We therefore set ONLY the `pausing`
  // in-flight flag (never `paused` optimistically): the Pause button renders
  // as a non-clickable spinner, the activity label reads "Pausing", and the
  // input stays locked (taskActive is still true — no premature nudge-resume).
  // The real paused state (unlocked input, Resume/Stop) is entered only on the
  // backend's session_paused event; a terminal event (task_complete/cancelled/
  // error) or the reconcile on session switch clears a stale flag.
  const handlePause = useCallback(async () => {
    if (!activeSessionId) return
    const store = useChatStore.getState()
    if (store.taskActive[activeSessionId] !== true) return
    store.setPausing(activeSessionId, true)
    store.setActivityStatus(activeSessionId, 'Pausing')
    try {
      await pauseSession(activeSessionId)
    } catch (err) {
      logger.error('Failed to pause session:', err)
      // The pause request failed — the task keeps running, so the in-flight
      // flag and label must not linger. Progress events will restore the
      // normal activity status on the next step.
      store.setPausing(activeSessionId, false)
      store.setActivityStatus(activeSessionId, null)
    }
  }, [activeSessionId])

  // Resume a paused task (no nudge). The backend's session_resumed/task_resumed
  // events reconcile. The user's current model/reasoning selection is forwarded
  // so a switch made before resuming is honored (same semantics as a fresh send).
  const handleResume = useCallback(async () => {
    if (!activeSessionId) return
    const modelOverride = useInputModeStore.getState().selectedModel ?? ''
    const reasoningOverride = useInputModeStore.getState().selectedReasoning ?? ''
    useChatStore.getState().setPaused(activeSessionId, false)
    useChatStore.getState().setTaskActive(activeSessionId, true)
    try {
      await resumeSession(activeSessionId, modelOverride, reasoningOverride, '')
    } catch (err) {
      logger.error('Failed to resume session:', err)
      useChatStore.getState().setPaused(activeSessionId, true)
      useChatStore.getState().setTaskActive(activeSessionId, false)
    }
  }, [activeSessionId])

  const handleOptimize = useCallback(async () => {
    // Capture the origin session at click time: every store write below —
    // the in-flight flag, the result draft, the error, the restored text —
    // targets THIS session, so completing after a session switch can never
    // land text in another session's editor.
    const originSessionId = useSessionStore.getState().activeSessionId
    const originKey = originSessionId ?? NULL_SESSION_KEY
    const text = editor.getText().trim()
    if (!text) return
    if (useChatInputStore.getState().inputs[originKey]?.isOptimizing) return
    const store = useChatInputStore.getState()
    store.setOptimizing(originKey, true)
    store.setOptimizeError(originKey, null)
    try {
      const result = await optimizePrompt(text)
      writeTextToSession(originSessionId, result.optimized_prompt)
    } catch (error) {
      logger.error('Failed to optimize prompt:', error)
      // Restore the original prompt text so the user doesn't lose it.
      writeTextToSession(originSessionId, text)
      // Set a dismissible banner error on the origin session (W-34).
      const message = error instanceof Error && error.message
        ? `Optimization failed: ${error.message}`
        : 'Optimization failed — try again.'
      useChatInputStore.getState().setOptimizeError(originKey, message)
    } finally {
      useChatInputStore.getState().setOptimizing(originKey, false)
    }
  }, [editor, writeTextToSession])

  // Auto-dismiss the optimize error (inline + banner) after a few seconds.
  // The timeout clears the ACTIVE session's error only; a stale error on a
  // background session is cleared the same way once its session is active.
  useEffect(() => {
    if (!optimizeError) return
    const sid = activeSessionId
    const handle = window.setTimeout(() => {
      useChatInputStore.getState().setOptimizeError(sid ?? NULL_SESSION_KEY, null)
    }, 4000)
    return () => window.clearTimeout(handle)
  }, [optimizeError, activeSessionId])

  // Auto-dismiss the send error after a few seconds. The timeout clears the
  // ACTIVE session's error only; a stale error on a background session is
  // cleared the same way once its session is active.
  useEffect(() => {
    if (!sendError) return
    const sid = activeSessionId
    const handle = window.setTimeout(() => {
      useChatInputStore.getState().setSendError(sid ?? NULL_SESSION_KEY, null)
    }, 6000)
    return () => window.clearTimeout(handle)
  }, [sendError, activeSessionId])

  // Refocus the editor when switching back to chat mode.
  useEffect(() => {
    if (mode === 'chat') {
      editor.focus()
    }
  }, [mode, editor])

  return {
    editor,
    hasContent,
    isOptimizing,
    optimizeError,
    sendError,
    showCancel,
    isInputDisabled,
    isNoProject,
    taskActive,
    paused,
    pausing,
    compacting,
    mode,
    setMode,
    height,
    setHeight,
    isExpanded,
    toggleExpanded,
    activeSessionId,
    handleSend,
    handleOptimize,
    handlePause,
    handleResume,
    cancel,
  }
}
