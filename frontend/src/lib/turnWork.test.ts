import { describe, it, expect } from 'vitest'
import { splitTurnWork, type TurnWorkSplit } from '@/lib/turnWork'
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

// The segments of a split as flat [work…, pinned…] rows per segment, for
// compact assertions over the multi-segment shapes.
function segRows(split: TurnWorkSplit): Array<{ work: string[]; pinned: string[] }> {
  return split.segments.map(seg => ({ work: idsOf(seg.work), pinned: idsOf(seg.pinned) }))
}

const user = (content = 'question') => ({ kind: 'user', message: mkMsg('user', content) }) as DisplayItem
const assistant = (content = 'answer') => ({ kind: 'assistant', message: mkMsg('assistant', content) }) as DisplayItem
const thought = () => ({ kind: 'thought', id: `th-${++seq}`, stepNum: 1, content: 'thinking' }) as DisplayItem
const tool = () => ({ kind: 'tool', id: `tool-${++seq}`, toolName: 'bash', args: 'ls', status: 'success' }) as DisplayItem
const service = () => ({ kind: 'service', id: `svc-${++seq}`, variant: 'status', content: 'Working…' }) as DisplayItem
const errorItem = () => ({ kind: 'error', message: mkMsg('error', 'boom') }) as DisplayItem
const toolConfirm = () => ({ kind: 'tool_confirm', message: mkMsg('tool_confirm', 'Allow bash?') }) as DisplayItem
const askUser = () => ({ kind: 'ask_user', message: mkMsg('ask_user', 'Which one?') }) as DisplayItem
const planStep = () => ({ kind: 'plan_step', id: `ps-${++seq}`, stepId: `step_${seq}`, stepNum: 1, title: 'Step', status: 'completed', children: [] }) as Extract<DisplayItem, { kind: 'plan_step' }>
const activeChecklist = () => ({ kind: 'checklist', id: `cl-${++seq}`, stepId: null, items: [{ text: 'todo', checked: false }], active: true }) as DisplayItem
const launcher = (id: string, name: 'execute_plan' | 'delegate' = 'execute_plan') =>
  ({ kind: 'tool', id, toolName: name, args: '', status: 'success' }) as DisplayItem

