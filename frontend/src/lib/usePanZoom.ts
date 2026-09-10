import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type Dispatch,
  type RefObject,
  type SetStateAction,
  type PointerEvent as ReactPointerEvent,
} from 'react'

/** Default lower zoom limit (inclusive). */
export const DEFAULT_MIN_SCALE = 0.2
/** Default upper zoom limit (inclusive). */
export const DEFAULT_MAX_SCALE = 5
/** Default multiplicative zoom step for wheel and toolbar zooming. */
export const DEFAULT_ZOOM_STEP = 1.25
/** Pointer travel (px) beyond which the drag is no longer a click. */
export const DRAG_CLICK_THRESHOLD_PX = 4

/** Pan/zoom transform: content is translated by (x, y) and scaled around its top-left origin. */
export interface View {
  scale: number
  x: number
  y: number
}

export const INITIAL_VIEW: View = { scale: 1, x: 0, y: 0 }

/** Natural (scale-1) content size. */
export interface Size {
  width: number
  height: number
}

/** Clamp `value` into [min, max]. */
export function clampScale(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value))
}

/**
 * Pure anchored zoom: scale `prev` by `factor` while keeping the viewport
 * point (cx, cy) fixed. Returns null when the scale is already pinned at a
 * limit (the caller then keeps the previous object so React bails out of the
 * re-render).
 */
export function zoomToPoint(
  prev: View,
  factor: number,
  cx: number,
  cy: number,
  min: number,
  max: number,
): View | null {
  const scale = clampScale(prev.scale * factor, min, max)
  if (scale === prev.scale) return null
  const k = scale / prev.scale
  return { scale, x: cx - k * (cx - prev.x), y: cy - k * (cy - prev.y) }
}

/**
 * Pure fit computation: scale so `naturalW` fits `canvasWidth` (never
 * upscaled), centered horizontally and top-aligned when the scaled content is
 * taller than the viewport. Returns null for degenerate inputs (unmeasurable
 * content or zero-width canvas) so the caller keeps the current view.
 */
export function computeFitView(
  canvasWidth: number,
  canvasHeight: number,
  naturalW: number,
  naturalH: number,
): View | null {
  if (!naturalW || !naturalH || !canvasWidth) return null
  const scale = Math.min(1, canvasWidth / naturalW)
  const scaledW = naturalW * scale
  const scaledH = naturalH * scale
  return {
    scale,
    x: (canvasWidth - scaledW) / 2,
    y: Math.max(0, (canvasHeight - scaledH) / 2),
  }
}

/**
 * True when a wheel gesture is horizontal-dominant (trackpad two-finger
 * swipe); such gestures are left to the browser instead of being swallowed
 * for zooming. Equal deltas count as vertical (zoom).
 */
export function isHorizontalWheelGesture(deltaX: number, deltaY: number): boolean {
  return Math.abs(deltaX) > Math.abs(deltaY)
}

/**
 * Recover the natural (scale-1) size of the first <svg> inside `container`
 * from its laid-out rect and the active transform scale. Falls back to the
 * SVG's intrinsic geometry (width/height attributes, then viewBox) when
 * layout yields 0 (some diagram types emit width="100%" / no viewBox and
 * collapse before paint).
 */
function measureSvgNaturalSize(container: HTMLElement | null, activeScale: number): Size | null {
  const svgEl = container?.querySelector('svg')
  if (!svgEl) return null
  const rect = svgEl.getBoundingClientRect()
  const prevScale = activeScale || 1
  let width = rect.width / prevScale
  let height = rect.height / prevScale
  if (!width || !height) {
    const attrW = parseFloat(svgEl.getAttribute('width') ?? '')
    const attrH = parseFloat(svgEl.getAttribute('height') ?? '')
    const vb = svgEl.getAttribute('viewBox')?.split(/[\s,]+/).map(Number)
    width = width || attrW || vb?.[2] || 0
    height = height || attrH || vb?.[3] || 0
  }
  if (!width || !height) return null
  return { width, height }
}

