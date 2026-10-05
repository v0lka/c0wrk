// @vitest-environment jsdom
//
// App shell sizing invariant.
//
// The app-wide UI scale is applied as CSS `zoom` on <html>. Per the CSS
// Viewport spec, `zoom` pre-multiplies the used value of every <length>
// property but leaves `auto`/<percentage> values untouched. Viewport units
// (100vh/100vw) are lengths, so a shell sized with them (Tailwind
// `h-screen`/`w-screen`) is magnified past the window whenever the scale is
// not 100% — producing horizontal AND vertical scrollbars around the whole
// app. The shell must therefore size itself with percentages (`h-full`/
// `w-full`), backed by the html/body/#root 100% chain in index.css.
//
// This renders the real AppLayout with its heavy children stubbed and asserts
// the top-level shell element keeps percentage sizing.

import { describe, it, expect, vi, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- stores: direct-field selector mocks (no localStorage needed) ---
vi.mock('@/stores/uiStore', () => ({
  useUIStore: (select: (s: Record<string, unknown>) => unknown) =>
    select({
      sidebarCollapsed: false,
      toggleSidebarCollapsed: () => {},
      sidebarWidth: 280,
      setSidebarWidth: () => {},
    }),
  SIDEBAR_MIN: 180,
  SIDEBAR_MAX: 500,
}))
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: (select: (s: Record<string, unknown>) => unknown) =>
    select({
      width: 400,
      collapsed: false,
      pinned: false,
      setWidth: () => {},
      setCollapsed: () => {},
    }),
}))

// --- shell dependencies: stubbed, not under test ---
vi.mock('@/hooks/useResize', () => ({
  useResize: () => ({ handleMouseDown: () => {}, handleKeyDown: () => {} }),
}))
vi.mock('@/components/ResizeHandle', () => ({ ResizeHandle: () => null }))
vi.mock('./Sidebar', () => ({ Sidebar: () => null }))
vi.mock('@/components/chat/ChatArea', () => ({ ChatArea: () => <div data-testid="chat-area" /> }))
vi.mock('@/components/chat/BonsaiProfileBanner', () => ({ BonsaiProfileBanner: () => <div data-testid="bonsai-banner" /> }))
vi.mock('@/components/layout/StatusBar', () => ({ StatusBar: () => null }))
vi.mock('@/components/fileViewer/FileViewerPanel', () => ({ FileViewerPanel: () => null }))
vi.mock('./floatingViewerOutside', () => ({
  createFloatingViewerOutsideHandler: () => () => {},
}))
vi.mock('@/api/runtime', () => ({
  getApp: () => ({ PersistWindowBounds: () => Promise.resolve() }),
  isWailsReady: () => false,
  subscribe: () => () => {},
}))
vi.mock('@/lib/logger', () => ({ logger: { warn: () => {} } }))

import { AppLayout } from './AppLayout'

let container: HTMLDivElement
let root: Root

afterEach(() => {
  act(() => {
    root?.unmount()
  })
  container?.remove()
  document.body.innerHTML = ''
})

function renderShell(): HTMLElement {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<AppLayout />)
  })
  const shell = container.querySelector<HTMLElement>(':scope > div')
  if (!shell) throw new Error('AppLayout did not render a shell element')
  return shell
}

describe('AppLayout shell sizing', () => {
  it('places the Bonsai profile banner directly above the chat area', () => {
    const shell = renderShell()
    const chat = shell.querySelector('[data-testid="chat-area"]')
    expect(chat?.previousElementSibling?.getAttribute('data-testid')).toBe('bonsai-banner')
  })

  it('fills the viewport with percentages, never viewport units', () => {
    const classes = renderShell().className.split(/\s+/)

    // Viewport units encode the un-zoomed viewport; CSS zoom then magnifies
    // them, overflowing the window. They must never appear on the shell.
    expect(classes).not.toContain('h-screen')
    expect(classes).not.toContain('w-screen')

    expect(classes).toContain('h-full')
    expect(classes).toContain('w-full')
    expect(classes).toContain('overflow-hidden')
  })
})
