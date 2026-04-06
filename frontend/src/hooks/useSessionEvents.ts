import { useEffect } from 'react'
import { useChatStore } from '@/stores/chatStore'
import { usePanelStore } from '@/stores/panelStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useWails } from './useWails'
import type { RoutingData, ToolCallData, ToolResultData, EvalData, PlanData, ToolConfirmData, ThoughtData, PlanStepStartData, PlanStepCompleteData, ContextFillData, AskUserData, AssistantChunkData } from '@/lib/wails'
import { isSessionTokensData } from '@/lib/wails'
import { GetSessionTokens } from '../../wailsjs/go/main/App'

// --- Type guards for event data validation ---
function isRoutingData(data: unknown): data is RoutingData {
  return typeof data === 'object' && data !== null && 'domain' in data && 'complexity' in data
}

function isStepData(data: unknown): data is { step_num: number } {
  return typeof data === 'object' && data !== null && 'step_num' in data
}

function isThoughtData(data: unknown): data is ThoughtData {
  return typeof data === 'object' && data !== null && 'content' in data && 'step_num' in data
}

function isToolCallData(data: unknown): data is ToolCallData {
  return typeof data === 'object' && data !== null && 'tool' in data && 'step' in data
}

function isToolResultData(data: unknown): data is ToolResultData {
  return typeof data === 'object' && data !== null && 'step' in data && 'result_len' in data
}

function isToolConfirmData(data: unknown): data is ToolConfirmData {
  return typeof data === 'object' && data !== null && 'confirm_id' in data && 'tool' in data
}

function isAskUserData(data: unknown): data is AskUserData {
  return typeof data === 'object' && data !== null && 'request_id' in data && 'question' in data
}

function isEvalData(data: unknown): data is EvalData {
  return typeof data === 'object' && data !== null && 'passed' in data && 'total' in data
}

function isPlanData(data: unknown): data is PlanData {
  return typeof data === 'object' && data !== null && 'step_count' in data
}

function isPlanStepStartData(data: unknown): data is PlanStepStartData {
  return typeof data === 'object' && data !== null && 'step_id' in data
}

function isPlanStepCompleteData(data: unknown): data is PlanStepCompleteData {
  return typeof data === 'object' && data !== null && 'step_id' in data && 'success' in data
}

function isAssistantChunkData(data: unknown): data is AssistantChunkData {
  return typeof data === 'object' && data !== null && ('content' in data || 'accumulated_content' in data)
}

function isErrorData(data: unknown): data is { error: string } {
  return typeof data === 'object' && data !== null && 'error' in data
}

function isTaskCompleteData(data: unknown): data is { output?: string; attempt_count?: number; routing_decision?: Record<string, unknown> } {
  return typeof data === 'object' && data !== null && ('output' in data || 'attempt_count' in data || 'routing_decision' in data)
}

function isRetryData(data: unknown): data is { attempt: number; max_attempts: number } {
  return typeof data === 'object' && data !== null && 'attempt' in data && 'max_attempts' in data
}

function isServiceData(data: unknown): data is { content: string } {
  return typeof data === 'object' && data !== null && 'content' in data
}

function isACExtractedData(data: unknown): data is { count?: number; criteria?: Array<{ name: string; description: string }> } {
  return typeof data === 'object' && data !== null && ('count' in data || 'criteria' in data)
}

function isSubAgentLaunchData(data: unknown): data is { step_id: string; description: string; plan_step_id?: string } {
  return typeof data === 'object' && data !== null && 'step_id' in data
}

function isSubAgentCompleteData(data: unknown): data is { step_id: string; success: boolean; duration: number; plan_step_id?: string } {
  return typeof data === 'object' && data !== null && 'step_id' in data && 'success' in data
}

function isContextFillData(data: unknown): data is ContextFillData {
  return typeof data === 'object' && data !== null && 'fill_percent' in data && 'status' in data
}

