// Package providerauth is the foundation for subscription-backed LLM provider
// sign-in (ChatGPT first; Kimi and Claude later): OS-keychain secret
// persistence (SecretStore), the browser OAuth flow (Flow: PKCE S256 + state,
// a loopback redirect on every loopback family, token exchange), and the
// token lifecycle (TokenManager, an sp4rk llm.TokenSource).
//
// Flow behavior and the security invariants — secret values never logged or
// embedded in errors, loopback-only single-request listener, single-use
// state, unverified-by-design ID-token claims — are specified in
// specs/domains/llm-providers.md (ChatGPT Subscription Auth) and
// specs/decisions/074-chatgpt-subscription-auth.md.
package providerauth
