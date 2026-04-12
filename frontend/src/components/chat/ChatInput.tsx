import { useState, useRef, useCallback, useLayoutEffect, type KeyboardEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useChatStore } from '@/stores/chatStore'
import { useWails } from '@/hooks/useWails'
import { Play, Square } from 'lucide-react'
import { logger } from '@/lib/logger'

/** Maximum visible lines before the textarea scrolls, balancing input visibility with chat area space. */
const MAX_LINES = 6

export function ChatInput() {
  const [text, setText] = useState('')
  const [isProcessing, setIsProcessing] = useState(false)
  const [planFirst, setPlanFirst] = useState(false)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const touchSession = useSessionStore(s => s.touchSession)
  const addSession = useSessionStore(s => s.addSession)
  const setActiveSession = useSessionStore(s => s.setActiveSession)
  const activeProjectId = useProjectStore(s => s.activeProjectId)
  const isThinking = useChatStore(s => s.isThinking)
  const isTaskActive = useChatStore(s => s.isTaskActive)
  const setTaskActive = useChatStore(s => s.setTaskActive)
  const addMessage = useChatStore(s => s.addMessage)
  const { api } = useWails()

  // Blocking conditions
  const isNoProject = !activeProjectId
  const isInputDisabled = isTaskActive || isNoProject

  const showCancel = isThinking || isProcessing

  // Auto-resize textarea
  useLayoutEffect(() => {
    const textarea = textareaRef.current
    if (!textarea) return

    textarea.style.height = 'auto'
    const lineHeight = parseInt(getComputedStyle(textarea).lineHeight) || 24
    const maxHeight = lineHeight * MAX_LINES
    const newHeight = Math.min(textarea.scrollHeight, maxHeight)
    textarea.style.height = `${newHeight}px`
  }, [text])

  const handleSend = useCallback(async () => {
    if (!text.trim() || !api) return

    const messageText = text.trim()
    setText('')
    setIsProcessing(true)

    // Reset textarea height
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto'
    }

    // Get or create session ID
    let sessionId = useSessionStore.getState().activeSessionId
    if (!sessionId) {
      try {
        // Create new session
        const newSession = await api.CreateSession()
        addSession(newSession)
        setActiveSession(newSession.id)
        sessionId = newSession.id
      } catch (error) {
        logger.error('Failed to create session:', error)
        // Show error to user - we don't have a session ID yet, so we can't add to chat
        setIsProcessing(false)
        // Restore the text so user can retry
        setText(messageText)
        return
      }
    }

    // Optimistically add user message to UI
    addMessage(sessionId, {
      id: `user-${Date.now()}`,
      sessionId: sessionId,
      type: 'user',
      content: messageText,
      timestamp: Date.now(),
    })

    // Move session to top of list
    touchSession(sessionId)

    // Mark task as active
    setTaskActive(true)
    useChatStore.getState().setActivityStatus('Processing...')

    try {
      await api.SendMessage(sessionId, messageText, planFirst)
    } catch (error) {
      logger.error('Failed to send message:', error)
      // Display the error in the chat UI so the user can see it
      const errorMessage = error instanceof Error ? error.message : String(error)
      addMessage(sessionId, {
        id: `error-send-${Date.now()}`,
        sessionId: sessionId,
        type: 'error',
        content: `Failed to send message: ${errorMessage}`,
        timestamp: Date.now(),
      })
      // Re-enable input since no backend task was started
      setTaskActive(false)
    } finally {
      setIsProcessing(false)
    }
  }, [text, api, addMessage, addSession, setActiveSession, touchSession, setTaskActive, planFirst])

  const handleCancel = useCallback(async () => {
    if (!activeSessionId || !api) return

    try {
      await api.CancelTask(activeSessionId)
    } catch (error) {
      logger.error('Failed to cancel task:', error)
    }
  }, [activeSessionId, api])

  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    // Enter to send, Shift+Enter for new line
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      if (!showCancel && !isInputDisabled) {
        handleSend()
      }
    }
  }, [handleSend, showCancel, isInputDisabled])

  // Determine placeholder and blocking message
  let placeholder = 'Type a message... (Enter to send, Shift+Enter for new line)'
  let blockingMessage: string | null = null
  if (isNoProject) {
    placeholder = 'Select or create a project to start'
    blockingMessage = 'Select or create a project to start'
  } else if (isTaskActive) {
    placeholder = 'Session is processing...'
  }

  return (
    <div className="border-t border-border bg-card p-4">
      <div className="flex flex-col">
        <div className="relative">
          <textarea
            ref={textareaRef}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={placeholder}
            rows={1}
            disabled={isInputDisabled}
            className={`w-full min-h-[44px] max-h-[160px] resize-none bg-transparent px-3 py-2.5 text-sm placeholder:text-muted-foreground focus-visible:outline-none${isInputDisabled ? ' opacity-50 cursor-not-allowed' : ''}`}
            style={{ overflow: text.split('\n').length > MAX_LINES ? 'auto' : 'hidden' }}
          />
        </div>

        {blockingMessage && (
          <p className="px-3 text-xs italic text-muted-foreground">{blockingMessage}</p>
        )}

        <div className="flex items-center justify-between pt-2 min-h-[40px]">
          <Tooltip>
            <TooltipTrigger asChild>
              <label className="flex items-center gap-1.5 cursor-pointer select-none">
                <input
                  type="checkbox"
                  checked={planFirst}
                  onChange={(e) => setPlanFirst(e.target.checked)}
                  disabled={isInputDisabled}
                  className="h-3.5 w-3.5 rounded border-border accent-emerald-500"
                />
                <span className={`text-xs ${isInputDisabled ? 'text-muted-foreground/50' : 'text-muted-foreground'}`}>
                  Plan first
                </span>
              </label>
            </TooltipTrigger>
            <TooltipContent side="top" className="max-w-xs text-xs">
              <p>When enabled, the agent creates a full plan before taking action — better for complex, multi-step tasks.</p>
              <p className="mt-1">When disabled, the agent thinks and acts one step at a time — faster for simple requests.</p>
            </TooltipContent>
          </Tooltip>
          
          {showCancel ? (
            <Button
              variant="destructive"
              size="icon"
              onClick={handleCancel}
              className="shrink-0 h-8 w-8 rounded-md"
              title="Cancel"
              aria-label="Cancel task"
            >
              <Square className="h-3.5 w-3.5 fill-current" />
            </Button>
          ) : (
            <Button
              onClick={handleSend}
              disabled={!text.trim() || isInputDisabled}
              className="shrink-0 h-8 w-8 rounded-md bg-emerald-500 hover:bg-emerald-600 active:bg-emerald-700 transition-colors text-white"
              title="Send message"
              aria-label="Send message"
            >
              <Play className="h-3.5 w-3.5 fill-current" />
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}
