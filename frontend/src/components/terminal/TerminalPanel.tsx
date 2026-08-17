import { useEffect, useState } from 'react'
import { Terminal } from './Terminal'
import { Loader2 } from 'lucide-react'
import { useTerminalRegistryStore } from '@/stores/terminalRegistryStore'
import { cn } from '@/lib/utils'

interface TerminalPanelProps {
    sessionId: string | null
    visible: boolean
}

/**
 * TerminalPanel owns the per-session terminal instances.
 *
 * Terminals are kept alive for the app lifetime: every session for which the
 * terminal was ever opened keeps its Terminal component (xterm.js instance
 * with scrollback + backend PTY) mounted; switching sessions or projects only
 * toggles which instance is visible. This is what makes terminals survive
 * switches instead of re-initializing every time.
 *
 * The instance registry lives in terminalRegistryStore; entries are dropped
 * only on explicit session/project deletion (the backend stops those PTYs at
 * the same points). See the store for why liveness is NOT derived from the
 * sessionStore list.
 */
export function TerminalPanel({ sessionId, visible }: TerminalPanelProps) {
    const [hasBeenVisible, setHasBeenVisible] = useState(false)
    const instances = useTerminalRegistryStore((s) => s.instances)
    const readySessions = useTerminalRegistryStore((s) => s.readySessions)
    const ensureInstance = useTerminalRegistryStore((s) => s.ensureInstance)
    const markReady = useTerminalRegistryStore((s) => s.markReady)

    useEffect(() => {
        if (visible && !hasBeenVisible) {
            setHasBeenVisible(true)
        }
    }, [visible, hasBeenVisible])

    // Register the active session's terminal the first time the terminal
    // panel is shown for it. The instance then lives until the session is
    // deleted or the app closes.
    useEffect(() => {
        if (!visible || !sessionId) return
        ensureInstance(sessionId)
    }, [visible, sessionId, ensureInstance])

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

    // The overlay only covers the initial start of the ACTIVE session's
    // terminal; re-activating a kept-alive instance is instant.
    const activeStarting = !readySessions.has(sessionId)

    return (
        <div className="relative w-full h-full">
            {activeStarting && (
                <div className="absolute inset-0 z-10 flex items-center justify-center bg-background/80">
                    <Loader2 className="h-5 w-5 animate-spin text-primary" />
                </div>
            )}
            {instances.map((id) => (
                <div key={id} className={cn('absolute inset-0', id !== sessionId && 'hidden')}>
                    <Terminal
                        sessionId={id}
                        visible={visible && id === sessionId}
                        isActive={id === sessionId}
                        onReady={markReady}
                    />
                </div>
            ))}
        </div>
    )
}