export interface UsePanZoomOptions {
  /** Lower zoom limit (inclusive). Default: DEFAULT_MIN_SCALE. */
  minScale?: number
  /** Upper zoom limit (inclusive). Default: DEFAULT_MAX_SCALE. */
  maxScale?: number
  /** Multiplicative zoom step for wheel and button zooming. Default: DEFAULT_ZOOM_STEP. */
  zoomStep?: number
  /**
   * Source of the content's natural (scale-1) size for fit(). Defaults to
   * measuring the first <svg> inside the content element.
   */
  getNaturalSize?: () => Size | null
}

export interface UsePanZoomResult {
  /** Current transform; render it as translate(x, y) scale(scale). */
  view: View
  setView: Dispatch<SetStateAction<View>>
  /** Viewport element — gets the pointer handlers and the wheel listener. */
  canvasRef: RefObject<HTMLDivElement | null>
  /** Transformed content element living inside the canvas. */
  contentRef: RefObject<HTMLDivElement | null>
  /** Ref mirror of `view` so handlers read the latest transform synchronously. */
  viewRef: RefObject<View>
  /**
   * Drag-distance counter: true once the last drag moved the pointer further
   * than DRAG_CLICK_THRESHOLD_PX. Check it in an onClick on the canvas (and
   * swallow the event) so drag-to-pan does not register as a click.
   */
  didDragRef: RefObject<boolean>
  /**
   * True while a pointer gesture is active (from pointerdown until
   * pointerup/pointer cancel). Read it inside effects that would reset the
   * camera (e.g. an auto-fit) so a geometry refresh landing mid-drag never
   * yanks the viewport out from under the user.
   */
  isPanningRef: RefObject<boolean>
  /** Zoom by `factor`, keeping the viewport point (cx, cy) fixed. */
  zoomAt: (factor: number, cx: number, cy: number) => void
  /** Zoom by `factor` around the canvas center. */
  zoomFromCenter: (factor: number) => void
  /** Fit the content into the canvas (never upscaled), centered. */
  fit: () => void
  onPointerDown: (e: ReactPointerEvent<HTMLDivElement>) => void
  onPointerMove: (e: ReactPointerEvent<HTMLDivElement>) => void
  onPointerUp: (e: ReactPointerEvent<HTMLDivElement>) => void
  onPointerCancel: (e: ReactPointerEvent<HTMLDivElement>) => void
}

interface DragState {
  startX: number
  startY: number
  ox: number
  oy: number
}

/**
 * Reusable pan/zoom state machine for a fixed-size canvas viewport holding a
 * transformable content element:
 *   - anchored wheel/button zoom toward cursor or center (zero-delta wheel
 *     events are ignored, not zoomed),
 *   - fit-to-canvas (never upscaled) from natural content size,
 *   - primary-pointer-only LMB drag-to-pan with lazily-engaged pointer
 *     capture (capture only after the drag threshold, so plain clicks on
 *     inner content are never retargeted away from their target),
 *   - stale-drag guards: a pointermove with no primary button held clears
 *     the armed drag, and a window-level pointerup/pointercancel fallback
 *     covers releases the canvas cannot see before capture engages — so the
 *     canvas never pans on a bare hover,
 *   - rAF-coalesced pan commits: at most one React state write per animation
 *     frame during a drag, with `viewRef` always current,
 *   - a didDragRef counter so consumers can suppress the click that follows
 *     a pan gesture.
 *
 * The wheel listener is attached non-passively so preventDefault can stop the
 * page from scrolling while zooming the canvas.
 */
