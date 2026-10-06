import { describe, it, expect } from 'vitest'
import { parseSegments } from './userMessageSegments'

describe('parseSegments', () => {
  it.each([
    '/github/cache/config.json', '/agentName/path',
    '/github.json', '/agentName,', '/githubé',
    '/mcp:github/cache', '/mcp: github/cache',
    '/agent:agentName/path', '/agent: agentName/path',
    '/skill:github/path', '/skill: github/path',
    '/mcp:github.json', '/agent: agentName,',
    '/mcp:', '/agent: ', '/skill:  github',
    '/github\v', '\v/github', '/github\u00a0', '\u00a0/github',
    '/github\u2028', '\u2028/github', '/github\u2029', '\u2029/github',
  ])('keeps non-boundary slash tokens as verbatim text: %j', (content) => {
    expect(parseSegments(content)).toEqual([{ type: 'text', content }])
  })

  it.each(['github-extra', 'github_extra', 'github2', 'agentName-extra', 'agentName_extra', 'agentName2'])(
    'displays the complete longer token %s, never a prefix chip', (name) => {
      expect(parseSegments(`/${name}`)).toEqual([{ type: 'ref', content: name, raw: `/${name}` }])
    })

  it.each(['agent', 'skill', 'mcp'] as const)('displays complete qualified %s names', (kind) => {
    for (const separator of ['', ' ', '\t']) {
      const raw = `/${kind}:${separator}github-extra`
      expect(parseSegments(raw)).toEqual([{ type: kind, content: 'github-extra', raw }])
    }
  })

  it.each([' ', '\t', '\n', '\r', '\f'])('uses Go-compatible boundary whitespace %j', (space) => {
    const content = `before${space}/github${space}/agent:agentName${space}after`
    expect(parseSegments(content)).toEqual([
      { type: 'text', content: `before${space}` },
      { type: 'ref', content: 'github', raw: '/github' },
      { type: 'text', content: space },
      { type: 'agent', content: 'agentName', raw: '/agent:agentName' },
      { type: 'text', content: `${space}after` },
    ])
  })

  it('keeps invalid tokens as text while recognizing following refs and files', () => {
    const content = '/mcp: github/cache /agent:agentName/path /github @x.go#L20'
    expect(parseSegments(content)).toEqual([
      { type: 'text', content: '/mcp: github/cache /agent:agentName/path ' },
      { type: 'ref', content: 'github', raw: '/github' },
      { type: 'text', content: ' ' },
      { type: 'file', content: '@x.go#L20', path: 'x.go', startLine: 20 },
    ])
  })
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

  // --- @'quoted path' form (canonical for paths with spaces) ---

  it("parses @'my file.go' with spaces", () => {
    const segs = parseSegments("@'my file.go'")
    expect(segs).toHaveLength(1)
    const file = segs[0]!
    expect(file.type).toBe('file')
    expect(file.path).toBe('my file.go')
    expect(file.startLine).toBeUndefined()
  })

  it("parses @'my file.go' inside prose without splitting on inner spaces", () => {
    const segs = parseSegments("see @'my file.go' here")
    expect(segs).toHaveLength(3)
    expect(segs[0]).toMatchObject({ type: 'text', content: 'see ' })
    expect(segs[1]).toMatchObject({ type: 'file', path: 'my file.go' })
    expect(segs[2]).toMatchObject({ type: 'text', content: ' here' })
  })

  it("parses a line anchor after the closing quote (@'f.go'#L20-L36)", () => {
    const segs = parseSegments("@'my file.go'#L20-L36")
    expect(segs).toHaveLength(1)
    const file = segs[0]!
    expect(file.type).toBe('file')
    expect(file.path).toBe('my file.go')
    expect(file.startLine).toBe(20)
  })

  it("parses a line anchor inside the quotes (@'f.go#L20')", () => {
    const segs = parseSegments("@'my file.go#L20'")
    expect(segs).toHaveLength(1)
    const file = segs[0]!
    expect(file.type).toBe('file')
    expect(file.path).toBe('my file.go')
    expect(file.startLine).toBe(20)
  })

  it('parses the legacy escaped-space form unchanged', () => {
    const segs = parseSegments('@my\\ file.go')
    expect(segs).toHaveLength(1)
    const file = segs[0]!
    expect(file.type).toBe('file')
    expect(file.path).toBe('my file.go')
  })

  it("strips the stray leading quote of an unterminated quoted ref (@'alpha.go)", () => {
    // An unbalanced @'… (open quote, no close) makes the quoted alternative of
    // REF_PATTERN fail, so the bare alternative captures the lone leading
    // quote. It must be stripped so the file chip label shows no stray
    // apostrophe.
    const segs = parseSegments("@'alpha.go")
    expect(segs).toHaveLength(1)
    const file = segs[0]!
    expect(file.type).toBe('file')
    expect(file.path).toBe('alpha.go')
  })

  it("strips the leading quote of an unterminated quoted ref inside prose", () => {
    const segs = parseSegments("open @'my file")
    const file = segs.find((s) => s.type === 'file')
    expect(file).toBeDefined()
    expect(file!.path).toBe('my')
    expect(file!.path!.startsWith("'")).toBe(false)
  })

  it('does NOT capture a #agent mention glued inside a quoted file ref', () => {
    // A quoted path may contain a word that looks like an agent mention; the
    // '#' there has no preceding whitespace and must stay part of the file.
    const segs = parseSegments("@'dir/my #stuff file.go'")
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ type: 'file', path: 'dir/my #stuff file.go' })
  })

  it('parses /skill-name as a plain ref segment (kind resolved at display time)', () => {
    const segs = parseSegments('/skill-name')
    expect(segs).toHaveLength(1)
    const ref = segs[0]!
    expect(ref.type).toBe('ref')
    expect(ref.content).toBe('skill-name')
    expect(ref.raw).toBe('/skill-name')
  })

  it('parses /skill-name inside prose with raw spelling preserved', () => {
    const segs = parseSegments('please use /commit now')
    expect(segs).toHaveLength(3)
    expect(segs[0]).toMatchObject({ type: 'text', content: 'please use ' })
    expect(segs[1]).toMatchObject({ type: 'ref', content: 'commit', raw: '/commit' })
    expect(segs[2]).toMatchObject({ type: 'text', content: ' now' })
  })

  // --- qualified /-refs (issue #110) ---

  it('parses /agent: name (canonical spaced form) as an agent segment', () => {
    const segs = parseSegments('/agent: code-reviewer')
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({
      type: 'agent',
      content: 'code-reviewer',
      raw: '/agent: code-reviewer',
    })
  })

  it('parses the glued qualified form /agent:name', () => {
    const segs = parseSegments('/agent:code-reviewer')
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ type: 'agent', content: 'code-reviewer', raw: '/agent:code-reviewer' })
  })

  it('parses /skill: name as a skill segment', () => {
    const segs = parseSegments('/skill: study-paper')
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ type: 'skill', content: 'study-paper', raw: '/skill: study-paper' })
  })

  it('parses /mcp: name (canonical spaced form) as an mcp segment', () => {
    const segs = parseSegments('/mcp: context7')
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ type: 'mcp', content: 'context7', raw: '/mcp: context7' })
  })

  it('parses the glued qualified form /mcp:context7', () => {
    const segs = parseSegments('/mcp:context7')
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ type: 'mcp', content: 'context7', raw: '/mcp:context7' })
  })

  it('parses /mcp: name inside prose with surrounding text preserved', () => {
    const segs = parseSegments('use tools from /mcp: github now')
    expect(segs).toHaveLength(3)
    expect(segs[0]).toMatchObject({ type: 'text', content: 'use tools from ' })
    expect(segs[1]).toMatchObject({ type: 'mcp', content: 'github' })
    expect(segs[2]).toMatchObject({ type: 'text', content: ' now' })
  })

  it('parses qualified refs after leading whitespace', () => {
    const segs = parseSegments('please use /skill: study-paper now')
    expect(segs).toHaveLength(3)
    expect(segs[1]).toMatchObject({ type: 'skill', content: 'study-paper' })
  })

  it('parses qualified refs after newline', () => {
    const segs = parseSegments('line one\n/agent: agent-two')
    const agentSeg = segs.find((s) => s.type === 'agent')
    expect(agentSeg).toMatchObject({ type: 'agent', content: 'agent-two' })
  })

  it('keeps a plain ref distinct from qualified refs of the same name', () => {
    const segs = parseSegments('/review /agent: review /skill: review')
    expect(segs.map((s) => s.type)).toEqual(['ref', 'text', 'agent', 'text', 'skill'])
    expect(segs[0]).toMatchObject({ type: 'ref', content: 'review' })
    expect(segs[2]).toMatchObject({ type: 'agent', content: 'review' })
    expect(segs[4]).toMatchObject({ type: 'skill', content: 'review' })
  })

  // --- historical #-mentions: plain text, never a segment ---

  it('renders historical #agent-name as plain text (no agent segment)', () => {
    const segs = parseSegments('please use #test-writer now')
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ type: 'text', content: 'please use #test-writer now' })
  })

  it('renders a lone #foo at start of text as plain text', () => {
    const segs = parseSegments('#reviewer is great')
    expect(segs).toHaveLength(1)
    expect(segs[0]).toMatchObject({ type: 'text', content: '#reviewer is great' })
  })

  it('renders multiple historical #mentions as plain text', () => {
    const segs = parseSegments('#code-reviewer and #test-writer please')
    expect(segs).toHaveLength(1)
    expect(segs[0]!.type).toBe('text')
    expect(segs[0]!.content).toContain('#code-reviewer')
    expect(segs[0]!.content).toContain('#test-writer')
  })

  it('does NOT capture @file#L20 line anchor as a slash ref', () => {
    // The '#' in an @file anchor is glued to the path token (no preceding
    // whitespace) and must remain part of the file ref.
    const segs = parseSegments('see @x.go#L20 here')
    expect(segs.filter((s) => s.type === 'ref')).toHaveLength(0)
    const fileSeg = segs.find((s) => s.type === 'file')
    expect(fileSeg?.path).toBe('x.go')
    expect(fileSeg?.startLine).toBe(20)
  })

  it('keeps /review (plain ref) and @review (file) distinct', () => {
    const segs = parseSegments('/review @review')
    expect(segs.map((s) => s.type)).toEqual(['ref', 'text', 'file'])
    expect(segs[0]).toMatchObject({ type: 'ref', content: 'review' })
    expect(segs[2]).toMatchObject({ type: 'file' })
  })
})
