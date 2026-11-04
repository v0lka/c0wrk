package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/v0lka/sp4rk/tools/builtins/websearch"
)

// ValidateSearchBaseURL checks search.base_url against the RAW config value
// (${ENV_VAR} placeholders unresolved). Cross-subsystem contract
// (specs/domains/tool-system/builtins.md, Web Search Base-URL Validation):
// only the searxng provider uses it; empty fails (ApplyDefaults
// seeds websearch.DefaultSearxngBaseURL); a ${...} placeholder passes here
// and is re-checked EXPANDED, fail-closed, by the consumption-time gate
// (core/tools CreateSearchProviderWithClient); otherwise it must be an
// absolute http(s) URL with a host. The Settings save path
// (UpdateSearchSettings) runs the same check up front.
func ValidateSearchBaseURL(provider, raw string) error {
	if !strings.EqualFold(provider, "searxng") {
		return nil
	}
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf(
			"search.provider %q requires search.base_url (the SearXNG instance URL, e.g. %s)",
			provider, websearch.DefaultSearxngBaseURL,
		)
	}
	if strings.Contains(raw, "${") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("search.base_url %q is not a valid URL: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("search.base_url %q must be an absolute http(s) URL with a host", raw)
	}
	return nil
}
