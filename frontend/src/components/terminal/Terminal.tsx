import { useCallback, useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { terminalInput, terminalResize, startTerminal, startTerminalInDir } from '@/api/terminal'
import { useTerminalEvents } from '@/hooks/events/useTerminalEvents'
import { useXTermTheme } from '@/hooks/useXTermTheme'
import { useInputModeStore } from '@/stores/inputModeStore'
import { useThemeStore, selectActiveThemeType } from '@/stores/themeStore'
import { useUiScaleStore } from '@/stores/uiScaleStore'
import { logger } from '@/lib/logger'

interface TerminalProps {
    sessionId: string
    visible: boolean
    /** True only for the instance backing the currently active session.
     * Gates global "Open in Terminal" requests (pendingTerminalDir) so
     * exactly one mounted instance — the active one — consumes them. */
    isActive: boolean
    onReady?: (sessionId: string) => void
}

/**
 * A single session's terminal. The component is mounted for the app lifetime
 * once the session's terminal is first opened (see TerminalPanel): switching
 * sessions/projects only toggles visibility — the xterm.js instance (with its
 * scrollback) and the backend PTY stay alive. The shell is NOT stopped on
 * unmount; unmounting only happens when the session is deleted (backend
 * already stopped that PTY) or the app closes (backend StopAll on shutdown).
 *
 * If the shell exits on its own (user typed `exit`), the backend emits
 * terminal_exited; the instance then resurrects the shell lazily — on next
 * activation, click, or keystroke — preserving the existing buffer.
 */
export function Terminal({ sessionId, visible, isActive, onReady }: TerminalProps) {
    const containerRef = useRef<HTMLDivElement>(null)
    const termRef = useRef<XTerm | null>(null)
    const fitAddonRef = useRef<FitAddon | null>(null)
    const appTheme = useThemeStore(selectActiveThemeType)
    const palette = useXTermTheme(appTheme)
    // Latest palette kept in a ref so the terminal-creation effect can read the
    // current palette at construction time without listing it in its
    // dependency array (which would tear down and restart the session on every
    // theme switch). A separate effect below applies palette changes live.
    const paletteRef = useRef(palette)
    paletteRef.current = palette

    // Dead-shell tracking: set on terminal_exited, cleared when a restart
    // succeeded. restarting guards against concurrent restart attempts.
    const endedRef = useRef(false)
    const restartingRef = useRef(false)
    // Keystrokes typed while a restart is in flight are accumulated here and
    // replayed once the new shell is up, so no keystroke is lost.
    const pendingInputRef = useRef('')

    // Subscribe to terminal output at the top level; the callback safely accesses
    // termRef.current (set in useEffect below, but events only flow after startTerminal).
    useTerminalEvents({
        sessionId,
        onOutput: (bytes: Uint8Array) => { termRef.current?.write(bytes) },
        onExited: () => { endedRef.current = true },
    })

    // Resurrect a dead shell. Input typed while dead is buffered and replayed
    // once the new shell is up, so no keystroke is lost.
    const restart = useCallback((pendingInput?: string) => {
        if (pendingInput) {
            pendingInputRef.current += pendingInput
        }
        if (restartingRef.current) return
        restartingRef.current = true
        startTerminal(sessionId)
            .then(() => {
                endedRef.current = false
                const buffered = pendingInputRef.current
                pendingInputRef.current = ''
                if (buffered) {
                    return terminalInput(sessionId, buffered)
                }
                return undefined
            })
            .catch((err) => {
                pendingInputRef.current = ''
                logger.error('Failed to restart terminal:', err)
                termRef.current?.writeln(
                    `\r\n\x1b[31mFailed to restart terminal: ${err instanceof Error ? err.message : String(err)}\x1b[0m`,
                )
            })
            .finally(() => {
                restartingRef.current = false
            })
    }, [sessionId])

    useEffect(() => {
        const container = containerRef.current
        if (!container) return

        const term = new XTerm({
            cursorBlink: true,
            fontSize: 10,
            fontFamily: 'SauceCodePro NF, Menlo, Monaco, "Courier New", monospace',
            theme: paletteRef.current,
            scrollback: 10000,
        })

        const fitAddon = new FitAddon()
        term.loadAddon(fitAddon)

        term.open(container)
        fitAddon.fit()
        term.focus()

        term.onData((data) => {
            if (endedRef.current) {
                // Shell is dead — resurrect it and replay this keystroke.
                restart(data)
                return
            }
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
            onReady?.(sessionId)
        }).catch((err) => {
            logger.error('Failed to start terminal:', err)
            // Mark the shell dead so the lazy-resurrection paths (click,
            // keystroke, panel re-activation) retry the start instead of
            // leaving a permanently blank terminal: without this, a failed
            // initial start could never be retried because endedRef stayed
            // false and terminalInput would just error "no terminal".
            endedRef.current = true
            term.writeln(`\r\n\x1b[31mFailed to start terminal: ${err instanceof Error ? err.message : String(err)}\x1b[0m`)
            term.writeln('\r\n\x1b[2mClick the terminal or type to retry.\x1b[0m')
            onReady?.(sessionId)
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
            // Deliberately NO stopTerminal here: the PTY must outlive this
            // component. Terminals are per-session and kept alive for the
            // whole app lifetime; the backend stops them on session deletion
            // (DeleteSession) and app shutdown (StopAll). Only the xterm.js
            // renderer is disposed.
            term.dispose()
            termRef.current = null
            fitAddonRef.current = null
        }
    }, [sessionId, onReady, restart])

    // Apply palette changes to the live terminal without restarting the session.
    // xterm.js re-renders when its theme option is reassigned, so switching the
    // palette updates the running terminal in place.
    //
    // Assign ONLY `theme` — never spread `term.options` back into the setter.
    // xterm 6's public options object also exposes the constructor-only
    // `cols`/`rows`, and re-assigning them through the options setter throws
    // `Option "cols" can only be set in the constructor`. That throw used to
    // escape the terminal's mount effect, hit the chat-input ErrorBoundary and
    // replace the WHOLE input panel with "Input error" on every terminal open
    // (persisting across reloads because terminal mode is persisted).
    useEffect(() => {
        const term = termRef.current
        if (term) {
            term.options.theme = palette
        }
    }, [palette])

    // Re-fit when the app-wide UI scale (CSS zoom on <html>) changes: zoom
    // resizes the container's layout box, which the container's own
    // ResizeObserver already reports — but the renderer's char measurement
    // and the canvas refresh can lag the style application by a frame.
    // Scheduling the fit on the next animation frame (with a timeout-0
    // fallback for environments without rAF, e.g. tests) lets the zoomed
    // layout settle first, preventing a stale rows/cols fit and the
    // "terminal larger than its box" overflow the stale fit causes.
    const uiScale = useUiScaleStore((s) => s.scale)
    const fitScheduledRef = useRef(false)
    useEffect(() => {
        const scheduleFit = () => {
            if (fitScheduledRef.current) return
            fitScheduledRef.current = true
            const runFit = () => {
                fitScheduledRef.current = false
                const fitAddon = fitAddonRef.current
                const term = termRef.current
                if (!fitAddon || !term) return
                try {
                    fitAddon.fit()
                    const { cols, rows } = term
                    if (cols > 0 && rows > 0) {
                        terminalResize(sessionId, cols, rows).catch((err) => {
                            logger.error('Terminal resize error:', err)
                        })
                    }
                } catch {
                    // FitAddon can throw if terminal is not fully initialized
                }
            }
            if (typeof requestAnimationFrame === 'function') {
                requestAnimationFrame(runFit)
            } else {
                setTimeout(runFit, 0)
            }
        }
        scheduleFit()
        return () => {
            fitScheduledRef.current = false
        }
    }, [uiScale, sessionId])

    // Watch for "Open in Terminal" requests from the file-tree context menu
    // that arrive after the terminal is already running. The initial mount
    // case is handled by the main effect above (which reads pendingTerminalDir
    // from the store); this effect handles subsequent directory changes by
    // restarting the terminal in the new directory. Only the ACTIVE session's
    // instance may consume the request — the directory targets the session
    // the user is currently looking at.
    const pendingTerminalDir = useInputModeStore((s) => s.pendingTerminalDir)
    const isFirstDirRun = useRef(true)
    useEffect(() => {
        if (isFirstDirRun.current) {
            isFirstDirRun.current = false
            return
        }
        if (!pendingTerminalDir || !isActive) return
        startTerminalInDir(sessionId, pendingTerminalDir)
            .then(() => {
                useInputModeStore.getState().clearPendingTerminalDir()
                endedRef.current = false
                termRef.current?.focus()
            })
            .catch((err) => {
                logger.error('Failed to restart terminal in directory:', err)
                // Mark dead for the same reason as the initial-start catch:
                // StartTerminalInDir stopped any previous shell, so after a
                // failed restart there is no live PTY to fall back to — only
                // the resurrection paths can bring one back.
                endedRef.current = true
                termRef.current?.writeln(
                    `\r\n\x1b[31mFailed to start terminal: ${err instanceof Error ? err.message : String(err)}\x1b[0m`,
                )
                useInputModeStore.getState().clearPendingTerminalDir()
            })
    }, [pendingTerminalDir, sessionId, isActive])

    useEffect(() => {
        let timer: ReturnType<typeof setTimeout> | undefined
        if (visible && termRef.current) {
            // Lazy resurrection: the shell died while the instance was hidden
            // (or in plain sight) — bring it back when the panel is shown.
            if (endedRef.current) {
                restart()
            }
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
    }, [visible, restart])

    const handleClick = () => {
        if (endedRef.current) {
            restart()
        }
        termRef.current?.focus()
    }

    return <div ref={containerRef} className="w-full h-full cursor-pointer" onClick={handleClick} />
}
