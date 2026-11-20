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
import { useUIStore } from '@/stores/uiStore'
import { useE2SStore } from '@/stores/e2sStore'
import type { AgentMetricsData } from '@/types/events'
import type { PlanGroup } from '@/types/models'

const metrics: AgentMetricsData = {
  finish: 'full',
  parse_errors: 0,
  nudges: { repeat: 0, same_tool: 0, fruitless: 0, parse: 0, truncation: 0, validation: 0 },
  aborts: { repeat: 0, same_tool: 0, fruitless: 0, parse: 0, truncation: 0, validation: 2 },
  steps: 3,
  output_tokens: 1200,
  invalid_tool_calls: 0,
  model_profiles: { enabled: false, variants: [] },
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
    // Stats row display is opt-in (Settings → General, off by default).
    useUIStore.setState({ showSessionStats: false })
    useE2SStore.getState().clearAll()
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

  it('hides the stats row when agent_metrics arrives but display is off (default)', () => {
    // Metrics are always collected into the store; the display toggle gates
    // only the rendering — with it off (the default), a plan-less task must
    // not render the container at all.
    usePlanStore.setState({
      sessionStats: { s1: { lastAgentMetrics: metrics } },
    })
    const container = render(<ExecutionPanels />)
    expect(container.innerHTML).toBe('')
  })

  it('renders the stats row once the agent_metrics report arrives and display is enabled', () => {
    useUIStore.setState({ showSessionStats: true })
    usePlanStore.setState({
      sessionStats: { s1: { lastAgentMetrics: metrics } },
    })
    const container = render(<ExecutionPanels />)
    expect(container.textContent).toContain('finish: full')
    expect(container.textContent).toContain('steps: 3')
    expect(container.textContent).toContain('out tokens: 1200')
    // The `·Nv` segment renders when non-zero and stays hidden at zero,
    // same rule as `·Nt`:
    expect(container.textContent).toContain('aborts: 2 (0r·0s·0f·0p·2v)')
    expect(container.textContent).toContain('nudges: 0 (0r·0s·0f·0p)')
  })

  it('renders the plan section without the stats row when display is off', () => {
    usePlanStore.setState({
      planGroups: [planGroup],
      sessionStats: { s1: { lastAgentMetrics: metrics } },
    })
    const container = render(<ExecutionPanels />)
    expect(container.textContent).toContain('Execution plan')
    expect(container.textContent).not.toContain('finish: full')
  })
})

describe('ExecutionPanels E2S mode (plan view replacement)', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    usePlanStore.setState({ planGroups: [], sessionStats: {} })
    useSessionStore.setState({ activeSessionId: 's1' })
    useUIStore.setState({ showSessionStats: false })
    useE2SStore.getState().clearAll()
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
  })

  it('an E2S session renders the Execution State panel instead of the plan view', () => {
    // Plan groups exist (e.g. restored from an earlier phase) — E2S must
    // still win: Σₜ replaces the plan DAG for the whole E2S session.
    usePlanStore.setState({ planGroups: [planGroup] })
    act(() => {
      useE2SStore.getState().applySnapshot('s1', {
        state: { objective: 'E2S objective', checklist: [{ text: 'step', checked: false }] },
        turn: 2,
        max_turns: 7,
        status: 'running',
      })
    })
    const container = render(<ExecutionPanels />)
    expect(container.textContent).toContain('Execution state')
    expect(container.textContent).toContain('turn 2/7')
    expect(container.textContent).toContain('E2S objective')
    expect(container.textContent).not.toContain('Execution plan')
  })

  it('an E2S session without a plan still renders the container', () => {
    act(() => {
      useE2SStore.getState().applySnapshot('s1', {
        state: { objective: 'planless E2S' },
        turn: 1,
        max_turns: 3,
        status: 'running',
      })
    })
    const container = render(<ExecutionPanels />)
    expect(container.textContent).toContain('Execution state')
    expect(container.innerHTML).not.toBe('')
  })

  it('an ordinary session (no e2s_state seen) keeps the plan view unchanged', () => {
    usePlanStore.setState({ planGroups: [planGroup] })
    const container = render(<ExecutionPanels />)
    expect(container.textContent).toContain('Execution plan')
    expect(container.textContent).not.toContain('Execution state')
  })

  it('clearing the E2S snapshot (switch away) restores the plan view', () => {
    usePlanStore.setState({ planGroups: [planGroup] })
    act(() => {
      useE2SStore.getState().applySnapshot('s1', {
        state: { objective: 'transient' },
        turn: 1,
        max_turns: 3,
        status: 'running',
      })
    })
    const container = render(<ExecutionPanels />)
    expect(container.textContent).toContain('Execution state')

    act(() => {
      useE2SStore.getState().clearSession('s1')
    })
    expect(container.textContent).not.toContain('Execution state')
    expect(container.textContent).toContain('Execution plan')
  })
})
