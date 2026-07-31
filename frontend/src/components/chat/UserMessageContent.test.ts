import { describe, it, expect } from 'vitest'
import { parseSegments } from './userMessageSegments'

describe('parseSegments', () => {
  it('parses a file ref with #L20-L36 anchor', () => {
    const segs = parseSegments('@desktop/x.go#L20-L36')
    expect(segs).toHaveLength(1)
    const file = segs[0]!
    expect(file.type).toBe('file')
    expect(file.path).toBe('desktop/x.go')
    expect(file.startLine).toBe(20)
  })

  it('parses @x.go#L5-L10', () => {
    const segs = parseSegments('@x.go#L5-L10')
    expect(segs).toHaveLength(1)
    const file = segs[0]!
    expect(file.type).toBe('file')
    expect(file.path).toBe('x.go')
    expect(file.startLine).toBe(5)
  })

  it('parses @x.go#L5-10 (mixed L prefix)', () => {
    const segs = parseSegments('@x.go#L5-10')
    expect(segs).toHaveLength(1)
    const file = segs[0]!
    expect(file.type).toBe('file')
    expect(file.path).toBe('x.go')
    expect(file.startLine).toBe(5)
  })

  it('parses @x.go#L42 (single L-prefixed line)', () => {
    const segs = parseSegments('@x.go#L42')
    expect(segs).toHaveLength(1)
    const file = segs[0]!
    expect(file.type).toBe('file')
    expect(file.path).toBe('x.go')
    expect(file.startLine).toBe(42)
  })

  it('parses @x.go#42 (bare line number)', () => {
    const segs = parseSegments('@x.go#42')
    expect(segs).toHaveLength(1)
    const file = segs[0]!
    expect(file.type).toBe('file')
    expect(file.path).toBe('x.go')
    expect(file.startLine).toBe(42)
  })

  it('parses plain @x.go without an anchor', () => {
    const segs = parseSegments('@x.go')
    expect(segs).toHaveLength(1)
    const file = segs[0]!
    expect(file.type).toBe('file')
    expect(file.path).toBe('x.go')
    expect(file.startLine).toBeUndefined()
  })

  it('parses /skill-name unchanged', () => {
    const segs = parseSegments('/skill-name')
    expect(segs).toHaveLength(1)
    const skill = segs[0]!
    expect(skill.type).toBe('skill')
    expect(skill.content).toBe('skill-name')
  })

  // --- #agent-name segments ---

  it('parses a #agent-name as an agent segment', () => {
    const segs = parseSegments('#code-reviewer')
    expect(segs).toHaveLength(1)
    const agent = segs[0]!
    expect(agent.type).toBe('agent')
    expect(agent.content).toBe('code-reviewer')
  })

  it('parses a #agent-name after leading whitespace', () => {
    const segs = parseSegments('please use #test-writer now')
    expect(segs).toHaveLength(3)
    expect(segs[0]).toMatchObject({ type: 'text', content: 'please use ' })
    expect(segs[1]).toMatchObject({ type: 'agent', content: 'test-writer' })
    expect(segs[2]).toMatchObject({ type: 'text', content: ' now' })
  })

  it('parses a #agent-name at start of text', () => {
    const segs = parseSegments('#reviewer is great')
    expect(segs[0]).toMatchObject({ type: 'agent', content: 'reviewer' })
  })

  it('parses a #agent-name after newline', () => {
    const segs = parseSegments('line one\n#agent-two')
    const agentSeg = segs.find((s) => s.type === 'agent')
    expect(agentSeg).toMatchObject({ type: 'agent', content: 'agent-two' })
  })

  it('parses multiple #agent-name segments', () => {
    const segs = parseSegments('#code-reviewer and #test-writer please')
    const agentSegs = segs.filter((s) => s.type === 'agent')
    expect(agentSegs.map((s) => s.content)).toEqual(['code-reviewer', 'test-writer'])
  })

  it('does NOT capture @file#L20 line anchor as an agent', () => {
    // The '#' in an @file anchor is glued to the path token (no preceding
    // whitespace) and must remain part of the file ref, not an agent segment.
    const segs = parseSegments('see @x.go#L20 here')
    const agentSegs = segs.filter((s) => s.type === 'agent')
    expect(agentSegs).toHaveLength(0)
    const fileSeg = segs.find((s) => s.type === 'file')
    expect(fileSeg?.path).toBe('x.go')
    expect(fileSeg?.startLine).toBe(20)
  })

  it('keeps #review (agent), /review (skill), and @review (file) distinct', () => {
    const segs = parseSegments('#review /review @review')
    expect(segs.map((s) => s.type)).toEqual(['agent', 'text', 'skill', 'text', 'file'])
    expect(segs[0]).toMatchObject({ type: 'agent', content: 'review' })
    expect(segs[2]).toMatchObject({ type: 'skill', content: 'review' })
    expect(segs[4]).toMatchObject({ type: 'file' })
  })
})