function isSessionRenamedData(data: unknown): data is { new_name: string } {
  return typeof data === 'object' && data !== null && 'new_name' in data
}

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

    const panelStore = usePanelStore.getState()

    // Helper: only update global UI state if this session is still active
    const isActiveSession = () => useSessionStore.getState().activeSessionId === sessionId

    // Reset global UI state from previous session
    useChatStore.getState().clearSessionUIState()
    
    // Load persisted session token totals
    GetSessionTokens(sessionId).then((resp) => {
      if (!isActiveSession()) return
      const current = useChatStore.getState().contextFill
      if (current) {
        setContextFill({
          ...current,
          sessionInputTokens: resp.total_input_tokens ?? 0,
          sessionOutputTokens: resp.total_output_tokens ?? 0,
        })
      }
    }).catch(() => {}) // Ignore errors, will show 0
    
    // Reset panel data for new session (before any events arrive)
    panelStore.resetPanels()

    const unsubs: (() => void)[] = []
    const on = (type: string, cb: (...data: unknown[]) => void) => {
      unsubs.push(runtime.EventsOn(`session:${sessionId}:${type}`, cb))
    }

    on('routing', (data: unknown) => {
      if (!isRoutingData(data)) return
      const routing = data
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Analyzing request...')
      addMessage(sessionId, {
        id: `routing-${Date.now()}`,
        sessionId,
        type: 'routing',
        content: `Domain: ${routing.domain} | Complexity: ${routing.complexity}`,
        metadata: { domain: routing.domain, complexity: routing.complexity },
        timestamp: Date.now(),
      })
      panelStore.updateStats({ routingDomain: routing.domain, routingComplexity: routing.complexity })
    })

    on('step_start', (data: unknown) => {
      if (!isStepData(data)) return
      if (isActiveSession()) {
        setThinking(true)
        useChatStore.getState().setActivityStatus('Thinking...')
      }
      const step = data
      addMessage(sessionId, {
        id: `step-${step.step_num}`,
        sessionId,
        type: 'thinking',
        content: `Step ${step.step_num || ''}...`,
        timestamp: Date.now(),
      })
    })

    on('thought', (data: unknown) => {
      if (!isThoughtData(data)) return
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Reasoning...')
      const thought = data
      addMessage(sessionId, {
        id: `thought-${thought.step_num}-${Date.now()}`,
        sessionId,
        type: 'thought',
        content: thought.content,
        metadata: { step_num: thought.step_num, reasoning: thought.reasoning, plan_step_id: thought.plan_step_id },
        timestamp: Date.now(),
      })
    })

    on('tool_call', (data: unknown) => {
      if (!isToolCallData(data)) return
      const toolCall = data
      if (isActiveSession()) useChatStore.getState().setActivityStatus(`Running tool: ${toolCall.tool}...`)
      const toolMsgId = toolCall.plan_step_id
        ? `tool-${toolCall.plan_step_id}-${toolCall.step}`
        : `tool-${toolCall.step}`
      addMessage(sessionId, {
        id: toolMsgId,
        sessionId,
        type: 'tool_call',
        content: `${toolCall.tool}(${toolCall.args})`,
        metadata: { step: toolCall.step, tool: toolCall.tool, args: toolCall.args, parsed_args: toolCall.parsed_args, plan_step_id: toolCall.plan_step_id },
        timestamp: Date.now(),
      })
    })

    on('tool_result', (data: unknown) => {
      if (!isToolResultData(data)) return
      const toolResult = data
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
      if (!isToolConfirmData(data)) return
      const toolConfirm = data
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
          confirm_id: toolConfirm.confirm_id,
          tool: toolConfirm.tool,
          args: toolConfirm.args,
          reasoning: toolConfirm.reasoning,
          tool_msg_id: toolMsgId,
          plan_step_id: toolPlanStepId,
        } as Record<string, unknown>,
        timestamp: Date.now(),
      })

      if (isActiveSession()) {
        setThinking(false)
        useChatStore.getState().setActivityStatus('Awaiting confirmation...')
      }
    })

    on('ask_user', (data: unknown) => {
      if (!isAskUserData(data)) return
      const askData = data
      addMessage(sessionId, {
        id: `ask-user-${askData.request_id}`,
        sessionId,
        type: 'ask_user',
        content: askData.question,
        metadata: {
          request_id: askData.request_id,
          question: askData.question,
          options: askData.options,
          multi_select: askData.multi_select,
          recommended: askData.recommended,
        } as Record<string, unknown>,
        timestamp: Date.now(),
      })
      if (isActiveSession()) {
        setThinking(false)
        useChatStore.getState().setActivityStatus('Waiting for your answer...')
      }
    })

    on('step_complete', (data: unknown) => {
      if (!isStepData(data)) return
      if (isActiveSession()) setThinking(false)
      updateMessage(sessionId, `step-${(data as { step_num: number }).step_num}`, {
        type: 'step_done',
      })
    })

    on('evaluation', (data: unknown) => {
      if (!isEvalData(data)) return
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Evaluating results...')
      const evalData = data
      // Route to panelStore - update existing group instead of creating new
      if (evalData.criteria) {
        panelStore.updateEvalGroupStatuses(evalData.criteria)
      }
    })

    on('reflection', () => {
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Reflecting on results...')
    })

    on('plan_generated', (data: unknown) => {
      if (!isPlanData(data)) return
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Executing plan...')
      const plan = data
      // Route to panelStore
      if (plan.steps) {
        panelStore.addPlanGroup(plan.steps, {
          progress: plan.progress,
          completed_count: plan.completed_count,
          total_count: plan.total_count,
        })
      }
      // Also add to chat messages so groupMessages can build stepIndexMap
      addMessage(sessionId, {
        id: `plan-${Date.now()}`,
        sessionId,
        type: 'plan',
        content: '',
        metadata: {
          steps: plan.steps,
          progress: plan.progress,
          current_step_index: plan.current_step_index,
          completed_count: plan.completed_count,
          total_count: plan.total_count,
        },
        timestamp: Date.now(),
      })
    })

    on('plan_step_start', (data: unknown) => {
      if (!isPlanStepStartData(data)) return
      const stepData = data
      if (isActiveSession()) useChatStore.getState().setActivityStatus(`Executing: ${stepData.description || 'step'}...`)
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
      if (!isPlanStepCompleteData(data)) return
      const stepData = data
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
      if (!isAssistantChunkData(data)) return
      if (!isActiveSession()) return
      useChatStore.getState().setActivityStatus('Generating response...')
      const chunk = data
      if (chunk.accumulated_content !== undefined) {
        // Use backend-accumulated content (direct set, no delta accumulation needed)
        setStreaming(chunk.accumulated_content)
      } else if (chunk.content) {
        // Fallback for older backend: append delta
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
      if (!isErrorData(data)) return
      const error = data
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
      if (!isTaskCompleteData(data)) return
      if (isActiveSession()) {
        setThinking(false)
        setStreaming(null)
        useChatStore.getState().setActivityStatus(null)
        useChatStore.getState().setTaskActive(false)
      }
      const taskData = data
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
      if (!isRetryData(data)) return
      const retry = data
      if (isActiveSession()) useChatStore.getState().setActivityStatus(`Retrying (attempt ${retry.attempt}/${retry.max_attempts})...`)
      panelStore.resetEvalStatuses()  // Reset criteria to pending on retry
      addMessage(sessionId, {
        id: `retry-${Date.now()}`,
        sessionId,
        type: 'routing',
        content: `Retry attempt ${retry.attempt}/${retry.max_attempts}`,
        metadata: { ...retry },
        timestamp: Date.now(),
      })
      panelStore.updateStats({ attempt: retry.attempt + 1, maxAttempts: retry.max_attempts })
    })

    on('service', (data: unknown) => {
      if (!isServiceData(data)) return
      if (!isActiveSession()) return
      const service = data as { content: string; phase?: string }
      if (service.content) {
        useChatStore.getState().setActivityStatus(service.content)
      }
      // Add orchestration phases to chat timeline for visible progress
      if (service.phase === 'orchestration' && service.content) {
        addMessage(sessionId, {
          id: `status-${Date.now()}`,
          sessionId,
          type: 'status',
          content: service.content,
          timestamp: Date.now(),
        })
      }
    })

    on('ac_extracted', (data: unknown) => {
      if (!isACExtractedData(data)) return
      // Display extracted acceptance criteria as pending eval items
      const acData = data
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
      if (!isSubAgentLaunchData(data)) return
      if (isActiveSession()) useChatStore.getState().setActivityStatus('Launching sub-agent...')
      const sa = data
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
      if (!isSubAgentCompleteData(data)) return
      const sa = data
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
      if (!isContextFillData(data)) return
      if (!isActiveSession()) return
      const fillData = data
      setContextFill({
        fillPercent: fillData.fill_percent,
        usedTokens: fillData.used_tokens,
        maxTokens: fillData.max_tokens,
        status: fillData.status,
        sessionInputTokens: fillData.session_input_tokens ?? 0,
        sessionOutputTokens: fillData.session_output_tokens ?? 0,
      })
    })

    on('session_tokens', (data: unknown) => {
      if (!isSessionTokensData(data)) return
      if (!isActiveSession()) return
      const current = useChatStore.getState().contextFill
      setContextFill({
        ...(current ?? { fillPercent: 0, usedTokens: 0, maxTokens: 0, status: 'ok', sessionInputTokens: 0, sessionOutputTokens: 0 }),
        sessionInputTokens: data.session_input_tokens,
        sessionOutputTokens: data.session_output_tokens,
      })
    })

    on('session_renamed', (data: unknown) => {
      if (!isSessionRenamedData(data)) return
      const renameData = data
      updateSession(sessionId, { name: renameData.new_name })
    })

    return () => unsubs.forEach(fn => fn())
  }, [sessionId, runtime, addMessage, updateMessage, setStreaming, appendStreamToken, setThinking, setContextFill, updateSession])
}
