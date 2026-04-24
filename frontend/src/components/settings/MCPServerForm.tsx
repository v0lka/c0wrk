import { useState } from 'react'
import { Plus, X, AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
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
}

interface KeyValueEntry { id: number; key: string; value: string }

let nextId = 1
function makeEntry(key = '', value = ''): KeyValueEntry {
  return { id: nextId++, key, value }
}

const emptyForm: ServerFormData = {
  name: '', transport: 'stdio', command: '', args: '', env: {}, url: '', headers: {},
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
      return { name: editingName, transport: isStdio ? 'stdio' : 'http', command: cfg.command || '', args: cfg.args?.join(', ') || '', env: cfg.env || {}, url: cfg.url || '', headers: cfg.headers || {} }
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
            <div className="flex gap-2 p-1 bg-muted rounded-lg">
              {(['stdio', 'http'] as const).map((t) => (
                <Button key={t} variant={formData.transport === t ? 'secondary' : 'ghost'} size="sm" className="flex-1" onClick={() => setFormData({ ...formData, transport: t })}>{t}</Button>
              ))}
            </div>
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
  keyPlaceholder: string; valuePlaceholder: string; addLabel: string
}) {
  return (
    <div className="space-y-2">
      <label className="text-xs text-muted-foreground">{label}</label>
      {entries.map((entry, i) => (
        <div key={entry.id} className="flex gap-2">
          <Input placeholder={keyPlaceholder} value={entry.key} onChange={(e) => setEntries((prev) => prev.map((en, idx) => idx === i ? { ...en, key: e.target.value } : en))} className="h-8 font-mono text-xs flex-1" />
          <Input placeholder={valuePlaceholder} value={entry.value} onChange={(e) => setEntries((prev) => prev.map((en, idx) => idx === i ? { ...en, value: e.target.value } : en))} className="h-8 font-mono text-xs flex-1" />
          <Button variant="ghost" size="sm" className="h-8 w-8 p-0" onClick={() => setEntries((prev) => prev.filter((_, idx) => idx !== i))}><X className="h-3 w-3" /></Button>
        </div>
      ))}
      <Button variant="outline" size="sm" onClick={() => setEntries((prev) => [...prev, makeEntry()])}><Plus className="h-3 w-3 mr-1" />{addLabel}</Button>
    </div>
  )
}
