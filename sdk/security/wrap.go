// Package security provides utilities for defending against indirect prompt
// injection attacks by delimiting untrusted content with XML boundary tags.
package security

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// UntrustedTag is the XML tag name used to delimit untrusted external content
// in LLM context messages.
const UntrustedTag = "untrusted-content"

var (
	tagOpenRe  = regexp.MustCompile(`(?i)<\s*` + UntrustedTag)
	tagCloseRe = regexp.MustCompile(`(?i)<\s*/\s*` + UntrustedTag)

	// metadataKeyRe whitelists metadata keys usable as XML attribute names.
	// Keys not matching are silently dropped by WrapUntrustedContent.
	metadataKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
)

// StripUntrustedTags escapes literal <untrusted-content patterns in content
// to prevent attackers from closing the wrapper tag early. Only the leading
// '<' is replaced with "&lt;" — the rest of the tag is preserved as-is.
//
// This operates on literal character sequences only. HTML-entity-encoded
// variants (e.g., &#60;/untrusted-content>) are NOT escaped because LLMs
// process raw text tokens — they do not decode HTML entities when
// interpreting context boundaries.
func StripUntrustedTags(content string) string {
	content = tagOpenRe.ReplaceAllStringFunc(content, func(m string) string {
		return "&lt;" + m[1:]
	})
	content = tagCloseRe.ReplaceAllStringFunc(content, func(m string) string {
		return "&lt;" + m[1:]
	})
	return content
}

// WrapUntrustedContent wraps content in <untrusted-content> XML tags with
// a source attribute identifying the tool that produced the data. Optional
// metadata entries are added as additional XML attributes.
//
// The content is first sanitized via StripUntrustedTags to prevent tag
// breakout attacks.
func WrapUntrustedContent(content, source string, metadata map[string]string) string {
	sanitized := StripUntrustedTags(content)

	var attrs strings.Builder
	// Escape quotes in source for valid XML attribute values, consistent
	// with metadata value escaping below. Using %q alone produces Go-style
	// \" escaping which is invalid in XML and can break attribute parsing.
	escapedSource := strings.ReplaceAll(source, `"`, "&quot;")
	fmt.Fprintf(&attrs, "source=%q", escapedSource)
	// Sort metadata keys for deterministic attribute ordering.
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		// Only emit keys that are valid XML attribute names — a key
		// containing spaces, quotes, '>' etc. could break out of the
		// attribute list and forge tags.
		if !metadataKeyRe.MatchString(key) {
			continue
		}
		escaped := strings.ReplaceAll(metadata[key], `"`, "&quot;")
		fmt.Fprintf(&attrs, " %s=%q", key, escaped)
	}

	return fmt.Sprintf("<%s %s>\n%s\n</%s>", UntrustedTag, attrs.String(), sanitized, UntrustedTag)
}
