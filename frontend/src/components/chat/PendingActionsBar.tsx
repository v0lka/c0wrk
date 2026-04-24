import { useMemo } from 'react'
import { useSessionMessages, extractPendingActions } from '@/stores/chatStore'
import { useSessionStore } from '@/stores/sessionStore'
import { ToolConfirmation } from './ToolConfirmation'
import { AskUserPanel } from './AskUserPanel'
import { ResumeActionPanel } from './ResumeActionPanel'
import { StepLimitPrompt } from './StepLimitPrompt'
import type { DisplayItem } from '@/types/messages'

type PendingActionItem = Extract<DisplayItem, { kind: 'tool_confirm' | 'ask_user' | 'resume_action' | 'step_limit' }>

export function PendingActionsBar() {
  const activeSessionId = useSessionStore(s => s.activeSessionId)
  const messages = useSessionMessages(activeSessionId)
  const pendingActions = useMemo(() => extractPendingActions(messages), [messages])

  if (pendingActions.length === 0) return null

  return (
    <div className="border-t border-x border-border bg-background/95 backdrop-blur-sm max-h-64 overflow-y-auto">
      <div className="p-3 space-y-3">
        {pendingActions.map((action) => {
          const a = action as PendingActionItem
          switch (a.kind) {
            case 'tool_confirm': return <ToolConfirmation key={a.message.id} item={a} />
            case 'ask_user': return <AskUserPanel key={a.message.id} item={a} />
            case 'resume_action': return <ResumeActionPanel key={a.message.id} item={a} />
            case 'step_limit': return <StepLimitPrompt key={a.message.id} item={a} />
          }
        })}
      </div>
    </div>
  )
}
