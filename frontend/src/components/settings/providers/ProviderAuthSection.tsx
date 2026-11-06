import { Button } from '@/components/ui/button'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { Loader2, LogOut } from 'lucide-react'
import type { KimiAuthRegion, SubscriptionAuthMode } from '@/types/models'
import type { ProviderAuthFlowState } from '@/hooks/useProviderAuthFlow'

interface ProviderAuthSectionProps {
  displayName: string
  authMode: SubscriptionAuthMode
  onAuthModeChange: (mode: SubscriptionAuthMode) => void
  authRegion?: KimiAuthRegion
  onAuthRegionChange?: (region: KimiAuthRegion) => void
  flow: ProviderAuthFlowState
}

export function ProviderAuthSection({ displayName, authMode, onAuthModeChange, authRegion, onAuthRegionChange, flow }: ProviderAuthSectionProps) {
  const { status, signInBusy, signOutBusy, authError, handleSignIn, handleCancel, handleSignOut } = flow
  const signedIn = status?.signed_in === true
  const options: readonly ComboboxOption[] = [
    { value: 'api_key', label: 'API key' },
    { value: 'oauth', label: `${displayName} subscription` },
  ]
  const regionOptions: readonly ComboboxOption[] = [
    { value: 'global', label: 'Global (kimi.ai)' },
    { value: 'mainland_cn', label: 'Mainland China (kimi.com)' },
  ]

  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-card/50 p-3">
      <div className="flex flex-col gap-2">
        <label className="text-xs text-muted-foreground">Authentication</label>
        <div className="max-w-[280px]">
          <Combobox
            ariaLabel={`${displayName} authentication mode`}
            value={authMode}
            options={options}
            onChange={(v) => onAuthModeChange(v === 'oauth' ? 'oauth' : 'api_key')}
          />
        </div>
        <p className="text-xs text-muted-foreground">
          {authMode === 'oauth'
            ? `Requests authenticate with your ${displayName} subscription — sign in below; the API key is not used.`
            : 'Requests authenticate with the static API key below.'}
        </p>
      </div>

      {authMode === 'oauth' && authRegion && onAuthRegionChange && (
        <div className="flex flex-col gap-2">
          <label className="text-xs text-muted-foreground">Region</label>
          <div className="max-w-[280px]">
            <Combobox
              ariaLabel="Kimi region"
              value={authRegion}
              options={regionOptions}
              onChange={(v) => onAuthRegionChange(v === 'mainland_cn' ? 'mainland_cn' : 'global')}
            />
          </div>
        </div>
      )}

      {authMode === 'oauth' && (
        <div className="flex flex-col gap-2 border-t pt-3">
          {signedIn ? (
            <div className="flex flex-col gap-0.5 text-xs">
              <span className="font-medium text-success">Signed in with {displayName}</span>
              {status?.email && <span className="text-muted-foreground">{status.email}</span>}
              {status?.account_id && <span className="text-muted-foreground">Account {status.account_id}</span>}
            </div>
          ) : (
            <span className="text-xs text-muted-foreground">
              Not signed in{status?.last_error ? ` — ${status.last_error}` : ''}
            </span>
          )}

          <div className="flex flex-wrap items-center gap-2">
            {!signedIn && !signInBusy && <Button size="sm" onClick={handleSignIn}>Sign in with {displayName}</Button>}
            {signInBusy && (
              <>
                <Button size="sm" disabled><Loader2 className="h-4 w-4 animate-spin" />Waiting for browser…</Button>
                <Button size="sm" variant="ghost" onClick={handleCancel}>Cancel</Button>
              </>
            )}
            {signedIn && (
              <Button size="sm" variant="outline" onClick={handleSignOut} disabled={signOutBusy || signInBusy}>
                {signOutBusy ? <Loader2 className="h-4 w-4 animate-spin" /> : <LogOut className="h-4 w-4" />}
                Sign out
              </Button>
            )}
          </div>
          {signInBusy && <span className="text-xs text-muted-foreground">Complete the sign-in in your browser, then return here.</span>}
          {authError && <span className="text-xs text-destructive">{authError}</span>}
        </div>
      )}
    </div>
  )
}
