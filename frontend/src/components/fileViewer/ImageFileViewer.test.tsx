// @vitest-environment jsdom
// ImageFileViewer — renders an image data URL in a pan/zoom canvas.
//
// The heavy pan/zoom geometry is unit-tested in lib/usePanZoom.test.ts; these
// tests cover the component's own contract: it paints the data URL, exposes
// the zoom toolbar (zoom out / percentage / zoom in / 1:1 / fit), returns to
// actual size from the 1:1 button, and surfaces a failure notice when the
// bytes cannot be decoded.

import { describe, it, expect, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { ImageFileViewer } from './ImageFileViewer'

const DATA_URL = 'data:image/png;base64,iVBORw0KGgo='

let root: Root | null = null
let container: HTMLDivElement | null = null

function renderViewer(dataUrl = DATA_URL, path = '/ws/assets/plot.png'): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(<ImageFileViewer dataUrl={dataUrl} path={path} />)
  })
}

function getImg(): HTMLImageElement {
  const img = container!.querySelector('img')
  if (!img) throw new Error('image element not rendered')
  return img
}

function pctText(): string {
  return container!.querySelector('[aria-live="polite"]')?.textContent ?? ''
}

function clickByLabel(label: string): void {
  const btn = container!.querySelector<HTMLButtonElement>(`[aria-label="${label}"]`)
  if (!btn) throw new Error(`button "${label}" not found`)
  act(() => {
    btn.click()
  })
}

afterEach(() => {
  act(() => {
    root?.unmount()
  })
  container?.remove()
  root = null
  container = null
})

describe('ImageFileViewer', () => {
  it('renders the image with the file name as alt text', () => {
    renderViewer()
    const img = getImg()
    expect(img.getAttribute('src')).toBe(DATA_URL)
    expect(img.getAttribute('alt')).toBe('plot.png')
  })

  it('renders the zoom / fit toolbar', () => {
    renderViewer()
    expect(container!.querySelector('[aria-label="Zoom in"]')).not.toBeNull()
    expect(container!.querySelector('[aria-label="Zoom out"]')).not.toBeNull()
    expect(container!.querySelector('[aria-label="Actual size (100%)"]')).not.toBeNull()
    expect(container!.querySelector('[aria-label="Fit image to view"]')).not.toBeNull()
  })

  it('zooms in and out from the toolbar, updating the percentage', () => {
    renderViewer()
    expect(pctText()).toBe('100%')

    clickByLabel('Zoom in')
    expect(pctText()).toBe('125%')

    clickByLabel('Zoom out')
    expect(pctText()).toBe('100%')
  })

  it('restores actual size (100%) via the 1:1 button', () => {
    renderViewer()
    const img = getImg()
    // jsdom never decodes images, so supply the intrinsic size the 1:1 action
    // needs to compute its centered translate.
    Object.defineProperty(img, 'naturalWidth', { value: 200, configurable: true })
    Object.defineProperty(img, 'naturalHeight', { value: 100, configurable: true })

    clickByLabel('Zoom in')
    expect(pctText()).toBe('125%')

    clickByLabel('Actual size (100%)')
    expect(pctText()).toBe('100%')
    expect(img.parentElement?.style.transform).toContain('scale(1)')
  })

  it('shows a failure notice when the image cannot be decoded', () => {
    renderViewer()
    const img = getImg()
    act(() => {
      img.dispatchEvent(new Event('error'))
    })
    expect(container!.textContent).toContain('Could not display this image.')
    expect(container!.querySelector('img')).toBeNull()
  })
})
