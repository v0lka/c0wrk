import { useState } from 'react'
import { HelpCircle, Check, Star } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useWails } from '@/hooks/useWails'
import { useChatStore } from '@/stores/chatStore'

interface AskUserPanelProps {
  sessionId: string
  metadata?: unknown
}

export function AskUserPanel({ sessionId, metadata }: AskUserPanelProps) {
  const { runtime } = useWails()
  const [resolved, setResolved] = useState<string | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [customText, setCustomText] = useState('')

  const meta = metadata as Record<string, unknown> | undefined
  const requestId = meta?.request_id as string | undefined
  const question = meta?.question as string | undefined
  const options = (meta?.options as Array<{ label: string; value: string }>) || []
  const multiSelect = meta?.multi_select as boolean | undefined
  const recommended = (meta?.recommended as string[]) || []

  const toggleOption = (value: string) => {
    setSelected(prev => {
      const next = new Set(prev)
      if (multiSelect) {
        if (next.has(value)) {
          next.delete(value)
        } else {
          next.add(value)
        }
      } else {
        if (next.has(value)) {
          next.clear()
        } else {
          next.clear()
          next.add(value)
        }
      }
      return next
    })
  }

  const handleSubmit = () => {
    if (!runtime) return

    const parts: string[] = []
    if (selected.size > 0) {
      const selectedLabels = options
        .filter(o => selected.has(o.value))
        .map(o => o.label)
      parts.push(selectedLabels.join(', '))
    }
    if (customText.trim()) {
      parts.push(`Custom: ${customText.trim()}`)
    }
    const answerSummary = parts.join('; ')

    setResolved(answerSummary)

    runtime.EventsEmit('ask_user_response', {
      request_id: requestId,
      selected: Array.from(selected),
      custom_text: customText,
    })

    useChatStore.getState().setActivityStatus(null)

    // Mark this question as resolved so groupMessages stops extracting it
    const askMsgId = `ask-user-${requestId}`
    useChatStore.getState().updateMessage(sessionId, askMsgId, {
      metadata: { resolved: true },
    })
  }

  // Resolved state
  if (resolved !== null) {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <Check className="h-3.5 w-3.5 text-blue-500" />
        <span className="text-sm">Answered: {resolved}</span>
      </div>
    )
  }

  const canSubmit = selected.size > 0 || customText.trim().length > 0

  return (
    <div className="border-2 border-blue-500/50 rounded-lg p-4 bg-blue-500/5 max-w-full overflow-hidden">
      {/* Header */}
      <div className="flex items-center gap-2 mb-3">
        <HelpCircle className="h-4 w-4 text-blue-500" />
        <span className="text-sm font-medium">Question</span>
      </div>

      {/* Question text */}
      <p className="text-base font-medium mb-4">{question}</p>

      {/* Options list */}
      {options.length > 0 && (
        <div className="space-y-2 mb-4">
          {options.map(opt => {
            const isSelected = selected.has(opt.value)
            const isRecommended = recommended.includes(opt.value)
            return (
              <button
                key={opt.value}
                type="button"
                onClick={() => toggleOption(opt.value)}
                className={`w-full flex items-center gap-3 p-2.5 rounded-md border text-left transition-colors ${
                  isSelected
                    ? 'bg-blue-500/10 border-blue-500/50'
                    : 'border-border hover:bg-accent/50'
                }`}
              >
                {/* Radio/Checkbox indicator */}
                <div
                  className={`shrink-0 flex items-center justify-center ${
                    multiSelect ? 'h-4 w-4 rounded-sm' : 'h-4 w-4 rounded-full'
                  } border ${
                    isSelected ? 'border-blue-500 bg-blue-500' : 'border-muted-foreground'
                  }`}
                >
                  {isSelected && (
                    <Check className="h-3 w-3 text-white" />
                  )}
                </div>
                <span className="text-sm flex-1">{opt.label}</span>
                {isRecommended && (
                  <Star className="h-3.5 w-3.5 text-yellow-400 shrink-0" />
                )}
              </button>
            )
          })}
        </div>
      )}

      {/* Custom text input */}
      <div className="mb-4">
        <label className="text-xs text-muted-foreground mb-1.5 block">Or type your own answer...</label>
        <Input
          value={customText}
          onChange={e => setCustomText(e.target.value)}
          placeholder="Type here..."
          onKeyDown={e => {
            if (e.key === 'Enter' && canSubmit) handleSubmit()
          }}
        />
      </div>

      {/* Submit button */}
      <Button
        size="sm"
        onClick={handleSubmit}
        disabled={!canSubmit}
        className="text-xs"
      >
        Submit
      </Button>
    </div>
  )
}
