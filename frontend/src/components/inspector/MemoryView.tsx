import { useState, useEffect, type ReactNode } from 'react'
import { Brain, BookOpen, Wrench, Lightbulb } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { useSessionStore } from '@/stores/sessionStore'
import { GetMemoryStats, GetSessionMemoryStats } from '../../../wailsjs/go/main/App'

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
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  
  // Memory stats from backend
  const [memStats, setMemStats] = useState({ semantic: 0, procedural: 0, reflexion: 0 })
  const [episodicCount, setEpisodicCount] = useState(0)
  const [fetchError, setFetchError] = useState<string | null>(null)
  
  useEffect(() => {
    const fetchStats = () => {
      GetMemoryStats()
        .then((stats) => {
          setMemStats({
            semantic: stats.semantic ?? 0,
            procedural: stats.procedural ?? 0,
            reflexion: stats.reflexion ?? 0,
          })
          setFetchError(null)
        })
        .catch((err) => {
          console.error('GetMemoryStats failed:', err)
          setFetchError('Failed to load memory stats')
        })
    }
    
    fetchStats() // initial fetch
    const interval = setInterval(fetchStats, 10000) // refresh every 10s
    return () => clearInterval(interval)
  }, [])
  
  useEffect(() => {
    if (!activeSessionId) return
    
    const fetchStats = () => {
      GetSessionMemoryStats(activeSessionId)
        .then((stats) => {
          setEpisodicCount(stats.episodic ?? 0)
        })
        .catch((err) => {
          console.error('GetSessionMemoryStats failed:', err)
          // Don't override global error state for session-specific error
        })
    }
    
    fetchStats() // initial fetch
    const interval = setInterval(fetchStats, 10000) // refresh every 10s
    return () => clearInterval(interval)
  }, [activeSessionId])

  if (fetchError) {
    return (
      <div className="text-center py-8 text-muted-foreground text-sm">
        <p className="text-destructive">{fetchError}</p>
        <p className="text-xs mt-1">Stats will refresh automatically</p>
      </div>
    )
  }

  return (
    <div className="space-y-3">
      <MemoryCard
        title="Episodic Memory"
        icon={<Brain className="h-4 w-4" />}
        count={episodicCount}
        description="Past conversations and events"
        colorClass="bg-blue-500/10 text-blue-500"
      />
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
