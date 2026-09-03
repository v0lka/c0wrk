import { AlertTriangle } from 'lucide-react'
import { CollapsibleBlock } from '@/components/chat/CollapsibleBlock'
import { bookmarkKey } from '@/lib/bookmarks'
import type { DisplayItem } from '@/types/messages'

type ReflectionItem = Extract<DisplayItem, { kind: 'reflection' }>

const actionBadgeColors: Record<string, string> = {
  retry: 'bg-info/15 text-info border-info/30',
  replan: 'bg-warning/15 text-warning border-warning/30',
  abort: 'bg-destructive/15 text-destructive border-destructive/30',
}

export function ReflectionBlock({ item }: { item: ReflectionItem }) {
  const { summary, suggestedAction, rootCause, failureAnalysis, actionPlan, reasoning, hypotheses, attempt, maxAttempts } = item
  const badgeColor = actionBadgeColors[suggestedAction] ?? 'bg-muted text-muted-foreground border-border'
  const hasDetails = rootCause || actionPlan || failureAnalysis || reasoning || hypotheses.length > 0

  return (
    <div className="border-l-2 border-warning/60 rounded pl-3 py-2">
      <div className="flex items-center gap-1.5 text-sm">
        <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-warning" />
        <span className="text-muted-foreground">{summary}</span>
        {suggestedAction && (
          <span className={`text-xs px-1.5 py-0.5 rounded border ${badgeColor}`}>{suggestedAction}</span>
        )}
        {maxAttempts > 0 && (
          <span className="text-xs text-muted-foreground/50 ml-auto shrink-0">
            Attempt {attempt}/{maxAttempts}
          </span>
        )}
      </div>
      {hasDetails && (
        <div className="mt-1.5">
          <CollapsibleBlock label="Details" revealId={bookmarkKey(item)}>
            <div className="mt-2 space-y-2 text-xs text-muted-foreground">
              {rootCause && (
                <div><span className="text-muted-foreground/60">Root cause:</span> {rootCause}</div>
              )}
              {failureAnalysis && (
                <div><span className="text-muted-foreground/60">Failure analysis:</span> {failureAnalysis}</div>
              )}
              {actionPlan && (
                <div><span className="text-muted-foreground/60">Action plan:</span> {actionPlan}</div>
              )}
              {hypotheses.length > 0 && (
                <div>
                  <span className="text-muted-foreground/60">Hypotheses:</span>
                  <ul className="list-disc list-inside mt-0.5 space-y-0.5">
                    {hypotheses.map((h, i) => <li key={`${i}-${h}`}>{h}</li>)}
                  </ul>
                </div>
              )}
              {reasoning && (
                <div><span className="text-muted-foreground/60">Reasoning:</span> {reasoning}</div>
              )}
            </div>
          </CollapsibleBlock>
        </div>
      )}
    </div>
  )
}
