import { useState, useEffect, type ReactNode } from 'react'
import { BookOpen, Wrench, Lightbulb } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { GetMemoryStats } from '../../../wailsjs/go/main/App'

interface MemoryCardProps {
  title: string
  icon: ReactNode
  count: number
  description: string
  colorClass: string
}

function MemoryCard({ title, icon, count, description, colorClass }: MemoryCardProps) {
  return (
    <div className="p-4 border border-border rounded-lg bg-card hover:bg-accent/30 transition-colors">
      <div className="flex items-start justify-between mb-2">
        <div className={`p-2 rounded-md ${colorClass}`}>
          {icon}
        </div>
        <Badge variant="secondary" className="text-xs">
          {count.toLocaleString()}
        </Badge>
      </div>
      <h4 className="text-sm font-medium mb-1">{title}</h4>
      <p className="text-xs text-muted-foreground">{description}</p>
    </div>
  )
}



export function GlobalView() {
  // Memory stats from backend
  const [memStats, setMemStats] = useState({ semantic: 0, procedural: 0, reflexion: 0 })
  
  useEffect(() => {
    const fetchStats = () => {
      GetMemoryStats()
        .then((stats) => {
          setMemStats({
            semantic: stats.semantic ?? 0,
            procedural: stats.procedural ?? 0,
            reflexion: stats.reflexion ?? 0,
          })
        })
        .catch((err) => console.warn('GetMemoryStats failed:', err))
    }
    
    fetchStats() // initial fetch
    const interval = setInterval(fetchStats, 10000) // refresh every 10s
    return () => clearInterval(interval)
  }, [])

  return (
    <div className="space-y-3">
      <MemoryCard
        title="Semantic Memory"
        icon={<BookOpen className="h-4 w-4" />}
        count={memStats.semantic}
        description="Facts and learned knowledge"
        colorClass="bg-emerald-500/10 text-emerald-500"
      />
      <MemoryCard
        title="Procedural Memory"
        icon={<Wrench className="h-4 w-4" />}
        count={memStats.procedural}
        description="Skills and available tools"
        colorClass="bg-orange-500/10 text-orange-500"
      />
      <MemoryCard
        title="Reflections"
        icon={<Lightbulb className="h-4 w-4" />}
        count={memStats.reflexion}
        description="Self-correction insights"
        colorClass="bg-yellow-500/10 text-yellow-500"
      />
    </div>
  )
}
