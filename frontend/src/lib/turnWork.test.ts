import { describe, it, expect } from 'vitest'
import { splitTurnWork } from '@/lib/turnWork'
import type { ChatMessageUI, DisplayItem } from '@/types/messages'

let seq = 0
function mkMsg(type: ChatMessageUI['type'], content: string): ChatMessageUI {
  seq++
  return { id: `msg-${seq}`, sessionId: 's1', type, content, timestamp: seq }
}

// Every DisplayItem variant carries either `id` or `message.id`.
function idsOf(items: DisplayItem[]): string[] {
  return items.map(it => ('message' in it ? it.message.id : it.id))
}

const user = (content = 'question') => ({ kind: 'user', message: mkMsg('user', content) }) as DisplayItem
const assistant = (content = 'answer') => ({ kind: 'assistant', message: mkMsg('assistant', content) }) as DisplayItem
const thought = () => ({ kind: 'thought', id: `th-${++seq}`, stepNum: 1, content: 'thinking' }) as DisplayItem
const tool = () => ({ kind: 'tool', id: `tool-${++seq}`, toolName: 'bash', args: 'ls', status: 'success' }) as DisplayItem
const service = () => ({ kind: 'service', id: `svc-${++seq}`, variant: 'status', content: 'Working…' }) as DisplayItem
const errorItem = () => ({ kind: 'error', message: mkMsg('error', 'boom') }) as DisplayItem
const toolConfirm = () => ({ kind: 'tool_confirm', message: mkMsg('tool_confirm', 'Allow bash?') }) as DisplayItem
const askUser = () => ({ kind: 'ask_user', message: mkMsg('ask_user', 'Which one?') }) as DisplayItem
const planStep = () => ({ kind: 'plan_step', id: `ps-${++seq}`, stepId: `step_${seq}`, stepNum: 1, title: 'Step', status: 'completed', children: [] }) as DisplayItem
const activeChecklist = () => ({ kind: 'checklist', id: `cl-${++seq}`, stepId: null, items: [{ text: 'todo', checked: false }], active: true }) as DisplayItem

