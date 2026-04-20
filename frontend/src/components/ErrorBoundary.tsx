import { Component, type ReactNode, type ErrorInfo } from 'react'

interface Props {
  children: ReactNode
  fallback?: ReactNode
}

interface State {
  hasError: boolean
  error: Error | null
}

export class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props)
    this.state = { hasError: false, error: null }
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('React error boundary caught:', error, info)
  }

  render() {
    if (this.state.hasError) {
      if (this.props.fallback !== undefined) {
        return this.props.fallback
      }
      return (
        <div className="p-10 text-foreground bg-background font-mono min-h-screen">
          <h1 className="mb-5">Something went wrong</h1>
          <pre className="whitespace-pre-wrap text-destructive mb-5">
            {this.state.error?.message}
          </pre>
          <pre className="whitespace-pre-wrap text-muted-foreground text-xs">
            {this.state.error?.stack}
          </pre>
        </div>
      )
    }

    return this.props.children
  }
}
