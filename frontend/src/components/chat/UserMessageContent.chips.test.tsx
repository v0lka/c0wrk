// Chip rendering for /-refs (issue #110), component-level: qualified refs
// render verbatim in their kind's color; a plain /name resolves its kind from
// the catalogs at display time (neutral until they load); historical #foo
// renders as plain text with no chip.
//
// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { listAgentsMock, listSkillsMock, getMCPMentionableServersMock } = vi.hoisted(() => ({
  listAgentsMock: vi.fn(),
  listSkillsMock: vi.fn(),
  getMCPMentionableServersMock: vi.fn(),
}))

vi.mock('@/api/agents', () => ({ listAgents: (...args: unknown[]) => listAgentsMock(...args) }))
vi.mock('@/api/skills', () => ({ listSkills: (...args: unknown[]) => listSkillsMock(...args) }))
vi.mock('@/api/mcp', () => ({
  getMCPMentionableServers: (...args: unknown[]) => getMCPMentionableServersMock(...args),
  // Pure helper mirrored verbatim (production behavior under test).
  mentionableMCPNames: (servers: Array<{ name: string; mode: string }>) =>
    servers.filter((s) => s.mode === 'auto' || s.mode === 'manual').map((s) => s.name),
}))
vi.mock('@/api/runtime', () => ({ subscribe: vi.fn(() => () => {}) }))

import { UserMessageContent } from './UserMessageContent'

let root: Root | null = null

function render(el: React.ReactElement): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(el)
  })
  return container
}

function chips(container: HTMLElement): HTMLElement[] {
  return [...container.querySelectorAll('span.font-mono')] as HTMLElement[]
}

beforeEach(() => {
  document.body.innerHTML = ''
  root = null
})

afterEach(() => {
  act(() => {
    root?.unmount()
  })
  root = null
})

describe('UserMessageContent /-ref chips', () => {
  it('renders qualified refs verbatim, colored by kind, without any catalog fetch', () => {
    const container = render(
      <UserMessageContent content="run /agent: code-reviewer with /skill: study-paper and /mcp: context7" />,
    )
    const [agentChip, skillChip, mcpChip] = chips(container)
    expect(agentChip).toBeDefined()
    expect(agentChip!.textContent).toBe('/agent: code-reviewer')
    // Agent chips carry the highlight color; skill chips the plain chip box;
    // mcp chips the info color.
    expect(agentChip!.getAttribute('style')).toContain('--color-highlight')
    expect(skillChip).toBeDefined()
    expect(skillChip!.textContent).toBe('/skill: study-paper')
    expect(skillChip!.className).toContain('bg-background')
    expect(mcpChip).toBeDefined()
    expect(mcpChip!.textContent).toBe('/mcp: context7')
    expect(mcpChip!.getAttribute('style')).toContain('--color-info')
    expect(listAgentsMock).not.toHaveBeenCalled()
    expect(listSkillsMock).not.toHaveBeenCalled()
    expect(getMCPMentionableServersMock).not.toHaveBeenCalled()
  })

  it('renders the glued qualified form verbatim too', () => {
    const container = render(<UserMessageContent content="/agent:code-reviewer" />)
    const [agentChip] = chips(container)
    expect(agentChip!.textContent).toBe('/agent:code-reviewer')
  })

  it('renders the glued /mcp qualified form verbatim', () => {
    const container = render(<UserMessageContent content="/mcp:context7" />)
    const [mcpChip] = chips(container)
    expect(mcpChip!.textContent).toBe('/mcp:context7')
    expect(mcpChip!.getAttribute('style')).toContain('--color-info')
  })

  it('renders historical #mentions as plain text (no chip)', () => {
    const container = render(<UserMessageContent content="please use #code-reviewer now" />)
    expect(chips(container)).toHaveLength(0)
    expect(container.textContent).toContain('#code-reviewer')
  })

  it('resolves a plain /name kind from the catalogs at display time', async () => {
    // Catalog fixture covers every resolution branch of this test: an agent
    // name, a skill name, an MCP server name, and an unknown name. The
    // refCatalogs cache is module-level, so one fixture set serves the whole
    // render sequence.
    listAgentsMock.mockResolvedValue([{ name: 'code-reviewer', description: '' }])
    listSkillsMock.mockResolvedValue([{ name: 'commit', description: '' }])
    getMCPMentionableServersMock.mockResolvedValue([{ name: 'context7', mode: 'auto' }])

    const container = render(
      <UserMessageContent content="run /code-reviewer /commit /context7 /mystery now" />,
    )
    const resolve = () => chips(container)

    // Before the catalog snapshot lands, plain refs render as neutral chips.
    let resolved = resolve()
    expect(resolved.map((c) => c.textContent)).toEqual([
      '/code-reviewer', '/commit', '/context7', '/mystery',
    ])
    expect(resolved.every((c) => c.className.includes('text-muted-foreground'))).toBe(true)

    // Catalogs settle → chips upgrade to their resolved kind (agent colored,
    // skill boxed, mcp info-colored, unknown stays neutral).
    await act(async () => {
      await Promise.resolve()
    })
    resolved = resolve()
    expect(resolved[0]!.getAttribute('style')).toContain('--color-highlight')
    expect(resolved[1]!.className).toContain('bg-background')
    expect(resolved[1]!.className).not.toContain('text-muted-foreground')
    expect(resolved[2]!.getAttribute('style')).toContain('--color-info')
    expect(resolved[3]!.className).toContain('text-muted-foreground')

    const longer = '/code-reviewer-extra /context7-extra /agent:code-reviewer-extra /mcp: context7-extra'
    act(() => {
      root!.render(<UserMessageContent content={longer} />)
    })
    resolved = resolve()
    expect(resolved.map((chip) => chip.textContent)).toEqual([
      '/code-reviewer-extra', '/context7-extra', '/agent:code-reviewer-extra', '/mcp: context7-extra',
    ])
    expect(resolved[0]!.className).toContain('text-muted-foreground')
    expect(resolved[1]!.className).toContain('text-muted-foreground')
    expect(container.textContent).toBe(longer)
  })

  it('never renders path prefixes or malformed qualified tokens as chips', () => {
    const content = 'read /github/cache/config.json /agentName/path /mcp: github/cache /agent:agentName/path /mcp: /agent: /skill: commit'
    const container = render(<UserMessageContent content={content} />)
    expect(chips(container).map((chip) => chip.textContent)).toEqual(['/skill: commit'])
    expect(container.textContent).toBe(content)
  })

  it('keeps @file chips and their #L anchors working alongside /-refs', () => {
    const container = render(
      <UserMessageContent content={'/agent: code-reviewer see @x.go#L20'} />,
    )
    const spans = chips(container)
    expect(spans[0]!.textContent).toBe('/agent: code-reviewer')
    const fileChip = spans.find((s) => s.textContent === '@x.go#L20')
    expect(fileChip).toBeDefined()
    expect(fileChip!.className).toContain('text-info')
  })
})
