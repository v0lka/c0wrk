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

  it('strips inline checkboxes carried after a field label', () => {
    const md = [
      '# Step 1: Review',
      '',
      '**Acceptance Criteria**: - [ ] tests pass',
      '**What**: [x] done first',
      '',
    ].join('\n')
    const plan = parsePlanMarkdown(md)
    expect(plan.steps[0]!.acceptanceCriteria).toBe('- tests pass')
    expect(plan.steps[0]!.what).toBe('done first')
  })

  it('strips doubled bare checkboxes after a label to a fixed point', () => {
    const md = [
      '# Step 1: Review',
      '',
      '**Acceptance Criteria**: [ ] [x] tests pass',
      '**What**: [X] [ ] done first',
      '',
    ].join('\n')
    const plan = parsePlanMarkdown(md)
    expect(plan.steps[0]!.acceptanceCriteria).toBe('tests pass')
    expect(plan.steps[0]!.what).toBe('done first')
  })

  it('strips a checkbox after a doubled colon separator like the Go normalizer', () => {
    // planBoldLabelContent consumes exactly one leading `:` or `-`, so the
    // doubled spelling `**AC**:: - [ ] x` reaches the Go label-content path
    // as `: - [ ] x`; stripSeparatorsToCheckbox drops the residue and strips
    // (core/plan_description.go). The TS rest-strip (`^[*: ]+`) already
    // exposed the box — pin it so both scanners stay aligned.
    const md = [
      '# Step 1: Doubled colon',
      '',
      '**What**: inspect',
      '**Where**: plans/',
      '**How**: read',
      '**Acceptance Criteria**:: - [ ] tests pass',
      '',
    ].join('\n')
    const plan = parsePlanMarkdown(md)
    expect(plan.steps[0]!.acceptanceCriteria).toBe('- tests pass')
    const md2 = serializePlanMarkdown(plan)
    expect(md2).not.toContain('[ ]')
    expect(md2).toContain('**Acceptance Criteria**: - tests pass')
    expect(parsePlanMarkdown(md2)).toEqual(plan)
  })

  it('strips a bare checkbox after a doubled colon separator like the Go normalizer', () => {
    const md = [
      '# Step 1: Doubled colon bare',
      '',
      '**What**: inspect',
      '**Where**: plans/',
      '**How**: read',
      '**Acceptance Criteria**:: [ ] tests pass',
      '',
    ].join('\n')
    const plan = parsePlanMarkdown(md)
    expect(plan.steps[0]!.acceptanceCriteria).toBe('tests pass')
    const md2 = serializePlanMarkdown(plan)
    expect(md2).not.toContain('[ ]')
    expect(md2).toContain('**Acceptance Criteria**: tests pass')
    expect(parsePlanMarkdown(md2)).toEqual(plan)
  })

  it('strips doubled boxes after a tripled colon separator to a fixed point', () => {
    const md = [
      '# Step 1: Tripled colon',
      '',
      '**What**: inspect',
      '**Where**: plans/',
      '**How**: read',
      '**Acceptance Criteria**::: - [ ] [x] tests pass',
      '',
    ].join('\n')
    const plan = parsePlanMarkdown(md)
    expect(plan.steps[0]!.acceptanceCriteria).toBe('- tests pass')
    const md2 = serializePlanMarkdown(plan)
    expect(md2).not.toContain('[ ]')
    expect(md2).not.toContain('[x]')
    expect(md2).toContain('**Acceptance Criteria**: - tests pass')
    expect(parsePlanMarkdown(md2)).toEqual(plan)
  })

  it('strips a checkbox preceding a separator run to a fixed point', () => {
    const md = [
      '# Step 1: Box before separators',
      '',
      '**What**: inspect',
      '**Where**: plans/',
      '**How**: read',
      '**Acceptance Criteria**: [ ] :: - [ ] tests pass',
      '',
    ].join('\n')
    const plan = parsePlanMarkdown(md)
    expect(plan.steps[0]!.acceptanceCriteria).toBe('- tests pass')
    const md2 = serializePlanMarkdown(plan)
    expect(md2).not.toContain('[ ]')
    expect(parsePlanMarkdown(md2)).toEqual(plan)
  })

  it('arms an inline fence whose opener is hidden behind an inline checkbox', () => {
    const md = [
      '# Step 1: Hidden opener',
      '',
      '**What**: inspect',
      '**Where**: plans/',
      '**How**: [ ] ```md',
      '- [ ] not an AC item',
      '**What**: also not a header',
      '```',
      '**Acceptance Criteria**: - [ ] real criterion',
      '',
    ].join('\n')
    const first = parsePlanMarkdown(md)
    // Go's publish pass strips the inline checkbox off the label content
    // (`**How**: [ ] ```md` → `**How**: ```md`) and arms from the rewritten
    // line; the mirror must arm the same way on the raw line or the body is
    // re-parsed as prose and the standalone ``` opens a phantom fence that
    // swallows the following fields.
    expect(first.steps[0]!.how).toBe(
      ['```md', '- [ ] not an AC item', '**What**: also not a header', '```'].join('\n')
    )
    expect(first.steps[0]!.acceptanceCriteria).toBe('- real criterion')
    expect(parsePlanMarkdown(serializePlanMarkdown(first))).toEqual(first)
  })

  it('arms an inline fence hidden behind a separator run and an inline checkbox', () => {
    const md = [
      '# Step 1: Separators then hidden opener',
      '',
      '**What**: inspect',
      '**Where**: plans/',
      '**How**: :: [ ] ```md',
      '- [ ] not an AC item',
      '**What**: also not a header',
      '```',
      '**Acceptance Criteria**: - [ ] real criterion',
      '',
    ].join('\n')
    const first = parsePlanMarkdown(md)
    // The gated separator strip inside stripInlineCheckboxes exposes the
    // checkbox-hidden opener in fenceCheck the same way Go's rewritten line
    // arms (stripSeparatorsToCheckbox in core/plan_description.go); without
    // it the fenceCheck keeps the box, never arms, and the phantom fence
    // hijacks Acceptance Criteria into How.
    expect(first.steps[0]!.how).toBe(
      ['```md', '- [ ] not an AC item', '**What**: also not a header', '```'].join('\n')
    )
    expect(first.steps[0]!.acceptanceCriteria).toBe('- real criterion')
    expect(parsePlanMarkdown(serializePlanMarkdown(first))).toEqual(first)
  })

  it('collapses extra whitespace after the checkbox like the Go normalizer', () => {
    const md = [
      '# Step 1: Review',
      '',
      '**Acceptance Criteria**:',
      '- [ ]   roomy item',
      '',
    ].join('\n')
    const plan = parsePlanMarkdown(md)
    expect(plan.steps[0]!.acceptanceCriteria).toBe('- roomy item')
  })

  it('leaves checkboxes and headers inside code fences verbatim', () => {
    const md = [
      '# Step 1: Fenced',
      '',
      '**How**:',
      '```md',
      '- [ ] not an AC item',
      '**What**: also not a header',
      '```',
      '**Acceptance Criteria**:',
      '- [ ] real criterion',
      '',
    ].join('\n')
    const plan = parsePlanMarkdown(md)
    expect(plan.steps[0]!.what).toBe('')
    expect(plan.steps[0]!.how).toBe(
      ['```md', '- [ ] not an AC item', '**What**: also not a header', '```'].join('\n')
    )
    expect(plan.steps[0]!.acceptanceCriteria).toBe('- real criterion')
  })

  it('keeps fence bodies byte-verbatim across the round-trip (blanks, indentation)', () => {
    const md = [
      '# Step 1 (step_1): Fenced blanks',
      '',
      '**What**: x',
      '**Where**: y',
      '**How**: ```sh',
      '  set -e',
      '',
      '    make all',
      '```',
      '**Acceptance Criteria**: - ok',
      '',
    ].join('\n')
    const verbatim = ['```sh', '  set -e', '', '    make all', '```'].join('\n')
    const first = parsePlanMarkdown(md)
    expect(first.steps[0]!.how).toBe(verbatim)
    const second = parsePlanMarkdown(serializePlanMarkdown(first))
    expect(second).toEqual(first)
    expect(second.steps[0]!.how).toBe(verbatim)
  })

  it('round-trip of a legacy inline-checkbox first criterion drops the checkbox', () => {
    const md = [
      '# Step 1 (step_1): Legacy inline',
      '',
      '**What**: x',
      '**Where**: y',
      '**How**: z',
      '**Acceptance Criteria**: - [ ] criterion a',
      '- [x] criterion b',
      '',
    ].join('\n')
    const md2 = serializePlanMarkdown(parsePlanMarkdown(md))
    expect(md2).not.toContain('[ ]')
    expect(md2).not.toContain('[x]')
    expect(md2).toContain('**Acceptance Criteria**: - criterion a')
    expect(parsePlanMarkdown(md2)).toEqual(parsePlanMarkdown(md))
  })

  it('round-trips a field whose serialized value opens a fence inline', () => {
    const md = [
      '# Step 1 (step_1): Fenced',
      '',
      '**What**: inspect the template',
      '**Where**: plans/',
      '**How**:',
      '```md',
      '- [ ] not an AC item',
      '**What**: also not a header',
      '```',
      '**Acceptance Criteria**: - [ ] real criterion',
      '',
    ].join('\n')
    const first = parsePlanMarkdown(md)
    // serializePlanMarkdown collapses the multi-line How value onto the
    // label line (`**How**: ```md …`), so the fence OPENS inside a header
    // line; re-parsing must arm the fence from the inline content and keep
    // the body verbatim instead of stripping checkboxes or hijacking fields.
    const second = parsePlanMarkdown(serializePlanMarkdown(first))
    expect(second).toEqual(first)
    expect(second.steps[0]!.how).toBe(
      ['```md', '- [ ] not an AC item', '**What**: also not a header', '```'].join('\n')
    )
    expect(second.steps[0]!.acceptanceCriteria).toBe('- real criterion')
  })

  it('arms an inline fence after a dash-separated label like the Go scanner', () => {
    const md = [
      '# Step 1: Dash label',
      '',
      '**What**: inspect',
      '**Where**: plans/',
      '**How** - ```sh',
      '# not a header',
      '- [ ] not an AC item',
      '```',
      '**Acceptance Criteria**: - [ ] real criterion',
      '',
    ].join('\n')
    const first = parsePlanMarkdown(md)
    // Go's planBoldLabelContent consumes the bare `-` separator of the
    // canonical dash spelling, so planInlineFenceContent arms from ` ```sh`;
    // the emitted inline content keeps the dash (`**How** - ```sh` passes
    // the rewrite untouched). Without the mirror the body is re-parsed as
    // prose and a checkbox line inside the fence gets stripped.
    expect(first.steps[0]!.how).toBe(
      ['- ```sh', '# not a header', '- [ ] not an AC item', '```'].join('\n')
    )
    expect(first.steps[0]!.what).toBe('inspect')
    expect(first.steps[0]!.acceptanceCriteria).toBe('- real criterion')
    expect(parsePlanMarkdown(serializePlanMarkdown(first))).toEqual(first)
  })

  it('arms an inline fence from a colon+dash label line like the Go scanner', () => {
    // serializePlanMarkdown collapses a How value that itself starts with
    // `- ```sh` onto the label as `**How**: - ```sh`; Go's planInlineFenceContent
    // exposes the opener past the surviving dash (TrimLeft of the separator
    // run after the consumed `:`), and the parser must arm the same way —
    // otherwise the body is re-parsed as prose (checkbox stripped, a `**What**:`
    // line hijacks the field) and the standalone ``` opens a phantom fence.
    const md = [
      '# Step 1: Colon dash',
      '',
      '**What**: inspect',
      '**Where**: plans/',
      '**How**: - ```sh',
      '# not a header',
      '- [ ] not an AC item',
      '```',
      '**Acceptance Criteria**: - [ ] real criterion',
      '',
    ].join('\n')
    const first = parsePlanMarkdown(md)
    expect(first.steps[0]!.how).toBe(
      ['- ```sh', '# not a header', '- [ ] not an AC item', '```'].join('\n')
    )
    expect(first.steps[0]!.what).toBe('inspect')
    expect(first.steps[0]!.acceptanceCriteria).toBe('- real criterion')
    expect(parsePlanMarkdown(serializePlanMarkdown(first))).toEqual(first)
  })

  it('strips CRLF line endings like the Go normalizer so CRLF files parse', () => {
    const md = [
      '# Step 1: CRLF',
      '',
      '**What**: x',
      '**Where**: y',
      '**How**: ```sh',
      'code',
      '```',
      '**Acceptance Criteria**: done',
      '',
    ].join('\r\n')
    const plan = parsePlanMarkdown(md)
    // Without the per-line `\r` strip the closing ``` would carry a `\r`
    // that fails the pure-marker-run check: the fence would never close and
    // swallow the Acceptance Criteria field into How.
    expect(plan.steps[0]!.how).toBe(['```sh', 'code', '```'].join('\n'))
    expect(plan.steps[0]!.what).toBe('x')
    expect(plan.steps[0]!.where).toBe('y')
    expect(plan.steps[0]!.acceptanceCriteria).toBe('done')
  })
})
