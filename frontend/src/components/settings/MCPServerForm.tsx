import { useState } from 'react'
import { Plus, X, AlertCircle, Terminal, Globe } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { SegmentedControl } from '@/components/ui/segmented-control'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogDescription,
} from '@/components/ui/dialog'
import type { MCPServerConfig } from '@/types/models'

type TransportType = 'stdio' | 'http'

interface ServerFormData {
  name: string
  transport: TransportType
  command: string
  args: string
  env: Record<string, string>
  url: string
  headers: Record<string, string>
  timeout: string
  callTimeout: string
}

interface KeyValueEntry { id: string; key: string; value: string }

function makeEntry(key = '', value = ''): KeyValueEntry {
  return { id: crypto.randomUUID(), key, value }
}

const emptyForm: ServerFormData = {
  name: '', transport: 'stdio', command: '', args: '', env: {}, url: '', headers: {}, timeout: '', callTimeout: '',
}

interface MCPServerFormProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  editingName: string | null
  serverConfigs: Record<string, MCPServerConfig>
  editServer?: { name: string; transport: string }
  isSaving: boolean
  onSave: (config: Record<string, MCPServerConfig>, editName: string | null) => Promise<string | null>
}

export function MCPServerForm({ open, onOpenChange, editingName, serverConfigs, editServer, isSaving, onSave }: MCPServerFormProps) {
  const [formData, setFormData] = useState<ServerFormData>(() => {
    if (editingName && serverConfigs[editingName]) {
      const cfg = serverConfigs[editingName]
      const isStdio = editServer?.transport === 'stdio'
      return { name: editingName, transport: isStdio ? 'stdio' : 'http', command: cfg.command || '', args: cfg.args?.join(', ') || '', env: cfg.env || {}, url: cfg.url || '', headers: cfg.headers || {}, timeout: cfg.timeout || '', callTimeout: cfg.call_timeout || '' }
    }
    return emptyForm
  })
  const [envEntries, setEnvEntries] = useState<KeyValueEntry[]>(() => {
    if (editingName && serverConfigs[editingName]?.env) {
      return Object.entries(serverConfigs[editingName].env).map(([k, v]) => makeEntry(k, v))
    }
    return []
  })
  const [headerEntries, setHeaderEntries] = useState<KeyValueEntry[]>(() => {
    if (editingName && serverConfigs[editingName]?.headers) {
      return Object.entries(serverConfigs[editingName].headers).map(([k, v]) => makeEntry(k, v))
    }
    return []
  })
  const [formError, setFormError] = useState<string | null>(null)

  const handleSave = async () => {
    if (!formData.name.trim()) { setFormError('Server name is required'); return }
    if (formData.name.includes(' ') || formData.name.includes('.')) { setFormError('Server name cannot contain spaces or dots'); return }

    const env: Record<string, string> = {}
    envEntries.forEach((e) => { if (e.key.trim()) env[e.key.trim()] = e.value })
    const headers: Record<string, string> = {}
    headerEntries.forEach((e) => { if (e.key.trim()) headers[e.key.trim()] = e.value })

    const isStdio = formData.transport === 'stdio'
    const newConfig: MCPServerConfig = {
      transport: formData.transport,
      command: isStdio ? formData.command : '',
      args: isStdio ? formData.args.split(',').map((a) => a.trim()).filter(Boolean) : [],
      env: isStdio ? env : {},
      url: isStdio ? '' : formData.url,
      headers: isStdio ? {} : headers,
      timeout: formData.timeout.trim(),
      call_timeout: formData.callTimeout.trim(),
    }

    const newServers = { ...serverConfigs }
    if (editingName) delete newServers[editingName]
    newServers[formData.name] = newConfig

    const err = await onSave(newServers, editingName)
    if (err) setFormError(err)
    else onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[500px]">
        <DialogHeader>
          <DialogTitle>{editingName ? 'Edit MCP Server' : 'Add MCP Server'}</DialogTitle>
          <DialogDescription>Configure an MCP server connection.</DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-4">
          {formError && (
            <div className="flex items-start gap-2 p-2 rounded bg-destructive/10 text-sm">
              <AlertCircle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
              <p className="text-destructive">{formError}</p>
            </div>
          )}

          <Field label="Server Name">
            <Input placeholder="my-mcp-server" value={formData.name} onChange={(e) => setFormData({ ...formData, name: e.target.value })} disabled={!!editingName} className="h-9" />
          </Field>

          <Field label="Transport Type">
            <SegmentedControl<'stdio' | 'http'>
              semantic="radio"
              fullWidth
              ariaLabel="Transport type"
              size="md"
              itemClassName="flex-1 h-9"
              items={[
                { value: 'stdio', label: 'stdio', icon: <Terminal className="size-4" /> },
                { value: 'http', label: 'http', icon: <Globe className="size-4" /> },
              ]}
              value={formData.transport}
              onValueChange={(t) => setFormData({ ...formData, transport: t })}
            />
          </Field>

          {formData.transport === 'stdio' ? (
            <>
              <Field label="Command"><Input placeholder="/usr/local/bin/mcp-server" value={formData.command} onChange={(e) => setFormData({ ...formData, command: e.target.value })} className="h-9 font-mono text-sm" /></Field>
              <Field label="Arguments (comma-separated)"><Input placeholder="--port, 8080" value={formData.args} onChange={(e) => setFormData({ ...formData, args: e.target.value })} className="h-9 font-mono text-sm" /></Field>
              <KeyValueList label="Environment Variables" entries={envEntries} setEntries={setEnvEntries} keyPlaceholder="KEY" valuePlaceholder="value" addLabel="Add Variable" />
            </>
          ) : (
            <>
              <Field label="URL"><Input placeholder="http://localhost:8080/mcp" value={formData.url} onChange={(e) => setFormData({ ...formData, url: e.target.value })} className="h-9 font-mono text-sm" /></Field>
              <KeyValueList label="Headers" entries={headerEntries} setEntries={setHeaderEntries} keyPlaceholder="Authorization" valuePlaceholder="Bearer ${API_KEY}" addLabel="Add Header" />
            </>
          )}

          <div className="grid grid-cols-2 gap-3">
            <Field label="Connect Timeout">
              <Input placeholder="60s" value={formData.timeout} onChange={(e) => setFormData({ ...formData, timeout: e.target.value })} className="h-9 font-mono text-sm" />
            </Field>
            <Field label="Call Timeout">
              <Input placeholder="60s" value={formData.callTimeout} onChange={(e) => setFormData({ ...formData, callTimeout: e.target.value })} className="h-9 font-mono text-sm" />
            </Field>
          </div>
          <p className="text-xs text-muted-foreground">Optional durations (e.g. 30s, 2m). Connect bounds the handshake and is the default bound for every tool call; Call overrides it for a single call. An empty Call inherits Connect; both empty use the 60s default.</p>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={handleSave} disabled={isSaving}>{isSaving ? 'Saving...' : 'Save'}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="space-y-2"><label className="text-xs text-muted-foreground">{label}</label>{children}</div>
}

function KeyValueList({ label, entries, setEntries, keyPlaceholder, valuePlaceholder, addLabel }: {
  label: string; entries: KeyValueEntry[]; setEntries: React.Dispatch<React.SetStateAction<KeyValueEntry[]>>
  keyPlaceholder: string; valuePlaceholder: string; addLabel: 'Add Variable' | 'Add Header'
}) {
  return (
    <div className="space-y-2">
      <label className="text-xs text-muted-foreground">{label}</label>
      {entries.map((entry, i) => (
        <div key={entry.id} className="flex gap-2">
          <Input placeholder={keyPlaceholder} value={entry.key} onChange={(e) => setEntries((prev) => prev.map((en, idx) => idx === i ? { ...en, key: e.target.value } : en))} className="h-8 font-mono text-xs flex-1" />
          <Input placeholder={valuePlaceholder} value={entry.value} onChange={(e) => setEntries((prev) => prev.map((en, idx) => idx === i ? { ...en, value: e.target.value } : en))} className="h-8 font-mono text-xs flex-1" />
          <Button variant="ghost" size="sm" className="h-8 w-8 p-0" title="Remove entry" onClick={() => setEntries((prev) => prev.filter((_, idx) => idx !== i))}><X className="h-3 w-3" /></Button>
        </div>
      ))}
      <div className="flex justify-end">
        <Button variant="outline" size="sm" className="h-7 px-2 text-xs" onClick={() => setEntries((prev) => [...prev, makeEntry()])}>
          <Plus className="h-3 w-3 mr-1" />
          {/* Literal ternary so the buttonTitleInvariant guard proves the
              static label — the label IS the explanation, no echo title. */}
          {addLabel === 'Add Variable' ? 'Add Variable' : 'Add Header'}
        </Button>
      </div>
    </div>
  )
}
