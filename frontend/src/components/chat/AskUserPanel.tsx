import { useState } from 'react'
import { HelpCircle, Check, Star } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useWails } from '@/hooks/useWails'
import { useChatStore } from '@/stores/chatStore'

export interface AskUserPanelMetadata {
  request_id?: string
  questions?: Array<{
    id: string
    question: string
    options: Array<{ label: string; value: string }>
    multi_select?: boolean
    recommended?: string[]
  }>
}

interface AskUserPanelProps {
  sessionId: string
  metadata?: Record<string, unknown>
}

export function AskUserPanel({ sessionId, metadata }: AskUserPanelProps) {
  const { runtime } = useWails()
  const [resolved, setResolved] = useState<string | null>(null)
  const [selections, setSelections] = useState<Map<string, Set<string>>>(new Map())
  const [customTexts, setCustomTexts] = useState<Map<string, string>>(new Map())

  const requestId = typeof metadata?.request_id === 'string' ? metadata.request_id : undefined
  const questions: Array<{
    id: string
    question: string
    options: Array<{ label: string; value: string }>
    multi_select?: boolean
    recommended?: string[]
  }> = Array.isArray(metadata?.questions) ? metadata.questions : []

  const toggleOption = (questionId: string, value: string) => {
    setSelections(prev => {
      const next = new Map(prev)
      const question = questions.find(q => q.id === questionId)
      const multiSelect = question?.multi_select ?? false
      let current = next.get(questionId) || new Set()
      const newSet = new Set(current)
      if (multiSelect) {
        if (newSet.has(value)) {
          newSet.delete(value)
        } else {
          newSet.add(value)
        }
      } else {
        if (newSet.has(value)) {
          newSet.clear()
        } else {
          newSet.clear()
          newSet.add(value)
        }
      }
      next.set(questionId, newSet)
      return next
    })
  }

  const handleCustomTextChange = (questionId: string, text: string) => {
    setCustomTexts(prev => {
      const next = new Map(prev)
      next.set(questionId, text)
      return next
    })
  }

  const handleSubmit = () => {
    if (!runtime) return

    // Build answer summary for display
    const summaryParts: string[] = []
    for (const q of questions) {
      const selected = selections.get(q.id) || new Set()
      const customText = customTexts.get(q.id) || ''
      const parts: string[] = []
      if (selected.size > 0) {
        const selectedLabels = q.options
          .filter(o => selected.has(o.value))
          .map(o => o.label)
        parts.push(selectedLabels.join(', '))
      }
      if (customText.trim()) {
        parts.push(`Custom: ${customText.trim()}`)
      }
      if (parts.length > 0) {
        summaryParts.push(parts.join('; '))
      }
    }
    const answerSummary = summaryParts.join(' | ')

    setResolved(answerSummary)

    runtime.EventsEmit('ask_user_response', {
      request_id: requestId,
      answers: questions.map(q => ({
        id: q.id,
        selected: Array.from(selections.get(q.id) || new Set()),
        custom_text: customTexts.get(q.id) || '',
      })),
    })

    useChatStore.getState().setActivityStatus(null)

    // Atomically mark resolved in messages AND remove from pendingActions
    const askMsgId = `ask-user-${requestId}`
    useChatStore.getState().resolveAction(sessionId, askMsgId)
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

  const canSubmit = questions.some(q => {
    const selected = selections.get(q.id)
    const customText = customTexts.get(q.id)
    return (selected && selected.size > 0) || (customText && customText.trim().length > 0)
  })

  return (
    <div className="border-2 border-blue-500/50 rounded-lg p-4 bg-blue-500/5 max-w-full overflow-hidden">
      {/* Header */}
      <div className="flex items-center gap-2 mb-3">
        <HelpCircle className="h-4 w-4 text-blue-500" />
        <span className="text-sm font-medium">{questions.length > 1 ? 'Questions' : 'Question'}</span>
      </div>

      {/* Questions */}
      {questions.map((q, qIndex) => {
        const selected = selections.get(q.id) || new Set()
        const customText = customTexts.get(q.id) || ''
        return (
          <div key={q.id}>
            {/* Question text */}
            <p className="text-base font-medium mb-3">{q.question}</p>

            {/* Options list */}
            {q.options.length > 0 && (
              <div className="space-y-2 mb-4">
                {q.options.map(opt => {
                  const isSelected = selected.has(opt.value)
                  const isRecommended = q.recommended?.includes(opt.value) ?? false
                  return (
                    <button
                      key={opt.value}
                      type="button"
                      onClick={() => toggleOption(q.id, opt.value)}
                      className={`w-full flex items-center gap-3 p-2.5 rounded-md border text-left transition-colors ${
                        isSelected
                          ? 'bg-blue-500/10 border-blue-500/50'
                          : 'border-border hover:bg-accent/50'
                      }`}
                    >
                      {/* Radio/Checkbox indicator */}
                      <div
                        className={`shrink-0 flex items-center justify-center ${
                          q.multi_select ? 'h-4 w-4 rounded-sm' : 'h-4 w-4 rounded-full'
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
                onChange={e => handleCustomTextChange(q.id, e.target.value)}
                placeholder="Type here..."
                onKeyDown={e => {
                  if (e.key === 'Enter' && canSubmit) handleSubmit()
                }}
              />
            </div>

            {/* Divider between questions */}
            {qIndex < questions.length - 1 && (
              <hr className="border-border mb-4" />
            )}
          </div>
        )
      })}

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
