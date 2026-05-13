// Package lexical provides a BM25-based lexical index backed by bleve.
//
// The package registers a custom token filter "c0wrk_camel_case" at init time
// and exposes an Index interface for upserting and querying documents by
// shared IDs (the same IDs used by the chromem vector collection).
package lexical

import (
	"unicode"

	"github.com/blevesearch/bleve/v2/analysis"
	"github.com/blevesearch/bleve/v2/registry"
)

// CamelCaseFilterName is the registered name and type of the custom
// camelCase / acronym splitter token filter.
const CamelCaseFilterName = "c0wrk_camel_case"

// AnalyzerName is the name of the analyzer chain used for the Content field.
// It is defined per-mapping via AddCustomAnalyzer in Open (not globally).
const AnalyzerName = "c0wrk_code"

func init() {
	// Registration errors only occur on duplicate registration, which is
	// impossible from an init() that runs exactly once per process.
	_ = registry.RegisterTokenFilter(CamelCaseFilterName, camelCaseFilterConstructor)
}

// camelCaseFilter splits tokens on case/letter-digit boundaries so that
// identifiers like camelCase, HTMLParser, and foo123bar each yield the
// component subwords. It replaces the input token with its parts; if a
// token cannot be split, it is passed through unchanged.
type camelCaseFilter struct{}

func camelCaseFilterConstructor(_ map[string]interface{}, _ *registry.Cache) (analysis.TokenFilter, error) {
	return &camelCaseFilter{}, nil
}

func (f *camelCaseFilter) Filter(input analysis.TokenStream) analysis.TokenStream {
	out := make(analysis.TokenStream, 0, len(input))
	for _, tok := range input {
		parts := splitIdentifier(tok.Term)
		if len(parts) <= 1 {
			out = append(out, tok)
			continue
		}
		offset := tok.Start
		for i, p := range parts {
			nt := &analysis.Token{
				Term:     p,
				Start:    offset,
				End:      offset + len(p),
				Position: tok.Position + i,
				Type:     tok.Type,
				KeyWord:  tok.KeyWord,
			}
			out = append(out, nt)
			offset += len(p)
		}
	}
	return out
}

// splitIdentifier splits an identifier-like token on:
//   - lower -> upper  (camelCase  -> camel, Case)
//   - upperN -> upper lower (HTMLParser -> HTML, Parser)
//   - letter <-> digit (foo123bar -> foo, 123, bar)
//
// Returns a slice with a single element if no split is applied.
func splitIdentifier(term []byte) [][]byte {
	if len(term) == 0 {
		return nil
	}
	runes := []rune(string(term))
	if len(runes) < 2 {
		return [][]byte{term}
	}

	var (
		parts   [][]byte
		current []rune
	)
	flush := func() {
		if len(current) > 0 {
			parts = append(parts, []byte(string(current)))
			current = current[:0]
		}
	}

	current = append(current, runes[0])
	for i := 1; i < len(runes); i++ {
		prev := runes[i-1]
		cur := runes[i]

		switch {
		case unicode.IsLower(prev) && unicode.IsUpper(cur):
			flush()
		case unicode.IsUpper(prev) && unicode.IsUpper(cur) &&
			i+1 < len(runes) && unicode.IsLower(runes[i+1]):
			flush()
		case unicode.IsLetter(prev) && unicode.IsDigit(cur):
			flush()
		case unicode.IsDigit(prev) && unicode.IsLetter(cur):
			flush()
		}
		current = append(current, cur)
	}
	flush()

	if len(parts) <= 1 {
		return [][]byte{term}
	}
	return parts
}
