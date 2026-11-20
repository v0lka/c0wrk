import { describe, expect, it } from 'vitest'

import { parsePlanMarkdown, serializePlanMarkdown } from './planParser'

const OLD_FORMAT = `# Step 1: Write tests

**What**: tests
**Where**: src
**How**: vitest
**Acceptance Criteria**: green
`

const NEW_FORMAT = `# Step 1 (step_1_tests): Write tests

**What**: tests
**Where**: src
**How**: vitest
**Acceptance Criteria**: green

# Step 2 (step_2_impl): Implement

**What**: impl
**Where**: src
**How**: code
**Acceptance Criteria**: works
`

describe('parsePlanMarkdown', () => {
  it('parses the legacy "# Step N: Summary" header format', () => {
    const plan = parsePlanMarkdown(OLD_FORMAT)
    expect(plan.steps).toHaveLength(1)
    expect(plan.steps[0]!.id).toBeUndefined()
    expect(plan.steps[0]!.title).toBe('Step 1: Write tests')
    expect(plan.steps[0]!.what).toBe('tests')
    expect(plan.steps[0]!.acceptanceCriteria).toBe('green')
  })

  it('parses the current "# Step N (id): Summary" format produced by Go SerializePlan', () => {
    const plan = parsePlanMarkdown(NEW_FORMAT)
    expect(plan.steps).toHaveLength(2)
    expect(plan.steps[0]!.id).toBe('step_1_tests')
    expect(plan.steps[0]!.title).toBe('Step 1: Write tests')
    expect(plan.steps[1]!.id).toBe('step_2_impl')
    expect(plan.steps[1]!.title).toBe('Step 2: Implement')
  })

  it('round-trips the new format preserving step ids', () => {
    const plan = parsePlanMarkdown(NEW_FORMAT)
    const md = serializePlanMarkdown(plan)
    expect(md).toContain('# Step 1 (step_1_tests): Write tests')
    expect(md).toContain('# Step 2 (step_2_impl): Implement')
    // Re-parsing the serialized output yields the same structure
    expect(parsePlanMarkdown(md)).toEqual(plan)
  })

  it('round-trips the legacy format without inventing ids', () => {
    const plan = parsePlanMarkdown(OLD_FORMAT)
    const md = serializePlanMarkdown(plan)
    expect(md).toContain('# Step 1: Write tests')
    expect(md).not.toContain('(')
  })

  it('returns no steps for content without plan headers', () => {
    expect(parsePlanMarkdown('just some text').steps).toHaveLength(0)
  })

  it('strips task-list checkboxes from acceptance criteria items', () => {
    const md = [
      '# Step 1: Review',
      '',
      '**What**: review the plan',
      '**Where**: plans/',
      '**How**: read',
      '**Acceptance Criteria**:',
      '- [ ] tests pass',
      '- [x] linter clean',
      '* [X] docs updated',
      '  - [ ] nested item',
      '',
    ].join('\n')
    const plan = parsePlanMarkdown(md)
    expect(plan.steps[0]!.acceptanceCriteria).toBe(
      ['- tests pass', '- linter clean', '* docs updated', '- nested item'].join('\n')
    )
  })

  it('demotes checkboxed ordered criteria to plain bullets', () => {
    const md = [
      '# Step 1: Review',
      '',
      '**Acceptance Criteria**:',
      '1. [ ] first criterion',
      '',
    ].join('\n')
    const plan = parsePlanMarkdown(md)
    expect(plan.steps[0]!.acceptanceCriteria).toBe('- first criterion')
  })

  it('round-trip of a legacy checkbox plan drops the checkboxes', () => {
    const md = [
      '# Step 1 (step_1): Legacy',
      '',
      '**What**: x',
      '**Where**: y',
      '**How**: z',
      '**Acceptance Criteria**:',
      '- [ ] criterion a',
      '- [x] criterion b',
      '',
    ].join('\n')
    const md2 = serializePlanMarkdown(parsePlanMarkdown(md))
    expect(md2).not.toContain('[ ]')
    expect(md2).not.toContain('[x]')
    expect(md2).toContain('- criterion a')
    expect(md2).toContain('- criterion b')
    expect(parsePlanMarkdown(md2)).toEqual(parsePlanMarkdown(md))
  })
})
