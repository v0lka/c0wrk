import type { MermaidConfig } from 'mermaid'

/**
 * Build the mermaid configuration used for every rendered diagram.
 *
 * `htmlLabels: false` is a hard requirement, not a preference: the rendered
 * SVG is injected via `dangerouslySetInnerHTML` after a DOMPurify pass with
 * the svg-only profile, which strips `<foreignObject>` and all of its HTML
 * children. Mermaid defaults to HTML labels, so with them enabled every node
 * and edge label would be emitted inside a `<foreignObject>` and then deleted
 * by the sanitizer — the diagram shapes stay visible but all text vanishes.
 * SVG `<text>` survives the sanitizer and is colored by the embedded `<style>`
 * (`.label text` fill), and it keeps a tighter injection surface (no HTML ever
 * reaches the SVG).
 *
 * The key MUST be the root-level one. Mermaid 11 deprecated
 * `flowchart.htmlLabels`, and the flowchart vertex-label path (`labelHelper`)
 * reads only the global `htmlLabels`, ignoring the flowchart-scoped one. With
 * `flowchart.htmlLabels: false` alone, edge labels switch to SVG `<text>` while
 * node labels stay HTML inside `<foreignObject>` — exactly the "shapes visible,
 * node text invisible" symptom.
 */
export function mermaidRenderConfig(theme: 'light' | 'dark'): MermaidConfig {
  return {
    startOnLoad: false,
    // Pin 'strict' so mermaid encodes HTML and strips scripts in the
    // generated SVG. The output is injected via dangerouslySetInnerHTML
    // from attacker-controllable (assistant) input — never use 'loose'.
    securityLevel: 'strict',
    theme: theme === 'light' ? 'default' : 'dark',
    htmlLabels: false,
  }
}
