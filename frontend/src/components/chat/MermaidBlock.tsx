import { useEffect, useRef, useState } from 'react'
import mermaid from 'mermaid'

// Initialize mermaid once
let initialized = false

function initMermaid() {
  if (!initialized) {
    mermaid.initialize({
      startOnLoad: false,
      theme: 'dark',
    })
    initialized = true
  }
}

interface MermaidBlockProps {
  code: string
}

export function MermaidBlock({ code }: MermaidBlockProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [svg, setSvg] = useState<string>('')
  const [error, setError] = useState<boolean>(false)
  const idRef = useRef(`mermaid-${Math.random().toString(36).slice(2, 11)}`)

  useEffect(() => {
    initMermaid()

    const renderDiagram = async () => {
      try {
        const { svg: renderedSvg } = await mermaid.render(idRef.current, code.trim())
        setSvg(renderedSvg)
        setError(false)
      } catch {
        setError(true)
        setSvg('')
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
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  )
}
