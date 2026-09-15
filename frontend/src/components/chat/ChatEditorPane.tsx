import { useRef } from 'react'
import { Button } from '@/components/ui/button'
import { Maximize2, Minimize2 } from 'lucide-react'
import { TerminalPanel } from '@/components/terminal/TerminalPanel'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { cn } from '@/lib/utils'
import type { ChatInputController } from '@/hooks/useChatInputController'

interface ChatEditorPaneProps {
  controller: ChatInputController
}

/**
 * ChatEditorPane renders the chat-vs-terminal pane swap. The chat editor
 * (CodeMirror container) and terminal panel are stacked absolutely so mode
 * switching does not unmount the underlying editor — the terminal pane only
 * mounts when its session id is set.
 *
 * The terminal pane is wrapped in its OWN error boundary: it wraps a
 * third-party xterm.js instance and is the most likely input subcomponent to
 * throw, and a failure there must not take down the input shell — the toolbar
 * above stays interactive, so the user can switch modes and recover. The
 * boundary resets when the session or mode changes, so a transient failure
 * does not stick for the app lifetime (React error boundaries never retry on
 * their own). The chat editor is deliberately NOT wrapped: unmounting the
 * CodeMirror container would detach the EditorView's host node and leave the
 * editor blank after a reset.
 */
export function ChatEditorPane({ controller }: ChatEditorPaneProps) {
  const inputAreaRef = useRef<HTMLDivElement>(null)
  const { editor, mode, isExpanded, toggleExpanded, isInputDisabled, activeSessionId, setMode } = controller

  return (
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
      <div
        ref={inputAreaRef}
        className={cn(
          'absolute inset-0 flex flex-col px-3 py-1',
          mode !== 'chat' && 'opacity-0 pointer-events-none -z-10',
        )}
      >
        <div
          ref={editor.containerRef}
          className={cn(
            'cm-chat-container w-full h-full custom-scrollbar pr-8',
            isInputDisabled && 'cm-chat-disabled',
          )}
        />
      </div>
      <div className={cn(
        'absolute inset-0 px-3 pb-1',
        mode !== 'terminal' && 'opacity-0 pointer-events-none -z-10',
      )}>
        <ErrorBoundary
          resetKeys={[activeSessionId, mode]}
          fallback={(
            <div className="flex h-full flex-col items-center justify-center gap-2 px-3 text-center text-xs text-destructive">
              <span>The terminal failed to start. Your chat input is unaffected.</span>
              <Button variant="outline" size="sm" onClick={() => setMode('chat')}>
                Back to chat
              </Button>
            </div>
          )}
        >
          <TerminalPanel sessionId={activeSessionId} visible={mode === 'terminal'} />
        </ErrorBoundary>
      </div>
    </div>
  )
}
