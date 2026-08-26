import { Button } from '@/components/ui/button'
import { Play, Pause, Square, MessageSquare, Terminal, Sparkles, Loader2, FolderPlus, Paperclip } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { ChatInputController } from '@/hooks/useChatInputController'
import { ModelCombobox } from './ModelCombobox'
import { ReasoningCombobox } from './ReasoningCombobox'
import { GoalToggle } from './GoalToggle'
import { BudgetCombobox } from './BudgetCombobox'
import { useWorkDirsStore } from '@/stores/workDirsStore'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useAttachmentsInput } from '@/hooks/useAttachmentsInput'

interface ChatInputToolbarProps {
  controller: ChatInputController
}

/**
 * ChatInputToolbar renders the bottom toolbar of the chat input: chat/terminal
 * mode toggles, blocking message, optimize-prompt error display, and the
 * optimize/send/cancel buttons.
 *
 * All state lives in `controller` (see useChatInputController). This component
 * is purely presentational so it stays small and easy to test in isolation.
 */
export function ChatInputToolbar({ controller }: ChatInputToolbarProps) {
  const {
    mode,
    setMode,
    isInputDisabled,
    isNoProject,
    showCancel,
    taskActive,
    paused,
    pausing,
    compacting,
    hasContent,
    isOptimizing,
    optimizeError,
    sendError,
    activeSessionId,
    handleSend,
    handleOptimize,
    handlePause,
    handleResume,
    cancel,
  } = controller

  const { handleAttach } = useAttachmentsInput(activeSessionId)

  // Budget selector is only meaningful when goal mode is enabled.
  const goalEnabled = useInputModeStore((s) => s.goalEnabled)

  const blockingMessage = isNoProject ? 'Select or create a project' : null

  return (
    <div className="flex items-center px-3 py-1.5 min-h-[36px] shrink-0 gap-1">
      <Button
        variant="ghost"
        size="icon-xs"
        onClick={handleAttach}
        disabled={compacting}
        title="Attach files"
        aria-label="Attach files"
        className="text-muted-foreground hover:text-foreground"
      >
        <Paperclip className="size-3.5" />
      </Button>
      <Button
        variant="ghost"
        size="icon-xs"
        onClick={() => useWorkDirsStore.getState().setOpen(true)}
        disabled={compacting}
        title="Add working directory"
        aria-label="Add working directory"
        className="text-muted-foreground hover:text-foreground"
      >
        <FolderPlus className="size-3.5" />
      </Button>
      <div className="w-px h-4 bg-border mx-1" />
      <Button
        variant="ghost"
        size="icon-xs"
        className={cn(
          'text-muted-foreground hover:text-foreground',
          mode === 'chat' && 'text-primary bg-muted/50',
        )}
        onClick={() => setMode('chat')}
        disabled={compacting}
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
        disabled={compacting}
        title="Terminal mode"
        aria-label="Switch to terminal mode"
      >
        <Terminal className="size-3.5" />
      </Button>
      {blockingMessage && mode === 'chat' && (
        <span className="text-xs italic text-muted-foreground">{blockingMessage}</span>
      )}
      {mode === 'chat' && (
        <>
          <div className="w-px h-4 bg-border mx-1" />
          {/* The selector cluster is inert while compacting: the compaction
              flow owns the session (pause-wait → history swap → auto-resume),
              and a model/reasoning/goal change mid-flow would race it. */}
          <div className={cn('flex items-center gap-1', compacting && 'pointer-events-none opacity-60')}>
            <ModelCombobox />
            <ReasoningCombobox />
            <div className="w-px h-4 bg-border mx-1" />
            <GoalToggle />
            {goalEnabled && <BudgetCombobox />}
          </div>
        </>
      )}
      <div className="flex-1" />
      {showCancel ? (
        <>
          {/* Pause (running) / Resume (paused) flank the Stop button. A
              cooperative pause unlocks the input for a nudge-resume; Resume
              re-enters the task without a nudge. While a pause is in flight
              (between the Pause click and the session_paused event) the
              action button is a NON-CLICKABLE spinner: the ReAct loop is
              still stopping, so neither Pause nor Resume is meaningful and
              the input stays locked (no premature nudge-resume).
              Suppressed entirely while compacting: the compaction flow owns
              the pause signal, and a stale taskActive (session_paused is
              suppressed while compacting) must not render a Pause button —
              only Stop remains. */}
          {compacting ? null : paused ? (
            <Button
              variant="outline"
              size="icon"
              onClick={handleResume}
              className="shrink-0 h-8 w-8 rounded-md border-success text-success hover:bg-success/10 active:bg-success/20"
              title="Resume task"
              aria-label="Resume task"
            >
              <Play className="h-3.5 w-3.5 fill-current" />
            </Button>
          ) : pausing ? (
            <Button
              variant="ghost"
              size="icon"
              disabled
              className="shrink-0 h-8 w-8 rounded-md text-warning"
              title="Pausing — waiting for the current step to finish"
              aria-label="Pausing task"
            >
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            </Button>
          ) : taskActive ? (
            <Button
              variant="ghost"
              size="icon"
              onClick={handlePause}
              className="shrink-0 h-8 w-8 rounded-md text-warning hover:bg-warning/10 active:bg-warning/20"
              title="Pause task"
              aria-label="Pause task"
            >
              <Pause className="h-3.5 w-3.5" />
            </Button>
          ) : null}
          <Button
            variant="outline"
            size="icon"
            onClick={cancel}
            className="shrink-0 h-8 w-8 rounded-md border-destructive text-destructive hover:bg-destructive/10 active:bg-destructive/20"
            title="Cancel"
            aria-label="Cancel task"
          >
            <Square className="h-3.5 w-3.5 fill-current" />
          </Button>
        </>
      ) : mode === 'chat' ? (
        <>
          {optimizeError && (
            <span
              className="text-xs text-destructive italic mr-1 truncate max-w-[200px]"
              title={optimizeError}
              role="alert"
            >
              {optimizeError}
            </span>
          )}
          {sendError && (
            <span
              className="text-xs text-destructive italic mr-1 truncate max-w-[200px]"
              title={sendError}
              role="alert"
            >
              {sendError}
            </span>
          )}
          <Button
            variant="ghost"
            size="icon-xs"
            onClick={handleOptimize}
            disabled={!hasContent || isOptimizing || isInputDisabled}
            title="Optimize prompt"
            aria-label="Optimize prompt"
            className="text-muted-foreground hover:text-foreground"
          >
            {isOptimizing
              ? <Loader2 className="size-3.5 animate-spin" />
              : <Sparkles className="size-3.5" />}
          </Button>
          <Button
            onClick={handleSend}
            disabled={!hasContent || isInputDisabled || isOptimizing}
            className="shrink-0 h-8 w-8 rounded-md text-input bg-success hover:bg-success/90 active:bg-success/75 transition-colors"
            title="Send message"
            aria-label="Send message"
          >
            <Play className="h-3.5 w-3.5 fill-current" />
          </Button>
        </>
      ) : null}
    </div>
  )
}
