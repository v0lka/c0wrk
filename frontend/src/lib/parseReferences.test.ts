import { describe, it, expect } from 'vitest'
import { extractRefs, partitionRefs, formatFileRefPath } from './parseReferences'
import { fuzzyMatch, fuzzyFilter } from './fuzzyMatch'

describe('extractRefs', () => {
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
  ])('never extracts a truncated ref from %j', (text) => {
    expect(extractRefs(text)).toEqual([])
    expect(partitionRefs(extractRefs(text), ['agentName', 'agent'], ['skill'], ['github', 'mcp']))
      .toEqual({ agents: [], skills: [], mcpServers: [], ambiguous: [] })
  })

  it.each(['github-extra', 'github_extra', 'github2', 'agentName-extra', 'agentName_extra', 'agentName2'])(
    'keeps the entire longer token %s without activating its registered prefix', (name) => {
      const refs = extractRefs(`/${name}`)
      expect(refs).toEqual([{ kind: 'skill', qualified: false, name }])
      const result = partitionRefs(refs, ['agentName'], [], ['github'])
      expect(result.agents).toEqual([])
      expect(result.mcpServers).toEqual([])
      expect(result.skills).toEqual([name])
    })

  it.each(['agent', 'skill', 'mcp'] as const)('keeps qualified %s names whole', (kind) => {
    for (const separator of ['', ' ', '\t']) {
      expect(extractRefs(`/${kind}:${separator}github-extra`)).toEqual([
        { kind, qualified: true, name: 'github-extra' },
      ])
    }
  })

  it.each([' ', '\t', '\n', '\r', '\f'])('uses Go-compatible boundary whitespace %j', (space) => {
    expect(extractRefs(`before${space}/github${space}/agent:agentName${space}after`)).toEqual([
      { kind: 'skill', qualified: false, name: 'github' },
      { kind: 'agent', qualified: true, name: 'agentName' },
    ])
  })

  it('finds valid mentions after invalid path and qualified tokens without backtracking', () => {
    expect(extractRefs('/mcp: github/cache /agent:agentName/path /github /agentName')).toEqual([
      { kind: 'skill', qualified: false, name: 'github' },
      { kind: 'skill', qualified: false, name: 'agentName' },
    ])
  })

  it('extracts a single plain ref', () => {
    expect(extractRefs('Fix using /commit approach')).toEqual([
      { kind: 'skill', qualified: false, name: 'commit' },
    ])
  })

  it('extracts multiple plain refs preserving order', () => {
    expect(extractRefs('/go-error-handling and /go-concurrency')).toEqual([
      { kind: 'skill', qualified: false, name: 'go-error-handling' },
      { kind: 'skill', qualified: false, name: 'go-concurrency' },
    ])
  })

  it('deduplicates identical plain refs', () => {
    expect(extractRefs('/commit and again /commit')).toEqual([
      { kind: 'skill', qualified: false, name: 'commit' },
    ])
  })

  it('does not match mid-word slashes', () => {
    expect(extractRefs('http://example.com')).toEqual([])
  })

  it('matches at start of text and after newline', () => {
    expect(extractRefs('/my-ref is great')).toEqual([{ kind: 'skill', qualified: false, name: 'my-ref' }])
    expect(extractRefs('line one\n/skill-two')).toEqual([{ kind: 'skill', qualified: false, name: 'skill-two' }])
  })

  it('extracts qualified refs in the canonical spaced form', () => {
    expect(extractRefs('/agent: code-reviewer')).toEqual([
      { kind: 'agent', qualified: true, name: 'code-reviewer' },
    ])
    expect(extractRefs('/skill: study-paper')).toEqual([
      { kind: 'skill', qualified: true, name: 'study-paper' },
    ])
  })

  it('extracts the mcp qualified form (spaced and glued)', () => {
    expect(extractRefs('/mcp: context7')).toEqual([
      { kind: 'mcp', qualified: true, name: 'context7' },
    ])
    expect(extractRefs('/mcp:context7')).toEqual([
      { kind: 'mcp', qualified: true, name: 'context7' },
    ])
  })

  it('extracts the mcp qualified form inside prose', () => {
    expect(extractRefs('use tools from /mcp: github now')).toEqual([
      { kind: 'mcp', qualified: true, name: 'github' },
    ])
  })

  it('keeps a plain mcp-server mention as a plain ref (kind resolved later)', () => {
    expect(extractRefs('/context7 look up repos')).toEqual([
      { kind: 'skill', qualified: false, name: 'context7' },
    ])
  })

  it('does not treat a plain name of mcp specially without a colon', () => {
    // `/mcp` is a plain ref named "mcp" — not the qualified prefix.
    expect(extractRefs('/mcp')).toEqual([{ kind: 'skill', qualified: false, name: 'mcp' }])
  })

  it('accepts the glued qualified form (no space after the colon)', () => {
    expect(extractRefs('/agent:code-reviewer')).toEqual([
      { kind: 'agent', qualified: true, name: 'code-reviewer' },
    ])
    expect(extractRefs('/skill:study-paper')).toEqual([
      { kind: 'skill', qualified: true, name: 'study-paper' },
    ])
  })

  it('extracts qualified refs inside prose', () => {
    expect(extractRefs('please run /agent: code-reviewer now')).toEqual([
      { kind: 'agent', qualified: true, name: 'code-reviewer' },
    ])
  })

  it('keeps a plain ref and a qualified ref of the same name as distinct refs', () => {
    expect(extractRefs('/review /agent: review /skill: review')).toEqual([
      { kind: 'skill', qualified: false, name: 'review' },
      { kind: 'agent', qualified: true, name: 'review' },
      { kind: 'skill', qualified: true, name: 'review' },
    ])
  })

  it('never extracts #-mentions (historical syntax is plain text)', () => {
    expect(extractRefs('see #code-reviewer and #42 here')).toEqual([])
  })

  it('does not match the # of an @file line anchor', () => {
    expect(extractRefs('see @x.go#L20 here')).toEqual([])
  })

  it('returns empty array for no refs', () => {
    expect(extractRefs('just plain text')).toEqual([])
  })

  it('does not treat a plain name of agent/skill specially without a colon', () => {
    // `/agent` is a plain ref named "agent" — not the qualified prefix.
    expect(extractRefs('/agent')).toEqual([{ kind: 'skill', qualified: false, name: 'agent' }])
  })
})

