import { isValidElement, useEffect, useMemo, useState, type AnchorHTMLAttributes, type ReactNode } from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkEmoji from 'remark-emoji'
import remarkBreaks from 'remark-breaks'
import rehypeSlug from 'rehype-slug'
import rehypeAutolinkHeadings from 'rehype-autolink-headings'
import rehypeHighlight from 'rehype-highlight'
import rehypeExternalLinks from 'rehype-external-links'
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize'
import { cn } from '@/lib/utils'
import { isLocalFileHref, isExternalUrl, parseLocalFileHref, normalizePath } from '@/lib/localFileLink'
import { readFileAsDataURL } from '@/api/workspace'
import { openExternalURL } from '@/api/runtime'
import { EXTERNAL_SRC_RE, candidateImagePaths } from '@/lib/markdownImageResolve'
import { resolveChatWorkspaceRoot } from '@/lib/chatWorkspaceRoot'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { MermaidBlock } from '@/components/chat/MermaidBlock'
import type { PluggableList } from 'unified'

// Custom sanitize schema — extends default to allow highlight.js classes,
// heading IDs, task list inputs, and mermaid container styles.
const customSanitizeSchema = {
  ...defaultSchema,
  attributes: {
    ...defaultSchema.attributes,
    code: ['className'],
    pre: ['className'],
    span: ['className'],
    div: ['className', 'style'],
    h1: ['id'],
    h2: ['id'],
    h3: ['id'],
    h4: ['id'],
    h5: ['id'],
    h6: ['id'],
    a: ['href', 'target', 'rel', 'className'],
    input: ['type', 'checked', 'disabled'],
  },
}

const remarkPlugins: PluggableList = [remarkGfm, remarkEmoji, remarkBreaks]

const rehypePlugins: PluggableList = [
  rehypeSlug,
  [rehypeAutolinkHeadings, { behavior: 'wrap' }],
  rehypeHighlight,
  [rehypeExternalLinks, { target: '_blank', rel: ['noopener', 'noreferrer'] }],
  [rehypeSanitize, customSanitizeSchema],
]

// --- Custom link component for local file navigation ---

type MarkdownLinkProps = AnchorHTMLAttributes<HTMLAnchorElement> & {
  node?: unknown
  workspaceRoot?: string | null
}

const MarkdownLink = ({ href, children, node: _node, workspaceRoot, ...rest }: MarkdownLinkProps) => {
  // External URLs (http, https, mailto, ftp, …) must be dispatched to the
  // system browser. The Wails webview ignores `<a target="_blank">` or opens
  // it inside the webview, which cannot render arbitrary web pages. We render
  // a non-navigating anchor and route clicks through `openExternalURL`, which
  // calls `runtime.BrowserOpenURL` (open / xdg-open / Windows shell handler).
  if (isExternalUrl(href)) {
    return (
      <a
        href={href}
        {...rest}
        onClick={(e) => {
          e.preventDefault()
          openExternalURL(href!)
        }}
      >
        {children}
      </a>
    )
  }

  if (!isLocalFileHref(href)) {
    return <a href={href} {...rest}>{children}</a>
  }

  const handleClick = async () => {
    const { path, line } = parseLocalFileHref(href!)
    const rootPath = workspaceRoot || (await resolveChatWorkspaceRoot())
    if (!rootPath) return
    const resolved = normalizePath(rootPath, path)
    if (line !== undefined) {
      useFileViewerStore.getState().openFileAtLine(resolved, line)
    } else {
      useFileViewerStore.getState().openFile(resolved)
    }
  }

  return (
    <span
      className="text-info hover:underline cursor-pointer"
      onClick={handleClick}
      role="link"
      tabIndex={0}
      onKeyDown={(e) => { if (e.key === 'Enter') void handleClick() }}
    >
      {children}
    </span>
  )
}

// --- Local image embedding for the file viewer ---
//
// The webview cannot load file:// or project-root-relative URLs, so images
// that live on disk are fetched as base64 data: URLs via the ReadFileAsDataURL
// RPC. Relative paths are resolved against the viewed markdown file's
// directory first, then against the workspace root as a fallback (this covers
// both the "image next to the .md" and "image path relative to repo root"
// conventions). External (http/https/data) URLs and absolute disk paths are
// left untouched.

/** 1×1 transparent PNG — used as a placeholder while a local image loads or
 *  when resolution fails, to avoid the broken-image icon flicker. */
const TRANSPARENT_PIXEL =
  'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII='

interface MarkdownImageProps {
  src?: string
  alt?: string
  title?: string
  baseFilePath?: string | null
  workspaceRoot?: string | null
}

