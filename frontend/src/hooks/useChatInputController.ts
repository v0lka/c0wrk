import { useState, useCallback, useEffect } from 'react'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useChatStore } from '@/stores/chatStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useMessageSender } from '@/hooks/useMessageSender'
import { useChatEditor, type ChatEditorAPI } from '@/hooks/useChatEditor'
import { extractSkillRefs } from '@/lib/parseReferences'
import { optimizePrompt } from '@/api/prompt'
import { createSession } from '@/api/sessions'
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
  const isInputDisabled = taskActive || isNoProject
  const showCancel = taskActive || isProcessing

  let placeholderText = 'Type a message... (Enter to send, Shift+Enter for new line)'
  if (isNoProject) {
    placeholderText = 'Select or create a project to start'
  } else if (taskActive) {
    placeholderText = 'Session is processing...'
  }

  // The editor needs a stable onSend reference, but handleSend captures the
  // editor itself. We resolve the cycle by holding the latest handleSend in a
  // closure that the editor invokes via a ref. The forward reference is set
  // immediately after handleSend is defined below.
  const handleSendHolder: { current: () => void } = { current: () => {} }

  const editor = useChatEditor({
    disabled: isInputDisabled,
    placeholder: placeholderText,
    onSend: () => handleSendHolder.current(),
    onContentChange: setHasContent,
  })

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
    editor.clear()
    setSendError(null)
    try {
      await send(messageText, skills)
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err)
      setSendError(message)
      editor.setText(messageText)
    }
  }, [editor, send])
  handleSendHolder.current = handleSend

  const handleOptimize = useCallback(async () => {
    const text = editor.getText().trim()
    if (!text || isOptimizing) return
    setIsOptimizing(true)
    setOptimizeError(null)
    try {
      const result = await optimizePrompt(text)
      editor.setText(result.optimized_prompt)
    } catch (error) {
      logger.error('Failed to optimize prompt:', error)
      // Surface a transient inline error so users get feedback when nothing
      // appears to happen after clicking the sparkles button (W-34).
      const message = error instanceof Error && error.message
        ? `Optimization failed: ${error.message}`
        : 'Optimization failed — try again.'
      setOptimizeError(message)
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
    mode,
    setMode,
    height,
    setHeight,
    isExpanded,
    toggleExpanded,
    activeSessionId,
    handleSend,
    handleOptimize,
    cancel,
  }
}
