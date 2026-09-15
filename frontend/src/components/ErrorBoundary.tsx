import { Component, type ReactNode, type ErrorInfo } from 'react'
import { logger } from '@/lib/logger'
import { reportCrash } from '@/lib/crashDiagnostics'

interface ErrorBoundaryProps {
  fallback?: ReactNode | ((error: Error) => ReactNode)
  children: ReactNode
  /**
   * Optional list of values that, when changed, clear a caught error and let
   * the subtree render again. A boundary that trapped an error otherwise stays
   * in the fallback state for the lifetime of its mount (React never retries),
   * so a transient failure would keep the affected UI dead even after the
   * context that caused it changed (a session switch, a mode toggle). Keys are
   * compared shallowly.
   */
  resetKeys?: readonly unknown[]
}

interface ErrorBoundaryState {
  error: Error | null
}

function resetKeysChanged(
  prev: readonly unknown[] | undefined,
  next: readonly unknown[] | undefined,
): boolean {
  if (prev === next) return false
  if (!prev || !next || prev.length !== next.length) return true
  for (let i = 0; i < prev.length; i++) {
    if (!Object.is(prev[i], next[i])) return true
  }
  return false
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props)
    this.state = { error: null }
  }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error }
  }

  componentDidUpdate(prevProps: ErrorBoundaryProps) {
    if (this.state.error && resetKeysChanged(prevProps.resetKeys, this.props.resetKeys)) {
      this.setState({ error: null })
    }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    logger.error('React error boundary caught:', error, info)
    // Persistent crash trace (wails.log + localStorage ring) — the webview
    // console is not persisted in the packaged app, so without this push a
    // render crash leaves no diagnosable trace.
    reportCrash(error, { componentStack: info.componentStack })
  }

  render() {
    const { error } = this.state
    if (error) {
      const { fallback } = this.props
      if (typeof fallback === 'function') return fallback(error)
      if (fallback !== undefined) return fallback
      return (
        <div className="p-2 text-sm text-destructive">
          {error.message}
        </div>
      )
    }
    return this.props.children
  }
}
