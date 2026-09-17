// A paper's `paper.html` (the LaTeXML-shaped static export) rendered as
// sanitized static React HTML.
//
// The document is parsed + sanitized up front by `paperHtmlSanitize` (zero
// script / iframe / style / event-handler survival; protocol-constrained
// hrefs/srcs; MathML presentation markup kept). This component renders the
// sanitized hast tree via `hast-util-to-jsx-runtime` with a small component
// layer that re-adds the behaviors a static export cannot carry:
//
// * `img` — relative srcs (`assets/fig1.png`) resolve against the paper
//   directory (then the workspace root) through `readFileAsDataURL` — the
//   webview cannot load file:// URLs, so bytes travel as data: URLs. Failed
//   resolution renders a visible placeholder instead of ever handing the raw
//   relative path to the webview (no remote request).
// * `a` — external hrefs dispatch to the system browser via `openExternalURL`
//   (the Wails webview must not navigate); in-document `#fragment` hrefs
//   scroll to the target element; other (local-file) hrefs are inert.
// * `math` — block-display equations get vertical rhythm + horizontal
//   overflow; MathML itself renders natively in the WebKit webview.
//
// All typography comes from the Tailwind `prose` layer over design tokens —
// the document's own `ltx-*` classes never survive sanitization, so no remote
// stylesheet is loaded. Zoom-safe by construction: no viewport units and no
// pointer-derived positioning anywhere.