describe('partitionRefs', () => {
  it('partitions plain refs by catalog membership', () => {
    const refs = extractRefs('/code-reviewer and /commit')
    const result = partitionRefs(refs, ['code-reviewer'], ['commit'])
    expect(result.agents).toEqual(['code-reviewer'])
    expect(result.skills).toEqual(['commit'])
    expect(result.ambiguous).toEqual([])
  })

  it('threads qualified refs verbatim without a catalog check', () => {
    const refs = extractRefs('/agent: typo-name /skill: unknown-skill')
    const result = partitionRefs(refs, [], [])
    expect(result.agents).toEqual(['typo-name'])
    expect(result.skills).toEqual(['unknown-skill'])
    expect(result.ambiguous).toEqual([])
  })

  it('reports a plain ref present in BOTH catalogs as ambiguous (never guessed)', () => {
    const refs = extractRefs('/review')
    const result = partitionRefs(refs, ['review'], ['review'])
    expect(result.agents).toEqual([])
    expect(result.skills).toEqual([])
    expect(result.ambiguous).toEqual(['review'])
  })

  it('is exactly case-sensitive: Review and review are different names', () => {
    const refs = extractRefs('/Review')
    const result = partitionRefs(refs, ['review'], ['review'])
    // /Review matches neither catalog (case-sensitive) → permissive skill.
    expect(result.agents).toEqual([])
    expect(result.skills).toEqual(['Review'])
    expect(result.ambiguous).toEqual([])
  })

  it('degrades a plain ref in neither catalog to a skill (permissive passthrough)', () => {
    const refs = extractRefs('/commit')
    const result = partitionRefs(refs, [], [])
    expect(result.skills).toEqual(['commit'])
    expect(result.agents).toEqual([])
  })

  it('deduplicates a name reached via both a plain and a qualified spelling', () => {
    const refs = extractRefs('/commit /skill: commit')
    const result = partitionRefs(refs, [], ['commit'])
    expect(result.skills).toEqual(['commit'])
  })

  it('deduplicates ambiguous names and preserves order', () => {
    const refs = extractRefs('/a /b /a')
    const result = partitionRefs(refs, ['a', 'b'], ['a', 'b'])
    expect(result.ambiguous).toEqual(['a', 'b'])
  })

  it('mixed: qualified, plain agent, plain skill, ambiguous together', () => {
    const refs = extractRefs('/agent: explicit /reviewer /deploy /dup')
    const result = partitionRefs(refs, ['reviewer', 'dup'], ['deploy', 'dup'])
    expect(result.agents).toEqual(['explicit', 'reviewer'])
    expect(result.skills).toEqual(['deploy'])
    expect(result.mcpServers).toEqual([])
    expect(result.ambiguous).toEqual(['dup'])
  })
})

