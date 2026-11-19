// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ListDirBody } from './ListDirBody'
import { parseListEntry } from '../shared/listDirEntry'
import type { ToolBodyProps } from '../toolCardRegistry'

// The click path resolves entry names against the listed directory + chat
// workspace root, then opens the viewer — both seams are mocked so click
// tests assert exactly which path reaches the viewer without a Wails runtime.
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

describe('parseListEntry', () => {
  it('parses the name/kind/size tab triple', () => {
    expect(parseListEntry('tasks.md\tfile\t15542')).toEqual({
      name: 'tasks.md', kind: 'file', size: '15542',
    })
    expect(parseListEntry('bodies\tdir\t384')).toEqual({
      name: 'bodies', kind: 'dir', size: '384',
    })
  })

  it('parses names containing tabs (fields read from the right)', () => {
    expect(parseListEntry('a\tb.md\tfile\t12')).toEqual({
      name: 'a\tb.md', kind: 'file', size: '12',
    })
  })

  it('rejects truncation markers and malformed lines', () => {
    expect(parseListEntry('[... truncated — directory listing capped at 10000 entries / 1048576 bytes ...]')).toBeNull()
    expect(parseListEntry('')).toBeNull()
    expect(parseListEntry('name\tfile')).toBeNull()
    expect(parseListEntry('name\tdir\tnot-a-number')).toBeNull()
    expect(parseListEntry('name\tother\t5')).toBeNull()
  })
})

describe('ListDirBody', () => {
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
      root.render(<ListDirBody {...props} />)
      await Promise.resolve()
      await Promise.resolve()
    })

  const links = () => container.querySelectorAll('span[role="button"]')
  const plainLines = () => Array.from(container.querySelectorAll('div.text-muted-foreground.font-mono'))
    .map(el => el.textContent)

  it('keeps kind/size in the display but links only the file name', async () => {
    await render(makeProps({
      args: '{"path":"/ws"}',
      result: 'tasks.md\tfile\t15542\nTEMPLATE.md\tfile\t73344\n',
    }))
    const link = links()[0]!
    // The DISPLAY carries the full raw line (kind and size visible, matching
    // the listing text); the click must open ONLY the entry name.
    expect(link.textContent).toBe('tasks.md\tfile\t15542')
    await act(async () => {
      link.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(fileViewerMock.openFile).toHaveBeenCalledTimes(1)
    expect(fileViewerMock.openFile).toHaveBeenCalledWith('/ws/tasks.md')
  })

  it('renders directories as plain text without a link', async () => {
    await render(makeProps({
      args: '{"path":"/ws"}',
      result: 'bodies\tdir\t384\nshared\tdir\t256\nREADME.md\tfile\t11097\n',
    }))
    expect(links()).toHaveLength(1) // only README.md
    expect(plainLines()).toEqual(['bodies\tdir\t384', 'shared\tdir\t256'])
    expect(container.textContent).toContain('README.md\tfile\t11097')
  })

  it('renders truncation markers as plain text', async () => {
    await render(makeProps({
      args: '{"path":"/ws"}',
      result: 'a.md\tfile\t1\n[... truncated — directory listing capped at 2 entries / 1048576 bytes ...]\n',
    }))
    expect(links()).toHaveLength(1)
    expect(plainLines()).toEqual([
      '[... truncated — directory listing capped at 2 entries / 1048576 bytes ...]',
    ])
  })

  it('resolves entry names against the listed directory, not the workspace root', async () => {
    // list_directory path=specs → entry names are relative to specs/.
    await render(makeProps({
      args: '{"path":"specs"}',
      result: 'domains\tdir\t1\nINDEX.md\tfile\t500\n',
    }))
    const link = links()[0]!
    await act(async () => {
      link.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(fileViewerMock.openFile).toHaveBeenCalledWith('/ws/specs/INDEX.md')
  })

  it('handles names containing tabs: display shows the raw line, link opens the full name', async () => {
    await render(makeProps({
      args: '{"path":"/ws"}',
      result: 'a\tb.md\tfile\t12\n',
    }))
    const link = links()[0]!
    expect(link.textContent).toBe('a\tb.md\tfile\t12')
    await act(async () => {
      link.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(fileViewerMock.openFile).toHaveBeenCalledWith('/ws/a\tb.md')
  })

  it('caps the visible entries behind a Show all control', async () => {
    const lines = Array.from({ length: 25 }, (_, i) => `f${i}.md\tfile\t${i}`).join('\n')
    await render(makeProps({ args: '{"path":"/ws"}', result: lines }))
    expect(links()).toHaveLength(20)
    expect(container.querySelector('button')?.textContent).toBe('Show all (25 entries)')
  })
})
