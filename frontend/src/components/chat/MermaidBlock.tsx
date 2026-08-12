import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
} from 'react'
import DOMPurify from 'dompurify'
import { Maximize, ZoomIn, ZoomOut } from 'lucide-react'
import { useThemeStore } from '@/stores/themeStore'
import { Button } from '@/components/ui/button'

interface MermaidBlockProps {
  code: string
}

/** Zoom limits and step for the pan/zoom canvas. */
const MIN_SCALE = 0.2
const MAX_SCALE = 5
const ZOOM_STEP = 1.25

interface View {
  scale: number
  x: number
  y: number
}

const INITIAL_VIEW: View = { scale: 1, x: 0, y: 0 }

function clampScale(value: number): number {
  return Math.min(MAX_SCALE, Math.max(MIN_SCALE, value))
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
 * upscaled); the reset button restores that fit.
 */
export function MermaidBlock({ code }: MermaidBlockProps) {
  const theme = useThemeStore((s) => s.theme)
  const canvasRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  // Mirror of `view` so event handlers read the latest transform without
  // stale closures or per-frame handler recreation.
  const viewRef = useRef<View>(INITIAL_VIEW)
  const dragRef = useRef<{ startX: number; startY: number; ox: number; oy: number } | null>(null)

  const [svgHtml, setSvgHtml] = useState<string | null>(null)
  const [error, setError] = useState(false)
  const [view, setView] = useState<View>(INITIAL_VIEW)
  // Keep the ref mirror in sync synchronously after commit so the render phase
  // stays pure (no side effects) while event handlers and the fit() layout
  // effect (declared below) always read the latest transform.
  useLayoutEffect(() => {
    viewRef.current = view
  }, [view])

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
  }, [code, theme])

  /**
   * Scale the diagram so its natural width fits the canvas (never upscaled),
   * centered within the viewport. Reads the current transform to recover the
   * scale-1 (natural) size regardless of the active zoom level. If the SVG
   * has no laid-out width yet (e.g. width="100%" or freshly injected before
   * layout), falls back to its viewBox / width/height attributes.
   */
  const fit = useCallback(() => {
    const canvas = canvasRef.current
    const svgEl = contentRef.current?.querySelector('svg')
    if (!canvas || !svgEl) return
    const cw = canvas.clientWidth
    const ch = canvas.clientHeight
    const rect = svgEl.getBoundingClientRect()
    const prevScale = viewRef.current.scale || 1
    let naturalW = rect.width / prevScale
    let naturalH = rect.height / prevScale
    // Fall back to the SVG's intrinsic geometry when layout yields 0 (some
    // diagram types emit width="100%"/no viewBox and collapse before paint).
    if (!naturalW || !naturalH) {
      const attrW = parseFloat(svgEl.getAttribute('width') ?? '')
      const attrH = parseFloat(svgEl.getAttribute('height') ?? '')
      const vb = svgEl.getAttribute('viewBox')?.split(/[\s,]+/).map(Number)
      naturalW = naturalW || attrW || vb?.[2] || 0
      naturalH = naturalH || attrH || vb?.[3] || 0
    }
    if (!naturalW || !naturalH || !cw) return
    const scale = Math.min(1, cw / naturalW)
    const scaledW = naturalW * scale
    const scaledH = naturalH * scale
    setView({ scale, x: (cw - scaledW) / 2, y: Math.max(0, (ch - scaledH) / 2) })
  }, [])

  // Fit on first paint of a freshly rendered diagram.
  useLayoutEffect(() => {
    if (!svgHtml) return
    fit()
  }, [svgHtml, fit])

  /** Zoom by `factor`, keeping the viewport point (cx, cy) fixed. */
  const zoomAt = useCallback((factor: number, cx: number, cy: number) => {
    setView((prev) => {
      const scale = clampScale(prev.scale * factor)
      if (scale === prev.scale) return prev
      const k = scale / prev.scale
      return { scale, x: cx - k * (cx - prev.x), y: cy - k * (cy - prev.y) }
    })
  }, [])

  const zoomFromCenter = useCallback(
    (factor: number) => {
      const rect = canvasRef.current?.getBoundingClientRect()
      if (!rect) return
      zoomAt(factor, rect.width / 2, rect.height / 2)
    },
    [zoomAt],
  )

  // Attach a non-passive wheel listener so we can preventDefault and stop the
  // page from scrolling while zooming the diagram. Only vertical (zoom) input
  // is intercepted; horizontal trackpad scrolling and plain panning are left
  // to the browser so the canvas behaves predictably on touchpads.
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const onWheel = (e: WheelEvent) => {
      // Ignore horizontal-dominant gestures (trackpad two-finger swipe);
      // let the browser handle them instead of swallowing the input.
      if (Math.abs(e.deltaX) > Math.abs(e.deltaY)) return
      e.preventDefault()
      const rect = canvas.getBoundingClientRect()
      zoomAt(
        e.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP,
        e.clientX - rect.left,
        e.clientY - rect.top,
      )
    }
    canvas.addEventListener('wheel', onWheel, { passive: false })
    return () => canvas.removeEventListener('wheel', onWheel)
  }, [zoomAt])

  const onPointerDown = useCallback((e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    canvasRef.current?.setPointerCapture(e.pointerId)
    dragRef.current = {
      startX: e.clientX,
      startY: e.clientY,
      ox: viewRef.current.x,
      oy: viewRef.current.y,
    }
  }, [])

  const onPointerMove = useCallback((e: ReactPointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current
    if (!drag) return
    const dx = e.clientX - drag.startX
    const dy = e.clientY - drag.startY
    setView((prev) => ({ ...prev, x: drag.ox + dx, y: drag.oy + dy }))
  }, [])

  const endDrag = useCallback((e: ReactPointerEvent<HTMLDivElement>) => {
    const canvas = canvasRef.current
    if (canvas?.hasPointerCapture(e.pointerId)) canvas.releasePointerCapture(e.pointerId)
    dragRef.current = null
  }, [])

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
          onClick={() => zoomFromCenter(1 / ZOOM_STEP)}
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
          onClick={() => zoomFromCenter(ZOOM_STEP)}
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
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
      >
        <div
          ref={contentRef}
          className="pointer-events-none absolute left-0 top-0 origin-top-left"
          style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }}
          dangerouslySetInnerHTML={svgHtml ? { __html: svgHtml } : undefined}
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
