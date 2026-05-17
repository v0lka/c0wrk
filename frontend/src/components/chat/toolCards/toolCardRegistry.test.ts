import { describe, it, expect } from 'vitest'
import { resolveCardConfig } from './toolCardRegistry'
import { Terminal, FilePen, Search, Puzzle, Wrench, Globe } from 'lucide-react'

describe('resolveCardConfig', () => {
  it('returns exec config for bash_exec', () => {
    const config = resolveCardConfig('bash_exec')
    expect(config.icon).toBe(Terminal)
    expect(config.verb).toBe('Executed')
    expect(config.Body).not.toBeNull()
  })

  it('returns file change config for write_file and edit_file', () => {
    const w = resolveCardConfig('write_file')
    const e = resolveCardConfig('edit_file')
    expect(w.icon).toBe(FilePen)
    expect(e.icon).toBe(FilePen)
    expect(w.verb).toBe('Changed')
  })

  it('returns search config for glob, ripgrep, web_search', () => {
    expect(resolveCardConfig('glob').icon).toBe(Search)
    expect(resolveCardConfig('ripgrep').icon).toBe(Search)
    expect(resolveCardConfig('web_search').icon).toBe(Search)
    expect(resolveCardConfig('semantic_search').verb).toBe('Searched')
  })

  it('returns web fetch config', () => {
    const config = resolveCardConfig('web_fetch')
    expect(config.icon).toBe(Globe)
    expect(config.verb).toBe('Fetched')
  })

  it('returns MCP config for non-core source', () => {
    const config = resolveCardConfig('my_custom_tool', 'my-mcp-server')
    expect(config.icon).toBe(Puzzle)
    expect(config.verb).toBe('Called')
    expect(config.extractTitle(undefined, '')).toBe('my_custom_tool')
  })

  it('ignores source=core and looks up by name', () => {
    const config = resolveCardConfig('bash_exec', 'core')
    expect(config.icon).toBe(Terminal)
  })

  it('returns fallback for unknown tools', () => {
    const config = resolveCardConfig('unknown_tool_xyz')
    expect(config.icon).toBe(Wrench)
    expect(config.verb).toBe('Used')
    expect(config.extractTitle(undefined, '')).toBe('unknown_tool_xyz')
  })
})
