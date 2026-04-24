import { Component, type ReactNode, type ErrorInfo } from 'react'
import { logger } from '@/lib/logger'

interface ErrorBoundaryProps {
  fallback?: ReactNode | ((error: Error) => ReactNode)
  children: ReactNode
}

interface ErrorBoundaryState {
  error: Error | null
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props)
    this.state = { error: null }
  }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    logger.error('React error boundary caught:', error, info)
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