describe('partitionRefs — mcp catalog', () => {
  it('partitions a plain ref present only in the mcp catalog as an mcp mention', () => {
    const refs = extractRefs('/context7 find docs')
    const result = partitionRefs(refs, [], [], ['context7'])
    expect(result.mcpServers).toEqual(['context7'])
    expect(result.agents).toEqual([])
    expect(result.skills).toEqual([])
    expect(result.ambiguous).toEqual([])
  })

  it('threads /mcp: qualified refs verbatim without a catalog check', () => {
    const refs = extractRefs('/mcp: context7 and /mcp:glued-name')
    const result = partitionRefs(refs, [], [], [])
    expect(result.mcpServers).toEqual(['context7', 'glued-name'])
    expect(result.skills).toEqual([])
    expect(result.agents).toEqual([])
  })

  it('reports a plain ref colliding with the mcp catalog as ambiguous (never guessed)', () => {
    // mcp × skill collision…
    const skillCollision = partitionRefs(extractRefs('/deploy'), [], ['deploy'], ['deploy'])
    expect(skillCollision.ambiguous).toEqual(['deploy'])
    expect(skillCollision.mcpServers).toEqual([])
    expect(skillCollision.skills).toEqual([])
    // …and an mcp × agent collision.
    const agentCollision = partitionRefs(extractRefs('/deploy'), ['deploy'], [], ['deploy'])
    expect(agentCollision.ambiguous).toEqual(['deploy'])
  })

  it('treats a triple-catalog collision as ambiguous too', () => {
    const result = partitionRefs(extractRefs('/review'), ['review'], ['review'], ['review'])
    expect(result.ambiguous).toEqual(['review'])
  })

  it('a name unknown to every catalog still degrades to the permissive skill path', () => {
    const result = partitionRefs(extractRefs('/mystery'), [], [], ['context7'])
    expect(result.skills).toEqual(['mystery'])
    expect(result.mcpServers).toEqual([])
  })

  it('deduplicates an mcp name reached via both a plain and a qualified spelling', () => {
    const result = partitionRefs(extractRefs('/context7 /mcp: context7'), [], [], ['context7'])
    expect(result.mcpServers).toEqual(['context7'])
  })

  it('omitting the mcp catalog (legacy 3-arg call) keeps the historical behavior', () => {
    const result = partitionRefs(extractRefs('/context7'), [], ['context7'])
    expect(result.skills).toEqual(['context7'])
    expect(result.mcpServers).toEqual([])
  })
})

describe('formatFileRefPath', () => {
  it('leaves a path without spaces unescaped and unquoted', () => {
    expect(formatFileRefPath('src/x.go')).toBe('src/x.go')
  })

  it('quotes a path containing spaces', () => {
    expect(formatFileRefPath('docs/my file.md')).toBe("'docs/my file.md'")
  })

  it('always quotes when forced (completion inside an open @\'…\' ref)', () => {
    expect(formatFileRefPath('alpha.txt', true)).toBe("'alpha.txt'")
  })

  it('falls back to backslash-escaped spaces when the path contains a single quote', () => {
    expect(formatFileRefPath("it's a file.txt")).toBe("it's\\ a\\ file.txt")
    expect(formatFileRefPath("it's a file.txt", true)).toBe("it's\\ a\\ file.txt")
  })
})

describe('fuzzyMatch', () => {
  it('matches exact substring', () => {
    const result = fuzzyMatch('chat', 'chat.ts')
    expect(result.match).toBe(true)
    expect(result.score).toBeGreaterThan(0)
  })

  it('matches subsequence', () => {
    const result = fuzzyMatch('cht', 'chat.ts')
    expect(result.match).toBe(true)
  })

  it('does not match impossible subsequence', () => {
    const result = fuzzyMatch('xyz', 'chat.ts')
    expect(result.match).toBe(false)
  })

  it('empty query matches everything', () => {
    const result = fuzzyMatch('', 'anything')
    expect(result.match).toBe(true)
  })

  it('scores word boundary matches higher', () => {
    const boundary = fuzzyMatch('c', 'src/components/chat.ts')
    const mid = fuzzyMatch('o', 'src/components/chat.ts')
    // 'c' at component boundary should score higher
    expect(boundary.score).toBeGreaterThanOrEqual(mid.score)
  })

  it('scores consecutive matches higher', () => {
    const consecutive = fuzzyMatch('cha', 'chat.ts')
    const spread = fuzzyMatch('c_a', 'c_hat_a.ts')
    expect(consecutive.score).toBeGreaterThan(spread.score)
  })
})

describe('fuzzyFilter', () => {
  const items = [
    { path: 'src/api/chat.ts' },
    { path: 'src/api/sessions.ts' },
    { path: 'src/components/chat/ChatInput.tsx' },
    { path: 'src/lib/utils.ts' },
  ]

  it('filters by subsequence', () => {
    const result = fuzzyFilter('chat', items, (i) => i.path)
    expect(result.length).toBe(2)
    expect(result.map((r) => r.path)).toContain('src/api/chat.ts')
    expect(result.map((r) => r.path)).toContain('src/components/chat/ChatInput.tsx')
  })

  it('returns all items for empty query (up to limit)', () => {
    const result = fuzzyFilter('', items, (i) => i.path, 2)
    expect(result.length).toBe(2)
  })

  it('respects limit', () => {
    const result = fuzzyFilter('s', items, (i) => i.path, 2)
    expect(result.length).toBeLessThanOrEqual(2)
  })

  it('returns empty for no matches', () => {
    const result = fuzzyFilter('zzz', items, (i) => i.path)
    expect(result.length).toBe(0)
  })
})
