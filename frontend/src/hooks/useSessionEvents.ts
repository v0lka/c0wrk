import { useEffect } from 'react'
import { useChatStore } from '@/stores/chatStore'
import { useInspectorStore } from '@/stores/inspectorStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useWails } from './useWails'
import type { RoutingData, ToolCallData, ToolResultData, EvalData, ReflectionData, PlanData, ToolConfirmData, ThoughtData, PlanStepStartData, PlanStepCompleteData, ContextFillData } from '@/lib/wails'

export function useSessionEvents(sessionId: string | null) {
  const { runtime } = useWails()
  const addMessage = useChatStore(s => s.addMessage)
  const updateMessage = useChatStore(s => s.updateMessage)
  const setStreaming = useChatStore(s => s.setStreaming)
  const appendStreamToken = useChatStore(s => s.appendStreamToken)
  const setThinking = useChatStore(s => s.setThinking)
  const setContextFill = useChatStore(s => s.setContextFill)
  const updateSession = useSessionStore(s => s.updateSession)

  useEffect(() => {
    if (!sessionId || !runtime) return

    const inspectorStore = useInspectorStore.getState()
    
    // Reset inspector data for new session
    inspectorStore.resetSessionData()

    const unsubs: (() => void)[] = []
    const on = (type: string, cb: (...data: unknown[]) => void) => {
      unsubs.push(runtime.EventsOn(`session:${sessionId}:${type}`, cb))
    }

    on('routing', (data: unknown) => {
      const routing = data as RoutingData
      addMessage(sessionId, {
        id: `routing-${Date.now()}`,
        sessionId,
        type: 'routing',
        content: `Mode: ${routing.mode} | Domain: ${routing.domain} | Complexity: ${routing.complexity}`,
        metadata: routing,
        timestamp: Date.now(),
      })
      inspectorStore.updateStats({ routingMode: routing.mode, routingDomain: routing.domain, routingComplexity: routing.complexity })
    })

    on('step_start', (data: unknown) => {
      setThinking(true)
      const step = data as { step_num: number }
      if (step.step_num > 0) {
        inspectorStore.updateStepStatus(step.step_num - 1, 'running')
      }
      addMessage(sessionId, {
        id: `step-${step.step_num}`,
        sessionId,
        type: 'thinking',
        content: `Step ${step.step_num || ''}...`,
        timestamp: Date.now(),
      })
    })

    on('thought', (data: unknown) => {
      const thought = data as ThoughtData
      addMessage(sessionId, {
        id: `thought-${thought.step_num}-${Date.now()}`,
        sessionId,
        type: 'thought',
        content: thought.content,
        metadata: { step_num: thought.step_num },
        timestamp: Date.now(),
      })
    })

    on('tool_call', (data: unknown) => {
      const toolCall = data as ToolCallData
      addMessage(sessionId, {
        id: `tool-${toolCall.step}`,
        sessionId,
        type: 'tool_call',
        content: `${toolCall.tool}(${toolCall.args})`,
        metadata: toolCall,
        timestamp: Date.now(),
      })
    })

    on('tool_result', (data: unknown) => {
      const toolResult = data as ToolResultData
      updateMessage(sessionId, `tool-${toolResult.step}`, {
        metadata: {
          ...toolResult,
          completed: true,
          result_preview: toolResult.result_preview,
          result_len: toolResult.result_len,
        },
      })
    })

    on('tool_confirm', (data: unknown) => {
      const toolConfirm = data as ToolConfirmData
      addMessage(sessionId, {
        id: `tool-confirm-${toolConfirm.confirm_id}`,
        sessionId,
        type: 'tool_confirm',
        content: `Confirm: ${toolConfirm.tool}`,
        metadata: toolConfirm,
        timestamp: Date.now(),
      })
      setThinking(false) // Pause thinking indicator while waiting for user input
    })

    on('step_complete', (data: unknown) => {
      setThinking(false)
      const step = data as { step_num: number }
      if (step.step_num > 0) {
        inspectorStore.updateStepStatus(step.step_num - 1, 'completed')
      }
      updateMessage(sessionId, `step-${step.step_num}`, {
        type: 'step_done',
      })
    })

    on('evaluation', (data: unknown) => {
      const evalData = data as EvalData
      addMessage(sessionId, {
        id: `eval-${Date.now()}`,
        sessionId,
        type: 'eval',
        content: `Evaluation: ${evalData.passed}/${evalData.total} passed`,
        metadata: evalData,
        timestamp: Date.now(),
      })
    })

    on('reflection', (data: unknown) => {
      const reflection = data as ReflectionData
      addMessage(sessionId, {
        id: `reflection-${Date.now()}`,
        sessionId,
        type: 'reflection',
        content: reflection.summary,
        metadata: reflection,
        timestamp: Date.now(),
      })
      inspectorStore.addReflection({
        id: `reflection-${Date.now()}`,
        attemptNumber: reflection.attempt || 1,
        summary: reflection.summary,
        insights: reflection.insights || [],
        suggestedAction: reflection.summary,
        actionType: 'retry',
        timestamp: Date.now(),
      })
    })

    on('plan_generated', (data: unknown) => {
      const plan = data as PlanData
      addMessage(sessionId, {
        id: `plan-${Date.now()}`,
        sessionId,
        type: 'plan',
        content: `Plan generated with ${plan.step_count} steps`,
        metadata: plan,
        timestamp: Date.now(),
      })
    })

    on('plan_step_start', (data: unknown) => {
      const stepData = data as PlanStepStartData
      inspectorStore.updateStepById(stepData.step_id, 'running')
      // Update plan message in chat for inline PlanCard
      const msgs = useChatStore.getState().messages[sessionId] || []
      const planMsg = [...msgs].reverse().find(m => m.id.startsWith('plan-'))
      if (planMsg) {
        const meta = (planMsg.metadata as Record<string, unknown>) || {}
        const steps = (meta.steps as Array<{ description: string; status?: string }>) || []
        const updatedSteps = steps.map((s, i) =>
          String(i + 1) === stepData.step_id ? { ...s, status: 'running' } : s
        )
        updateMessage(sessionId, planMsg.id, {
          metadata: { ...meta, steps: updatedSteps },
        })
      }
    })

    on('plan_step_complete', (data: unknown) => {
      const stepData = data as PlanStepCompleteData
      inspectorStore.updateStepById(
        stepData.step_id,
        stepData.success ? 'completed' : 'failed',
        stepData.duration
      )
      // Update plan message in chat for inline PlanCard
      const msgs = useChatStore.getState().messages[sessionId] || []
      const planMsg = [...msgs].reverse().find(m => m.id.startsWith('plan-'))
      if (planMsg) {
        const meta = (planMsg.metadata as Record<string, unknown>) || {}
        const steps = (meta.steps as Array<{ description: string; status?: string; duration?: number }>) || []
        const updatedSteps = steps.map((s, i) =>
          String(i + 1) === stepData.step_id
            ? { ...s, status: stepData.success ? 'completed' : 'failed', duration: stepData.duration }
            : s
        )
        updateMessage(sessionId, planMsg.id, {
          metadata: { ...meta, steps: updatedSteps },
        })
      }
    })

    on('assistant_chunk', (data: unknown) => {
      const chunk = data as { content: string }
      if (chunk.content) {
        appendStreamToken(chunk.content)
      }
    })

    on('assistant_done', () => {
      const streamingText = useChatStore.getState().streamingText
      if (streamingText) {
        addMessage(sessionId, {
          id: `assistant-${Date.now()}`,
          sessionId,
          type: 'assistant',
          content: streamingText,
          timestamp: Date.now(),
        })
        setStreaming(null)
      }
    })

    on('error', (data: unknown) => {
      const error = data as { error: string }
      addMessage(sessionId, {
        id: `error-${Date.now()}`,
        sessionId,
        type: 'error',
        content: error.error || 'An error occurred',
        timestamp: Date.now(),
      })
      setThinking(false)
      setStreaming(null)
    })

    // Task lifecycle events
    on('task_complete', (data: unknown) => {
      setThinking(false)
      setStreaming(null)
      const taskData = data as { output?: string; attempt_count?: number; routing_decision?: { mode?: string } }
      // Add completion message
      if (taskData.output) {
        addMessage(sessionId, {
          id: `assistant-done-${Date.now()}`,
          sessionId,
          type: 'assistant',
          content: taskData.output,
          timestamp: Date.now(),
        })
      }
    })

    on('task_cancelled', () => {
      setThinking(false)
      setStreaming(null)
      addMessage(sessionId, {
        id: `cancelled-${Date.now()}`,
        sessionId,
        type: 'error',
        content: 'Task was cancelled',
        timestamp: Date.now(),
      })
    })

    on('retry', (data: unknown) => {
      const retry = data as { attempt: number; max_attempts: number }
      addMessage(sessionId, {
        id: `retry-${Date.now()}`,
        sessionId,
        type: 'routing',
        content: `Retry attempt ${retry.attempt}/${retry.max_attempts}`,
        metadata: { mode: 'retry', ...retry },
        timestamp: Date.now(),
      })
      inspectorStore.updateStats({ attempt: retry.attempt + 1, maxAttempts: retry.max_attempts })
    })

    on('escalation', (data: unknown) => {
      const escalation = data as { from_mode: string; to_mode: string }
      addMessage(sessionId, {
        id: `escalation-${Date.now()}`,
        sessionId,
        type: 'routing',
        content: `Escalated: ${escalation.from_mode} → ${escalation.to_mode}`,
        metadata: { mode: escalation.to_mode, domain: '', complexity: '' },
        timestamp: Date.now(),
      })
    })

    on('ac_extracted', (data: unknown) => {
      const ac = data as { count: number }
      addMessage(sessionId, {
        id: `ac-${Date.now()}`,
        sessionId,
        type: 'routing',
        content: `${ac.count} acceptance criteria extracted`,
        metadata: { mode: 'ac', domain: '', complexity: '' },
        timestamp: Date.now(),
      })
    })

    on('subagent_launch', (data: unknown) => {
      const sa = data as { step_id: string; description: string }
      addMessage(sessionId, {
        id: `subagent-${sa.step_id}-launch`,
        sessionId,
        type: 'tool_call',
        content: `SubAgent: ${sa.description}`,
        metadata: { tool: 'subagent', args: sa.description, step: sa.step_id },
        timestamp: Date.now(),
      })
    })

    on('subagent_complete', (data: unknown) => {
      const sa = data as { step_id: string; success: boolean; duration: number }
      updateMessage(sessionId, `subagent-${sa.step_id}-launch`, {
        metadata: {
          tool: 'subagent',
          completed: true,
          error: sa.success ? undefined : 'SubAgent failed',
          result_preview: sa.success ? `Completed in ${sa.duration}ms` : `Failed after ${sa.duration}ms`,
          result_len: 0,
        },
      })
    })

    on('context_fill', (data: unknown) => {
      const fillData = data as ContextFillData
      setContextFill({
        fillPercent: fillData.fill_percent,
        usedTokens: fillData.used_tokens,
        maxTokens: fillData.max_tokens,
        status: fillData.status,
      })
    })

    on('session_renamed', (data: unknown) => {
      const renameData = data as { new_name: string }
      updateSession(sessionId, { name: renameData.new_name })
    })

    return () => unsubs.forEach(fn => fn())
  }, [sessionId, runtime, addMessage, updateMessage, setStreaming, appendStreamToken, setThinking, setContextFill, updateSession])
}
