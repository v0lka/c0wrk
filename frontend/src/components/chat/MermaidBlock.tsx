import { useEffect, useLayoutEffect, useState } from 'react'
import DOMPurify from 'dompurify'
import { Maximize, ZoomIn, ZoomOut } from 'lucide-react'
import { useThemeStore } from '@/stores/themeStore'
import { Button } from '@/components/ui/button'
import { DEFAULT_ZOOM_STEP, INITIAL_VIEW, usePanZoom } from '@/lib/usePanZoom'

interface MermaidBlockProps {
  code: string
}

/**
 * MermaidBlock renders a fenced ```mermaid code block as an interactive SVG.
 *
 * The diagram is rendered lazily (mermaid is code-split and themed to match
 * the app theme). The rendered graph lives inside a fixed-height "canvas"
 * viewport that supports:
 *   - drag-to-pan (control the visible region of the graph),
 *   - wheel-to-zoom toward the cursor,
 *   - zoom-in / zoom-out / reset buttons in a floating toolbar.
 *
 * On first paint the diagram is scaled to fit the canvas width (never
 * upscaled); the reset button restores that fit. All pan/zoom behavior
 * (anchored zoom, fit, drag-to-pan, wheel handling) lives in the reusable
 * `usePanZoom` hook.
 */
export function MermaidBlock({ code }: MermaidBlockProps) {
  const theme = useThemeStore((s) => s.theme)
  const {
    view,
    setView,
    canvasRef,
    contentRef,
    zoomFromCenter,
    fit,
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel,
  } = usePanZoom()

  const [svgHtml, setSvgHtml] = useState<string | null>(null)
  const [error, setError] = useState(false)

  // (Re)render the diagram whenever the source or theme changes.
  useEffect(() => {
    let cancelled = false
    // Mermaid appends a temporary <div id="${renderId}"> to document.body to
    // compute SVG layout; capture the id so the cleanup can remove any
    // orphaned node (it is not always auto-removed, especially on error).
    let renderId = ''
    async function run() {
      try {
        const { default: mermaid } = await import('mermaid')
        if (cancelled) return
        mermaid.initialize({
          startOnLoad: false,
          // Pin 'strict' so mermaid encodes HTML and strips scripts in the
          // generated SVG. The output is injected via dangerouslySetInnerHTML
          // from attacker-controllable (assistant) input — never use 'loose'.
          securityLevel: 'strict',
          theme: theme === 'light' ? 'default' : 'dark',
          // Render labels as SVG <text> instead of HTML inside <foreignObject>.
          // The DOMPurify sink below uses the svg-only profile, which strips
          // <foreignObject> and all of its HTML children. Mermaid defaults to
          // htmlLabels:true, so without this every node/edge label would be
          // emitted as HTML inside <foreignObject> and then deleted by the
          // sanitizer — the diagram shapes stay visible but the text vanishes.
          // SVG <text> survives the sanitizer and is colored via the embedded
          // <style> (.nodeLabel fill), and it keeps a tighter injection surface
          // (no HTML ever reaches the SVG).
          flowchart: { htmlLabels: false },
        })
        renderId = `mermaid-${crypto.randomUUID()}`
        const { svg } = await mermaid.render(renderId, code.trim())
        if (cancelled) return
        // Defense-in-depth: mermaid's strict mode already sanitizes, but
        // DOMPurify guards against upstream regressions and is already bundled
        // (mermaid depends on it). The svg-only profile strips <script>,
        // on* handlers, and any stray <foreignObject>/HTML payload before the
        // SVG reaches the dangerouslySetInnerHTML sink — which is exactly why
        // htmlLabels must be false above, so labels are plain SVG <text>.
        setSvgHtml(
          DOMPurify.sanitize(svg, { USE_PROFILES: { svg: true, svgFilters: true } }),
        )
        setError(false)
        setView(INITIAL_VIEW)
      } catch {
        if (!cancelled) {
          setError(true)
          setSvgHtml(null)
        }
      }
    }
    run()
    return () => {
      cancelled = true
      document.getElementById(renderId)?.remove()
    }
  }, [code, theme, setView])

  // Fit on first paint of a freshly rendered diagram.
  useLayoutEffect(() => {
    if (!svgHtml) return
    fit()
  }, [svgHtml, fit])

  if (error) {
    return (
      <div className="my-3 rounded-lg border border-destructive/30 bg-muted/40 p-3 text-sm">
        <div className="mb-1 font-medium text-destructive">Diagram render error</div>
        <pre className="m-0 whitespace-pre-wrap break-words font-mono text-xs text-muted-foreground">
          {code}
        </pre>
      </div>
    )
  }

  const pct = `${Math.round(view.scale * 100)}%`

  return (
    <div className="group relative my-3 overflow-hidden rounded-lg border border-border bg-muted/20">
      <div className="absolute right-2 top-2 z-10 flex items-center gap-0.5 rounded-md border border-border bg-background/85 p-0.5 shadow-sm backdrop-blur">
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={() => zoomFromCenter(1 / DEFAULT_ZOOM_STEP)}
          title="Zoom out"
          aria-label="Zoom out"
        >
          <ZoomOut />
        </Button>
        <span
          className="min-w-[4ch] select-none text-center text-xs tabular-nums text-muted-foreground"
          aria-live="polite"
        >
          {pct}
        </span>
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={() => zoomFromCenter(DEFAULT_ZOOM_STEP)}
          title="Zoom in"
          aria-label="Zoom in"
        >
          <ZoomIn />
        </Button>
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={fit}
          title="Reset view"
          aria-label="Reset zoom and pan"
        >
          <Maximize />
        </Button>
      </div>
      <div
        ref={canvasRef}
        // Canvas height: ~44% of viewport height keeps small diagrams from
        // dominating the chat while leaving room to scroll large ones; the
        // min/max clamps prevent collapse on short viewports and runaway
        // height on large monitors.
        className="mermaid-canvas relative h-[44vh] max-h-[520px] min-h-[160px] w-full cursor-grab touch-none select-none overflow-hidden active:cursor-grabbing"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerCancel}
      >
        <div
          ref={contentRef}
          className="pointer-events-none absolute left-0 top-0 origin-top-left"
          style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }}
          dangerouslySetInnerHTML={svgHtml ? { __html: svgHtml } : undefined} // eslint-disable-line react/no-danger -- DOMPurify-sanitized SVG (svg-only profile) from mermaid strict mode
        />
        {!svgHtml && (
          <div className="absolute inset-0 flex items-center justify-center">
            <span className="text-xs text-muted-foreground">Rendering diagram…</span>
          </div>
        )}
      </div>
    </div>
  )
}