export function usePanZoom(options: UsePanZoomOptions = {}): UsePanZoomResult {
  const minScale = options.minScale ?? DEFAULT_MIN_SCALE
  const maxScale = options.maxScale ?? DEFAULT_MAX_SCALE
  const zoomStep = options.zoomStep ?? DEFAULT_ZOOM_STEP

  const canvasRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  // Mirror of `view` so event handlers read the latest transform without
  // stale closures or per-frame handler recreation.
  const viewRef = useRef<View>(INITIAL_VIEW)
  const didDragRef = useRef<boolean>(false)
  const dragRef = useRef<DragState | null>(null)
  // Gesture-lifetime flag: true between pointerdown and pointerup/cancel so
  // consumers can defer camera resets (auto-fit) until the gesture ends.
  const isPanningRef = useRef<boolean>(false)
  // Detach callback for the window-level end-of-gesture fallback ([7]b),
  // active from pointerdown until pointer capture takes over (or the gesture
  // ends). Kept in a ref so every drag-end path can tear it down idempotently.
  const detachWindowEndRef = useRef<(() => void) | null>(null)
  // rAF-coalesced pan commits ([26]b): `pendingViewRef` holds the latest
  // transform computed during the drag; `rafIdRef` tracks the scheduled flush
  // so at most one React state write happens per animation frame.
  const pendingViewRef = useRef<View | null>(null)
  const rafIdRef = useRef<number | null>(null)

  // Keep the caller-provided natural-size accessor in a ref so `fit` (and the
  // handlers/effects built on it) stay referentially stable even when
  // `options` is an inline object literal. Synced after commit so the render
  // phase stays pure (same pattern as the viewRef mirror below).
  const getNaturalSizeRef = useRef(options.getNaturalSize)
  useLayoutEffect(() => {
    getNaturalSizeRef.current = options.getNaturalSize
  })

  const [view, setView] = useState<View>(INITIAL_VIEW)
  // Keep the ref mirror in sync synchronously after commit so the render phase
  // stays pure (no side effects) while event handlers and the fit() layout
  // effect in consumers always read the latest transform.
  useLayoutEffect(() => {
    viewRef.current = view
    // A committed view that differs from a still-pending drag frame means an
    // external write (consumer setView / fit / zoom) landed mid-drag — e.g.
    // MermaidBlock resetting the camera for a freshly rendered diagram. The
    // external write is the newer intent: drop the pending frame instead of
    // letting it overwrite the reset one animation frame later.
    if (pendingViewRef.current && pendingViewRef.current !== view) {
      pendingViewRef.current = null
      if (rafIdRef.current !== null) {
        cancelAnimationFrame(rafIdRef.current)
        rafIdRef.current = null
      }
    }
  }, [view])

  /**
   * Scale the content so its natural width fits the canvas (never upscaled),
   * centered within the viewport. The natural size comes from the caller's
   * `getNaturalSize` when provided, otherwise from measuring the first <svg>
   * inside the content element (recovering the scale-1 size from the current
   * transform, with intrinsic-geometry fallbacks).
   */
  const fit = useCallback(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const natural =
      getNaturalSizeRef.current?.() ??
      measureSvgNaturalSize(contentRef.current, viewRef.current.scale)
    if (!natural) return
    const next = computeFitView(canvas.clientWidth, canvas.clientHeight, natural.width, natural.height)
    if (next) setView(next)
  }, [])

  /** Zoom by `factor`, keeping the viewport point (cx, cy) fixed. */
  const zoomAt = useCallback(
    (factor: number, cx: number, cy: number) => {
      // Read the ref mirror (not React state) so a zoom landing while a drag
      // frame is still pending composes on the freshest transform.
      const next = zoomToPoint(viewRef.current, factor, cx, cy, minScale, maxScale)
      if (next) setView(next)
    },
    [minScale, maxScale],
  )

  const zoomFromCenter = useCallback(
    (factor: number) => {
      const rect = canvasRef.current?.getBoundingClientRect()
      if (!rect) return
      zoomAt(factor, rect.width / 2, rect.height / 2)
    },
    [zoomAt],
  )

  // Attach a non-passive wheel listener so we can preventDefault and stop the
  // page from scrolling while zooming the canvas. Only vertical (zoom) input
  // is intercepted; horizontal trackpad scrolling and plain panning are left
  // to the browser so the canvas behaves predictably on touchpads.
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const onWheel = (e: WheelEvent) => {
      // Ignore horizontal-dominant gestures (trackpad two-finger swipe);
      // let the browser handle them instead of swallowing the input.
      if (isHorizontalWheelGesture(e.deltaX, e.deltaY)) return
      // Zero-delta events (trackpad momentum tails on Chromium-derived
      // engines) carry no input at all: ignore them instead of preventDefault
      // and zooming out one extra step.
      if (e.deltaX === 0 && e.deltaY === 0) return
      e.preventDefault()
      const rect = canvas.getBoundingClientRect()
      zoomAt(
        e.deltaY < 0 ? zoomStep : 1 / zoomStep,
        e.clientX - rect.left,
        e.clientY - rect.top,
      )
    }
    canvas.addEventListener('wheel', onWheel, { passive: false })
    return () => canvas.removeEventListener('wheel', onWheel)
  }, [zoomAt, zoomStep])

  /**
   * rAF-coalesced state commit for the drag path ([26]b): `viewRef` updates
   * synchronously (every synchronous reader — drag math, zoom, fit — sees the
   * latest transform) while the React state write is deferred to at most one
   * per animation frame, so a 60-120Hz pointer stream re-renders the consumer
   * once per frame instead of once per event. React state remains the source
   * of truth, so an unrelated re-render mid-drag can never paint a stale
   * transform (unlike an imperative style-write scheme).
   */
  const commitView = useCallback((next: View) => {
    viewRef.current = next
    pendingViewRef.current = next
    if (rafIdRef.current === null) {
      rafIdRef.current = requestAnimationFrame(() => {
        rafIdRef.current = null
        const pending = pendingViewRef.current
        pendingViewRef.current = null
        if (pending) setView(pending)
      })
    }
  }, [])

  /** Commit a still-pending drag frame synchronously (used at gesture end). */
  const flushPendingView = useCallback(() => {
    if (rafIdRef.current !== null) {
      cancelAnimationFrame(rafIdRef.current)
      rafIdRef.current = null
    }
    const pending = pendingViewRef.current
    pendingViewRef.current = null
    if (pending) setView(pending)
  }, [])

  /** Detach the window-level end-of-gesture fallback (idempotent). */
  const detachWindowEnd = useCallback(() => {
    detachWindowEndRef.current?.()
    detachWindowEndRef.current = null
  }, [])

  /**
   * End the gesture: release capture when held, commit any pending drag
   * frame, clear the armed drag, and tear down the window fallback. Takes the
   * raw pointerId so both React canvas handlers and native window listeners
   * can call it.
   */
  const finishDrag = useCallback(
    (pointerId: number) => {
      detachWindowEnd()
      const canvas = canvasRef.current
      if (canvas?.hasPointerCapture(pointerId)) canvas.releasePointerCapture(pointerId)
      if (dragRef.current) flushPendingView()
      dragRef.current = null
      isPanningRef.current = false
    },
    [detachWindowEnd, flushPendingView],
  )

  /**
   * While a drag is armed but pointer capture is not yet engaged, the
   * pointerup can land outside the canvas (press near the panel edge, drift a
   * few pixels onto the floating zoom toolbar / resize handle, release): the
   * canvas never sees that event and the armed drag would pan the content on
   * every later hover move. A window-level capture-phase pointerup /
   * pointercancel listener closes that gap. It is detached once capture takes
   * over in onPointerMove — from then on the canvas receives every event of
   * the pointer directly, and didDragRef (flipped at the same moment) must
   * stay untouched so the trailing click stays suppressed.
   */
  const attachWindowEnd = useCallback(() => {
    if (detachWindowEndRef.current) return
    const onWindowUp = (ev: PointerEvent) => {
      // An auxiliary button released while the primary is still held must
      // not end the pan — only the primary button's release does.
      if ((ev.buttons & 1) !== 0) return
      finishDrag(ev.pointerId)
    }
    const onWindowCancel = (ev: PointerEvent) => finishDrag(ev.pointerId)
    window.addEventListener('pointerup', onWindowUp, true)
    window.addEventListener('pointercancel', onWindowCancel, true)
    detachWindowEndRef.current = () => {
      window.removeEventListener('pointerup', onWindowUp, true)
      window.removeEventListener('pointercancel', onWindowCancel, true)
    }
  }, [finishDrag])

  // A mid-gesture unmount must neither leave window listeners attached nor
  // fire a post-unmount rAF state commit.
  useEffect(
    () => () => {
      detachWindowEnd()
      if (rafIdRef.current !== null) cancelAnimationFrame(rafIdRef.current)
    },
    [detachWindowEnd],
  )

  const onPointerDown = useCallback(
    (e: ReactPointerEvent<HTMLDivElement>) => {
      if (e.button !== 0) return
      // Multi-pointer guard: only the primary pointer (first mouse button,
      // first touch point, dominant pen contact) drives the pan — a second
      // finger landing mid-gesture must not re-arm the drag origin and make
      // the pan jump to the new pointer's delta.
      if (!e.isPrimary) return
      // No pointer capture here: while capture is active the browser retargets
      // ALL subsequent events of that pointer — including the compatibility
      // mouse `click` — to the capture element (this canvas), so a plain click
      // on interactive content inside the canvas (e.g. DAG hypothesis nodes)
      // would never reach it. Capture is engaged lazily in onPointerMove once
      // the gesture crosses the drag threshold; see the comment there.
      didDragRef.current = false
      isPanningRef.current = true
      dragRef.current = {
        startX: e.clientX,
        startY: e.clientY,
        ox: viewRef.current.x,
        oy: viewRef.current.y,
      }
      // Window-level release fallback for the pre-capture window ([7]b).
      attachWindowEnd()
    },
    [attachWindowEnd],
  )

  const onPointerMove = useCallback(
    (e: ReactPointerEvent<HTMLDivElement>) => {
      const drag = dragRef.current
      if (!drag) return
      // Stale-drag guard ([7]a): a pointermove with the primary button no
      // longer held means the release happened where no pointerup could reach
      // this canvas (e.g. released outside the OS window before capture
      // engaged). Clear the armed drag instead of panning on a bare hover.
      if ((e.buttons & 1) === 0) {
        finishDrag(e.pointerId)
        return
      }
      const dx = e.clientX - drag.startX
      const dy = e.clientY - drag.startY
      // Drag-distance counter: once the pointer has travelled further than the
      // click threshold, the gesture is a pan — consumers checking didDragRef
      // in onClick suppress the trailing click.
      if (!didDragRef.current && Math.hypot(dx, dy) > DRAG_CLICK_THRESHOLD_PX) {
        didDragRef.current = true
        // The gesture is now definitively a pan (not a click), so it is safe —
        // and necessary — to capture the pointer: tracking continues even when
        // the pointer leaves the canvas, while any trailing click is already
        // suppressed via didDragRef. Capturing only here (never on pointerdown)
        // is what keeps plain clicks on inner content delivered normally.
        canvasRef.current?.setPointerCapture(e.pointerId)
        // Capture now delivers every event of this pointer straight to the
        // canvas; the window-level release fallback is no longer needed.
        detachWindowEnd()
      }
      commitView({ scale: viewRef.current.scale, x: drag.ox + dx, y: drag.oy + dy })
    },
    [finishDrag, detachWindowEnd, commitView],
  )

  const onPointerUp = useCallback(
    (e: ReactPointerEvent<HTMLDivElement>) => {
      // Only the primary button's release ends the drag; releasing an
      // auxiliary button mid-pan (buttons still include the primary) keeps
      // the gesture alive.
      if ((e.buttons & 1) === 0) finishDrag(e.pointerId)
    },
    [finishDrag],
  )

  const onPointerCancel = useCallback(
    (e: ReactPointerEvent<HTMLDivElement>) => {
      finishDrag(e.pointerId)
    },
    [finishDrag],
  )

  return {
    view,
    setView,
    canvasRef,
    contentRef,
    viewRef,
    didDragRef,
    isPanningRef,
    zoomAt,
    zoomFromCenter,
    fit,
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel,
  }
}
