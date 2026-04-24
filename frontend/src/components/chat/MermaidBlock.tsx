import { useEffect, useRef, useState } from 'react'

interface MermaidBlockProps {
  code: string
}

export function MermaidBlock({ code }: MermaidBlockProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [error, setError] = useState(false)
  const idRef = useRef(`mermaid-${crypto.randomUUID()}`)

  useEffect(() => {
    let cancelled = false

    async function renderDiagram() {
      if (!containerRef.current) return
      try {
        const { default: mermaid } = await import('mermaid')
        if (cancelled) return
        mermaid.initialize({ startOnLoad: false, theme: 'dark' })
        const { svg } = await mermaid.render(idRef.current, code.trim())
        if (cancelled) return
        const temp = document.createElement('div')
        temp.innerHTML = svg
        const svgEl = temp.firstElementChild
        containerRef.current.replaceChildren()
        if (svgEl) containerRef.current.appendChild(svgEl)
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
  }, [code])

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
      className="mermaid-container bg-muted rounded-lg p-4 overflow-x-auto max-w-full"
      style={{ height: 'auto' }}
    />
  )
}
