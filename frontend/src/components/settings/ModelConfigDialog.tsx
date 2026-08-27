import { useState, useEffect, useCallback } from 'react'
import { getModelConfig, setModelConfig } from '@/api/config'
import { logger } from '@/lib/logger'
import type { ModelCapabilities } from '@/types/models'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Combobox } from '@/components/ui/combobox'

interface ModelConfigDialogProps {
  /** Bare model name to configure. */
  model: string
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Optional callback after a successful save. */
  onSaved?: () => void
}

// Curated dropdown option lists. The backend validates against the same sets —
// these mirror the sp4rk llm.Family*/Protocol* constants and the NewTokenCounter
// switch. No free text is permitted.
const TOKENIZER_OPTIONS: Array<{ value: string; label: string }> = [
  { value: 'approximate', label: 'Approximate (~4 chars/token)' },
  { value: 'tiktoken/o200k_base', label: 'Tiktoken — o200k_base (GPT-4o/o-series)' },
  { value: 'tiktoken/cl100k_base', label: 'Tiktoken — cl100k_base (GPT-4)' },
  { value: 'anthropic-api', label: 'Anthropic API (server-corrected)' },
]

const FAMILY_OPTIONS: Array<{ value: string; label: string }> = [
  { value: 'anthropic', label: 'Anthropic (Claude)' },
  { value: 'openai_flagship', label: 'OpenAI Flagship (GPT-4o/o-series)' },
  { value: 'openai_standard', label: 'OpenAI Standard (GPT-4.1)' },
  { value: 'openai_codex', label: 'OpenAI Codex' },
  { value: 'google', label: 'Google (Gemini/Gemma)' },
  { value: 'mistral', label: 'Mistral / Codestral' },
  { value: 'deepseek', label: 'DeepSeek' },
  { value: 'qwen', label: 'Qwen / QwQ' },
  { value: 'glm', label: 'GLM / ChatGLM' },
  { value: 'kimi', label: 'Kimi (Moonshot)' },
  { value: 'default', label: 'Default (generic)' },
]

const PROTOCOL_OPTIONS: Array<{ value: string; label: string }> = [
  { value: 'chat_completions', label: 'Chat Completions (/chat/completions)' },
  { value: 'responses', label: 'Responses (/responses)' },
  { value: 'anthropic', label: 'Anthropic Messages (/messages)' },
  { value: 'google', label: 'Google (/models/{model}:generateContent)' },
]

const CAPABILITY_FIELDS: Array<{ key: keyof ModelCapabilities; label: string; hint: string }> = [
  { key: 'attachment', label: 'Attachments', hint: 'Image / PDF support' },
  { key: 'reasoning', label: 'Reasoning', hint: 'Thinking / extended-thinking mode' },
  { key: 'temperature', label: 'Temperature', hint: 'Accepts the temperature parameter' },
  { key: 'tool_call', label: 'Tool Calls', hint: 'Function calling support' },
]

/**
 * Per-model Configure dialog. Lets the user override a model's context window,
 * output limit, tokenizer type, family, protocol, and capabilities. Inputs are
 * pre-filled with the currently-effective value (override value when set,
 * otherwise the built-in default) and the built-in factory default is shown as a
 * hint. On save, only values that differ from the built-in default are persisted
 * to config.yaml (the backend handles the field-level omission); values equal to
 * the default clear the override.
 */
