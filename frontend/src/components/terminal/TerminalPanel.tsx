import { useState, useCallback, useEffect } from 'react'
import { Terminal } from './Terminal'
import { Loader2 } from 'lucide-react'

interface TerminalPanelProps {
    sessionId: string | null
    visible: boolean
}

export function TerminalPanel({ sessionId, visible }: TerminalPanelProps) {
    const [hasBeenVisible, setHasBeenVisible] = useState(false)
    const [isStarting, setIsStarting] = useState(true)

    useEffect(() => {
        if (visible && !hasBeenVisible) {
            setHasBeenVisible(true)
        }
    }, [visible, hasBeenVisible])

    const handleReady = useCallback(() => {
        setIsStarting(false)
    }, [])

    if (!sessionId) {
        return (
            <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
                Start a conversation to use the terminal
            </div>
        )
    }

    if (!hasBeenVisible) {
        return null
    }

    return (
        <div className="relative w-full h-full">
            {isStarting && (
                <div className="absolute inset-0 z-10 flex items-center justify-center bg-background/80">
                    <Loader2 className="h-5 w-5 animate-spin text-primary" />
                </div>
            )}
            <Terminal key={sessionId} sessionId={sessionId} visible={visible} onReady={handleReady} />
        </div>
    )
}
