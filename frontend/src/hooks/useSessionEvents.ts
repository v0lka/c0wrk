import { useEffect } from 'react'
import { useChatStore } from '@/stores/chatStore'
import { useInspectorStore } from '@/stores/inspectorStore'
import { usePanelStore } from '@/stores/panelStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useWails } from './useWails'
import type { RoutingData, ToolCallData, ToolResultData, EvalData, PlanData, ToolConfirmData, ThoughtData, PlanStepStartData, PlanStepCompleteData, ContextFillData, AskUserData } from '@/lib/wails'

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
    const panelStore = usePanelStore.getState()

    // Reset global UI state from previous session
    useChatStore.getState().clearSessionUIState()
    
    // Reset inspector data for new session
    inspectorStore.resetSessionData()
    
    // Reset panel data for new session (before any events arrive)
    panelStore.resetPanels()

    // Helper: only update global UI state if this session is still active
    const isActiveSession = () => useSessionStore.getState().activeSessionId === sessionId

    const unsubs: (() => void)[] = []
    const on = (type: string, cb: (...data: unknown[]) => void) => {
      unsubs.push(runtime.EventsOn(`session:${sessionId}:${type}`, cb))
    }

    on('routing', (data: unknown) => {
      const routing = data as RoutingData
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Analyzing request...')
      addMessage(sessionId, {
        id: `routing-${Date.now()}`,
        sessionId,
        type: 'routing',
        content: `Mode: ${routing.mode} | Domain: ${routing.domain} | Complexity: ${routing.complexity}`,
        metadata: { mode: routing.mode, domain: routing.domain, complexity: routing.complexity },
        timestamp: Date.now(),
      })
      inspectorStore.updateStats({ routingMode: routing.mode, routingDomain: routing.domain, routingComplexity: routing.complexity })
    })

    on('step_start', (data: unknown) => {
      if (isActiveSession()) {
        setThinking(true)
        useChatStore.getState().setActivityStatus('Thinking...')
      }
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
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Reasoning...')
      const thought = data as ThoughtData
      addMessage(sessionId, {
        id: `thought-${thought.step_num}-${Date.now()}`,
        sessionId,
        type: 'thought',
        content: thought.content,
        metadata: { step_num: thought.step_num, reasoning: thought.reasoning },
        timestamp: Date.now(),
      })
    })

    on('tool_call', (data: unknown) => {
      const toolCall = data as ToolCallData
      if (isActiveSession()) useChatStore.getState().setActivityStatus(`Running tool: ${toolCall.tool}...`)
      const toolMsgId = toolCall.plan_step_id
        ? `tool-${toolCall.plan_step_id}-${toolCall.step}`
        : `tool-${toolCall.step}`
      addMessage(sessionId, {
        id: toolMsgId,
        sessionId,
        type: 'tool_call',
        content: `${toolCall.tool}(${toolCall.args})`,
        metadata: { step: toolCall.step, tool: toolCall.tool, args: toolCall.args, plan_step_id: toolCall.plan_step_id },
        timestamp: Date.now(),
      })
    })

    on('tool_result', (data: unknown) => {
      const toolResult = data as ToolResultData
      const toolMsgId = toolResult.plan_step_id
        ? `tool-${toolResult.plan_step_id}-${toolResult.step}`
        : `tool-${toolResult.step}`
      updateMessage(sessionId, toolMsgId, {
        metadata: {
          step: toolResult.step,
          completed: true,
          result: toolResult.result ?? toolResult.result_preview,
          result_preview: toolResult.result_preview,
          result_len: toolResult.result_len,
          plan_step_id: toolResult.plan_step_id,
        },
      })
    })

    on('tool_confirm', (data: unknown) => {
      const toolConfirm = data as ToolConfirmData
      const msgs = useChatStore.getState().messages[sessionId] || []

      // Link to the last tool_call for this tool
      let toolMsgId: string | undefined
      let toolPlanStepId: string | undefined
      for (let i = msgs.length - 1; i >= 0; i--) {
        const m = msgs[i]
        if (m.type === 'tool_call' && m.metadata?.tool === toolConfirm.tool) {
          toolMsgId = m.id
          toolPlanStepId = m.metadata?.plan_step_id as string | undefined
          updateMessage(sessionId, m.id, {
            metadata: { ...m.metadata, awaiting_confirmation: true },
          })
          break
        }
      }

      addMessage(sessionId, {
        id: `tool-confirm-${toolConfirm.confirm_id}`,
        sessionId,
        type: 'tool_confirm',
        content: `Confirm: ${toolConfirm.tool}`,
        metadata: {
          ...toolConfirm,
          tool_msg_id: toolMsgId,
          plan_step_id: toolPlanStepId,
        } as unknown as Record<string, unknown>,
        timestamp: Date.now(),
      })

      if (isActiveSession()) {
        setThinking(false)
        useChatStore.getState().setActivityStatus('Awaiting confirmation...')
      }
    })

    on('ask_user', (data: unknown) => {
      const askData = data as AskUserData
      addMessage(sessionId, {
        id: `ask-user-${askData.request_id}`,
        sessionId,
        type: 'ask_user',
        content: askData.question,
        metadata: askData as unknown as Record<string, unknown>,
        timestamp: Date.now(),
      })
      if (isActiveSession()) {
        setThinking(false)
        useChatStore.getState().setActivityStatus('Waiting for your answer...')
      }
    })

    on('step_complete', (data: unknown) => {
      if (isActiveSession()) setThinking(false)
      const step = data as { step_num: number }
      if (step.step_num > 0) {
        inspectorStore.updateStepStatus(step.step_num - 1, 'completed')
      }
      updateMessage(sessionId, `step-${step.step_num}`, {
        type: 'step_done',
      })
    })

    on('evaluation', (data: unknown) => {
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Evaluating results...')
      const evalData = data as EvalData
      // Route to panelStore - update existing group instead of creating new
      if (evalData.criteria) {
        panelStore.updateEvalGroupStatuses(evalData.criteria)
      }
    })

    on('reflection', () => {
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Reflecting on results...')
      // Skip reflection events - not shown in chat timeline
      // Inspector can still track internal reflection data if needed elsewhere
    })

    on('plan_generated', (data: unknown) => {
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Executing plan...')
      const plan = data as PlanData
      // Route to panelStore
      if (plan.steps) {
        panelStore.addPlanGroup(plan.steps)
      }
      // Also add to chat messages so groupMessages can build stepIndexMap
      addMessage(sessionId, {
        id: `plan-${Date.now()}`,
        sessionId,
        type: 'plan',
        content: '',
        metadata: { steps: plan.steps },
        timestamp: Date.now(),
      })
    })

    on('plan_step_start', (data: unknown) => {
      const stepData = data as PlanStepStartData
      if (isActiveSession()) useChatStore.getState().setActivityStatus(`Executing: ${stepData.description || 'step'}...`)
      inspectorStore.updateStepById(stepData.step_id, 'running')
      // Route to panelStore
      panelStore.updatePlanItemStatus(stepData.step_id, 'running')
      // Add to chat messages for plan step containers
      addMessage(sessionId, {
        id: `plan-step-start-${stepData.step_id}-${Date.now()}`,
        sessionId,
        type: 'plan_step_start',
        content: stepData.description || '',
        metadata: { step_id: stepData.step_id, description: stepData.description },
        timestamp: Date.now(),
      })
    })

    on('plan_step_complete', (data: unknown) => {
      const stepData = data as PlanStepCompleteData
      inspectorStore.updateStepById(
        stepData.step_id,
        stepData.success ? 'completed' : 'failed',
        stepData.duration
      )
      // Route to panelStore
      panelStore.updatePlanItemStatus(
        stepData.step_id,
        stepData.success ? 'completed' : 'failed',
        stepData.duration
      )
      // Add to chat messages for plan step lifecycle
      addMessage(sessionId, {
        id: `plan-step-complete-${stepData.step_id}-${Date.now()}`,
        sessionId,
        type: 'plan_step_complete',
        content: '',
        metadata: { step_id: stepData.step_id, success: stepData.success, duration: stepData.duration },
        timestamp: Date.now(),
      })
    })

    on('assistant_chunk', (data: unknown) => {
      if (!isActiveSession()) return
      useChatStore.getState().setActivityStatus('Generating response...')
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
        if (isActiveSession()) setStreaming(null)
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
      if (isActiveSession()) {
        setThinking(false)
        setStreaming(null)
        useChatStore.getState().setActivityStatus(null)
        useChatStore.getState().setTaskActive(false)
      }
    })

    // Task lifecycle events
    on('task_complete', (data: unknown) => {
      if (isActiveSession()) {
        setThinking(false)
        setStreaming(null)
        useChatStore.getState().setActivityStatus(null)
        useChatStore.getState().setTaskActive(false)
      }
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
      if (isActiveSession()) {
        setThinking(false)
        setStreaming(null)
        useChatStore.getState().setActivityStatus(null)
        useChatStore.getState().setTaskActive(false)
      }
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
      if (isActiveSession()) useChatStore.getState().setActivityStatus(`Retrying (attempt ${retry.attempt}/${retry.max_attempts})...`)
      panelStore.resetEvalStatuses()  // Reset criteria to pending on retry
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
      if (isActiveSession()) useChatStore.getState().setActivityStatus(`Escalating to ${escalation.to_mode}...`)
      addMessage(sessionId, {
        id: `escalation-${Date.now()}`,
        sessionId,
        type: 'routing',
        content: `Escalated: ${escalation.from_mode} → ${escalation.to_mode}`,
        metadata: { mode: escalation.to_mode, domain: '', complexity: '' },
        timestamp: Date.now(),
      })
    })

    on('service', (data: unknown) => {
      if (!isActiveSession()) return
      const service = data as { content: string }
      if (service.content) {
        useChatStore.getState().setActivityStatus(service.content)
      }
    })

    on('ac_extracted', (data: unknown) => {
      // Display extracted acceptance criteria as pending eval items
      const acData = data as { count?: number; criteria?: Array<{ name: string; description: string }> }
      if (acData.criteria && acData.criteria.length > 0) {
        // Map to eval format with pending status (passed: undefined means pending)
        const pendingCriteria = acData.criteria.map(c => ({
          name: c.name,
          description: c.description,
        }))
        panelStore.addEvalGroup(pendingCriteria)

        // Also add to chat timeline
        if (isActiveSession()) {
          addMessage(sessionId, {
            id: `ac-extracted-${Date.now()}`,
            sessionId,
            type: 'ac_extracted',
            content: `${acData.criteria.length} acceptance criteria extracted`,
            metadata: { count: acData.criteria.length, criteria: acData.criteria },
            timestamp: Date.now(),
          })
        }
      }
    })

    on('subagent_launch', (data: unknown) => {
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Launching sub-agent...')
      const sa = data as { step_id: string; description: string; plan_step_id?: string }
      addMessage(sessionId, {
        id: `subagent-${sa.step_id}-launch`,
        sessionId,
        type: 'tool_call',
        content: `SubAgent: ${sa.description}`,
        metadata: { tool: 'subagent', args: sa.description, step: sa.step_id, plan_step_id: sa.plan_step_id },
        timestamp: Date.now(),
      })
    })

    on('subagent_complete', (data: unknown) => {
      const sa = data as { step_id: string; success: boolean; duration: number; plan_step_id?: string }
      updateMessage(sessionId, `subagent-${sa.step_id}-launch`, {
        metadata: {
          tool: 'subagent',
          completed: true,
          error: sa.success ? undefined : 'SubAgent failed',
          result_preview: sa.success ? `Completed in ${sa.duration}ms` : `Failed after ${sa.duration}ms`,
          result_len: 0,
          plan_step_id: sa.plan_step_id,
        },
      })
    })

    on('context_fill', (data: unknown) => {
      if (!isActiveSession()) return
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
