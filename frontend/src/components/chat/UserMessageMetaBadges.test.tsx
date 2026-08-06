// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { UserMessageMetaBadges } from './UserMessageMetaBadges'
import type { UserMessageMeta } from '@/lib/userMessageMeta'

// Enable React's act() flushing in this jsdom environment.
;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true

// Mock the file-viewer store so image clicks never reach the real store. We
// expose both the hook (unused here but imported by the component) and a
// getState stub returning the spied openFile.
const openFileMock = vi.fn()
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: Object.assign(
    vi.fn(),
    { getState: () => ({ openFile: openFileMock }) },
  ),
}))

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

const IMG: UserMessageMeta = {
  images: [
    {
      id: 'i1',
      name: 'shot.png',
      thumbnail: 'data:image/png;base64,abc',
      path: '/abs/path/shot.png',
      media_type: 'image/png',
    },
  ],
}

const DOC: UserMessageMeta = {
  attachments: [{ original_name: 'report.pdf', format: 'pdf', size_bytes: 1024 }],
}

describe('UserMessageMetaBadges', () => {
  beforeEach(() => {
    openFileMock.mockClear()
    document.body.innerHTML = ''
    root = null
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
  })

  it('renders nothing when meta is empty (no goal/docs/images)', () => {
    const container = render(<UserMessageMetaBadges meta={{}} />)
    expect(container.innerHTML).toBe('')
  })

  it('renders only the goal badge when meta.goal === true', () => {
    const container = render(<UserMessageMetaBadges meta={{ goal: true }} />)
    expect(container.textContent).toContain('Goal')
    expect(container.querySelector('img')).toBeNull()
  })

  it('does not render a goal badge when meta.goal is false/absent', () => {
    const container = render(<UserMessageMetaBadges meta={{ goal: false }} />)
    expect(container.textContent).not.toContain('Goal')
    expect(container.innerHTML).toBe('')
  })

  it('renders goal badge + doc chips together', () => {
    const container = render(
      <UserMessageMetaBadges meta={{ goal: true, attachments: DOC.attachments }} />,
    )
    expect(container.textContent).toContain('Goal')
    expect(container.textContent).toContain('report.pdf')
    expect(container.textContent).toContain('(pdf)')
  })

  it('doc chip shows name + format for each document', () => {
    const container = render(
      <UserMessageMetaBadges
        meta={{
          attachments: [
            { original_name: 'a.md', format: 'markdown', size_bytes: 10 },
            { original_name: 'b.txt', format: 'text', size_bytes: 20 },
          ],
        }}
      />,
    )
    expect(container.textContent).toContain('a.md')
    expect(container.textContent).toContain('(markdown)')
    expect(container.textContent).toContain('b.txt')
    expect(container.textContent).toContain('(text)')
  })

  it('renders image thumbnails and opens the file in the viewer on click', () => {
    const container = render(<UserMessageMetaBadges meta={IMG} />)
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img?.getAttribute('src')).toBe('data:image/png;base64,abc')
    expect(img?.getAttribute('alt')).toBe('shot.png')
    // 24px = size-6 (24px). className should carry object-cover.
    expect(img?.className).toContain('object-cover')

    // Click the wrapping button -> openFile called with the absolute path.
    const btn = container.querySelector('button')
    expect(btn).not.toBeNull()
    act(() => {
      btn!.click()
    })
    expect(openFileMock).toHaveBeenCalledWith('/abs/path/shot.png')
  })

  it('renders images only (no goal/docs row)', () => {
    const container = render(<UserMessageMetaBadges meta={IMG} />)
    expect(container.textContent).not.toContain('Goal')
    expect(container.querySelector('img')).not.toBeNull()
  })

  it('renders all three signals together', () => {
    const container = render(
      <UserMessageMetaBadges meta={{ goal: true, ...DOC, ...IMG }} />,
    )
    expect(container.textContent).toContain('Goal')
    expect(container.textContent).toContain('report.pdf')
    expect(container.querySelector('img')).not.toBeNull()
  })
})
