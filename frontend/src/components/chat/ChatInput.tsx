import { useState, useRef, useCallback, useLayoutEffect, type KeyboardEvent } from 'react'
import { Button } from '@/components/ui/button'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useChatStore } from '@/stores/chatStore'
import { useUIStore } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { sendMessage, cancelTask } from '@/api/chat'
import { createSession } from '@/api/sessions'
import { generateMessageId } from '@/lib/ids'
import { Play, Square } from 'lucide-react'
import { cn } from '@/lib/utils'
import { logger } from '@/lib/logger'

const MAX_LINES = 6

export function ChatInput() {
  const [text, setText] = useState('')
  const [isProcessing, setIsProcessing] = useState(false)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const activeProjectId = useProjectStore(s => s.activeProjectId)
  const taskActive = useChatStore(s => activeSessionId ? s.taskActive[activeSessionId] ?? false : false)
  const sidebarCollapsed = useUIStore(s => s.sidebarCollapsed)
  const viewerCollapsed = useFileViewerStore(s => s.collapsed)
  const hasViewerTabs = useFileViewerStore(s => s.openTabs.length > 0)

  const isNoProject = !activeProjectId
  const isInputDisabled = taskActive || isNoProject
  const showCancel = taskActive || isProcessing

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
    if (!text.trim()) return

    const messageText = text.trim()
    setText('')
    setIsProcessing(true)

    if (textareaRef.current) textareaRef.current.style.height = 'auto'

    let sessionId = useSessionStore.getState().activeSessionId
    if (!sessionId) {
      try {
        const newSession = await createSession()
        useSessionStore.getState().addSession(newSession)
        useSessionStore.getState().setActiveSessionId(newSession.id)
        sessionId = newSession.id
      } catch (error) {
        logger.error('Failed to create session:', error)
        setIsProcessing(false)
        setText(messageText)
        return
      }
    }

    // Optimistically add user message
    useChatStore.getState().addMessage(sessionId, {
      id: generateMessageId(),
      sessionId,
      type: 'user',
      content: messageText,
      timestamp: Date.now(),
    })

    useSessionStore.getState().touchSession(sessionId)
    useChatStore.getState().setTaskActive(sessionId, true)
    useChatStore.getState().setActivityStatus('Processing...')

    try {
      await sendMessage(sessionId, messageText)
    } catch (error) {
      logger.error('Failed to send message:', error)
      const errorMessage = error instanceof Error ? error.message : String(error)
      useChatStore.getState().addMessage(sessionId, {
        id: generateMessageId(),
        sessionId,
        type: 'error',
        content: `Failed to send message: ${errorMessage}`,
        timestamp: Date.now(),
      })
      useChatStore.getState().setTaskActive(sessionId, false)
    } finally {
      setIsProcessing(false)
    }
  }, [text])

  const handleCancel = useCallback(async () => {
    if (!activeSessionId) return
    try {
      await cancelTask(activeSessionId)
    } catch (error) {
      logger.error('Failed to cancel task:', error)
    }
  }, [activeSessionId])

  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      if (!showCancel && !isInputDisabled) handleSend()
    }
  }, [handleSend, showCancel, isInputDisabled])

  let placeholder = 'Type a message... (Enter to send, Shift+Enter for new line)'
  let blockingMessage: string | null = null
  if (isNoProject) {
    placeholder = 'Select or create a project to start'
    blockingMessage = 'Select or create a project'
  } else if (taskActive) {
    placeholder = 'Session is processing...'
  }

  return (
    <div className={cn(
      'border-t border-x border-border bg-card p-4 mb-1',
      sidebarCollapsed && 'ml-1',
      viewerCollapsed && hasViewerTabs && 'mr-1',
    )}>
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
            className={cn(
              'w-full min-h-[44px] max-h-[160px] resize-none bg-transparent px-3 py-2.5 text-sm placeholder:text-muted-foreground focus-visible:outline-none custom-scrollbar',
              isInputDisabled && 'opacity-50 cursor-not-allowed',
              text.split('\n').length > MAX_LINES ? 'overflow-auto' : 'overflow-hidden',
            )}
          />
        </div>

        {blockingMessage && (
          <p className="px-3 text-xs italic text-muted-foreground">{blockingMessage}</p>
        )}

        <div className="flex items-center justify-end pt-2 min-h-[40px]">
          {showCancel ? (
            <Button
              variant="outline"
              size="icon"
              onClick={handleCancel}
              className="shrink-0 h-8 w-8 rounded-md border-destructive text-destructive hover:bg-destructive/10 active:bg-destructive/20"
              title="Cancel"
              aria-label="Cancel task"
            >
              <Square className="h-3.5 w-3.5 fill-current" />
            </Button>
          ) : (
            <Button
              onClick={handleSend}
              disabled={!text.trim() || isInputDisabled}
              className="shrink-0 h-8 w-8 rounded-md bg-success hover:bg-success/90 active:bg-success/75 transition-colors text-success-foreground"
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
