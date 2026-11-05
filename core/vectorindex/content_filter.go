package vectorindex

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ContentSkipReason is a stable, log-safe explanation for a deterministic
// content-filter decision. It never contains file content or paths.
type ContentSkipReason string

const (
	ContentSkipGenerated    ContentSkipReason = "generated_marker"
	ContentSkipMinified     ContentSkipReason = "minified_long_line_low_whitespace"
	ContentSkipPathological ContentSkipReason = "pathological_long_token"
)

const contentFilterVersion = "v1"

// ContentFilterConfig controls early rejection of generated, minified, and
// pathological text before chunking (policy: ADR-036). A nil config selects
// DefaultContentFilterConfig; a non-nil config is used exactly as supplied.
type ContentFilterConfig struct {
	Enabled bool

	DetectGenerated    bool
	DetectMinified     bool
	DetectPathological bool

	GeneratedHeaderBytes       int
	MinifiedMinBytes           int
	MinifiedMaxLineBytes       int
	MinifiedMaxWhitespaceRatio float64
	PathologicalMinBytes       int
	PathologicalMaxTokenBytes  int
}

// DefaultContentFilterConfig is deliberately conservative: each heuristic
// requires a strong signal, and operators can disable the policy or tune every
// numeric threshold. Exact path-level control remains available via .aiignore.
func DefaultContentFilterConfig() ContentFilterConfig {
	return ContentFilterConfig{
		Enabled:                    true,
		DetectGenerated:            true,
		DetectMinified:             true,
		DetectPathological:         true,
		GeneratedHeaderBytes:       2048,
		MinifiedMinBytes:           16 * 1024,
		MinifiedMaxLineBytes:       10 * 1024,
		MinifiedMaxWhitespaceRatio: 0.08,
		PathologicalMinBytes:       32 * 1024,
		PathologicalMaxTokenBytes:  24 * 1024,
	}
}

func resolveContentFilterConfig(cfg *ContentFilterConfig) ContentFilterConfig {
	if cfg == nil {
		return DefaultContentFilterConfig()
	}
	return *cfg
}

// Fingerprint returns a stable serialization of every decision-affecting
// input, included in the chunker fingerprint so policy changes invalidate
// sidecars just like a max-chunk-size change does.
func (c ContentFilterConfig) Fingerprint() string {
	return fmt.Sprintf("%s|enabled=%t|generated=%t|minified=%t|pathological=%t|header=%d|minbytes=%d|maxline=%d|maxws=%.6f|pathbytes=%d|maxtoken=%d",
		contentFilterVersion, c.Enabled, c.DetectGenerated, c.DetectMinified,
		c.DetectPathological, c.GeneratedHeaderBytes, c.MinifiedMinBytes,
		c.MinifiedMaxLineBytes, c.MinifiedMaxWhitespaceRatio,
		c.PathologicalMinBytes, c.PathologicalMaxTokenBytes)
}

// DetectContentSkip classifies bounded file content before chunking. The
// decision is deterministic and depends only on content plus cfg.
func DetectContentSkip(content []byte, cfg ContentFilterConfig) (ContentSkipReason, bool) {
	if !cfg.Enabled || len(content) == 0 {
		return "", false
	}
	if cfg.DetectGenerated && hasGeneratedMarker(content, cfg.GeneratedHeaderBytes) {
		return ContentSkipGenerated, true
	}
	if cfg.DetectMinified && looksMinified(content, cfg) {
		return ContentSkipMinified, true
	}
	if cfg.DetectPathological && hasPathologicalToken(content, cfg) {
		return ContentSkipPathological, true
	}
	return "", false
}

func hasGeneratedMarker(content []byte, headerBytes int) bool {
	if headerBytes <= 0 {
		return false
	}
	if len(content) > headerBytes {
		content = content[:headerBytes]
	}
	header := strings.ToLower(string(content))
	markers := [...]string{
		"code generated ",
		"generated code -- do not edit",
		"generated file -- do not edit",
		"this file is generated. do not edit",
		"this file was automatically generated",
		"@generated",
	}
	for _, marker := range markers {
		if strings.Contains(header, marker) {
			return true
		}
	}
	return false
}

func looksMinified(content []byte, cfg ContentFilterConfig) bool {
	if cfg.MinifiedMinBytes <= 0 || len(content) < cfg.MinifiedMinBytes ||
		cfg.MinifiedMaxLineBytes <= 0 || cfg.MinifiedMaxWhitespaceRatio < 0 {
		return false
	}
	totalBytes := len(content)
	maxLine, whitespace := 0, 0
	lineLen := 0
	for len(content) > 0 {
		r, size := utf8.DecodeRune(content)
		if r == utf8.RuneError && size == 0 {
			break
		}
		if unicode.IsSpace(r) {
			whitespace += size
		}
		if r == '\n' || r == '\r' {
			if lineLen > maxLine {
				maxLine = lineLen
			}
			lineLen = 0
		} else {
			lineLen += size
		}
		content = content[size:]
	}
	if lineLen > maxLine {
		maxLine = lineLen
	}
	return maxLine > cfg.MinifiedMaxLineBytes &&
		float64(whitespace)/float64(totalBytes) <= cfg.MinifiedMaxWhitespaceRatio
}

func hasPathologicalToken(content []byte, cfg ContentFilterConfig) bool {
	if cfg.PathologicalMinBytes <= 0 || len(content) < cfg.PathologicalMinBytes ||
		cfg.PathologicalMaxTokenBytes <= 0 {
		return false
	}
	tokenLen := 0
	for _, b := range content {
		if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
			tokenLen = 0
			continue
		}
		tokenLen++
		if tokenLen > cfg.PathologicalMaxTokenBytes {
			return true
		}
	}
	return false
}
