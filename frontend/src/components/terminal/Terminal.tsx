import { useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { terminalInput, terminalResize, startTerminal, stopTerminal } from '@/api/terminal'
import { onSessionEvent } from '@/api/runtime'
import { useXTermTheme } from '@/hooks/useXTermTheme'
import { logger } from '@/lib/logger'

interface TerminalProps {
    sessionId: string
    visible: boolean
    onReady?: () => void
}

export function Terminal({ sessionId, visible, onReady }: TerminalProps) {
    const containerRef = useRef<HTMLDivElement>(null)
    const termRef = useRef<XTerm | null>(null)
    const fitAddonRef = useRef<FitAddon | null>(null)
    const unsubscribeRef = useRef<(() => void) | null>(null)
    const theme = useXTermTheme()

    useEffect(() => {
        const container = containerRef.current
        if (!container) return

        const term = new XTerm({
            cursorBlink: true,
            fontSize: 10,
            fontFamily: 'SauceCodePro NF, Menlo, Monaco, "Courier New", monospace',
            theme,
            scrollback: 10000,
        })

        const fitAddon = new FitAddon()
        term.loadAddon(fitAddon)

        term.open(container)
        fitAddon.fit()
        term.focus()

        term.onData((data) => {
            terminalInput(sessionId, data).catch((err) => {
                logger.error('Terminal input error:', err)
                term.writeln(`\r\n\x1b[31m[Input error: ${err instanceof Error ? err.message : String(err)}]\x1b[0m`)
            })
        })

        termRef.current = term
        fitAddonRef.current = fitAddon

        const unsubscribe = onSessionEvent(sessionId, 'terminal_output', (data) => {
            // Data is base64-encoded to preserve raw PTY bytes through JSON serialization.
            // Without base64, invalid UTF-8 (e.g. split multi-byte sequences across read
            // boundaries) gets replaced with U+FFFD by Go's json.Marshal, corrupting
            // escape sequences used by shell completion, autosuggestions, and cursor movement.
            const decoded = atob(data.data)
            const bytes = new Uint8Array(decoded.length)
            for (let i = 0; i < decoded.length; i++) {
                bytes[i] = decoded.charCodeAt(i)
            }
            term.write(bytes)
        })
        unsubscribeRef.current = unsubscribe

        startTerminal(sessionId).then(() => {
            onReady?.()
        }).catch((err) => {
            logger.error('Failed to start terminal:', err)
            term.writeln(`\r\n\x1b[31mFailed to start terminal: ${err instanceof Error ? err.message : String(err)}\x1b[0m`)
            onReady?.()
        })

        const handleResize = () => {
            if (!fitAddonRef.current || !termRef.current) return
            try {
                fitAddonRef.current.fit()
                const { cols, rows } = termRef.current
                if (cols > 0 && rows > 0) {
                    terminalResize(sessionId, cols, rows).catch((err) => {
                        logger.error('Terminal resize error:', err)
                    })
                }
            } catch {
                // FitAddon can throw if terminal is not fully initialized
            }
        }

        const ro = new ResizeObserver(() => {
            handleResize()
        })
        ro.observe(container)

        const resizeTimeout = setTimeout(handleResize, 50)

        return () => {
            clearTimeout(resizeTimeout)
            ro.disconnect()
            unsubscribe()
            stopTerminal(sessionId).catch((err) => {
                logger.error('Failed to stop terminal:', err)
            })
            term.dispose()
            termRef.current = null
            fitAddonRef.current = null
            unsubscribeRef.current = null
        }
    }, [sessionId, onReady, theme])

    useEffect(() => {
        let timer: ReturnType<typeof setTimeout> | undefined
        if (visible && termRef.current) {
            timer = setTimeout(() => {
                termRef.current?.focus()
                try {
                    fitAddonRef.current?.fit()
                } catch {
                    // FitAddon can throw if terminal is not fully initialized
                }
            }, 50)
        } else if (!visible && termRef.current) {
            termRef.current.blur()
        }
        return () => {
            if (timer) clearTimeout(timer)
        }
    }, [visible])

    const handleClick = () => {
        termRef.current?.focus()
    }

    return <div ref={containerRef} className="w-full h-full" onClick={handleClick} />
}
