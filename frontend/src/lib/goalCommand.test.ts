import { describe, expect, it } from 'vitest'
import { detectGoalCommand, goalCommandShadowedByMention } from './goalCommand'

// Parity table with core/message_preprocess.go TestDetectAndStripGoalMode:
// every form the backend accepts/rejects must produce the same activation
// decision here (the optimistic Goal badge rides on it).
describe('detectGoalCommand', () => {
  const cases: Array<{
    name: string
    text: string
    activates: boolean
    budgetTurns: number | null
  }> = [
    { name: 'bare command with message', text: '/goal make tests pass', activates: true, budgetTurns: null },
    { name: 'bare command at end', text: '/goal', activates: true, budgetTurns: null },
    { name: 'budgeted command', text: '/goal:5 ship the release', activates: true, budgetTurns: 5 },
    { name: 'budget without message', text: '/goal:3', activates: true, budgetTurns: 3 },
    { name: 'budget leading zeros', text: '/goal:007 x', activates: true, budgetTurns: 7 },
    { name: 'budget tab boundary', text: '/goal:5\tx', activates: true, budgetTurns: 5 },
    // Invalid budget forms are inert, mirroring the backend.
    { name: 'zero budget', text: '/goal:0 ship it', activates: false, budgetTurns: null },
    { name: 'colon without number', text: '/goal: ship it', activates: false, budgetTurns: null },
    { name: 'non-numeric budget', text: '/goal:abc ship it', activates: false, budgetTurns: null },
    { name: 'space after colon', text: '/goal: 5 ship it', activates: false, budgetTurns: null },
    { name: 'budget beyond int64', text: '/goal:99999999999999999999 x', activates: false, budgetTurns: null },
    { name: 'budget glued to word', text: '/goal:12more', activates: false, budgetTurns: null },
    { name: 'no goal prefix', text: 'make tests pass', activates: false, budgetTurns: null },
    { name: 'goals-report does not match', text: '/goals-report run', activates: false, budgetTurns: null },
    { name: 'goal5 does not match', text: '/goal5 x', activates: false, budgetTurns: null },
    { name: 'mid-text prefix not matched', text: 'do /goal:5 now', activates: false, budgetTurns: null },
  ]

  for (const tc of cases) {
    it(tc.name, () => {
      expect(detectGoalCommand(tc.text)).toEqual({
        activates: tc.activates,
        budgetTurns: tc.budgetTurns,
      })
    })
  }
})

// The optimistic mirror's shadowing suppression (ADR-106): when the leading
// plain /goal token was partitioned as a mention ("goal" rides a threaded
// array), the backend strip consumes it, no goal arms, and the optimistic
// stamp must stay off. The budgeted /goal:N form is never a mention token,
// so its activation is always honest.
describe('goalCommandShadowedByMention', () => {
  it('true when the leading plain token threads goal as any mention kind', () => {
    expect(goalCommandShadowedByMention('/goal ship it', ['goal'], [], [])).toBe(true)
    expect(goalCommandShadowedByMention('/goal ship it', [], ['goal'], [])).toBe(true)
    expect(goalCommandShadowedByMention('/goal ship it', [], [], ['goal'])).toBe(true)
  })

  it('false when nothing threads goal (the command rides the text)', () => {
    expect(goalCommandShadowedByMention('/goal ship it', [], [], [])).toBe(false)
    expect(goalCommandShadowedByMention('/goal ship it', ['other'], [], [])).toBe(false)
  })

  it('false when the leading token is the budgeted form — never a mention token', () => {
    // Even a qualified /skill: goal elsewhere in the text cannot strip the
    // /goal:5 token, so the optimistic activation stays honest.
    expect(goalCommandShadowedByMention('/goal:5 ship it /skill: goal', ['goal'], [], [])).toBe(false)
  })

  it('false without a leading plain goal token', () => {
    expect(goalCommandShadowedByMention('ship it /goal later', ['goal'], [], [])).toBe(false)
    expect(goalCommandShadowedByMention('/goals-report run', ['goal'], [], [])).toBe(false)
  })

  it('false for inert :N spellings (the token is not the plain form)', () => {
    expect(goalCommandShadowedByMention('/goal:0 ship it', ['goal'], [], [])).toBe(false)
  })

  it('undefined arrays read as empty', () => {
    expect(goalCommandShadowedByMention('/goal ship it')).toBe(false)
  })
})
