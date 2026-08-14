import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useSettingsStore } from '@/stores/settingsStore'
import { ConfigWarningBanner } from './ConfigWarningBanner'
import { LogLevelSelector } from './LogLevelSelector'
import { ThemeSelector } from './ThemeSelector'
import { ProxySettings } from './ProxySettings'
import { SoundSettings } from './SoundSettings'
import { LLMSettings } from './LLMSettings'
import { SmallLLMSettings } from './SmallLLMSettings'
import { SearchSettings } from './SearchSettings'
import { MCPSettings } from './MCPSettings'
import { SecuritySettings } from './SecuritySettings'
import { UpdateSettings } from './UpdateSettings'
import { Settings, Brain, Search, Shield, Info, Server, AlertTriangle, X, Gauge } from 'lucide-react'
import { useState, useEffect, useRef, useCallback } from 'react'
import { hasDefaultModel } from '@/api/config'

export function SettingsModal() {
  const open = useSettingsStore((s) => s.open)
  const activeTab = useSettingsStore((s) => s.activeTab)
  const closeSettings = useSettingsStore((s) => s.closeSettings)
  const setActiveTab = useSettingsStore((s) => s.setActiveTab)
  const [bannerRefreshKey, setBannerRefreshKey] = useState(0)
  const [closeBlocked, setCloseBlocked] = useState(false)
  const [checkingClose, setCheckingClose] = useState(false)
  const [currentDefaultModel, setCurrentDefaultModel] = useState('')
  const checkingRef = useRef(false)
  const prevOpenRef = useRef(open)

  useEffect(() => {
    if (open && !prevOpenRef.current) {
      setBannerRefreshKey((k) => k + 1)
      setCloseBlocked(false)
      // Pessimistically reset the cached default so the close fast-path can't
      // fire on a stale value left over from a previous open. LLMSettings
      // remounts on open and its loadConfig reports the effective default via
      // onDefaultModelChange, repopulating this before the user can act.
      setCurrentDefaultModel('')
    }
    prevOpenRef.current = open
  }, [open])

  const handleDefaultModelChange = useCallback((model: string) => {
    setCurrentDefaultModel(model)
    if (model) {
      setCloseBlocked(false)
    }
  }, [])

  const handleSettingsSaved = useCallback(() => {
    setBannerRefreshKey((k) => k + 1)
    // A save can REMOVE the default (provider/model deletion), so we cannot
    // blindly clear closeBlocked. But a save that PRESERVES a valid default
    // should not leave a stale "default not configured" banner on screen: if
    // local UI state already has a model, clear the block defensively. The
    // valid-default case is also reported through handleDefaultModelChange
    // (fired with a non-empty model), and close-block correctness is
    // ultimately driven by the authoritative hasDefaultModel re-check in
    // handleOpenChange.
    if (currentDefaultModel) {
      setCloseBlocked(false)
    }
  }, [currentDefaultModel])

  const handleOpenChange = useCallback(async (isOpen: boolean) => {
    if (!isOpen) {
      // Fast path: if local UI state already has a model, allow close immediately.
      if (currentDefaultModel) {
        setCloseBlocked(false)
        closeSettings()
        return
      }
      // Guard against concurrent close checks.
      if (checkingRef.current) return
      checkingRef.current = true
      setCheckingClose(true)
      try {
        const hasDefault = await hasDefaultModel()
        if (!hasDefault) {
          setCloseBlocked(true)
          setActiveTab('llm')
          return
        }
      } catch {
        // If config is unavailable, allow close.
      } finally {
        checkingRef.current = false
        setCheckingClose(false)
      }
      setCloseBlocked(false)
      closeSettings()
    }
  }, [closeSettings, currentDefaultModel, setActiveTab])

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-[600px] max-h-[80vh] flex flex-col overflow-hidden" showCloseButton={false}>
        <DialogHeader className="flex flex-row items-center justify-between">
          <DialogTitle>Settings</DialogTitle>
          <button
            onClick={() => handleOpenChange(false)}
            disabled={checkingClose}
            className="rounded-xs opacity-70 transition-opacity hover:opacity-100 focus:outline-none disabled:opacity-50"
            aria-label="Close"
          >
            {checkingClose ? (
              <div className="size-4 animate-spin rounded-full border-2 border-primary border-t-transparent" />
            ) : (
              <X className="size-4" />
            )}
          </button>
        </DialogHeader>

        {closeBlocked && (
          <div className="flex items-start gap-2 rounded-md bg-destructive/10 border border-destructive/20 p-3 text-sm">
            <AlertTriangle className="h-4 w-4 text-destructive flex-shrink-0 mt-0.5" />
            <span className="text-destructive font-medium">Default model is not configured.</span>
          </div>
        )}

        <Tabs
          value={activeTab}
          onValueChange={(v) => setActiveTab(v as typeof activeTab)}
          className="mt-4 flex-1 flex flex-col overflow-hidden min-h-0"
        >
          <ConfigWarningBanner className="mb-2" refreshKey={bannerRefreshKey} />
          <TabsList className="grid w-full grid-cols-7">
            <TabsTrigger value="general" className="gap-1">
              <Settings className="h-4 w-4" />
              <span className="hidden sm:inline text-xs">General</span>
            </TabsTrigger>
            <TabsTrigger value="llm" className="gap-1">
              <Brain className="h-4 w-4" />
              <span className="hidden sm:inline text-xs">LLM</span>
            </TabsTrigger>
            <TabsTrigger value="small-llm" className="gap-1">
              <Gauge className="h-4 w-4" />
              <span className="hidden sm:inline text-xs">Small LLM</span>
            </TabsTrigger>
            <TabsTrigger value="search" className="gap-1">
              <Search className="h-4 w-4" />
              <span className="hidden sm:inline text-xs">Search</span>
            </TabsTrigger>
            <TabsTrigger value="mcp" className="gap-1">
              <Server className="h-4 w-4" />
              <span className="hidden sm:inline text-xs">MCP</span>
            </TabsTrigger>
            <TabsTrigger value="security" className="gap-1">
              <Shield className="h-4 w-4" />
              <span className="hidden sm:inline text-xs">Security</span>
            </TabsTrigger>
            <TabsTrigger value="about" className="gap-1">
              <Info className="h-4 w-4" />
              <span className="hidden sm:inline text-xs">About</span>
            </TabsTrigger>
          </TabsList>

          <TabsContent value="general" className="mt-4 overflow-y-auto min-h-0 custom-scrollbar">
            <div className="space-y-6">
              <ThemeSelector />
              <LogLevelSelector />
              <div className="border-t border-border pt-4">
                <SoundSettings />
              </div>
              <div className="border-t border-border pt-4">
                <h3 className="text-sm font-medium mb-3">HTTP Proxy</h3>
                <ProxySettings />
              </div>
            </div>
          </TabsContent>

          <TabsContent value="llm" className="mt-4 overflow-y-auto min-h-0 custom-scrollbar">
            <LLMSettings onSettingsSaved={handleSettingsSaved} onDefaultModelChange={handleDefaultModelChange} />
          </TabsContent>

          <TabsContent value="small-llm" className="mt-4 overflow-y-auto min-h-0 custom-scrollbar">
            <SmallLLMSettings />
          </TabsContent>

          <TabsContent value="search" className="mt-4 overflow-y-auto min-h-0 custom-scrollbar">
            <SearchSettings />
          </TabsContent>

          <TabsContent value="mcp" className="mt-4 overflow-y-auto min-h-0 custom-scrollbar">
            <MCPSettings />
          </TabsContent>

          <TabsContent value="security" className="mt-4 overflow-y-auto min-h-0 custom-scrollbar">
            <SecuritySettings />
          </TabsContent>

          <TabsContent value="about" className="mt-4 overflow-y-auto min-h-0 custom-scrollbar">
            <div className="space-y-4">
              <div className="flex items-center gap-3">
                <div className="h-12 w-12 rounded-lg bg-primary/10 flex items-center justify-center">
                  <span className="text-xl font-bold text-primary">c0</span>
                </div>
                <div>
                  <h3 className="font-semibold">c0wrk</h3>
                  <p className="text-sm text-muted-foreground">Desktop AI Coding Agent</p>
                </div>
              </div>
              <div className="text-sm text-muted-foreground space-y-2">
                <p>An AI-powered coding assistant with multi-agent orchestration.</p>
                <p>Built with warmth, love, and AI.</p>
              </div>
              <UpdateSettings />
            </div>
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  )
}
