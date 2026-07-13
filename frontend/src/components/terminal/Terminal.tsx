import { useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { terminalInput, terminalResize, startTerminal, startTerminalInDir, stopTerminal } from '@/api/terminal'
import { useTerminalEvents } from '@/hooks/events/useTerminalEvents'
import { useXTermTheme } from '@/hooks/useXTermTheme'
import { useInputModeStore } from '@/stores/inputModeStore'
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
    const theme = useXTermTheme()

    // Subscribe to terminal output at the top level; the callback safely accesses
    // termRef.current (set in useEffect below, but events only flow after startTerminal).
    useTerminalEvents({
        sessionId,
        onOutput: (bytes: Uint8Array) => { termRef.current?.write(bytes) },
    })

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

        // Consume a pending terminal directory (set by "Open in Terminal" from
        // the file-tree context menu). When set, start the terminal in that
        // directory instead of the session workspace default.
        const pendingDir = useInputModeStore.getState().pendingTerminalDir
        const startPromise = pendingDir
          ? startTerminalInDir(sessionId, pendingDir)
          : startTerminal(sessionId)
        if (pendingDir) {
          useInputModeStore.getState().clearPendingTerminalDir()
        }

        startPromise.then(() => {
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
            stopTerminal(sessionId).catch((err) => {
                logger.error('Failed to stop terminal:', err)
            })
            term.dispose()
            termRef.current = null
            fitAddonRef.current = null
        }
    }, [sessionId, onReady, theme])

    // Watch for "Open in Terminal" requests from the file-tree context menu
    // that arrive after the terminal is already running. The initial mount
    // case is handled by the main effect above (which reads pendingTerminalDir
    // from the store); this effect handles subsequent directory changes by
    // restarting the terminal in the new directory.
    const pendingTerminalDir = useInputModeStore((s) => s.pendingTerminalDir)
    const isFirstDirRun = useRef(true)
    useEffect(() => {
        if (isFirstDirRun.current) {
            isFirstDirRun.current = false
            return
        }
        if (!pendingTerminalDir) return
        startTerminalInDir(sessionId, pendingTerminalDir)
            .then(() => {
                useInputModeStore.getState().clearPendingTerminalDir()
                termRef.current?.focus()
            })
            .catch((err) => {
                logger.error('Failed to restart terminal in directory:', err)
                termRef.current?.writeln(
                    `\r\n\x1b[31mFailed to start terminal: ${err instanceof Error ? err.message : String(err)}\x1b[0m`,
                )
                useInputModeStore.getState().clearPendingTerminalDir()
            })
    }, [pendingTerminalDir, sessionId])

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
