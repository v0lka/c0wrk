import { useEffect, useRef, useState } from 'react'
import mermaid from 'mermaid'

interface MermaidBlockProps {
  code: string
}

export function MermaidBlock({ code }: MermaidBlockProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const initializedRef = useRef(false)
  const [error, setError] = useState<boolean>(false)
  const idRef = useRef(`mermaid-${Math.random().toString(36).slice(2, 11)}`)

  useEffect(() => {
    // Initialize mermaid once using ref
    if (!initializedRef.current) {
      mermaid.initialize({
        startOnLoad: false,
        theme: 'dark',
      })
      initializedRef.current = true
    }

    const renderDiagram = async () => {
      if (!containerRef.current) return
      try {
        const { svg } = await mermaid.render(idRef.current, code.trim())
        // Use ref-based DOM manipulation instead of dangerouslySetInnerHTML
        containerRef.current.innerHTML = svg
        setError(false)
      } catch {
        setError(true)
        if (containerRef.current) {
          containerRef.current.innerHTML = ''
        }
      }
    }

    renderDiagram()
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
      className="mermaid-container bg-muted rounded-lg p-4 overflow-x-auto"
    />
  )
}
