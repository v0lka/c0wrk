import { useEffect, useState } from 'react'
import { Bell, BellRing } from 'lucide-react'
import { useSystemNotificationStore } from '@/stores/systemNotificationStore'
import { Toggle } from './ModelProfilesControls'
import {
  checkNotificationAuthorization,
  initSystemNotifications,
  showTestNotification,
} from '@/api/notifications'
import { sendSystemNotification } from '@/lib/systemNotifications'
import { isWailsReady } from '@/api/runtime'
import { logger } from '@/lib/logger'

/** Tri-state of the lazy mount-time authorization probe (macOS-only concern:
 *  Linux/Windows always grant, so the probe never yields `denied` there). */
type PermissionState = 'checking' | 'granted' | 'denied'

/**
 * General-tab control for system (OS-level) notifications, mirroring
 * SoundSettings directly above it. A single master toggle governs ALL
 * banners: turning it on initializes the Go notification bridge (idempotent)
 * and previews one banner; turning it off simply stops future sends — the OS
 * retires already-delivered banners on its own schedule, so there is no
 * teardown to run.
 *
 * The authorization probe is deliberately lazy: it runs on this section's
 * mount (when the General tab renders it), not at app start, so opening
 * Settings — not launching the app — pays for it. A denial only ever happens
 * on macOS and is surfaced as a muted hint instead of a silently dead
 * channel; previews are suppressed in that state.
 */
export function SystemNotificationSettings() {
  const enabled = useSystemNotificationStore((s) => s.enabled)
  const setEnabled = useSystemNotificationStore((s) => s.setEnabled)
  const [permission, setPermission] = useState<PermissionState>('checking')

  // One authorization read per mount. Fail-open (a transport error resolves
  // to granted inside checkNotificationAuthorization), so the hint only ever
  // appears for a genuine macOS denial. Without the Wails runtime (vitest,
  // dev-frontend) the section stays in the neutral granted state.
  useEffect(() => {
    if (!isWailsReady()) return
    let cancelled = false
    void checkNotificationAuthorization().then((granted) => {
      if (!cancelled) setPermission(granted ? 'granted' : 'denied')
    })
    return () => {
      cancelled = true
    }
  }, [])

  const handleChange = (next: boolean): void => {
    setEnabled(next)
    if (!next) return
    // Fire-and-forget init (idempotent on both sides — App.tsx already ran
    // it at startup, this is the retry/first-touch path) + an immediate
    // preview banner so the user can confirm the channel works. With a
    // denied OS authorization the send lands nowhere; the muted hint is
    // what surfaces that case, so the preview is skipped there.
    void initSystemNotifications().catch(() => {})
    if (permission !== 'denied') {
      void sendSystemNotification({
        kind: 'success',
        title: 'c0wrk — System notifications enabled',
        body: 'You will see notifications like this when tasks finish or need your input.',
      })
    }
  }

  // The test button uses the Go-authored banner (ShowTestNotification): it
  // demonstrates click routing too — activating it focuses the window via the
  // Go callback without navigating anywhere.
  const handleTest = (): void => {
    void showTestNotification().catch((err) => {
      logger.warn('[system-notifications] test notification failed', err)
    })
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        {enabled ? (
          <BellRing className="h-4 w-4 text-muted-foreground" />
        ) : (
          <Bell className="h-4 w-4 text-muted-foreground" />
        )}
        <span className="text-sm font-medium">System Notifications</span>
      </div>
      <Toggle
        checked={enabled}
        onChange={handleChange}
        label={enabled ? 'Enabled' : 'Disabled'}
        description="Show a system notification when a task finishes, when your input is needed, or on errors. Works alongside sound notifications."
      />
      {permission === 'denied' ? (
        <p
          className="text-xs text-muted-foreground pl-12"
          data-testid="notification-permission-hint"
        >
          Notifications are disabled for c0wrk in macOS Settings — enable them to receive alerts
        </p>
      ) : (
        enabled && (
          <button
            type="button"
            onClick={handleTest}
            className="self-start rounded border border-input px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-muted/50 active:bg-muted/30 focus-visible:outline-none"
          >
            Send test notification
          </button>
        )
      )}
    </div>
  )
}
