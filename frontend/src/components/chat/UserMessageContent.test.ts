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
})
