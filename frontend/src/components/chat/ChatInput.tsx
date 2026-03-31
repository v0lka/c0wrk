import { useState, useRef, useCallback, useEffect, type KeyboardEvent } from 'react'
import { Button } from '@/components/ui/button'
import { useSessionStore } from '@/stores/sessionStore'
import { useChatStore } from '@/stores/chatStore'
import { useWails } from '@/hooks/useWails'
import { Play, Square } from 'lucide-react'

const MAX_LINES = 6

export function ChatInput() {
  const [text, setText] = useState('')
  const [isProcessing, setIsProcessing] = useState(false)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const touchSession = useSessionStore(s => s.touchSession)
  const addSession = useSessionStore(s => s.addSession)
  const setActiveSession = useSessionStore(s => s.setActiveSession)
  const isThinking = useChatStore(s => s.isThinking)
  const addMessage = useChatStore(s => s.addMessage)
  const { api } = useWails()

  const showCancel = isThinking || isProcessing

  // Auto-resize textarea
  useEffect(() => {
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
        console.error('Failed to create session:', error)
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

    try {
      await api.SendMessage(sessionId, messageText)
    } catch (error) {
      console.error('Failed to send message:', error)
      // Display the error in the chat UI so the user can see it
      const errorMessage = error instanceof Error ? error.message : String(error)
      addMessage(sessionId, {
        id: `error-send-${Date.now()}`,
        sessionId: sessionId,
        type: 'error',
        content: `Failed to send message: ${errorMessage}`,
        timestamp: Date.now(),
      })
    } finally {
      setIsProcessing(false)
    }
  }, [text, api, addMessage, addSession, setActiveSession, touchSession])

  const handleCancel = useCallback(async () => {
    if (!activeSessionId || !api) return

    try {
      await api.CancelTask(activeSessionId)
    } catch (error) {
      console.error('Failed to cancel task:', error)
    }
  }, [activeSessionId, api])

  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    // Enter to send, Shift+Enter for new line
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      if (!showCancel) {
        handleSend()
      }
    }
  }, [handleSend, showCancel])

  return (
    <div className="border-t border-border bg-card p-4">
      <div className="flex flex-col max-w-4xl mx-auto">
        <div className="relative">
          <textarea
            ref={textareaRef}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Type a message... (Enter to send, Shift+Enter for new line)"
            rows={1}
            className="w-full min-h-[44px] max-h-[160px] resize-none bg-transparent px-3 py-2.5 text-sm placeholder:text-muted-foreground focus-visible:outline-none"
            style={{ overflow: text.split('\n').length > MAX_LINES ? 'auto' : 'hidden' }}
          />
        </div>

        <div className="flex items-center justify-end pt-2 min-h-[40px]">
          {showCancel ? (
            <Button
              variant="destructive"
              size="icon"
              onClick={handleCancel}
              className="shrink-0 h-8 w-8 rounded-md"
              title="Cancel"
            >
              <Square className="h-3.5 w-3.5 fill-current" />
            </Button>
          ) : (
            <Button
              onClick={handleSend}
              disabled={!text.trim()}
              className="shrink-0 h-8 w-8 rounded-md bg-emerald-500 hover:bg-emerald-600 active:bg-emerald-700 transition-colors text-white"
              title="Send message"
            >
              <Play className="h-3.5 w-3.5 fill-current" />
            </Button>
          )}
        </div>
      </div>
    </div>
  )
}
