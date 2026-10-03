import { Button } from '@/components/ui/button'
import { Combobox, type ComboboxOption } from '@/components/ui/combobox'
import { Loader2, LogOut } from 'lucide-react'
import { useChatGPTAuthFlow } from '@/hooks/useChatGPTAuthFlow'
import { formatAuthExpiry } from '@/lib/chatgptFormat'
import type { ChatGPTAuthMode } from '@/types/models'

const AUTH_MODE_OPTIONS: readonly ComboboxOption[] = [
  { value: 'api_key', label: 'API key' },
  { value: 'oauth', label: 'ChatGPT subscription' },
]

interface ChatGPTAuthSectionProps {
  /** The DRAFT auth mode (useLLMConfig providerConfigs.chatgpt.auth_mode) —
   *  flipping the selector updates the draft and persists through the same
   *  debounced full-form save as every other provider field. */
  authMode: ChatGPTAuthMode
  onAuthModeChange: (mode: ChatGPTAuthMode) => void
}

/**
 * The ChatGPT provider's authentication section: the api_key/oauth mode
 * selector plus, in oauth mode, the browser sign-in flow (Sign in with
 * ChatGPT → StartChatGPTSignIn opens the system browser), the live account
 * snapshot (email / account id / token expiry from GetChatGPTAuthStatus),
 * and Sign out. The flow state — busy flags, the status snapshot, and the
 * auth error — lives in useChatGPTAuthFlow; this component only renders it.
 *
 * The section holds NO secrets and no chat-pipeline state: the auth status
 * is local UI state refreshed on mount and on every terminal event; the
 * mode is the only piece that persists, and it rides the ordinary provider
 * draft.
 */
export function ChatGPTAuthSection({ authMode, onAuthModeChange }: ChatGPTAuthSectionProps) {
  const { status, signInBusy, signOutBusy, authError, handleSignIn, handleCancel, handleSignOut } =
    useChatGPTAuthFlow()

  const signedIn = status?.signed_in === true

  return (
    <div className="flex flex-col gap-3 rounded-lg border bg-card/50 p-3">
      {/* Authentication mode selector */}
      <div className="flex flex-col gap-2">
        <label className="text-xs text-muted-foreground">Authentication</label>
        <div className="max-w-[280px]">
          <Combobox
            ariaLabel="ChatGPT authentication mode"
            value={authMode}
            options={AUTH_MODE_OPTIONS}
            onChange={(v) => onAuthModeChange(v === 'oauth' ? 'oauth' : 'api_key')}
          />
        </div>
        <p className="text-[11px] text-muted-foreground">
          {authMode === 'oauth'
            ? 'Requests authenticate with your ChatGPT subscription — sign in below; the API key is not used.'
            : 'Requests authenticate with the static API key below.'}
        </p>
      </div>

      {/* Subscription panel — oauth mode only */}
      {authMode === 'oauth' && (
        <div className="flex flex-col gap-2 border-t pt-3">
          {signedIn ? (
            <div className="flex flex-col gap-0.5 text-xs">
              <span className="font-medium text-success">Signed in with ChatGPT</span>
              {status?.email && <span className="text-muted-foreground">{status.email}</span>}
              {status?.account_id && (
                <span className="text-muted-foreground">Account {status.account_id}</span>
              )}
              {status?.expires_at && (
                <span className="text-muted-foreground">
                  Session valid until {formatAuthExpiry(status.expires_at)}
                </span>
              )}
            </div>
          ) : (
            <span className="text-xs text-muted-foreground">
              Not signed in
              {status?.last_error ? ` — ${status.last_error}` : ''}
            </span>
          )}

          <div className="flex flex-wrap items-center gap-2">
            {!signedIn && !signInBusy && (
              <Button size="sm" onClick={handleSignIn}>
                Sign in with ChatGPT
              </Button>
            )}
            {signInBusy && (
              <>
                <Button size="sm" disabled>
                  <Loader2 className="h-4 w-4 animate-spin" />
                  Waiting for browser…
                </Button>
                <Button size="sm" variant="ghost" onClick={handleCancel}>
                  Cancel
                </Button>
              </>
            )}
            {signedIn && (
              <Button
                size="sm"
                variant="outline"
                onClick={handleSignOut}
                disabled={signOutBusy || signInBusy}
              >
                {signOutBusy ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <LogOut className="h-4 w-4" />
                )}
                Sign out
              </Button>
            )}
          </div>

          {signInBusy && (
            <span className="text-[11px] text-muted-foreground">
              Complete the sign-in in your browser, then return here.
            </span>
          )}
          {authError && <span className="text-xs text-destructive">{authError}</span>}
        </div>
      )}
    </div>
  )
}
