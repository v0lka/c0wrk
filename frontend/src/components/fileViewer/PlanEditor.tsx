import { useState, useEffect, useRef, useCallback } from 'react'
import { MiniCodeMirrorField } from './MiniCodeMirrorField'
import { parsePlanMarkdown, serializePlanMarkdown, type ParsedPlan, type ParsedStep } from '@/lib/planParser'
import { writeFile } from '@/api/files'
import { useSessionStore } from '@/stores/sessionStore'

interface PlanEditorProps {
  content: string
  path: string
}

/**
 * PlanEditor renders the structured plan editing UI in the file viewer panel.
 * Step titles are read-only; What/Where/How/AC fields are editable via mini CodeMirror.
 * Auto-saves to disk with 500ms debounce. Re-reads file on window focus.
 */
export function PlanEditor({ content, path }: PlanEditorProps) {
  const activeSessionId = useSessionStore((s) => s.activeSessionId)
  const [parsed, setParsed] = useState<ParsedPlan>(() => parsePlanMarkdown(content))
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const lastSavedRef = useRef(content)
  const parsedRef = useRef(parsed)
  const pathRef = useRef(path)
  const sessionIdRef = useRef(activeSessionId)
  parsedRef.current = parsed
  pathRef.current = path
  sessionIdRef.current = activeSessionId

  // Re-parse when content prop changes (e.g., file reload)
  useEffect(() => {
    setParsed(parsePlanMarkdown(content))
    lastSavedRef.current = content
  }, [content])

  const saveNow = useCallback((plan: ParsedPlan) => {
    const md = serializePlanMarkdown(plan)
    if (md !== lastSavedRef.current) {
      lastSavedRef.current = md
      writeFile(sessionIdRef.current ?? '', pathRef.current, md).catch(() => { /* ignore save errors */ })
    }
  }, []) // uses pathRef.current, stable across re-renders

  const scheduleSave = useCallback((plan: ParsedPlan) => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => saveNow(plan), 500)
  }, [saveNow])

  // Flush on unmount only (e.g. tab close, file switch). Clear debounce here
  // instead of on every render so the 500ms timer can actually fire.
  useEffect(() => {
    return () => {
      if (debounceRef.current) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
      saveNow(parsedRef.current)
    }
  }, [saveNow])

  const handleFieldChange = useCallback((stepIndex: number, field: keyof ParsedStep, value: string) => {
    setParsed((prev) => {
      const newSteps = [...prev.steps]
      newSteps[stepIndex] = { ...newSteps[stepIndex]!, [field]: value }
      const updated = { steps: newSteps }
      scheduleSave(updated)
      return updated
    })
  }, [scheduleSave])

  if (parsed.steps.length === 0) {
    return (
      <div className="flex-1 flex items-center justify-center text-muted-foreground p-4">
        No steps found in plan.
      </div>
    )
  }

  return (
    <div className="flex-1 overflow-auto custom-scrollbar p-4 space-y-6">
      {parsed.steps.map((step, i) => (
        <div key={i} className="border border-border rounded-lg p-4 space-y-3">
          <h3 className="text-base font-medium text-highlight select-none">
            Step {i + 1}: {step.title.replace(/^Step \d+: /, '')}
          </h3>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="text-xs font-medium text-muted-foreground mb-1 block">What</label>
              <MiniCodeMirrorField
                value={step.what}
                onChange={(v) => handleFieldChange(i, 'what', v)}
              />
            </div>
            <div>
              <label className="text-xs font-medium text-muted-foreground mb-1 block">Where</label>
              <MiniCodeMirrorField
                value={step.where}
                onChange={(v) => handleFieldChange(i, 'where', v)}
              />
            </div>
          </div>

          <div>
            <label className="text-xs font-medium text-muted-foreground mb-1 block">How</label>
            <MiniCodeMirrorField
              value={step.how}
              onChange={(v) => handleFieldChange(i, 'how', v)}
            />
          </div>

          <div>
            <label className="text-xs font-medium text-muted-foreground mb-1 block">Acceptance Criteria</label>
            <MiniCodeMirrorField
              value={step.acceptanceCriteria}
              onChange={(v) => handleFieldChange(i, 'acceptanceCriteria', v)}
            />
          </div>
        </div>
      ))}
    </div>
  )
}
