import { useChatStore, type DisplayItem } from '@/stores/chatStore'
import { ToolConfirmation } from './ToolConfirmation'
import { AskUserPanel } from './AskUserPanel'
import { ResumeActionPanel } from './ResumeActionPanel'

type PendingActionItem = Extract<DisplayItem, { kind: 'tool_confirm' | 'ask_user' | 'resume_action' }>

function assertNever(x: never): never {
  throw new Error(`Unexpected action kind: ${JSON.stringify(x)}`)
}

export function PendingActionsBar() {
  const pendingActions = useChatStore(s => s.pendingActions)

  if (pendingActions.length === 0) return null

  return (
    <div className="border-t border-border bg-background/95 backdrop-blur-sm max-h-64 overflow-y-auto">
      <div className="p-3 space-y-3">
        {pendingActions.map((action) => {
          const item = action as PendingActionItem
          switch (item.kind) {
            case 'tool_confirm':
              return (
                <ToolConfirmation
                  key={item.message.id}
                  sessionId={item.message.sessionId}
                  metadata={item.message.metadata}
                />
              )
            case 'ask_user':
              return (
                <AskUserPanel
                  key={item.message.id}
                  sessionId={item.message.sessionId}
                  metadata={item.message.metadata}
                />
              )
            case 'resume_action':
              return (
                <ResumeActionPanel
                  key={item.message.id}
                  sessionId={item.message.sessionId}
                  content={item.message.content}
                  metadata={item.message.metadata}
                />
              )
            default:
              return assertNever(item)
          }
        })}
      </div>
    </div>
  )
}