export function ModelConfigDialog({ model, open, onOpenChange, onSaved }: ModelConfigDialogProps) {
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Number field state is `number | ''`: '' lets the user clear the input to
  // retype without snapping to 0 (Number('') === 0). At save time an empty
  // field falls back to the built-in default so the backend always receives a
  // positive integer.
  const [contextWindow, setContextWindow] = useState<number | ''>(0)
  const [outputLimit, setOutputLimit] = useState<number | ''>(0)
  const [tokenizerType, setTokenizerType] = useState('')
  const [family, setFamily] = useState('')
  const [protocol, setProtocol] = useState('')
  const [capabilities, setCapabilities] = useState<ModelCapabilities>({
    attachment: false,
    reasoning: false,
    temperature: false,
    tool_call: false,
  })
  // Built-in factory defaults, exposed for the "reset to default" affordance.
  const [defaults, setDefaults] = useState<{
    contextWindow: number
    outputLimit: number
    tokenizerType: string
    family: string
    protocol: string
    capabilities: ModelCapabilities
  }>({
    contextWindow: 0,
    outputLimit: 0,
    tokenizerType: '',
    family: '',
    protocol: '',
    capabilities: { attachment: false, reasoning: false, temperature: false, tool_call: false },
  })

  // Fetch the model's effective config + built-in defaults whenever the dialog
  // opens for a (possibly different) model. The effect re-runs when `open`
  // transitions to true or `model` changes while open.
  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const cfg = await getModelConfig(model)
      setContextWindow(cfg.context_window)
      setOutputLimit(cfg.output_limit)
      setTokenizerType(cfg.tokenizer_type)
      setFamily(cfg.family)
      setProtocol(cfg.protocol)
      setCapabilities(cfg.capabilities)
      setDefaults({
        contextWindow: cfg.default_context_window,
        outputLimit: cfg.default_output_limit,
        tokenizerType: cfg.default_tokenizer_type,
        family: cfg.default_family,
        protocol: cfg.default_protocol,
        capabilities: cfg.default_capabilities,
      })
    } catch (err) {
      logger.error('Failed to load model config:', err)
      setError('Failed to load model configuration.')
    } finally {
      setLoading(false)
    }
  }, [model])

  useEffect(() => {
    if (open) load()
  }, [open, load])

  const handleSave = async () => {
    setSaving(true)
    setError(null)
    try {
      // Empty number fields fall back to the built-in default so the request
      // always carries a positive integer (the backend rejects <= 0).
      await setModelConfig(model, {
        context_window: contextWindow === '' ? defaults.contextWindow : contextWindow,
        output_limit: outputLimit === '' ? defaults.outputLimit : outputLimit,
        tokenizer_type: tokenizerType,
        family,
        protocol,
        capabilities,
      })
      onSaved?.()
      onOpenChange(false)
    } catch (err) {
      logger.error('Failed to save model config:', err)
      const detail = err instanceof Error ? err.message : String(err)
      setError(`Failed to save model configuration: ${detail}`)
    } finally {
      setSaving(false)
    }
  }

  const capsAtDefault =
    capabilities.attachment === defaults.capabilities.attachment &&
    capabilities.reasoning === defaults.capabilities.reasoning &&
    capabilities.temperature === defaults.capabilities.temperature &&
    capabilities.tool_call === defaults.capabilities.tool_call

  const toggleCapability = (key: keyof ModelCapabilities) =>
    setCapabilities((prev) => ({ ...prev, [key]: !prev[key] }))

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md max-h-[85vh] overflow-y-auto custom-scrollbar">
        <DialogHeader>
          <DialogTitle>Configure {model}</DialogTitle>
          <DialogDescription>
            Override the model's parameters. Values equal to the built-in default
            are not persisted.
          </DialogDescription>
        </DialogHeader>

        {loading ? (
          <div className="flex items-center justify-center py-8">
            <span className="text-sm text-muted-foreground">Loading...</span>
          </div>
        ) : (
          <div className="space-y-4">
            <NumberField
              label="Context Window"
              value={contextWindow}
              onChange={setContextWindow}
              defaultValue={defaults.contextWindow}
              onReset={() => setContextWindow(defaults.contextWindow)}
              disabled={saving}
            />
            <NumberField
              label="Max Output Tokens"
              value={outputLimit}
              onChange={setOutputLimit}
              defaultValue={defaults.outputLimit}
              onReset={() => setOutputLimit(defaults.outputLimit)}
              disabled={saving}
            />
            <SelectField
              label="Tokenizer Type"
              value={tokenizerType}
              options={TOKENIZER_OPTIONS}
              onChange={setTokenizerType}
              defaultValue={defaults.tokenizerType}
              onReset={() => setTokenizerType(defaults.tokenizerType)}
              disabled={saving}
            />
            <SelectField
              label="Family"
              value={family}
              options={FAMILY_OPTIONS}
              onChange={setFamily}
              defaultValue={defaults.family}
              onReset={() => setFamily(defaults.family)}
              disabled={saving}
            />
            <SelectField
              label="Protocol"
              value={protocol}
              options={PROTOCOL_OPTIONS}
              onChange={setProtocol}
              defaultValue={defaults.protocol}
              onReset={() => setProtocol(defaults.protocol)}
              disabled={saving}
            />
            <CapabilitiesField
              capabilities={capabilities}
              onToggle={toggleCapability}
              onReset={() => setCapabilities(defaults.capabilities)}
              capsAtDefault={capsAtDefault}
              disabled={saving}
            />
            {error && (
              <p className="text-xs text-destructive">{error}</p>
            )}
          </div>
        )}

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={saving}>
            Cancel
          </Button>
          <Button onClick={handleSave} disabled={loading || saving}>
            {saving ? 'Saving...' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// Field components
// ---------------------------------------------------------------------------

interface NumberFieldProps {
  label: string
  value: number | ''
  onChange: (value: number | '') => void
  defaultValue: number
  onReset: () => void
  disabled?: boolean
}

/** A numeric config field with default hint and reset-to-default action. */
function NumberField({ label, value, onChange, defaultValue, onReset, disabled }: NumberFieldProps) {
  // An empty field falls back to the default at save time, so treat it as
  // "using default" for the hint/reset affordance.
  const isDefault = value === '' || value === defaultValue
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between">
        <label className="text-sm font-medium text-foreground">{label}</label>
        {!isDefault && <ResetButton onClick={onReset} disabled={disabled} />}
      </div>
      <Input
        type="number"
        min={1}
        value={value}
        onChange={(e) => onChange(e.target.value === '' ? '' : Number(e.target.value))}
        disabled={disabled}
        className="h-9 text-sm"
      />
      <p className="text-xs text-muted-foreground">
        Default: {defaultValue.toLocaleString('en-US')} tokens
        {isDefault ? ' (using default)' : ' (overridden)'}
      </p>
    </div>
  )
}

