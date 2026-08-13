import { useState, useCallback, useEffect } from 'react'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useChatStore } from '@/stores/chatStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useAttachmentsStore } from '@/stores/attachmentsStore'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useChatEditor, type ChatEditorAPI } from '@/hooks/useChatEditor'
import { usePasteHandler } from '@/hooks/usePasteHandler'
import { extractSkillRefs, extractAgentRefs, filterKnownAgentRefs } from '@/lib/parseReferences'
import { optimizePrompt } from '@/api/prompt'
import { pauseSession, resumeSession } from '@/api/chat'
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

  // Actions
  handleSend: () => void
  handleOptimize: () => void
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
  const [isOptimizing, setIsOptimizing] = useState(false)
  const [optimizeError, setOptimizeError] = useState<string | null>(null)
  const [sendError, setSendError] = useState<string | null>(null)
  const [hasContent, setHasContent] = useState(false)

  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const taskActive = useChatStore((s) => (activeSessionId ? s.taskActive[activeSessionId] ?? false : false))
  const paused = useChatStore((s) => (activeSessionId ? s.paused[activeSessionId] ?? false : false))

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
          setSendError('Failed to create session — please create one first.')
          return // stay in current mode
        }
      }
    }
    storeSetMode(newMode)
  }, [storeSetMode])

  const { send, cancel, isProcessing } = useMessageSender()

  const isNoProject = !activeProjectId
  // Input is locked while a task is actively running (taskActive) or when there
  // is no project. A cooperatively paused task sets taskActive=false, so the
  // input is unlocked on pause — letting the user send a nudge-resume.
  const isInputDisabled = taskActive || isNoProject
  // Stop (cancel) is available whenever a task is running, paused, or a send is
  // in flight; Pause/Resume flank it depending on the active vs paused state.
  const showCancel = taskActive || paused || isProcessing

  let placeholderText = 'Type a message... (Enter to send, Shift+Enter for new line)'
  if (isNoProject) {
    placeholderText = 'Select or create a project to start'
  } else if (paused) {
    placeholderText = 'Paused — send a message to nudge-resume, or press Resume'
  } else if (taskActive) {
    placeholderText = 'Session is processing...'
  }

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
    onSend: () => handleSendHolder.current(),
    onContentChange: setHasContent,
    onPaste: (data: DataTransfer) => onPasteHolder.current(data),
  })

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

  const handleSend = useCallback(async () => {
    const messageText = editor.getText().trim()
    if (!messageText) return
    const skills = extractSkillRefs(messageText)
    const rawAgentRefs = extractAgentRefs(messageText)
    // Clear the editor SYNCHRONOUSLY before any async work. An #mention
    // message awaits listAgents() below; clearing first means a second Enter
    // press during that fetch reads an empty editor and returns early instead
    // of duplicating the message/task (the catch restores text on failure).
    editor.clear()
    setSendError(null)
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
      setSendError(message)
      editor.setText(messageText)
    }
  }, [editor, send])
  handleSendHolder.current = handleSend

  // Cooperatively pause the running task. The executor stops at the next step
  // boundary; the backend emits session_paused to reconcile. Optimistically set
  // ONLY the paused flag for immediate feedback — taskActive is finalized by
  // the session_paused event (the task is still running until the step boundary
  // fires), so setting it here would create a flicker window where a streaming
  // event (assistant_chunk) could re-assert it before the pause lands.
  const handlePause = useCallback(async () => {
    if (!activeSessionId) return
    useChatStore.getState().setPaused(activeSessionId, true)
    try {
      await pauseSession(activeSessionId)
    } catch (err) {
      logger.error('Failed to pause session:', err)
      useChatStore.getState().setPaused(activeSessionId, false)
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
    const text = editor.getText().trim()
    if (!text || isOptimizing) return
    setIsOptimizing(true)
    setOptimizeError(null)
    useAttachmentsStore.getState().setPromptOptimizeError(null)
    try {
      const result = await optimizePrompt(text)
      editor.setText(result.optimized_prompt)
    } catch (error) {
      logger.error('Failed to optimize prompt:', error)
      // Restore the original prompt text so the user doesn't lose it.
      editor.setText(text)
      // Set a persistent banner (dismissable) alongside the transient
      // inline error for immediate feedback (W-34).
      const message = error instanceof Error && error.message
        ? `Optimization failed: ${error.message}`
        : 'Optimization failed — try again.'
      setOptimizeError(message)
      useAttachmentsStore.getState().setPromptOptimizeError(message)
    } finally {
      setIsOptimizing(false)
    }
  }, [editor, isOptimizing])

  // Auto-dismiss the optimize error after a few seconds.
  useEffect(() => {
    if (!optimizeError) return
    const handle = window.setTimeout(() => setOptimizeError(null), 4000)
    return () => window.clearTimeout(handle)
  }, [optimizeError])

  // Auto-dismiss the persistent optimize banner after 8 seconds.
  const bannerError = useAttachmentsStore((s) => s.promptOptimizeError)
  useEffect(() => {
    if (!bannerError) return
    const handle = window.setTimeout(() => {
      useAttachmentsStore.getState().setPromptOptimizeError(null)
    }, 8000)
    return () => window.clearTimeout(handle)
  }, [bannerError])

  // Auto-dismiss the send error after a few seconds.
  useEffect(() => {
    if (!sendError) return
    const handle = window.setTimeout(() => setSendError(null), 6000)
    return () => window.clearTimeout(handle)
  }, [sendError])

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
