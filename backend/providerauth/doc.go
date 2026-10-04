// Package providerauth is the shared foundation for subscription-backed LLM
// provider sign-in (ChatGPT first; Kimi and Claude later).
//
// The package owns three concerns:
//
//   - Secret persistence (SecretStore): OAuth tokens live in the OS keychain
//     (service "c0wrk"), never in config.yaml and never in logs. On Linux a
//     Secret Service provider (gnome-keyring) must be available; without it
//     every store operation fails with an actionable error.
//   - The browser OAuth flow: PKCE (S256) + state, a loopback redirect
//     listener serving the redirect URI the OAuth client registered
//     (http://localhost:1455/auth/callback for ChatGPT), and the
//     authorization-code exchange at the issuer's token endpoint. The issuer
//     matches redirect_uri against the client's allow-list byte-for-byte, so
//     the listener binds the registered port on every loopback family (IPv4
//     and IPv6) and never substitutes another host, path, or port. ID-token
//     claims (chatgpt_account_id, email) are parsed but NOT cryptographically
//     verified — the token arrived directly from the token endpoint over TLS,
//     so its origin is already trusted.
//   - Token lifecycle (TokenManager): proactive refresh before expiry, a
//     single-flight guard so concurrent callers share one refresh, persistence
//     to the keychain, and a sign-out that clears everything. TokenManager
//     implements sp4rk's llm.TokenSource.
//
// Security invariants:
//
//   - Secret values (access/refresh/ID tokens) are never written to logs and
//     never embedded in error messages; only non-secret key names and
//     endpoints appear in diagnostics.
//   - The loopback listener binds loopback addresses only (127.0.0.1 and
//     ::1, every family the host provides) and serves exactly one request;
//     the OAuth state is single-use and validated on that request.
//   - All token-endpoint requests carry a context and go through an injectable
//     *http.Client so callers control timeouts, TLS policy, and proxies.
package providerauth
