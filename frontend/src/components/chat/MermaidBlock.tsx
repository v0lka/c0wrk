import { useEffect, useRef, useState } from 'react'
import mermaid from 'mermaid'

interface MermaidBlockProps {
  code: string
}

export function MermaidBlock({ code }: MermaidBlockProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [error, setError] = useState<boolean>(false)
  const idRef = useRef(`mermaid-${crypto.randomUUID()}`)

  // Initialize mermaid once on mount
  useEffect(() => {
    mermaid.initialize({
      startOnLoad: false,
      theme: 'dark',
    })
  }, [])

  // Render diagram when code changes
  useEffect(() => {
    let isMounted = true

    const renderDiagram = async () => {
      if (!containerRef.current) return
      try {
        const { svg } = await mermaid.render(idRef.current, code.trim())
        if (!isMounted) return
        // Safely insert SVG without direct innerHTML on the container
        const temp = document.createElement('div')
        temp.innerHTML = svg
        const svgEl = temp.firstElementChild
        containerRef.current.replaceChildren()
        if (svgEl) containerRef.current.appendChild(svgEl)
        setError(false)
      } catch {
        if (isMounted) {
          setError(true)
          if (containerRef.current) {
            containerRef.current.replaceChildren()
          }
        }
      }
    }

    renderDiagram()

    return () => {
      isMounted = false
    }
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
