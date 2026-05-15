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
import { ProxySettings } from './ProxySettings'
import { LLMSettings } from './LLMSettings'
import { SearchSettings } from './SearchSettings'
import { MCPSettings } from './MCPSettings'
import { SecuritySettings } from './SecuritySettings'
import { Settings, Brain, Search, Shield, Info, Server } from 'lucide-react'
import { useState, useEffect, useRef, useCallback } from 'react'

export function SettingsModal() {
  const open = useSettingsStore((s) => s.open)
  const activeTab = useSettingsStore((s) => s.activeTab)
  const closeSettings = useSettingsStore((s) => s.closeSettings)
  const setActiveTab = useSettingsStore((s) => s.setActiveTab)
  const [bannerRefreshKey, setBannerRefreshKey] = useState(0)
  const prevOpenRef = useRef(open)

  useEffect(() => {
    if (open && !prevOpenRef.current) {
      setBannerRefreshKey((k) => k + 1)
    }
    prevOpenRef.current = open
  }, [open])

  const handleSettingsSaved = useCallback(() => {
    setBannerRefreshKey((k) => k + 1)
  }, [])

  return (
    <Dialog open={open} onOpenChange={(isOpen) => { if (!isOpen) closeSettings() }}>
      <DialogContent className="sm:max-w-[600px] max-h-[80vh] flex flex-col overflow-hidden">
        <DialogHeader>
          <DialogTitle>Settings</DialogTitle>
        </DialogHeader>

        <Tabs
          value={activeTab}
          onValueChange={(v) => setActiveTab(v as typeof activeTab)}
          className="mt-4 flex-1 flex flex-col overflow-hidden min-h-0"
        >
          <ConfigWarningBanner className="mb-2" refreshKey={bannerRefreshKey} />
          <TabsList className="grid w-full grid-cols-6">
            <TabsTrigger value="general" className="gap-1">
              <Settings className="h-4 w-4" />
              <span className="hidden sm:inline text-xs">General</span>
            </TabsTrigger>
            <TabsTrigger value="llm" className="gap-1">
              <Brain className="h-4 w-4" />
              <span className="hidden sm:inline text-xs">LLM</span>
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

          <TabsContent value="general" className="mt-4 overflow-y-auto min-h-0">
            <div className="space-y-6">
              <LogLevelSelector />
              <div className="border-t border-border pt-4">
                <h3 className="text-sm font-medium mb-3">HTTP Proxy</h3>
                <ProxySettings />
              </div>
            </div>
          </TabsContent>

          <TabsContent value="llm" className="mt-4 overflow-y-auto min-h-0">
            <LLMSettings onSettingsSaved={handleSettingsSaved} />
          </TabsContent>

          <TabsContent value="search" className="mt-4 overflow-y-auto min-h-0">
            <SearchSettings />
          </TabsContent>

          <TabsContent value="mcp" className="mt-4 overflow-y-auto min-h-0">
            <MCPSettings />
          </TabsContent>

          <TabsContent value="security" className="mt-4 overflow-y-auto min-h-0">
            <SecuritySettings />
          </TabsContent>

          <TabsContent value="about" className="mt-4 overflow-y-auto min-h-0">
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
            </div>
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  )
}
