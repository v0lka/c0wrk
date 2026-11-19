// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SearchBody } from './SearchBody'
import type { ToolBodyProps } from '../toolCardRegistry'

// The click path resolves entry paths against the chat workspace root (the
// search root for relative shapes), then opens the viewer — both seams are
// mocked so click tests assert exactly which path reaches the viewer.
const chatRootMock = vi.hoisted(() => ({
  resolveChatWorkspaceRoot: vi.fn(),
}))
const fileViewerMock = vi.hoisted(() => ({
  openFile: vi.fn(),
  openFileAtLine: vi.fn(),
}))
vi.mock('@/lib/chatWorkspaceRoot', () => ({
  resolveChatWorkspaceRoot: chatRootMock.resolveChatWorkspaceRoot,
}))
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: { getState: () => fileViewerMock },
}))

function makeProps(overrides: Partial<ToolBodyProps>): ToolBodyProps {
  return {
    args: '',
    status: 'success',
    ...overrides,
  }
}

describe('SearchBody', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    fileViewerMock.openFile.mockClear()
    fileViewerMock.openFileAtLine.mockClear()
    chatRootMock.resolveChatWorkspaceRoot.mockReset()
    chatRootMock.resolveChatWorkspaceRoot.mockResolvedValue('/ws')
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => {
      root.unmount()
    })
    container.remove()
    document.body.replaceChildren()
  })

  const render = (props: ToolBodyProps) =>
    act(async () => {
      root.render(<SearchBody {...props} />)
      await Promise.resolve()
      await Promise.resolve()
    })

  const links = () => Array.from(container.querySelectorAll('span[role="button"]'))

  const clickFirst = async () => {
    await act(async () => {
      links()[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })
  }

  it('links only the path of a ripgrep match, keeping the full line as display', async () => {
    await render(makeProps({
      toolName: 'ripgrep',
      result: '/ws/src/loop.go:42: run(ctx)\nFound 1 match in 1 file\n',
    }))
    expect(links()).toHaveLength(1)
    const link = links()[0]!
    expect(link.textContent).toBe('/ws/src/loop.go:42: run(ctx)')
    await clickFirst()
    expect(fileViewerMock.openFileAtLine).toHaveBeenCalledTimes(1)
    expect(fileViewerMock.openFileAtLine).toHaveBeenCalledWith('/ws/src/loop.go', 42)
    expect(fileViewerMock.openFile).not.toHaveBeenCalled()
  })

  it('renders the ripgrep summary line as plain text', async () => {
    await render(makeProps({
      toolName: 'ripgrep',
      result: '/ws/src/loop.go:42: run(ctx)\n\nFound 1 match in 1 file\n',
    }))
    // Only the match line links; "Found N matches…" is not a path.
    expect(links()).toHaveLength(1)
    await clickFirst()
    expect(fileViewerMock.openFileAtLine).toHaveBeenCalledWith('/ws/src/loop.go', 42)
  })

  it('parses context lines (dash separator) with their line number', async () => {
    await render(makeProps({
      toolName: 'ripgrep',
      result: '/ws/src/loop.go:40-func run(ctx) {\n',
    }))
    await clickFirst()
    expect(fileViewerMock.openFileAtLine).toHaveBeenCalledWith('/ws/src/loop.go', 40)
  })

  it('links glob matches, resolving them against the search root', async () => {
    // glob results are relative to the call's own `path` argument.
    await render(makeProps({
      toolName: 'glob',
      args: '{"pattern":"**/*.ts","path":"specs"}',
      result: 'domains/a.ts\nb.ts\n\nwarning: results limited to 2 matches; narrow the pattern or path\n',
    }))
    expect(links()).toHaveLength(2)
    const plain = Array.from(container.querySelectorAll('span'))
      .map(el => el.textContent)
      .filter(t => t?.startsWith('warning:'))
    expect(plain).toEqual(['warning: results limited to 2 matches; narrow the pattern or path'])
    await clickFirst()
    expect(fileViewerMock.openFile).toHaveBeenCalledWith('/ws/specs/domains/a.ts')
    expect(fileViewerMock.openFileAtLine).not.toHaveBeenCalled()
  })

  it('renders glob directory entries (trailing "/") as plain text', async () => {
    // sp4rk glob marks directories with a trailing slash (type: dirs|all);
    // the viewer reads files only, so dir entries stay unlinked.
    await render(makeProps({
      toolName: 'glob',
      args: '{"pattern":"**","path":"specs","type":"all"}',
      result: 'domains/\nb.ts\n',
    }))
    expect(links()).toHaveLength(1)
    const texts = Array.from(container.querySelectorAll('span')).map(el => el.textContent)
    expect(texts).toContain('domains/')
    await clickFirst()
    expect(fileViewerMock.openFile).toHaveBeenCalledWith('/ws/specs/b.ts')
  })

  it('renders the glob zero-match summary as plain text', async () => {
    await render(makeProps({
      toolName: 'glob',
      args: '{"pattern":"*.zz"}',
      result: 'no matching files found',
    }))
    expect(links()).toHaveLength(0)
    expect(container.textContent).toContain('no matching files found')
  })

  it('renders an errored glob result as plain text (no links)', async () => {
    // The result of a failed call is diagnostic text ("glob error: …",
    // "path error: …") — the status gate keeps every line unlinked.
    await render(makeProps({
      toolName: 'glob',
      status: 'error',
      args: '{"pattern":"[["}',
      result: 'glob error: malformed pattern "[["',
    }))
    expect(links()).toHaveLength(0)
    expect(container.textContent).toContain('glob error: malformed pattern "[["')
  })

  it('renders an errored ripgrep result as plain text', async () => {
    await render(makeProps({
      toolName: 'ripgrep',
      status: 'error',
      result: 'search error: invalid regex',
    }))
    expect(links()).toHaveLength(0)
  })

  it('keeps the legacy absolute-line heuristic for unknown producers', async () => {
    await render(makeProps({
      args: '{"pattern":"x"}',
      result: '/ws/src/a.go\nsome other text\n',
    }))
    expect(links()).toHaveLength(1)
    await clickFirst()
    // An absolute candidate opens normalized, without a speculative probe.
    expect(fileViewerMock.openFile).toHaveBeenCalledWith('/ws/src/a.go')
  })

  it('passes a relative unknown-producer path through the workspace root', async () => {
    await render(makeProps({
      args: '{"path":"src"}',
      result: './a.go\n',
    }))
    expect(links()).toHaveLength(1)
    await clickFirst()
    expect(fileViewerMock.openFile).toHaveBeenCalledWith('/ws/src/a.go')
  })
})
