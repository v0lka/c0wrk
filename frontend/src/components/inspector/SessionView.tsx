import { useState, useEffect, type ReactNode } from 'react'
import { Brain } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { useSessionStore } from '@/stores/sessionStore'
import { GetSessionMemoryStats } from '../../../wailsjs/go/main/App'

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

export function SessionView() {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  
  // Episodic memory count from backend
  const [episodicCount, setEpisodicCount] = useState(0)
  
  useEffect(() => {
    if (!activeSessionId) return
    
    const fetchStats = () => {
      GetSessionMemoryStats(activeSessionId)
        .then((stats) => {
          setEpisodicCount(stats.episodic ?? 0)
        })
        .catch((err) => console.warn('GetSessionMemoryStats failed:', err))
    }
    
    fetchStats() // initial fetch
    const interval = setInterval(fetchStats, 10000) // refresh every 10s
    return () => clearInterval(interval)
  }, [activeSessionId])
  
  return (
    <div className="space-y-3">
      <MemoryCard
        title="Episodic Memory"
        icon={<Brain className="h-4 w-4" />}
        count={episodicCount}
        description="Past conversations and events"
        colorClass="bg-blue-500/10 text-blue-500"
      />
    </div>
  )
}
