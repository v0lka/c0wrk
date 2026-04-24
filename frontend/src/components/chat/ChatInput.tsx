import { useState, useRef, useCallback, useEffect, type KeyboardEvent } from 'react'
import { Button } from '@/components/ui/button'
import { ResizeHandle } from '@/components/ResizeHandle'
import { useSessionStore } from '@/stores/sessionStore'
import { useProjectStore } from '@/stores/projectStore'
import { useChatStore } from '@/stores/chatStore'
import { useUIStore } from '@/stores/uiStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { TerminalPanel } from '@/components/terminal/TerminalPanel'
import { sendMessage, cancelTask } from '@/api/chat'
import { optimizePrompt } from '@/api/prompt'
import { createSession } from '@/api/sessions'
import { generateMessageId } from '@/lib/ids'
import { Play, Square, Maximize2, Minimize2, MessageSquare, Terminal, Sparkles, Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { logger } from '@/lib/logger'

export function ChatInput() {
  const [text, setText] = useState('')
  const [isProcessing, setIsProcessing] = useState(false)
  const [isOptimizing, setIsOptimizing] = useState(false)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const activeProjectId = useProjectStore(s => s.activeProjectId)
  const taskActive = useChatStore(s => activeSessionId ? s.taskActive[activeSessionId] ?? false : false)
  const sidebarCollapsed = useUIStore(s => s.sidebarCollapsed)
  const viewerCollapsed = useFileViewerStore(s => s.collapsed)
  const hasViewerTabs = useFileViewerStore(s => s.openTabs.length > 0)

  const mode = useInputModeStore(s => s.mode)
  const height = useInputModeStore(s => s.height)
  const isExpanded = useInputModeStore(s => s.isExpanded)
  const setMode = useInputModeStore(s => s.setMode)
  const setHeight = useInputModeStore(s => s.setHeight)
  const toggleExpanded = useInputModeStore(s => s.toggleExpanded)

  useEffect(() => {
    if (mode === 'terminal' && textareaRef.current) {
      textareaRef.current.blur()
    }
  }, [mode])

  const isNoProject = !activeProjectId
  const isInputDisabled = taskActive || isNoProject
  const showCancel = taskActive || isProcessing

  const handleSend = useCallback(async () => {
    if (!text.trim()) return

    const messageText = text.trim()
    setText('')
    setIsProcessing(true)

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

  const handleOptimize = useCallback(async () => {
    if (!text.trim() || isOptimizing) return
    setIsOptimizing(true)
    try {
      const result = await optimizePrompt(text.trim())
      setText(result.optimized_prompt)
    } catch (error) {
      logger.error('Failed to optimize prompt:', error)
    } finally {
      setIsOptimizing(false)
    }
  }, [text, isOptimizing])

  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      if (!showCancel && !isInputDisabled) handleSend()
    }
  }, [handleSend, showCancel, isInputDisabled])

  const handleResizeStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    const startY = e.clientY
    const startHeight = height

    const handleMouseMove = (e: MouseEvent) => {
      const delta = startY - e.clientY
      const newHeight = Math.max(140, Math.min(800, startHeight + delta))
      setHeight(newHeight)
    }

    const handleMouseUp = () => {
      document.removeEventListener('mousemove', handleMouseMove)
      document.removeEventListener('mouseup', handleMouseUp)
    }

    document.addEventListener('mousemove', handleMouseMove)
    document.addEventListener('mouseup', handleMouseUp)
  }, [height, setHeight])

  const handleResizeKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHeight(height + 20)
    } else if (e.key === 'ArrowDown') {
      e.preventDefault()
      setHeight(height - 20)
    }
  }, [height, setHeight])

  let placeholder = 'Type a message... (Enter to send, Shift+Enter for new line)'
  let blockingMessage: string | null = null
  if (isNoProject) {
    placeholder = 'Select or create a project to start'
    blockingMessage = 'Select or create a project'
  } else if (taskActive) {
    placeholder = 'Session is processing...'
  }

  return (
    <div
      className={cn(
        'flex flex-col flex-shrink-0 border-t border-x border-border bg-card mb-1 overflow-hidden',
        sidebarCollapsed && 'ml-1',
        viewerCollapsed && hasViewerTabs && 'mr-1',
      )}
      style={{ height }}
    >
      <ResizeHandle
        orientation="horizontal"
        onMouseDown={handleResizeStart}
        onKeyDown={handleResizeKeyDown}
      />

      <div className="flex-1 min-h-0 px-3 py-1 relative">
        <Button
          variant="ghost"
          size="icon-xs"
          className="absolute top-0 right-3 z-20 text-muted-foreground hover:text-foreground"
          onClick={toggleExpanded}
          title={isExpanded ? 'Collapse' : 'Expand'}
        >
          {isExpanded ? <Minimize2 className="size-3.5" /> : <Maximize2 className="size-3.5" />}
        </Button>
        <div className={cn(
          'absolute inset-0 flex flex-col px-3 py-1',
          mode !== 'chat' && 'opacity-0 pointer-events-none -z-10',
        )}>
          <textarea
            ref={textareaRef}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={placeholder}
            disabled={isInputDisabled}
            className={cn(
              'w-full h-full resize-none bg-transparent text-sm placeholder:text-muted-foreground focus-visible:outline-none custom-scrollbar pr-8',
              isInputDisabled && 'opacity-50 cursor-not-allowed',
            )}
          />
        </div>
        <div className={cn(
          'absolute inset-0 px-3 py-1',
          mode !== 'terminal' && 'opacity-0 pointer-events-none -z-10',
        )}>
          <TerminalPanel sessionId={activeSessionId} visible={mode === 'terminal'} />
        </div>
      </div>

      <div className="flex items-center px-3 py-1.5 min-h-[36px] shrink-0 gap-1">
        <Button
          variant="ghost"
          size="icon-xs"
          className={cn(
            'text-muted-foreground hover:text-foreground',
            mode === 'chat' && 'text-primary bg-muted/50',
          )}
          onClick={() => setMode('chat')}
          title="Chat mode"
          aria-label="Switch to chat mode"
        >
          <MessageSquare className="size-3.5" />
        </Button>
        <Button
          variant="ghost"
          size="icon-xs"
          className={cn(
            'text-muted-foreground hover:text-foreground',
            mode === 'terminal' && 'text-primary bg-muted/50',
          )}
          onClick={() => setMode('terminal')}
          title="Terminal mode"
          aria-label="Switch to terminal mode"
        >
          <Terminal className="size-3.5" />
        </Button>
        {mode === 'chat' && (
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={handleOptimize}
            disabled={!text.trim() || isOptimizing || isInputDisabled}
            title="Optimize prompt"
            aria-label="Optimize prompt"
            className="text-muted-foreground hover:text-foreground"
          >
            {isOptimizing
              ? <Loader2 className="size-3.5 animate-spin" />
              : <Sparkles className="size-3.5" />}
          </Button>
        )}
        {blockingMessage && mode === 'chat' && (
          <span className="text-xs italic text-muted-foreground">{blockingMessage}</span>
        )}
        <div className="flex-1" />
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
        ) : mode === 'chat' ? (
          <Button
            onClick={handleSend}
            disabled={!text.trim() || isInputDisabled || isOptimizing}
            className="shrink-0 h-8 w-8 rounded-md text-input bg-success hover:bg-success/90 active:bg-success/75 transition-colors"
            title="Send message"
            aria-label="Send message"
          >
            <Play className="h-3.5 w-3.5 fill-current" />
          </Button>
        ) : null}
      </div>
    </div>
  )
}
