package providerauth

// ChatGPT subscription sign-in profile.
//
// The client id is the public Codex CLI OAuth client published in the
// openai/codex repository (codex-rs/login/src/auth/manager.rs,
// CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann"); it is a public PKCE client
// with no secret, usable by third-party clients. The port 1455 redirect and
// the issuer endpoints mirror that client's allow-listed configuration.
const (
	// chatgptProviderID keys the persisted secret ("providerauth:chatgpt").
	chatgptProviderID = "chatgpt"
	// chatgptIssuer is the OAuth issuer base URL.
	chatgptIssuer = "https://auth.openai.com"
	// chatgptClientID is the public Codex CLI OAuth client id.
	chatgptClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// chatgptOriginator identifies c0wrk in requests to OpenAI services.
	chatgptOriginator = "c0wrk"
	// chatgptAPIBaseURL is the ChatGPT Codex backend; /responses is appended
	// by the transport layer.
	chatgptAPIBaseURL = "https://chatgpt.com/backend-api/codex"
	// chatgptRedirectPort is the loopback redirect port.
	chatgptRedirectPort = 1455
	// chatgptRedirectHost and chatgptRedirectPath form the exact redirect
	// URI registered for the Codex CLI client:
	// http://localhost:1455/auth/callback. The issuer matches redirect_uri
	// against that allow-list byte-for-byte — the numeric host 127.0.0.1, a
	// different path, or a different port is rejected with
	// "invalid_authorize_request" before any login page renders.
	chatgptRedirectHost = "localhost"
	chatgptRedirectPath = "/auth/callback"
)

// chatgptScopes is the requested OAuth scope set.
var chatgptScopes = []string{"openid", "profile", "email", "offline_access"}

// chatgptAuthorizeParams mirrors the Codex CLI / OpenCode authorize requests:
// organizations are embedded in the ID token (an account-id source) and the
// simplified login page is requested; originator identifies the caller.
var chatgptAuthorizeParams = map[string]string{
	"id_token_add_organizations": "true",
	"codex_cli_simplified_flow":  "true",
	"originator":                 chatgptOriginator,
}

// ChatGPT returns the production ChatGPT subscription profile.
func ChatGPT() Profile {
	return Profile{
		ProviderID:      chatgptProviderID,
		Issuer:          chatgptIssuer,
		ClientID:        chatgptClientID,
		Scopes:          chatgptScopes,
		Originator:      chatgptOriginator,
		APIBaseURL:      chatgptAPIBaseURL,
		RedirectPort:    chatgptRedirectPort,
		RedirectHost:    chatgptRedirectHost,
		RedirectPath:    chatgptRedirectPath,
		AuthorizeParams: chatgptAuthorizeParams,
	}
}
