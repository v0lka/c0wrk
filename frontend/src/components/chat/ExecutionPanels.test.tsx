// @vitest-environment jsdom
//
// Regression tests for the ExecutionPanels render guard: for plan-less tasks
// the container must not appear until the per-run agent_metrics report
// arrives. Routing/retry stats alone (written to planStore.sessionStats by
// routing/step_retry events mid-run) previously produced an empty bordered
// container between the routing event and task finish.
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ReactElement } from 'react'

import { ExecutionPanels } from './ExecutionPanels'
import { usePlanStore } from '@/stores/planStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { AgentMetricsData } from '@/types/events'
import type { PlanGroup } from '@/types/models'

const metrics: AgentMetricsData = {
  finish: 'full',
  parse_errors: 0,
  nudges: { repeat: 0, same_tool: 0, fruitless: 0, parse: 0 },
  aborts: { repeat: 0, same_tool: 0, fruitless: 0, parse: 0 },
  steps: 3,
  output_tokens: 1200,
  small_llm: { enabled: false, variants: [] },
}

const planGroup: PlanGroup = {
  id: 'plan-1',
  items: [{ id: 'step-1', title: 'Step 1', status: 'completed', dependsOn: [] }],
  completedCount: 1,
  totalCount: 1,
}

let root: Root | null = null

function render(el: ReactElement): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(el)
  })
  return container
}

describe('ExecutionPanels render guard', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    usePlanStore.setState({ planGroups: [], sessionStats: {} })
    useSessionStore.setState({ activeSessionId: 's1' })
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
  })

  it('renders nothing for a plan-less task before the first agent_metrics report', () => {
    // Mid-run state: routing/retry stats exist, no plan, no metrics yet.
    usePlanStore.setState({
      sessionStats: { s1: { routingDomain: 'code', attemptCount: 1, maxAttempts: 3 } },
    })
    const container = render(<ExecutionPanels />)
    expect(container.innerHTML).toBe('')
  })

  it('renders the stats row once the agent_metrics report arrives (plan-less task)', () => {
    usePlanStore.setState({
      sessionStats: { s1: { lastAgentMetrics: metrics } },
    })
    const container = render(<ExecutionPanels />)
    expect(container.textContent).toContain('finish: full')
    expect(container.textContent).toContain('steps: 3')
  })

  it('renders the plan section without any stats', () => {
    usePlanStore.setState({ planGroups: [planGroup] })
    const container = render(<ExecutionPanels />)
    expect(container.textContent).toContain('Execution plan')
  })
})
