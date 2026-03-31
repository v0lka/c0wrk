import { create } from 'zustand'

type InspectorTab = 'session' | 'global'

interface PlanStep {
  id: string
  description: string
  status: 'pending' | 'running' | 'completed' | 'failed'
  duration?: number // milliseconds
}

interface EvalCriterion {
  id: string
  name: string
  description: string
  status: 'pass' | 'fail'
}

interface InspectorReflection {
  id: string
  attemptNumber: number
  summary: string
  insights: string[]
  suggestedAction: string
  actionType: 'retry' | 'modify' | 'continue' | 'abort'
  timestamp: number
}

interface SessionStats {
  routingMode: string
  routingDomain: string
  routingComplexity: string
  attempt: number
  maxAttempts: number
}

interface InspectorState {
  activeTab: InspectorTab
  setTab: (tab: InspectorTab) => void

  // Data fields
  planSteps: PlanStep[]
  evalCriteria: EvalCriterion[]
  reflections: InspectorReflection[]
  sessionStats: SessionStats

  // Actions
  setPlanSteps: (steps: PlanStep[]) => void
  updateStepStatus: (stepIndex: number, status: PlanStep['status']) => void
  updateStepById: (stepId: string, status: PlanStep['status'], duration?: number) => void
  setEvalCriteria: (criteria: EvalCriterion[]) => void
  addReflection: (reflection: InspectorReflection) => void
  updateStats: (update: Partial<SessionStats>) => void
  resetSessionData: () => void
}

const defaultStats: SessionStats = {
  routingMode: '',
  routingDomain: '',
  routingComplexity: '',
  attempt: 1,
  maxAttempts: 3,
}

export const useInspectorStore = create<InspectorState>((set) => ({
  activeTab: 'session',
  setTab: (tab) => set({ activeTab: tab }),

  planSteps: [],
  evalCriteria: [],
  reflections: [],
  sessionStats: { ...defaultStats },

  setPlanSteps: (steps) => set({ planSteps: steps }),
  updateStepStatus: (stepIndex, status) => set((s) => {
    const newSteps = [...s.planSteps]
    if (newSteps[stepIndex]) {
      newSteps[stepIndex] = { ...newSteps[stepIndex], status }
    }
    return { planSteps: newSteps }
  }),
  updateStepById: (stepId, status, duration) => set((s) => {
    const newSteps = [...s.planSteps]
    const index = newSteps.findIndex(step => step.id === stepId)
    if (index !== -1) {
      newSteps[index] = { ...newSteps[index], status, ...(duration !== undefined && { duration }) }
    }
    return { planSteps: newSteps }
  }),
  setEvalCriteria: (criteria) => set({ evalCriteria: criteria }),
  addReflection: (reflection) => set((s) => ({
    reflections: [...s.reflections, reflection],
  })),
  updateStats: (update) => set((s) => ({
    sessionStats: { ...s.sessionStats, ...update },
  })),
  resetSessionData: () => set({
    planSteps: [],
    evalCriteria: [],
    reflections: [],
    sessionStats: { ...defaultStats },
  }),
}))