describe('splitTurnWork', () => {
  it('plain chat (user → answer): work is empty, tail holds only the answer', () => {
    const u = user()
    const a = assistant()
    const { work, tail } = splitTurnWork([u, a])

    expect(work).toEqual([])
    expect(idsOf(tail)).toEqual(idsOf([a]))
    expect(tail[0]).toBe(a)
  })

  it('typical ReAct turn: thoughts/tools and intermediate texts go to work, plan_step pins out, final answer to tail', () => {
    const u = user('refactor the parser')
    const th = thought()
    const tl = tool()
    const ps = planStep()
    const mid = assistant('Progress: half done.')
    const tl2 = tool()
    const fin = assistant('All done.')
    const { work, pinned, tail } = splitTurnWork([u, th, tl, ps, mid, tl2, fin])

    expect(idsOf(work)).toEqual(idsOf([th, tl, mid, tl2]))
    expect(idsOf(pinned)).toEqual(idsOf([ps]))
    expect(idsOf(tail)).toEqual(idsOf([fin]))
  })

  it('pinned steps keep their stream order across multiple lifts', () => {
    const u = user('run the plan')
    const ps1 = planStep()
    const tl = tool()
    const ps2 = planStep()
    const fin = assistant('done')
    const { work, pinned, tail } = splitTurnWork([u, ps1, tl, ps2, fin])

    expect(idsOf(work)).toEqual(idsOf([tl]))
    expect(idsOf(pinned)).toEqual(idsOf([ps1, ps2]))
    expect(idsOf(tail)).toEqual(idsOf([fin]))
  })

  it('lifts a pinned step while the turn is still running (no answer yet)', () => {
    const u = user('run the plan')
    const th = thought()
    const ps = planStep()
    const { work, pinned, tail } = splitTurnWork([u, th, ps])

    expect(idsOf(work)).toEqual(idsOf([th]))
    expect(idsOf(pinned)).toEqual(idsOf([ps]))
    expect(tail).toEqual([])
  })

  it('errors mid-turn go to work; errors after the final answer stay in the tail', () => {
    const u = user()
    const th = thought()
    const midErr = errorItem()
    const fin = assistant()
    const endErr = errorItem()
    const { work, tail } = splitTurnWork([u, th, midErr, fin, endErr])

    expect(idsOf(work)).toEqual(idsOf([th, midErr]))
    expect(idsOf(tail)).toEqual(idsOf([fin, endErr]))
  })

  it('unresolved HITL panels and the active checklist land in the tail below the answer', () => {
    const u = user()
    const tl = tool()
    const fin = assistant('Need your input.')
    const tc = toolConfirm()
    const au = askUser()
    const cl = activeChecklist()
    const { work, tail } = splitTurnWork([u, tl, fin, tc, au, cl])

    expect(idsOf(work)).toEqual(idsOf([tl]))
    expect(idsOf(tail)).toEqual(idsOf([fin, tc, au, cl]))
  })

  it('lifts an unresolved panel out of the work segment while the turn is still running (no answer yet)', () => {
    // The confirmation wait IS this shape: the run pauses mid-work with no
    // final assistant item, so the anchor-only split would bury the panel
    // inside the collapsible work container (its collapsed content is
    // unmounted — unreachable exactly when the user must act on it).
    const u = user()
    const tl = tool()
    const tc = toolConfirm()
    const { work, tail } = splitTurnWork([u, tl, tc])

    expect(idsOf(work)).toEqual(idsOf([tl]))
    expect(idsOf(tail)).toEqual(idsOf([tc]))
    expect(tail[0]).toBe(tc)
  })

  it('a resolved panel keeps its stream position inside the work (settled decision history)', () => {
    const u = user()
    const tl = tool()
    const resolvedMsg = { ...mkMsg('tool_confirm', 'Allow bash?'), metadata: { resolved: true, decision: 'confirmed' } }
    const resolved = { kind: 'tool_confirm', message: resolvedMsg } as DisplayItem
    const { work, tail } = splitTurnWork([u, tl, resolved])

    expect(idsOf(work)).toEqual(idsOf([tl, resolved]))
    expect(tail).toEqual([])
  })

  it('lifts multiple panels keeping their order, after the answer when one exists', () => {
    const u = user()
    const fin = assistant('Working, need input.')
    const tc = toolConfirm()
    const au = askUser()
    const { work, tail } = splitTurnWork([u, fin, tc, au])

    expect(work).toEqual([])
    expect(idsOf(tail)).toEqual(idsOf([fin, tc, au]))
  })

  it('multiple answers: intermediate ones go to work, only the last heads the tail', () => {
    const u = user()
    const a1 = assistant('First part.')
    const th = thought()
    const a2 = assistant('Second part.')
    const fin = assistant('Final.')
    const { work, tail } = splitTurnWork([u, a1, th, a2, fin])

    expect(idsOf(work)).toEqual(idsOf([a1, th, a2]))
    expect(idsOf(tail)).toEqual(idsOf([fin]))
  })

  it('turn without an assistant item: everything after the user message is work, tail is empty', () => {
    const u = user()
    const th = thought()
    const tl = tool()
    const { work, tail } = splitTurnWork([u, th, tl])

    expect(idsOf(work)).toEqual(idsOf([th, tl]))
    expect(tail).toEqual([])
  })

  it('a turn consisting of a single service marker (no user, no assistant)', () => {
    const svc = service()
    const { work, tail } = splitTurnWork([svc])

    expect(idsOf(work)).toEqual(idsOf([svc]))
    expect(tail).toEqual([])
  })

  it('empty input splits into three empty lists', () => {
    expect(splitTurnWork([])).toEqual({ work: [], pinned: [], tail: [] })
  })

  it('only the last user message anchors the turn: earlier turns belong to neither half', () => {
    const u1 = user('first task')
    const a1 = assistant('first answer')
    const u2 = user('second task')
    const th = thought()
    const fin = assistant('second answer')
    const { work, tail } = splitTurnWork([u1, a1, u2, th, fin])

    expect(idsOf(work)).toEqual(idsOf([th]))
    expect(idsOf(tail)).toEqual(idsOf([fin]))
  })

  it('an assistant answer OLDER than the last user message does not start a tail (turn still running)', () => {
    // Live-stream shape: the user re-sent / a new turn began while the previous
    // answer is still in the transcript above it. The old answer must not be
    // treated as the current turn's final one.
    const u1 = user()
    const a1 = assistant('stale answer')
    const u2 = user('follow-up')
    const th = thought()
    const { work, tail } = splitTurnWork([u1, a1, u2, th])

    expect(idsOf(work)).toEqual(idsOf([th]))
    expect(tail).toEqual([])
  })

  it('a turn consisting of a single pinned step (no user, no assistant)', () => {
    const ps = planStep()
    const { work, pinned, tail } = splitTurnWork([ps])

    expect(work).toEqual([])
    expect(idsOf(pinned)).toEqual(idsOf([ps]))
    expect(tail).toEqual([])
  })

  it('does not mutate the input and keeps item references', () => {
    const u = user()
    const th = thought()
    const fin = assistant()
    const cl = activeChecklist()
    const input = [u, th, fin, cl]
    const snapshot = [...input]
    const { work, tail } = splitTurnWork(input)

    expect(input).toEqual(snapshot)
    expect(input).toHaveLength(4)
    expect(tail[0]).toBe(fin)
    expect(work[0]).toBe(th)
    expect(tail[1]).toBe(cl)
  })
})