/** Resolves a markdown image src to a loadable value (data URL or external URL). */
function MarkdownImage({ src, alt, title, baseFilePath, workspaceRoot }: MarkdownImageProps) {
  const resolved = useMarkdownImageSrc(src, baseFilePath, workspaceRoot)
  return <img src={resolved ?? TRANSPARENT_PIXEL} alt={alt} title={title} loading="lazy" />
}

/**
 * Hook that resolves a markdown image src. External/data URLs and unresolvable
 * relative paths are returned verbatim; local relative/absolute paths are
 * fetched as data URLs via the backend RPC, trying candidates in order.
 */
function useMarkdownImageSrc(
  src: string | undefined,
  baseFilePath: string | null | undefined,
  workspaceRoot: string | null | undefined,
): string | undefined {
  const [resolved, setResolved] = useState<string | undefined>(() =>
    // External/data URLs are stable — resolve synchronously on first render.
    src && EXTERNAL_SRC_RE.test(src) ? src : undefined,
  )

  useEffect(() => {
    if (!src) {
      setResolved(undefined)
      return
    }
    // External/data URLs pass through unchanged.
    if (EXTERNAL_SRC_RE.test(src)) {
      setResolved(src)
      return
    }
    // Local (relative or absolute disk) path — reset any previously resolved
    // value so the TRANSPARENT_PIXEL placeholder shows while the next image
    // loads, instead of briefly displaying the stale previous image when src
    // changes between two local images.
    setResolved(undefined)
    const candidates = candidateImagePaths(src, baseFilePath, workspaceRoot)
    if (candidates.length === 0) {
      // No base to resolve against — leave src as-is (will likely fail to load
      // in the webview, matching previous behavior).
      setResolved(src)
      return
    }

    let cancelled = false
    let idx = 0
    const tryNext = () => {
      if (cancelled || idx >= candidates.length) {
        if (!cancelled) setResolved(src)
        return
      }
      const candidate = candidates[idx++]!
      readFileAsDataURL(candidate)
        .then((dataUrl) => { if (!cancelled) setResolved(dataUrl) })
        .catch(() => { tryNext() })
    }
    tryNext()
    return () => { cancelled = true }
  }, [src, baseFilePath, workspaceRoot])

  return resolved
}

/**
 * Flatten a react-markdown `<code>` element's children into a plain string.
 * Mermaid source is normally a single text node, but rehype-highlight may
 * leave token spans behind — recurse through them to recover the raw text.
 */
function codeToString(children: ReactNode): string {
  if (children == null) return ''
  if (typeof children === 'string' || typeof children === 'number') return String(children)
  if (Array.isArray(children)) return children.map(codeToString).join('')
  if (isValidElement(children)) {
    return codeToString((children.props as { children?: ReactNode }).children)
  }
  return ''
}

/**
 * Intercepts fenced code blocks. A ```mermaid block is handed off to
 * {@link MermaidBlock} for interactive rendering; everything else renders as a
 * normal highlighted `<pre>` (the default react-markdown `<pre>` wrapper).
 */
const MarkdownPre: Components['pre'] = ({ children }) => {
  const child = Array.isArray(children) ? children[0] : children
  if (isValidElement(child)) {
    const props = child.props as { className?: string; children?: ReactNode }
    if (props.className && /language-mermaid/.test(props.className)) {
      return <MermaidBlock code={codeToString(props.children).replace(/\n$/, '')} />
    }
  }
  return <pre>{children}</pre>
}

function createMarkdownComponents(
  baseFilePath?: string | null,
  workspaceRoot?: string | null,
): Components {
  return {
    a: ((props: MarkdownLinkProps) => (
      <MarkdownLink {...props} workspaceRoot={workspaceRoot} />
    )) as Components['a'],
    pre: MarkdownPre,
    img: ({ src, alt, title, node: _node }) => (
      <MarkdownImage
        src={src}
        alt={alt}
        title={title}
        baseFilePath={baseFilePath}
        workspaceRoot={workspaceRoot}
      />
    ),
  }
}

// --- Markdown wrapper component ---

interface MarkdownProps {
  content: string
  className?: string
  compact?: boolean
  /** Absolute path of the markdown document being rendered. Used to resolve
   *  relative image paths (tried before the workspace root). */
  baseFilePath?: string | null
  /** Workspace/project root used to resolve relative image paths as a
   *  fallback when they aren't found relative to the document. */
  workspaceRoot?: string | null
}

export function Markdown({ content, className, compact, baseFilePath, workspaceRoot }: MarkdownProps) {
  const components = useMemo(
    () => createMarkdownComponents(baseFilePath, workspaceRoot),
    [baseFilePath, workspaceRoot],
  )
  return (
    <div className={cn('prose prose-sm max-w-none', compact && 'prose-xs', className)}>
      <ReactMarkdown
        remarkPlugins={remarkPlugins}
        rehypePlugins={rehypePlugins}
        components={components}
      >
        {content}
      </ReactMarkdown>
    </div>
  )
}
