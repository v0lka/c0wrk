import { useRef } from 'react'
import { Button } from '@/components/ui/button'
import { Maximize2, Minimize2 } from 'lucide-react'
import { TerminalPanel } from '@/components/terminal/TerminalPanel'
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
 */
export function ChatEditorPane({ controller }: ChatEditorPaneProps) {
  const inputAreaRef = useRef<HTMLDivElement>(null)
  const { editor, mode, isExpanded, toggleExpanded, isInputDisabled, activeSessionId } = controller

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
        <TerminalPanel sessionId={activeSessionId} visible={mode === 'terminal'} />
      </div>
    </div>
  )
}
