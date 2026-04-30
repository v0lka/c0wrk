import { useState } from 'react'
import { HelpCircle, Check, Star } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { emit } from '@/api/runtime'
import { useChatStore } from '@/stores/chatStore'
import type { DisplayItem } from '@/types/messages'
import { getAskUserResolution, parseAskUserQuestions, askUserResolved } from '@/types/messages'

type AskUserItem = Extract<DisplayItem, { kind: 'ask_user' }>

export function AskUserPanel({ item }: { item: AskUserItem }) {
  const { sessionId, metadata } = item.message
  const resolved = getAskUserResolution(metadata)
  const [selections, setSelections] = useState<Map<string, Set<string>>>(new Map())
  const [customTexts, setCustomTexts] = useState<Map<string, string>>(new Map())

  const updateMessage = useChatStore(s => s.updateMessage)
  const setActivityStatus = useChatStore(s => s.setActivityStatus)

  const requestId = typeof metadata?.request_id === 'string' ? metadata.request_id : undefined
  const questions = parseAskUserQuestions(metadata)

  const toggleOption = (questionId: string, value: string) => {
    setSelections(prev => {
      const next = new Map(prev)
      const q = questions.find(q => q.id === questionId)
      const current = new Set(next.get(questionId) ?? [])
      if (q?.multi_select) {
        if (current.has(value)) current.delete(value); else current.add(value)
      } else {
        const wasSelected = current.has(value)
        current.clear()
        if (!wasSelected) current.add(value)
      }
      next.set(questionId, current)
      return next
    })
  }

  const canSubmit = questions.some(q => {
    const sel = selections.get(q.id)
    const txt = customTexts.get(q.id)
    return (sel && sel.size > 0) || (txt && txt.trim().length > 0)
  })

  const handleSubmit = () => {
    const parts = questions.map(q => {
      const sel = selections.get(q.id) ?? new Set<string>()
      const custom = customTexts.get(q.id) ?? ''
      const labels = q.options.filter(o => sel.has(o.value)).map(o => o.label)
      return [...labels, custom.trim() ? `Custom: ${custom.trim()}` : ''].filter(Boolean).join('; ')
    }).filter(Boolean)
    const answer = parts.join(' | ')
    emit('ask_user_response', {
      request_id: requestId,
      answers: questions.map(q => ({ id: q.id, selected: Array.from(selections.get(q.id) ?? []), custom_text: customTexts.get(q.id) ?? '' })),
    })
    updateMessage(sessionId, item.message.id, { metadata: askUserResolved(answer) })
    setActivityStatus(null)
  }

  if (resolved !== null) {
    return (
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <Check className="h-3.5 w-3.5 text-primary" /><span className="text-sm">Answered: {resolved}</span>
      </div>
    )
  }

  return (
    <div className="border-2 border-primary/50 rounded-lg p-4 bg-primary/5 max-w-full overflow-hidden">
      <div className="flex items-center gap-2 mb-3">
        <HelpCircle className="h-4 w-4 text-primary" />
        <span className="text-sm font-medium">{questions.length > 1 ? 'Questions' : 'Question'}</span>
      </div>
      {questions.map((q, qi) => {
        const sel = selections.get(q.id) ?? new Set<string>()
        const customText = customTexts.get(q.id) ?? ''
        return (
          <div key={q.id}>
            <p className="text-base font-medium mb-3">{q.question}</p>
            {q.options.length > 0 && (
              <div className="space-y-2 mb-4">
                {q.options.map(opt => (
                  <button key={opt.value} type="button" onClick={() => toggleOption(q.id, opt.value)}
                    className={`w-full flex items-center gap-3 p-2.5 rounded-md border text-left transition-colors ${sel.has(opt.value) ? 'bg-primary/10 border-primary/50' : 'border-border hover:bg-accent/50'}`}
                  >
                    <div className={`shrink-0 flex items-center justify-center ${q.multi_select ? 'h-4 w-4 rounded-sm' : 'h-4 w-4 rounded-full'} border ${sel.has(opt.value) ? 'border-primary bg-primary' : 'border-muted-foreground'}`}>
                      {sel.has(opt.value) && <Check className="h-3 w-3 text-primary-foreground" />}
                    </div>
                    <span className="text-sm flex-1">{opt.label}</span>
                    {q.recommended?.includes(opt.value) && <Star className="h-3.5 w-3.5 text-warning shrink-0" />}
                  </button>
                ))}
              </div>
            )}
            <div className="mb-4">
              <label className="text-xs text-muted-foreground mb-1.5 block">Or type your own answer...</label>
              <Input value={customText} onChange={e => setCustomTexts(p => new Map(p).set(q.id, e.target.value))} placeholder="Type here..."
                onKeyDown={e => { if (e.key === 'Enter' && canSubmit) handleSubmit() }} />
            </div>
            {qi < questions.length - 1 && <hr className="border-border mb-4" />}
          </div>
        )
      })}
      <Button size="sm" onClick={handleSubmit} disabled={!canSubmit} className="text-xs">Submit</Button>
    </div>
  )
}
