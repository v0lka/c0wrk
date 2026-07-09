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
      const labels = q.options.filter((o: { label: string; value: string }) => sel.has(o.value)).map((o: { label: string; value: string }) => o.label)
      return [...labels, custom.trim() ? `Custom: ${custom.trim()}` : ''].filter(Boolean).join('; ')
    }).filter(Boolean)
    const answer = parts.join(' | ')
    emit('ask_user_response', {
      request_id: requestId,
      answers: questions.map(q => ({ id: q.id, selected: Array.from(selections.get(q.id) ?? []), custom_text: customTexts.get(q.id) ?? '' })),
    })
    updateMessage(sessionId, item.message.id, { metadata: askUserResolved(answer) })
    setActivityStatus(sessionId, null)
  }

  if (resolved !== null) {
    return (
      <div className="rounded-md border border-success/30 bg-success/5 px-3 py-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <Check className="h-3.5 w-3.5 shrink-0 text-success" />
          <span>Question</span>
        </div>
        <p className="mt-1.5 text-xs text-muted-foreground">{resolved ? `Answered: ${resolved}` : 'Answered'}</p>
      </div>
    )
  }

  return (
    <div className="rounded-md border border-info/30 bg-info/5 px-3 py-2">
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <HelpCircle className="h-3.5 w-3.5 shrink-0 text-info" />
        <span>{questions.length > 1 ? 'Questions' : 'Question'}</span>
      </div>
      {questions.map((q, qi) => {
        const sel = selections.get(q.id) ?? new Set<string>()
        const customText = customTexts.get(q.id) ?? ''
        return (
          <div key={q.id} className="mt-1.5">
            <p className="text-xs font-medium text-foreground mb-1.5">{q.question}</p>
            {q.options.length > 0 && (
              <div className="space-y-1.5 mb-2">
                {q.options.map((opt: { label: string; value: string }) => (
                  <button key={opt.value} type="button" onClick={() => toggleOption(q.id, opt.value)}
                    className={`w-full flex items-center gap-2 p-2 rounded-md border text-left transition-colors ${sel.has(opt.value) ? 'bg-info/10 border-info/50' : 'border-border hover:bg-accent/50'}`}
                  >
                    <div className={`shrink-0 flex items-center justify-center ${q.multi_select ? 'h-3.5 w-3.5 rounded-sm' : 'h-3.5 w-3.5 rounded-full'} border ${sel.has(opt.value) ? 'border-info bg-info' : 'border-muted-foreground'}`}>
                      {sel.has(opt.value) && <Check className="h-2.5 w-2.5 text-primary-foreground" />}
                    </div>
                    <span className="text-xs flex-1">{opt.label}</span>
                    {q.recommended?.includes(opt.value) && <Star className="h-3 w-3 text-warning shrink-0" />}
                  </button>
                ))}
              </div>
            )}
            <div className="mb-2">
              <label className="text-xs text-muted-foreground/60 mb-1 block">Or type your own answer...</label>
              <Input value={customText} onChange={e => setCustomTexts(p => new Map(p).set(q.id, e.target.value))} placeholder="Type here..."
                onKeyDown={e => { if (e.key === 'Enter' && canSubmit) handleSubmit() }} />
            </div>
            {qi < questions.length - 1 && <hr className="border-border mb-2" />}
          </div>
        )
      })}
      <Button size="sm" onClick={handleSubmit} disabled={!canSubmit} className="text-xs">Submit</Button>
    </div>
  )
}
