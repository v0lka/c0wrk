import { useEffect, useRef, useState } from 'react'
import { useThemeStore } from '@/stores/themeStore'

interface MermaidBlockProps {
  code: string
}

export function MermaidBlock({ code }: MermaidBlockProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [error, setError] = useState(false)
  const idRef = useRef(`mermaid-${crypto.randomUUID()}`)
  const theme = useThemeStore((s) => s.theme)

  useEffect(() => {
    let cancelled = false

    async function renderDiagram() {
      if (!containerRef.current) return
      try {
        const { default: mermaid } = await import('mermaid')
        if (cancelled) return
        mermaid.initialize({ startOnLoad: false, theme: theme === 'light' ? 'default' : 'dark' })
        const { svg } = await mermaid.render(idRef.current, code.trim())
        if (cancelled) return
        const parser = new DOMParser()
        const doc = parser.parseFromString(svg, 'image/svg+xml')
        const svgEl = doc.documentElement
        containerRef.current.replaceChildren()
        if (svgEl && svgEl.tagName === 'svg') containerRef.current.appendChild(svgEl)
        setError(false)
      } catch {
        if (!cancelled) {
          setError(true)
          if (containerRef.current) containerRef.current.replaceChildren()
        }
      }
    }

    renderDiagram()
    return () => { cancelled = true }
  }, [code, theme])

  if (error) {
    return (
      <div className="bg-muted rounded-lg p-4 text-muted-foreground text-sm">
        Failed to render diagram
      </div>
    )
  }

  return (
    <div
      ref={containerRef}
      className="mermaid-container bg-muted rounded-lg p-4 overflow-x-auto max-w-full h-auto"
    />
  )
}
