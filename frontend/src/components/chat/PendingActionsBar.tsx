import { useChatStore } from '@/stores/chatStore'
import { ToolConfirmation } from './ToolConfirmation'
import { AskUserPanel } from './AskUserPanel'

export function PendingActionsBar() {
  const pendingActions = useChatStore(s => s.pendingActions)

  if (pendingActions.length === 0) return null

  return (
    <div className="border-t border-border bg-background/95 backdrop-blur-sm max-h-64 overflow-y-auto">
      <div className="p-3 space-y-3">
        {pendingActions.map((action) => {
          if (action.kind === 'tool_confirm') {
            return (
              <ToolConfirmation
                key={action.message.id}
                sessionId={action.message.sessionId}
                metadata={action.message.metadata}
              />
            )
          }
          if (action.kind === 'ask_user') {
            return (
              <AskUserPanel
                key={action.message.id}
                sessionId={action.message.sessionId}
                metadata={action.message.metadata}
              />
            )
          }
          return null
        })}
      </div>
    </div>
  )
}