import {
  createElement,
  memo,
  useEffect,
  useMemo,
  useRef,
  useState,
  type AnchorHTMLAttributes,
  type ComponentProps,
  type ImgHTMLAttributes,
  type MouseEvent as ReactMouseEvent,
} from 'react'
import { Fragment, jsx, jsxs } from 'react/jsx-runtime'
import { toJsxRuntime, type Components } from 'hast-util-to-jsx-runtime'
import { ImageIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { isExternalUrl } from '@/lib/localFileLink'
import { EXTERNAL_SRC_RE, candidateImagePaths } from '@/lib/markdownImageResolve'
import { readFileAsDataURL } from '@/api/workspace'
import { openExternalURL } from '@/api/runtime'
import {
  PAPER_HTML_MAX_BYTES,
  paperHtmlByteLength,
  sanitizePaperHtml,
} from '@/lib/paperHtmlSanitize'

// --- Anchors -----------------------------------------------------------------

function decodeFragment(fragment: string): string {
  try {
    return decodeURIComponent(fragment)
  } catch {
    // Malformed percent-encoding — fall back to the raw fragment.
    return fragment
  }
}

function PaperHtmlAnchor({ href, className, children, ...rest }: AnchorHTMLAttributes<HTMLAnchorElement>) {
  const handleClick = (event: ReactMouseEvent<HTMLAnchorElement>) => {
    // The webview must never navigate: every click is intercepted. External
    // URLs go to the system browser, #fragments scroll in-document, and
    // anything else (local file hrefs) is inert in v1.
    event.preventDefault()
    if (typeof href !== 'string' || href === '') return
    if (href.startsWith('#')) {
      const target = document.getElementById(decodeFragment(href.slice(1)))
      target?.scrollIntoView({ behavior: 'smooth', block: 'start' })
      return
    }
    if (isExternalUrl(href)) {
      openExternalURL(href)
    }
  }
  return (
    <a href={href} {...rest} onClick={handleClick} className={cn('text-info hover:underline', className)}>
      {children}
    </a>
  )
}

// --- Local images -------------------------------------------------------------

/** 1×1 transparent PNG — reserves no visible space while a local image loads,
 *  avoiding the broken-image icon flicker (same trick as the markdown image). */
const TRANSPARENT_PIXEL =
  'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII='

/** Resolution state of an img src: `'pending'` while candidates are tried,
 *  `'failed'` when none resolve, or the loadable value (data URL / external
 *  URL) once resolved. */
type ImageSrcState = 'pending' | 'failed' | string

function usePaperHtmlImageSrc(
  src: string | undefined,
  baseFilePath: string | null | undefined,
  workspaceRoot: string | null | undefined,
): ImageSrcState {
  // External/data URLs are stable — resolve synchronously on first render so
  // no placeholder frame flashes for them.
  const [state, setState] = useState<ImageSrcState>(() =>
    src !== undefined && EXTERNAL_SRC_RE.test(src) ? src : src === undefined ? 'failed' : 'pending',
  )

  useEffect(() => {
    if (src === undefined) {
      setState('failed')
      return
    }
    if (EXTERNAL_SRC_RE.test(src)) {
      setState(src)
      return
    }
    setState('pending')
    // Same resolution strategy as the markdown renderer: the paper directory
    // first (LaTeXML's assets/ convention), then the workspace root.
    const candidates = candidateImagePaths(src, baseFilePath, workspaceRoot)
    if (candidates.length === 0) {
      setState('failed')
      return
    }
    let cancelled = false
    let index = 0
    const tryNext = () => {
      if (cancelled) return
      if (index >= candidates.length) {
        // No candidate existed on disk — a placeholder, never the raw relative
        // path (which the webview would request from the app origin).
        setState('failed')
        return
      }
      const candidate = candidates[index++]!
      readFileAsDataURL(candidate)
        .then((dataUrl) => {
          if (!cancelled) setState(dataUrl)
        })
        .catch(() => {
          tryNext()
        })
    }
    tryNext()
    return () => {
      cancelled = true
    }
  }, [src, baseFilePath, workspaceRoot])

  return state
}

interface PaperHtmlImageProps extends ImgHTMLAttributes<HTMLImageElement> {
  baseFilePath?: string | null
  workspaceRoot?: string | null
}

function PaperHtmlImage({
  src,
  alt,
  className,
  baseFilePath,
  workspaceRoot,
  ...rest
}: PaperHtmlImageProps) {
  const state = usePaperHtmlImageSrc(
    typeof src === 'string' ? src : undefined,
    baseFilePath,
    workspaceRoot,
  )
  if (state === 'failed') {
    return (
      <span
        data-testid="paper-html-image-missing"
        role="img"
        aria-label={alt !== undefined && alt !== '' ? `Image unavailable: ${alt}` : 'Image unavailable'}
        className="my-1 inline-flex max-w-full items-center gap-1.5 rounded border border-dashed border-border px-2 py-1 align-middle text-xs text-muted-foreground"
      >
        <ImageIcon className="size-3.5 shrink-0" aria-hidden="true" />
        <span className="truncate">
          Image unavailable{typeof src === 'string' && src !== '' ? ` (${src})` : ''}
        </span>
      </span>
    )
  }
  return (
    <img
      data-testid="paper-html-image"
      src={state === 'pending' ? TRANSPARENT_PIXEL : state}
      alt={alt}
      loading="lazy"
      className={cn('max-w-full rounded', className)}
      {...rest}
    />
  )
}

// --- MathML -------------------------------------------------------------------

// React's `JSX.IntrinsicElements` does not type MathML tags (so `<math>` is
// not valid JSX), but React DOM mounts a `math` element with the MathML
// namespace natively — the element is therefore built with `createElement`,
// and the component map needs a single bridging cast for the untyped tag.
type PaperHtmlMathProps = { className?: string; display?: string } & Record<string, unknown>

function PaperHtmlMath({ className, display, ...rest }: PaperHtmlMathProps) {
  // `display="block"` is native MathML; the classes only add vertical rhythm
  // and keep wide equations horizontally scrollable instead of overflowing.
  const isBlock = display === 'block'
  return createElement('math', {
    ...rest,
    display,
    className: cn('align-middle', isBlock && 'my-4 block overflow-x-auto custom-scrollbar text-center', className),
  } as unknown as ComponentProps<'span'>)
}

// --- Component layer wiring -----------------------------------------------------

function createPaperHtmlComponents(
  baseFilePath?: string | null,
  workspaceRoot?: string | null,
): Components {
  return {
    a: PaperHtmlAnchor,
    img: ({ node: _node, ...props }) => (
      <PaperHtmlImage {...props} baseFilePath={baseFilePath} workspaceRoot={workspaceRoot} />
    ),
    // `math` is intentionally not `ComponentProps<'math'>`-typed (see above);
    // it receives the raw sanitized hast properties (alttext, display, …).
    math: PaperHtmlMath as unknown as Components['div'],
  }
}

// --- Resolved anchor reveal -----------------------------------------------------

/** A resolved paper anchor to reveal: the element descriptor from
 *  `resolveHtmlAnchor` (id when the target carries one, else the element-child
 *  index path) plus a nonce so a repeated click on the same anchor
 *  re-triggers the reveal. */
export interface PendingHtmlAnchor {
  id: string | null
  path: number[]
  nonce: number
}

/** Transient highlight for a revealed anchor — the same design token the
 *  extracted-text view uses for its resolved-line highlight. */
const ANCHOR_FLASH_CLASSES = ['bg-highlight/20', 'rounded', 'transition-colors'] as const
const ANCHOR_FLASH_MS = 1800

/** Resolve an anchor descriptor to the live DOM element inside `container`.
 *  The id lookup is scoped to the container (an id elsewhere in the app never
 *  steals the scroll). The path walk counts ELEMENT children only — text
 *  nodes are skipped on both the hast and the rendered side, so the path the
 *  pure resolver computed survives the React render. */
function findHtmlAnchorTarget(
  container: HTMLElement,
  target: { id: string | null; path: number[] },
): HTMLElement | null {
  if (target.id !== null) {
    const byId = document.getElementById(target.id)
    if (byId !== null && container.contains(byId)) return byId
  }
  let node: Node = container
  for (const step of target.path) {
    let seen = -1
    let next: Element | null = null
    for (const child of Array.from(node.childNodes)) {
      if (child.nodeType === Node.ELEMENT_NODE) {
        seen++
        if (seen === step) {
          next = child as Element
          break
        }
      }
    }
    if (next === null) return null
    node = next
  }
  return node instanceof HTMLElement ? node : null
}

// --- Main component -------------------------------------------------------------

export interface PaperHtmlViewProps {
  /** The raw paper.html document text. */
  content: string
  className?: string
  /** Absolute path of the paper.html document — relative img srcs resolve
   *  against its directory first (the `assets/<name>` convention). */
  baseFilePath?: string | null
  /** Workspace root — second-chance resolution base for relative img srcs. */
  workspaceRoot?: string | null
  /** A resolved anchor to reveal (scroll + transient highlight). */
  pendingAnchor?: PendingHtmlAnchor | null
}

/**
 * Sanitized static renderer for a paper's `paper.html`. Memoized on its props
 * so an unchanged document short-circuits before the (cached) parse; content
 * over the size cap renders an explicit message instead of the tree.
 */
export const PaperHtmlView = memo(
  function PaperHtmlView({ content, className, baseFilePath, workspaceRoot, pendingAnchor }: PaperHtmlViewProps) {
    const containerRef = useRef<HTMLDivElement>(null)
    const components = useMemo(
      () => createPaperHtmlComponents(baseFilePath, workspaceRoot),
      [baseFilePath, workspaceRoot],
    )
    const oversize = useMemo(
      () => paperHtmlByteLength(content) > PAPER_HTML_MAX_BYTES,
      [content],
    )
    const body = useMemo(() => {
      if (oversize || content === '') return null
      return toJsxRuntime(sanitizePaperHtml(content), {
        components,
        jsx,
        jsxs,
        Fragment,
      })
    }, [oversize, content, components])

    // Reveal a resolved anchor: scroll it into view and flash a transient
    // highlight. The timer is cleared on re-run/unmount so the classes never
    // outlive their element.
    useEffect(() => {
      if (pendingAnchor === null || pendingAnchor === undefined) return
      const container = containerRef.current
      if (container === null) return
      const el = findHtmlAnchorTarget(container, pendingAnchor)
      if (el === null) return
      el.scrollIntoView({ behavior: 'smooth', block: 'start' })
      el.classList.add(...ANCHOR_FLASH_CLASSES)
      const timer = window.setTimeout(() => {
        el.classList.remove(...ANCHOR_FLASH_CLASSES)
      }, ANCHOR_FLASH_MS)
      return () => {
        window.clearTimeout(timer)
        el.classList.remove(...ANCHOR_FLASH_CLASSES)
      }
    }, [pendingAnchor])

    if (oversize) {
      const mib = paperHtmlByteLength(content) / (1024 * 1024)
      return (
        <div
          data-testid="paper-html-oversize"
          className="px-3 py-4 text-center text-xs text-muted-foreground"
        >
          This paper's HTML is too large to render in-app ({mib.toFixed(1)} MB; the limit is{' '}
          {PAPER_HTML_MAX_BYTES / (1024 * 1024)} MB). Open the file in a browser instead.
        </div>
      )
    }
    if (body === null) return null
    return (
      <div
        ref={containerRef}
        data-testid="paper-html-view"
        className={cn('prose prose-sm max-w-none', className)}
      >
        {body}
      </div>
    )
  },
  (prev, next) =>
    prev.content === next.content &&
    prev.className === next.className &&
    prev.baseFilePath === next.baseFilePath &&
    prev.workspaceRoot === next.workspaceRoot &&
    prev.pendingAnchor?.nonce === next.pendingAnchor?.nonce &&
    prev.pendingAnchor?.id === next.pendingAnchor?.id &&
    prev.pendingAnchor?.path.join(',') === next.pendingAnchor?.path.join(','),
)
