import { useCallback } from 'react'
import { Toggle } from './ModelProfilesControls'
import { useSystemFontStore } from '@/stores/systemFontStore'

/**
 * Appearance-tab "System Font" block: the opt-in switch over the persisted
 * systemFontStore, mirroring SystemNotificationSettings (header row + the
 * shared Toggle) and UIScaleSelector (no apply/save step — the store applies
 * the family to <html> the moment the flag flips).
 *
 * Visibility: the block only exists where the feature means something — a
 * family detected this session (`systemFontFamily !== null`, GNOME via
 * gsettings), or the user's own persisted opt-in. On KDE/Windows/macOS the
 * backend reports no system UI font and the switch was never flipped, so
 * nothing renders there at all ("the setting does not exist here"). A flag
 * left on from a session where a font WAS detected keeps the block visible
 * with a muted "not detected" note instead of silently vanishing — the user
 * can still see and undo their opt-in.
 */
export function SystemFontToggle() {
  const follow = useSystemFontStore((s) => s.followSystemFont)
  const family = useSystemFontStore((s) => s.systemFontFamily)
  const setFollowSystemFont = useSystemFontStore((s) => s.setFollowSystemFont)

  const handleToggle = useCallback(
    (next: boolean) => {
      // setFollowSystemFont applies the already-cached family to <html>
      // before persisting, so the switch takes effect immediately.
      setFollowSystemFont(next)
    },
    [setFollowSystemFont],
  )

  if (family === null && !follow) return null

  return (
    <div className="flex flex-col gap-3" data-testid="system-font-settings">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">System Font</span>
      </div>
      <Toggle
        checked={follow}
        onChange={handleToggle}
        label={follow ? 'Enabled' : 'Disabled'}
        description="Use the desktop environment's UI font instead of c0wrk's default typeface. Font changes on the desktop apply after restarting c0wrk."
      />
      {family !== null ? (
        <p className="text-xs text-muted-foreground pl-12" data-testid="system-font-family">
          detected: {family}
        </p>
      ) : (
        <p className="text-xs text-muted-foreground pl-12" data-testid="system-font-not-detected">
          not detected
        </p>
      )}
    </div>
  )
}
