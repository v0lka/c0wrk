import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ThemeToggle } from './ThemeToggle'
import { LogLevelSelector } from './LogLevelSelector'
import { LLMSettings } from './LLMSettings'
import { SearchSettings } from './SearchSettings'
import { SecuritySettings } from './SecuritySettings'
import { Settings, Brain, Search, Shield, Info } from 'lucide-react'
import { ConfigWarningBanner } from './ConfigWarningBanner'
import { useState, useEffect, useRef, useCallback } from 'react'

interface SettingsModalProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function SettingsModal({ open, onOpenChange }: SettingsModalProps) {
  const [bannerRefreshKey, setBannerRefreshKey] = useState(0)
  const prevOpenRef = useRef(open)

  // Refresh banner when dialog opens
  useEffect(() => {
    if (open && !prevOpenRef.current) {
      setBannerRefreshKey(k => k + 1)
    }
    prevOpenRef.current = open
  }, [open])

  const handleSettingsSaved = useCallback(() => {
    setBannerRefreshKey(k => k + 1)
  }, [])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[600px] max-h-[80vh] flex flex-col overflow-hidden top-[40px] translate-y-0">
        <DialogHeader>
          <DialogTitle>Settings</DialogTitle>
        </DialogHeader>
        
        <Tabs defaultValue="general" className="mt-4 flex-1 flex flex-col overflow-hidden min-h-0">
          <ConfigWarningBanner className="mb-2" refreshKey={bannerRefreshKey} />
          <TabsList className="grid w-full grid-cols-5">
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
              <ThemeToggle />
              <LogLevelSelector />
            </div>
          </TabsContent>

          <TabsContent value="llm" className="mt-4 overflow-y-auto min-h-0">
            <div className="space-y-4">
              <LLMSettings onSettingsSaved={handleSettingsSaved} />
            </div>
          </TabsContent>

          <TabsContent value="search" className="mt-4 overflow-y-auto min-h-0">
            <div className="space-y-4">
              <SearchSettings />
            </div>
          </TabsContent>

          <TabsContent value="security" className="mt-4 overflow-y-auto min-h-0">
            <div className="space-y-4">
              <SecuritySettings />
            </div>
          </TabsContent>

          <TabsContent value="about" className="mt-4 overflow-y-auto min-h-0">
            <div className="space-y-4">
              <div className="flex items-center gap-3">
                <div className="h-12 w-12 rounded-lg bg-primary/10 flex items-center justify-center">
                  <span className="text-xl font-bold text-primary">c0</span>
                </div>
                <div>
                  <h3 className="font-semibold">c0wrk</h3>
                  <p className="text-sm text-muted-foreground">Version 0.0.1</p>
                </div>
              </div>
              
              <div className="text-sm text-muted-foreground space-y-2">
                <p>
                  An AI-powered coding assistant with multi-agent orchestration.
                </p>
                <p>
                  Built with warmth, love, and AI.
                </p>
              </div>

              <div className="pt-4 border-t border-border">
                <div className="flex flex-col gap-2 text-sm">
                  <a 
                    href="#"
                    className="text-primary hover:underline"
                    onClick={(e) => e.preventDefault()}
                  >
                    Documentation
                  </a>
                  <a 
                    href="#"
                    className="text-primary hover:underline"
                    onClick={(e) => e.preventDefault()}
                  >
                    Report an Issue
                  </a>
                  <a 
                    href="#"
                    className="text-primary hover:underline"
                    onClick={(e) => e.preventDefault()}
                  >
                    GitHub Repository
                  </a>
                </div>
              </div>
            </div>
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  )
}