describe('splitTurnWork', () => {
  it('plain chat (user → answer): no segments, tail holds only the answer', () => {
    const u = user()
    const a = assistant()
    const { segments, tail } = splitTurnWork([u, a])

    expect(segments).toEqual([])
    expect(idsOf(tail)).toEqual(idsOf([a]))
    expect(tail[0]).toBe(a)
  })

  it('a step closes the segment whose work produced it and starts a new one', () => {
    const u = user('refactor the parser')
    const th = thought()
    const tl = tool()
    const ps = planStep()
    const mid = assistant('Progress: half done.')
    const tl2 = tool()
    const fin = assistant('All done.')
    const { segments, tail } = splitTurnWork([u, th, tl, ps, mid, tl2, fin])

    // The step attaches to the segment it CLOSED (its producing work); the
    // work after it opens the next, live segment. Render: [block₁] → [ps] →
    // [block₂] — exact stream order.
    expect(segRows({ segments, tail })).toEqual([
      { work: idsOf([th, tl]), pinned: [ps.id] },
      { work: idsOf([mid, tl2]), pinned: [] },
    ])
    expect(idsOf(tail)).toEqual(idsOf([fin]))
  })

  it('consecutive steps close their preceding work; no empty segments', () => {
    const u = user('run the plan')
    const ps1 = planStep()
    const tl = tool()
    const ps2 = planStep()
    const fin = assistant('done')
    const { segments, tail } = splitTurnWork([u, ps1, tl, ps2, fin])

    expect(segRows({ segments, tail })).toEqual([
      { work: [], pinned: [ps1.id] },
      { work: idsOf([tl]), pinned: [ps2.id] },
    ])
    expect(idsOf(tail)).toEqual(idsOf([fin]))
  })

  it('a step while the turn is still running (no answer yet) still segments', () => {
    const u = user('run the plan')
    const th = thought()
    const ps = planStep()
    const { segments, tail } = splitTurnWork([u, th, ps])

    expect(segRows({ segments, tail })).toEqual([
      { work: idsOf([th]), pinned: [ps.id] },
    ])
    expect(tail).toEqual([])
  })

  it('errors mid-turn go to work; errors after the final answer stay in the tail', () => {
    const u = user()
    const th = thought()
    const midErr = errorItem()
    const fin = assistant()
    const endErr = errorItem()
    const { segments, tail } = splitTurnWork([u, th, midErr, fin, endErr])

    expect(segments).toHaveLength(1)
    expect(idsOf(segments[0]!.work)).toEqual(idsOf([th, midErr]))
    expect(idsOf(tail)).toEqual(idsOf([fin, endErr]))
  })

  it('unresolved HITL panels and the active checklist land in the tail below the answer', () => {
    const u = user()
    const tl = tool()
    const fin = assistant('Need your input.')
    const tc = toolConfirm()
    const au = askUser()
    const cl = activeChecklist()
    const { segments, tail } = splitTurnWork([u, tl, fin, tc, au, cl])

    expect(segments).toHaveLength(1)
    expect(idsOf(segments[0]!.work)).toEqual(idsOf([tl]))
    expect(idsOf(tail)).toEqual(idsOf([fin, tc, au, cl]))
  })

  it('lifts an unresolved panel out of the work region while the turn is still running (no answer yet)', () => {
    // The confirmation wait IS this shape: the run pauses mid-work with no
    // final assistant item, so the anchor-only split would bury the panel
    // inside the collapsible work container (its collapsed content is
    // unmounted — unreachable exactly when the user must act on it).
    const u = user()
    const tl = tool()
    const tc = toolConfirm()
    const { segments, tail } = splitTurnWork([u, tl, tc])

    expect(segments).toHaveLength(1)
    expect(idsOf(segments[0]!.work)).toEqual(idsOf([tl]))
    expect(idsOf(tail)).toEqual(idsOf([tc]))
    expect(tail[0]).toBe(tc)
  })

  it('a resolved panel keeps its stream position inside the work (settled decision history)', () => {
    const u = user()
    const tl = tool()
    const resolvedMsg = { ...mkMsg('tool_confirm', 'Allow bash?'), metadata: { resolved: true, decision: 'confirmed' } }
    const resolved = { kind: 'tool_confirm', message: resolvedMsg } as DisplayItem
    const { segments, tail } = splitTurnWork([u, tl, resolved])

    expect(segments).toHaveLength(1)
    expect(idsOf(segments[0]!.work)).toEqual(idsOf([tl, resolved]))
    expect(tail).toEqual([])
  })

  it('an answered turn whose only content is lifted panels: no segments, tail flat', () => {
    // The model answered immediately; the panels after it are lifted. No work
    // region → no segments — the tail renders flat (renderer contract).
    const u = user()
    const fin = assistant('Working, need input.')
    const tc = toolConfirm()
    const au = askUser()
    const { segments, tail } = splitTurnWork([u, fin, tc, au])

    expect(segments).toEqual([])
    expect(idsOf(tail)).toEqual(idsOf([fin, tc, au]))
  })

  it('multiple answers: intermediate ones go to work, only the last heads the tail', () => {
    const u = user()
    const a1 = assistant('First part.')
    const th = thought()
    const a2 = assistant('Second part.')
    const fin = assistant('Final.')
    const { segments, tail } = splitTurnWork([u, a1, th, a2, fin])

    expect(segments).toHaveLength(1)
    expect(idsOf(segments[0]!.work)).toEqual(idsOf([a1, th, a2]))
    expect(idsOf(tail)).toEqual(idsOf([fin]))
  })

  it('turn without an assistant item: everything after the user message is the work region, tail is empty', () => {
    const u = user()
    const th = thought()
    const tl = tool()
    const { segments, tail } = splitTurnWork([u, th, tl])

    expect(segments).toHaveLength(1)
    expect(idsOf(segments[0]!.work)).toEqual(idsOf([th, tl]))
    expect(tail).toEqual([])
  })

  it('a turn consisting of a single service marker (no user, no assistant)', () => {
    const svc = service()
    const { segments, tail } = splitTurnWork([svc])

    expect(segments).toHaveLength(1)
    expect(idsOf(segments[0]!.work)).toEqual(idsOf([svc]))
    expect(tail).toEqual([])
  })

  it('empty input splits into empty segments and tail', () => {
    const { segments, tail } = splitTurnWork([])
    expect(segments).toEqual([])
    expect(tail).toEqual([])
  })

  it('only the last user message anchors the turn: earlier turns belong to neither half', () => {
    const u1 = user('first task')
    const a1 = assistant('first answer')
    const u2 = user('second task')
    const th = thought()
    const fin = assistant('second answer')
    const { segments, tail } = splitTurnWork([u1, a1, u2, th, fin])

    expect(segments).toHaveLength(1)
    expect(idsOf(segments[0]!.work)).toEqual(idsOf([th]))
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
    const { segments, tail } = splitTurnWork([u1, a1, u2, th])

    expect(segments).toHaveLength(1)
    expect(idsOf(segments[0]!.work)).toEqual(idsOf([th]))
    expect(tail).toEqual([])
  })

  it('a turn consisting of a single pinned step (no user, no assistant)', () => {
    const ps = planStep()
    const { segments, tail } = splitTurnWork([ps])

    expect(segRows({ segments, tail })).toEqual([
      { work: [], pinned: [ps.id] },
    ])
    expect(tail).toEqual([])
  })

  it('does not mutate the input and keeps item references', () => {
    const u = user()
    const th = thought()
    const fin = assistant()
    const cl = activeChecklist()
    const input = [u, th, fin, cl]
    const snapshot = [...input]
    const { segments, tail } = splitTurnWork(input)

    expect(input).toEqual(snapshot)
    expect(input).toHaveLength(4)
    expect(tail[0]).toBe(fin)
    expect(segments[0]!.work[0]).toBe(th)
    expect(tail[1]).toBe(cl)
  })

  it('a step cut segment keeps lifting unresolved panels into the tail (both segment shapes)', () => {
    // The step appeared mid-run; after it the model asked a question (panel,
    // still unresolved) and more work landed. The panel must surface in the
    // tail, not inside the collapsed post-step block.
    const u = user()
    const th = thought()
    const ps = planStep()
    const tl = tool()
    const tc = toolConfirm()
    const { segments, tail } = splitTurnWork([u, th, ps, tl, tc])

    expect(segRows({ segments, tail })).toEqual([
      { work: idsOf([th]), pinned: [ps.id] },
      { work: idsOf([tl]), pinned: [] },
    ])
    expect(idsOf(tail)).toEqual(idsOf([tc]))
  })

  it('a launcher card (execute_plan) rides WITH its step blocks, outside the work blocks', () => {
    // "Executing: plan" STARTS the plan hierarchy: it must not stay buried
    // inside the work container whose cut it caused — it belongs to the same
    // outside-the-collapsible region as the steps it opened, ahead of them.
    const u = user('run the plan')
    const th = thought()
    const lp = launcher('launcher-1')
    const ps1 = planStep()
    const tl = tool()
    const ps2 = planStep()
    const fin = assistant('done')
    const { segments, tail } = splitTurnWork([u, th, lp, ps1, tl, ps2, fin])

    expect(segRows({ segments, tail })).toEqual([
      { work: idsOf([th]), pinned: idsOf([lp, ps1]) },
      { work: idsOf([tl]), pinned: [ps2.id] },
    ])
    expect(idsOf(tail)).toEqual(idsOf([fin]))
  })

  it('a delegate card rides with its subagent blocks (same launcher rule)', () => {
    const u = user('review it')
    const dl = launcher('launcher-1', 'delegate')
    const sub = { kind: 'subagent', id: 'sub-1', stepId: 'step-1', title: 'Reviewer', status: 'completed', children: [] } as DisplayItem
    const fin = assistant('done')
    const { segments, tail } = splitTurnWork([u, dl, sub, fin])

    expect(segRows({ segments, tail })).toEqual([
      { work: [], pinned: idsOf([dl, sub]) },
    ])
    expect(idsOf(tail)).toEqual(idsOf([fin]))
  })

  it('a denied launcher still renders in the pinned slot (launch markers live outside work)', () => {
    // The user denied the plan: no step blocks ever followed the launcher
    // card. The card (and a retried attempt) is STILL a launch marker — it
    // renders in the pinned slot (a bare tool row among the step rows), and
    // the work before it settles as "before the launch attempt". An empty
    // work region renders no block (the renderer's mounting guard).
    const u = user('run the plan')
    const th = thought()
    const lp = launcher('launcher-1')
    const denied = { kind: 'tool', id: 'launcher-2', toolName: 'execute_plan', args: '', status: 'error' } as DisplayItem
    const fin = assistant('Plan was rejected.')
    const { segments, tail } = splitTurnWork([u, th, lp, denied, fin])

    expect(segRows({ segments, tail })).toEqual([
      { work: idsOf([th]), pinned: idsOf([lp, denied]) },
    ])
    expect(idsOf(tail)).toEqual(idsOf([fin]))
  })

  it('an execute_plan name is matched exactly: lookalike tools stay in the work', () => {
    const u = user()
    const impostor = { kind: 'tool', id: 'tool-x', toolName: 'execute_plan_v2', args: '', status: 'success' } as DisplayItem
    const fin = assistant('done')
    const { segments, tail } = splitTurnWork([u, impostor, fin])

    // Not a launcher: no cut, no pinned — the impostor is ordinary work.
    expect(segRows({ segments, tail })).toEqual([
      { work: idsOf([impostor]), pinned: [] },
    ])
    expect(idsOf(tail)).toEqual(idsOf([fin]))
  })
})
