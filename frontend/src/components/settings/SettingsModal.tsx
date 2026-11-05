import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useSettingsStore } from '@/stores/settingsStore'
import { useExperimentalStore } from '@/stores/experimentalStore'
import { ConfigWarningBanner } from './ConfigWarningBanner'
import { LogLevelSelector } from './LogLevelSelector'
import { ThemeSelector } from './ThemeSelector'
import { TabsSettings } from './TabsSettings'
import { isWebTransport } from '@/api/runtime'
import { UIScaleSelector } from './UIScaleSelector'
import { FontSettings } from './FontSettings'
import { ProxySettings } from './ProxySettings'
import { SoundSettings } from './SoundSettings'
import { SystemNotificationSettings } from './SystemNotificationSettings'
import { PromptSuggestionsSettings } from './PromptSuggestionsSettings'
import { ResearchSpecsSettings } from './ResearchSpecsSettings'
import { SessionStatsSettings } from './SessionStatsSettings'
import { WebAccessSettings } from './WebAccessSettings'
import { WebviewGpuPolicySettings } from './WebviewGpuPolicySettings'
import { LLMSettings } from './LLMSettings'
import { ModelProfilesSettings } from './ModelProfilesSettings'
import { SearchSettings } from './SearchSettings'
import { CapabilitiesSettings } from './CapabilitiesSettings'
import { SecuritySettings } from './SecuritySettings'
import { UpdateSettings } from './UpdateSettings'
import { ExperimentalSettings } from './ExperimentalSettings'
import { Settings, Palette, Brain, Search, Shield, Info, Component, AlertTriangle, X, Gauge, Globe } from 'lucide-react'
import { useState, useEffect, useRef, useCallback } from 'react'
import { hasDefaultModel } from '@/api/config'
// The canonical app mark — the very same SVG Wails derives the bundled
// appicon.png from, imported by URL so the About artwork can never drift
// from the real icon. Served by the dev server via server.fs.allow in
// vite.config.ts; hashed into dist/assets by production builds.
import appIconUrl from '../../../../build/appicon.svg'

/**
 * Shared classes for every tab's scroll container. `pr-2` keeps a small
 * horizontal gutter between the scrolling content and the app-wide
 * `.custom-scrollbar` scrollbar, which otherwise sits flush against the
 * text and visually merges with it. `min-w-0` lets the column shrink inside
 * the horizontal flex row (nav + content) instead of overflowing it.
 */
const TAB_CONTENT_CLASS = 'overflow-y-auto min-h-0 min-w-0 custom-scrollbar pr-2'

/**
 * Left section nav (the vertical `TabsList`). The shared `TabsList` /
 * `TabsTrigger` chrome is a filled, rounded track with a raised active pill —
 * not what a quiet sidebar index wants. So the list drops the track entirely
 * (`bg-transparent p-0`) and opens up the vertical rhythm (`gap-3`), while the
 * per-trigger `TAB_TRIGGER_CLASS` below strips the state-dependent
 * background/shadow chrome and the 1px frame so the entries sit directly on
 * the dialog surface.
 */
const TAB_LIST_CLASS =
  'h-fit w-44 shrink-0 flex-col items-stretch justify-start gap-3 bg-transparent p-0'

/**
 * One entry in the left section nav. Beyond dropping the pill chrome, the
 * active/inactive text colors are swapped: inactive entries keep the
 * full-contrast foreground (the former active color) and the active entry
 * recedes to a muted tone (the former inactive color), distinguished by its
 * bold weight (`data-[state=active]:font-bold`) rather than by color. Labels
 * are uppercased at the trigger level so the `text-xs` span inherits it.
 *
 * `border-0` removes the shared trigger's 1px frame. The base trigger already
 * requests `border-transparent`, but that color utility cannot win here:
 * index.css ships an UNLAYERED `* { border-color: var(--color-border) }` rule,
 * and in CSS cascade layers an unlayered declaration outranks a layered one
 * regardless of specificity — so Tailwind's layered `border-transparent` is
 * overridden and the frame paints in the theme border color. Zeroing the
 * border WIDTH is what actually removes it (no unlayered rule touches
 * border-width). The hover background highlight (`hover:bg-muted/50`,
 * inherited from the shared trigger) is intentionally left intact.
 */
const TAB_TRIGGER_CLASS = [
  'flex-initial h-auto min-w-0 gap-2 uppercase border-0',
  'text-foreground dark:text-foreground',
  'data-[state=active]:bg-transparent data-[state=active]:text-foreground/60',
  'dark:data-[state=active]:bg-transparent dark:data-[state=active]:text-muted-foreground',
  'data-[state=active]:font-bold',
  'group-data-[variant=default]/tabs-list:data-[state=active]:shadow-none',
].join(' ')

