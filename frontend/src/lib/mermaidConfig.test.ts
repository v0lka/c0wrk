// @vitest-environment jsdom
import { describe, it, expect } from 'vitest'
import DOMPurify, { type Config as DOMPurifyConfig } from 'dompurify'
import { mermaidRenderConfig } from './mermaidConfig'

// jsdom implements no SVG text layout; mermaid measures label boxes with the
// SVG geometry APIs below. Stub them with a deterministic character-based
// approximation so real rendering can run headlessly.
const svgProto = (globalThis as unknown as { SVGElement: { prototype: Record<string, unknown> } })
  .SVGElement.prototype
const textWidth = (el: SVGElement): number => Math.max(10, (el.textContent ?? '').length * 7)
svgProto.getBBox = function (this: SVGElement) {
  return { x: 0, y: 0, width: textWidth(this), height: 16 }
}
svgProto.getComputedTextLength = function (this: SVGElement) {
  return textWidth(this)
}
svgProto.getSubStringLength = function (this: SVGElement) {
  return textWidth(this)
}
svgProto.getNumberOfChars = function (this: SVGElement) {
  return (this.textContent ?? '').length
}
svgProto.getBoundingClientRect = function () {
  return { x: 0, y: 0, top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0, toJSON: () => ({}) }
}

const SANITIZER_CONFIG: DOMPurifyConfig = { USE_PROFILES: { svg: true, svgFilters: true } }

describe('mermaidRenderConfig', () => {
  it('disables HTML labels at the root level, not the deprecated flowchart key', () => {
    const config = mermaidRenderConfig('dark')
    // Root-level key: the flowchart vertex-label path reads only this one.
    expect(config.htmlLabels).toBe(false)
    // The deprecated flowchart-scoped key must not be the carrier of the fix.
    expect(config.flowchart?.htmlLabels).toBeUndefined()
    expect(config.securityLevel).toBe('strict')
    expect(config.startOnLoad).toBe(false)
  })

  it('maps the app theme onto a mermaid theme', () => {
    expect(mermaidRenderConfig('dark').theme).toBe('dark')
    expect(mermaidRenderConfig('light').theme).toBe('default')
  })

  it('renders node and edge labels as sanitizer-surviving SVG text', async () => {
    const { default: mermaid } = await import('mermaid')
    mermaid.initialize(mermaidRenderConfig('dark'))
    const { svg } = await mermaid.render(
      'mermaid-config-label-test',
      'flowchart TD\n  A[Alpha] -->|enables| B[Beta]\n',
    )
    // The config must take effect before sanitization: with HTML labels the
    // text is emitted inside <foreignObject>, which DOMPurify then deletes.
    expect(svg).not.toContain('<foreignObject')

    const clean = DOMPurify.sanitize(svg, SANITIZER_CONFIG)
    expect(clean).not.toContain('<foreignObject')

    const parsed = new DOMParser().parseFromString(clean, 'image/svg+xml')
    const text = Array.from(parsed.querySelectorAll('text'))
      .map((node) => node.textContent ?? '')
      .join(' ')
    // Both node labels and the edge label keep their content.
    expect(text).toContain('Alpha')
    expect(text).toContain('Beta')
    expect(text).toContain('enables')
    // The embedded stylesheet survives too — it carries the label fill color,
    // without which the surviving <text> would be invisible on a dark canvas.
    expect(parsed.querySelector('style')).not.toBeNull()
  })
})
