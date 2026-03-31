import { Component, type ReactNode, type ErrorInfo } from 'react'

interface Props {
  children: ReactNode
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
      return (
        <div
          style={{
            padding: 40,
            color: 'white',
            background: '#1a1a1a',
            fontFamily: 'monospace',
            minHeight: '100vh',
          }}
        >
          <h1 style={{ marginBottom: 20 }}>Something went wrong</h1>
          <pre
            style={{
              whiteSpace: 'pre-wrap',
              color: '#ff6b6b',
              marginBottom: 20,
            }}
          >
            {this.state.error?.message}
          </pre>
          <pre
            style={{
              whiteSpace: 'pre-wrap',
              color: '#888',
              fontSize: 12,
            }}
          >
            {this.state.error?.stack}
          </pre>
        </div>
      )
    }

    return this.props.children
  }
}
