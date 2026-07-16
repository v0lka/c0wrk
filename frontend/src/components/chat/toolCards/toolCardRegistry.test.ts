import { describe, it, expect } from 'vitest'
import { resolveCardConfig } from './toolCardRegistry'
import { Terminal, FilePen, Search, Puzzle, Wrench, Globe, Layers, History, ListTree, Brain, StickyNote, BookOpen } from 'lucide-react'

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

  describe('blackboard / memory operations', () => {
    it('renders read_step_output as a recovered step (not Read: file)', () => {
      const config = resolveCardConfig('read_step_output')
      expect(config.icon).toBe(Layers)
      expect(config.verb).toBe('Recovered')
      expect(config.extractTitle({ step_id: 'step_1' }, '')).toBe('step_1')
      expect(config.extractTitle(undefined, '{}')).toBe('step output')
      expect(config.Body).not.toBeNull()
    })

    it('renders read_final_result as a recovered previous result', () => {
      const config = resolveCardConfig('read_final_result')
      expect(config.icon).toBe(History)
      expect(config.verb).toBe('Recovered')
      expect(config.extractTitle(undefined, '')).toBe('previous result')
      expect(config.Body).not.toBeNull()
    })

    it('maps stale read_evidence to the same result config', () => {
      expect(resolveCardConfig('read_evidence').icon).toBe(History)
    })

    it('renders list_step_outputs as listed available steps (not a directory)', () => {
      const config = resolveCardConfig('list_step_outputs')
      expect(config.icon).toBe(ListTree)
      expect(config.verb).toBe('Listed')
      expect(config.extractTitle(undefined, '')).toBe('available steps')
    })

    it('renders search_facts as recalled memory (not a generic search)', () => {
      const config = resolveCardConfig('search_facts')
      expect(config.icon).toBe(Brain)
      expect(config.verb).toBe('Recalled')
      expect(config.extractTitle({ keywords: ['auth', 'jwt'] }, '')).toBe('auth, jwt')
    })

    it('keeps store_fact on its own stored-fact config', () => {
      const config = resolveCardConfig('store_fact')
      expect(config.icon).toBe(StickyNote)
      expect(config.verb).toBe('Stored')
      expect(config.extractTitle({ keywords: ['api'] }, '')).toBe('fact: api')
    })

    it('renders read_attachment as a compact BookOpen card with no body', () => {
      const config = resolveCardConfig('read_attachment')
      expect(config.icon).toBe(BookOpen)
      expect(config.verb).toBe('Read')
      expect(config.extractTitle({ attachment_id: 'att-42' }, '')).toBe('att-42')
      expect(config.extractTitle(undefined, '{}')).toBe('attachment')
      expect(config.Body).toBeNull()
    })
  })
})
