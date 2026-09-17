import { useCallback, useEffect, useRef, useState } from 'react'
import { ImageOff, Maximize, ZoomIn, ZoomOut } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DEFAULT_ZOOM_STEP,
  INITIAL_VIEW,
  usePanZoom,
  type Size,
} from '@/lib/usePanZoom'
import { fileNameFromPath } from '@/lib/fileViewerUtils'

interface ImageFileViewerProps {
  /** Base64 `data:` URL of the image bytes (from ReadImageAsDataURL). */
  dataUrl: string
  /** Full file path — used for the alt text and the failure notice. */
  path: string
}

/**
 * ImageFileViewer renders an image file in the file-viewer pane.
 *
 * The image lives inside a fixed "canvas" viewport that supports:
 *   - drag-to-pan,
 *   - wheel-to-zoom toward the cursor,
 *   - a floating toolbar: zoom out / live percentage / zoom in / actual size
 *     (1:1) / fit.
 *
 * On load the image is scaled to fit the canvas width (never upscaled), which
 * is also what the fit button restores. All pan/zoom behavior (anchored zoom,
 * fit, drag-to-pan, wheel handling, UI-scale compensation) lives in the
 * reusable `usePanZoom` hook; this component only supplies the image's natural
 * size.
 */
export function ImageFileViewer({ dataUrl, path }: ImageFileViewerProps) {
  const imgRef = useRef<HTMLImageElement>(null)
  const [failed, setFailed] = useState(false)

  // Natural (scale-1) size of the loaded image — the hook's fit() reads it.
  const getNaturalSize = useCallback((): Size | null => {
    const img = imgRef.current
    if (!img || !img.naturalWidth || !img.naturalHeight) return null
    return { width: img.naturalWidth, height: img.naturalHeight }
  }, [])

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
  } = usePanZoom({ getNaturalSize })

  // A new image resets the camera, then fits once its bytes are decodable.
  // `img.complete` covers the cached case, where the browser may have already
  // fired `load` before React attached the handler (so onLoad never runs).
  useEffect(() => {
    setFailed(false)
    setView(INITIAL_VIEW)
    const img = imgRef.current
    if (img?.complete && img.naturalWidth) fit()
  }, [dataUrl, fit, setView])

  // Actual size (100%): scale 1, centered in the canvas. Unlike fit() this may
  // exceed the viewport, letting the user pan around a pixel-exact image.
  const actualSize = useCallback(() => {
    const canvas = canvasRef.current
    const natural = getNaturalSize()
    if (!canvas || !natural) return
    setView({
      scale: 1,
      x: (canvas.clientWidth - natural.width) / 2,
      y: (canvas.clientHeight - natural.height) / 2,
    })
  }, [canvasRef, getNaturalSize, setView])

  return (
    <div className="relative flex flex-1 flex-col min-h-0 overflow-hidden">
      <div className="absolute right-2 top-2 z-10 flex items-center gap-0.5 rounded-md border border-border bg-background/85 p-0.5 shadow-sm backdrop-blur">
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={() => zoomFromCenter(1 / DEFAULT_ZOOM_STEP)}
          title="Zoom out"
          aria-label="Zoom out"
        >
          <ZoomOut className="size-4" />
        </Button>
        <span
          className="min-w-[4ch] select-none text-center text-xs tabular-nums text-muted-foreground"
          aria-live="polite"
        >
          {`${Math.round(view.scale * 100)}%`}
        </span>
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={() => zoomFromCenter(DEFAULT_ZOOM_STEP)}
          title="Zoom in"
          aria-label="Zoom in"
        >
          <ZoomIn className="size-4" />
        </Button>
        <Button
          variant="ghost"
          size="icon-xs"
          className="w-auto px-1.5"
          onClick={actualSize}
          title="Actual size (100%)"
          aria-label="Actual size (100%)"
        >
          <span className="text-xs tabular-nums">1:1</span>
        </Button>
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={fit}
          title="Fit to view"
          aria-label="Fit image to view"
        >
          <Maximize className="size-4" />
        </Button>
      </div>

      <div
        ref={canvasRef}
        className="relative flex-1 cursor-grab touch-none select-none overflow-hidden bg-muted/20 active:cursor-grabbing"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerCancel}
      >
        {failed ? (
          <div className="flex h-full w-full flex-col items-center justify-center gap-2 p-4 text-muted-foreground">
            <ImageOff className="size-8" />
            <p className="text-center text-sm">Could not display this image.</p>
          </div>
        ) : (
          <div
            ref={contentRef}
            className="pointer-events-none absolute left-0 top-0 origin-top-left"
            style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }}
          >
            <img
              ref={imgRef}
              src={dataUrl}
              alt={fileNameFromPath(path)}
              draggable={false}
              className="block h-auto max-w-none"
              onLoad={fit}
              onError={() => setFailed(true)}
            />
          </div>
        )}
      </div>
    </div>
  )
}