export function SettingsModal() {
  const open = useSettingsStore((s) => s.open)
  const activeTab = useSettingsStore((s) => s.activeTab)
  const closeSettings = useSettingsStore((s) => s.closeSettings)
  const setActiveTab = useSettingsStore((s) => s.setActiveTab)
  // The master experimental switch — the Session Statistics and Web Access
  // blocks on the General tab are part of its gate scope (read reactively, so
  // flipping the switch in ExperimentalSettings hides/reveals the blocks
  // without a reload).
  const experimentalEnabled = useExperimentalStore((s) => s.enabled)
  const [bannerRefreshKey, setBannerRefreshKey] = useState(0)
  // One banner refresher shared by every writer that can move the
  // config-load-warning channel: the LLM saves (handleSettingsSaved) and the
  // Web Access control RPCs (ADR-102 — the banner follows the live section).
  const refreshBanner = useCallback(() => setBannerRefreshKey((k) => k + 1), [])
  const [closeBlocked, setCloseBlocked] = useState(false)
  const [checkingClose, setCheckingClose] = useState(false)
  const [currentDefaultModel, setCurrentDefaultModel] = useState('')
  const checkingRef = useRef(false)
  const prevOpenRef = useRef(open)

  useEffect(() => {
    if (open && !prevOpenRef.current) {
      refreshBanner()
      setCloseBlocked(false)
      // Pessimistically reset the cached default so the close fast-path can't
      // fire on a stale value left over from a previous open. LLMSettings
      // remounts on open and its loadConfig reports the effective default via
      // onDefaultModelChange, repopulating this before the user can act.
      setCurrentDefaultModel('')
    }
    prevOpenRef.current = open
  }, [open, refreshBanner])

  const handleDefaultModelChange = useCallback((model: string) => {
    setCurrentDefaultModel(model)
    if (model) {
      setCloseBlocked(false)
    }
  }, [])

  const handleSettingsSaved = useCallback(() => {
    refreshBanner()
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
  }, [currentDefaultModel, refreshBanner])

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
      <DialogContent
        className="sm:max-w-[800px] h-[calc(var(--ui-vh)*0.8)] flex flex-col overflow-hidden"
        showCloseButton={false}
        // No DialogDescription in this panel; opt out explicitly so Radix
        // does not warn about the missing description (and does not point
        // aria-describedby at a non-existent node).
        aria-describedby={undefined}
      >
        <DialogHeader className="flex flex-row items-center justify-between">
          <DialogTitle>Settings</DialogTitle>
          <button
            title="Close"
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

        {/* The dialog's own `gap-4` spaces the children, so the banner needs
            no extra margins; it spans the full width above the nav + content
            row. */}
        <ConfigWarningBanner refreshKey={bannerRefreshKey} />

        <Tabs
          value={activeTab}
          onValueChange={(v) => setActiveTab(v as typeof activeTab)}
          orientation="vertical"
          className="mt-4 flex-1 flex-row gap-4 overflow-hidden min-h-0"
        >
          <TabsList className={TAB_LIST_CLASS}>
            <TabsTrigger value="general" className={TAB_TRIGGER_CLASS}>
              <Settings className="h-4 w-4" />
              <span className="text-xs">General</span>
            </TabsTrigger>
            <TabsTrigger value="appearance" className={TAB_TRIGGER_CLASS}>
              <Palette className="h-4 w-4" />
              <span className="text-xs">Appearance</span>
            </TabsTrigger>
            <TabsTrigger value="llm" className={TAB_TRIGGER_CLASS}>
              <Brain className="h-4 w-4" />
              <span className="text-xs">LLM</span>
            </TabsTrigger>
            {/* Model Profiles is a first-class settings tab, independent of the
                experimental-features switch (that gate covers E2S, the embedded
                local model's frontend surfaces, and the Session Statistics
                block — not Model Profiles). */}
            <TabsTrigger value="model-profiles" className={TAB_TRIGGER_CLASS}>
              <Gauge className="h-4 w-4" />
              <span className="text-xs">Model Profiles</span>
            </TabsTrigger>
            <TabsTrigger value="search" className={TAB_TRIGGER_CLASS}>
              <Search className="h-4 w-4" />
              <span className="text-xs">Search</span>
            </TabsTrigger>
            <TabsTrigger value="capabilities" className={TAB_TRIGGER_CLASS}>
              <Component className="h-4 w-4" />
              <span className="text-xs">Capabilities</span>
            </TabsTrigger>
            <TabsTrigger value="security" className={TAB_TRIGGER_CLASS}>
              <Shield className="h-4 w-4" />
              <span className="text-xs">Security</span>
            </TabsTrigger>
            <TabsTrigger value="about" className={TAB_TRIGGER_CLASS}>
              <Info className="h-4 w-4" />
              <span className="text-xs">About</span>
            </TabsTrigger>
          </TabsList>

          <TabsContent value="general" className={TAB_CONTENT_CLASS}>
            <div className="space-y-6">
              <LogLevelSelector />
              <div className="border-t border-border pt-4">
                <SoundSettings />
              </div>
              <div className="border-t border-border pt-4">
                <SystemNotificationSettings />
              </div>
              <div className="border-t border-border pt-4">
                <PromptSuggestionsSettings />
              </div>
              <div className="border-t border-border pt-4">
                <ResearchSpecsSettings />
              </div>
              {/* Session Statistics is an experimental surface: rendered only
                  while the master experimental switch is on. The toggle itself
                  stays off by default and gates DISPLAY only — the counters
                  are always collected. Gated here (not inside the component)
                  so the border wrapper disappears with the block. */}
              {experimentalEnabled && (
                <div className="border-t border-border pt-4">
                  <SessionStatsSettings />
                </div>
              )}
              <div className="border-t border-border pt-4">
                <WebviewGpuPolicySettings />
              </div>
              <div className="border-t border-border pt-4">
                <ExperimentalSettings />
              </div>
              <div className="border-t border-border pt-4">
                <h3 className="flex items-center gap-2 text-sm font-medium mb-3">
                  <Globe className="h-4 w-4 text-muted-foreground" />
                  HTTP Proxy
                </h3>
                <ProxySettings />
              </div>
              {/* Web Access is an experimental surface, gated on the master
                  switch like Session Statistics — and a DESKTOP-window control:
                  in the web transport the control RPCs are unreachable from a
                  tab (ADR-101 isolation), so the section would only ever
                  render its "Status unavailable" state. Gated here (not inside
                  the component) so the border wrapper disappears with the
                  block. */}
              {experimentalEnabled && !isWebTransport() && (
                <div className="border-t border-border pt-4">
                  {/* ADR-102: every successful transport-affecting Web Access
                      write recomputes the section's config-load warnings, so
                      the banner above must re-read them. */}
                  <WebAccessSettings onConfigChanged={refreshBanner} />
                </div>
              )}
            </div>
          </TabsContent>

          <TabsContent value="appearance" className={TAB_CONTENT_CLASS}>
            <div className="space-y-6">
              <ThemeSelector />
              <div className="border-t border-border pt-4 flex flex-col gap-6">
                <UIScaleSelector />
                {/* FontSettings always renders: its comboboxes are free-text
                    fields, so they work on every OS — where the backend
                    reports no system font the dropdown simply offers Default
                    and the typed input carries the rest. */}
                <FontSettings />
              </div>
              {/* Tabs sits last on the tab: a master switch that reshapes the
                  whole workspace reads best after the pure appearance knobs
                  (theme / scale / fonts) above it. Hidden in the web
                  transport: the browser owns the tabs there, and the gate
                  forces tabsEnabled off — a switch the user could only fight
                  has no place in the settings. */}
              {!isWebTransport() && (
                <div className="border-t border-border pt-4">
                  <TabsSettings />
                </div>
              )}
            </div>
          </TabsContent>

          <TabsContent value="llm" className={TAB_CONTENT_CLASS}>
            <LLMSettings onSettingsSaved={handleSettingsSaved} onDefaultModelChange={handleDefaultModelChange} />
          </TabsContent>

          <TabsContent value="model-profiles" className={TAB_CONTENT_CLASS}>
            <ModelProfilesSettings />
          </TabsContent>

          <TabsContent value="search" className={TAB_CONTENT_CLASS}>
            <SearchSettings />
          </TabsContent>

          <TabsContent value="capabilities" className={TAB_CONTENT_CLASS}>
            <CapabilitiesSettings />
          </TabsContent>

          <TabsContent value="security" className={TAB_CONTENT_CLASS}>
            <SecuritySettings />
          </TabsContent>

          <TabsContent value="about" className={TAB_CONTENT_CLASS}>
            <div className="space-y-4">
              <div className="flex items-center gap-3">
                {/* The brand artwork itself, mirroring the real app icon, so
                    it deliberately bypasses theme tokens. */}
                <img
                  src={appIconUrl}
                  alt=""
                  aria-hidden="true"
                  className="size-12 shrink-0"
                />
                <div>
                  <h3 className="font-semibold">c0wrk</h3>
                  <p className="text-sm text-muted-foreground">Desktop AI assistant</p>
                </div>
              </div>
              <div className="text-sm text-muted-foreground space-y-2">
                <p>An AI-powered desktop assistant for research and development.</p>
                <p>Built with warmth, love, and c0wrk ❤️</p>
              </div>
              <UpdateSettings />
            </div>
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  )
}