interface SelectFieldProps {
  label: string
  value: string
  options: Array<{ value: string; label: string }>
  onChange: (value: string) => void
  defaultValue: string
  onReset: () => void
  disabled?: boolean
}

/** A dropdown select field with default hint and reset-to-default action. */
function SelectField({ label, value, options, onChange, defaultValue, onReset, disabled }: SelectFieldProps) {
  const isDefault = value === defaultValue
  const defaultLabel = options.find((o) => o.value === defaultValue)?.label ?? defaultValue
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between">
        <label className="text-sm font-medium text-foreground">{label}</label>
        {!isDefault && <ResetButton onClick={onReset} disabled={disabled} />}
      </div>
      <Combobox
        ariaLabel={label}
        value={value}
        onChange={onChange}
        disabled={disabled}
        options={options}
      />
      <p className="text-xs text-muted-foreground">
        Default: {defaultLabel}
        {isDefault ? ' (using default)' : ' (overridden)'}
      </p>
    </div>
  )
}

interface CapabilitiesFieldProps {
  capabilities: ModelCapabilities
  onToggle: (key: keyof ModelCapabilities) => void
  onReset: () => void
  capsAtDefault: boolean
  disabled?: boolean
}

/** A group of capability toggle checkboxes with a section-level reset. */
function CapabilitiesField({ capabilities, onToggle, onReset, capsAtDefault, disabled }: CapabilitiesFieldProps) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between">
        <label className="text-sm font-medium text-foreground">Capabilities</label>
        {!capsAtDefault && <ResetButton onClick={onReset} disabled={disabled} />}
      </div>
      <div className="space-y-2 rounded-md border border-border p-3">
        {CAPABILITY_FIELDS.map(({ key, label, hint }) => (
          <label
            key={key}
            className="flex cursor-pointer items-center gap-2.5 select-none"
          >
            <input
              type="checkbox"
              checked={capabilities[key]}
              onChange={() => onToggle(key)}
              disabled={disabled}
              className="size-4 rounded border-border accent-[rgb(var(--primary-rgb))]"
            />
            <div className="flex flex-col">
              <span className="text-sm text-foreground">{label}</span>
              <span className="text-xs text-muted-foreground">{hint}</span>
            </div>
          </label>
        ))}
      </div>
      <p className="text-xs text-muted-foreground">
        {capsAtDefault ? 'Using defaults' : 'Overridden — reset to restore defaults'}
      </p>
    </div>
  )
}

/** Small inline "Reset to default" link shown when a field differs from default. */
function ResetButton({ onClick, disabled }: { onClick: () => void; disabled?: boolean }) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="text-xs text-muted-foreground underline-offset-2 hover:underline disabled:opacity-50"
    >
      Reset to default
    </button>
  )
}
