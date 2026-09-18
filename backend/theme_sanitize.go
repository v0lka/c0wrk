package backend

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gorilla/css/scanner"
)

// Theme CSS sanitization.
//
// Imported themes are injected verbatim into a global <style> element in the
// webview, so a theme file is untrusted input. Rather than enumerating
// resource-referencing constructs (@import, url(), image-set(), escaped
// identifiers, …) and rejecting them one by one, the sanitizer parses the
// whole document with a real CSS tokenizer and keeps ONLY what a theme may
// legitimately contain:
//
//   - the `/* c0wrk-theme: <name> | <dark|light> */` metadata header comment;
//   - ordinary comments and whitespace (dropped from the output);
//   - exactly one top-level `:root { … }` rule whose declarations are
//     exclusively custom-property assignments (`--token: value;`) plus at
//     most one `color-scheme: dark|light;` declaration;
//   - values built from identifiers, hashes (#hex), numbers, dimensions,
//     percentages, quoted strings, commas, `!important`, data:-only url()
//     tokens, and a small allowlist of functions (rgb/rgba/hsl/hsla/hwb,
//     var, calc, min/max/clamp) with balanced parentheses.
//
// Every other construct — at-rules (including @import, @media, @theme),
// ordinary declarations, selectors other than :root, any function outside
// the allowlist (image-set(), -webkit-image-set(), env(), …), non-data:
// url() tokens, unbalanced parens, HTML CDO/CDC tokens — is rejected
// outright. The canonical output cannot reference any resource and cannot
// style anything outside custom properties, removing the entire class of
// resource-referencing tricks instead of enumerating them.

// sanitizeThemeCSS parses a theme document and returns its canonical
// sanitized form: the metadata header comment (when present) followed by a
// single `:root { … }` block holding only custom-property declarations and
// the color-scheme declaration, whitespace-normalized. It returns an error
// when the document contains anything outside the allowed shape or lacks the
// two required core tokens.
func sanitizeThemeCSS(content string) (string, error) {
	p := &themeCSSParser{scanner: scanner.New(content)}
	if err := p.run(); err != nil {
		return "", err
	}
	if !p.decls.Has("--color-background") || !p.decls.Has("--color-foreground") {
		return "", errors.New("theme CSS must declare the --color-background and --color-foreground custom properties on :root")
	}

	var b strings.Builder
	if p.header != "" {
		b.WriteString(p.header)
		b.WriteString("\n")
	}
	b.WriteString(":root {\n")
	for _, d := range p.decls.Slice() {
		b.WriteString("  ")
		b.WriteString(d.prop)
		b.WriteString(": ")
		b.WriteString(d.value)
		b.WriteString(";\n")
	}
	if p.colorScheme != "" {
		b.WriteString("  color-scheme: ")
		b.WriteString(p.colorScheme)
		b.WriteString(";\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// themeDecl is one sanitized declaration.
type themeDecl struct {
	prop  string
	value string
}

// declSet preserves declaration order while offering membership checks.
type declSet struct {
	order []themeDecl
	seen  map[string]struct{}
}

func (s *declSet) Has(prop string) bool {
	_, ok := s.seen[prop]
	return ok
}

func (s *declSet) Slice() []themeDecl { return s.order }

func (s *declSet) add(prop, value string) {
	if s.seen == nil {
		s.seen = map[string]struct{}{}
	}
	if _, dup := s.seen[prop]; dup {
		// Last declaration wins, mirroring the CSS cascade.
		for i := range s.order {
			if s.order[i].prop == prop {
				s.order[i].value = value
				return
			}
		}
	}
	s.seen[prop] = struct{}{}
	s.order = append(s.order, themeDecl{prop: prop, value: value})
}

// themeFuncAllowlist is the closed set of functions permitted inside
// declaration values. Anything else (image-set, attr, env, element, …) is
// rejected. Escaped identifiers such as `\75rl(` arrive as Function tokens
// with the escape intact, so they fail the exact-name check automatically.
var themeFuncAllowlist = map[string]bool{
	"rgb(":   true,
	"rgba(":  true,
	"hsl(":   true,
	"hsla(":  true,
	"hwb(":   true,
	"var(":   true,
	"calc(":  true,
	"min(":   true,
	"max(":   true,
	"clamp(": true,
}

// themeCSSParser is a one-pass sanitizer over the gorilla/css tokenizer.
type themeCSSParser struct {
	scanner *scanner.Scanner

	decls       declSet        // custom-property declarations in source order
	header      string         // raw metadata header comment, preserved verbatim
	colorScheme string         // sanitized `color-scheme` value, "" when absent
	sawRoot     bool           // guards the single-:root-rule invariant
	pending     *scanner.Token // pushback slot (a '}' that closes the block)
}

// run walks the token stream: leading comments/whitespace, one :root rule,
// trailing comments/whitespace, EOF.
func (p *themeCSSParser) run() error {
	for {
		tok := p.next()
		switch tok.Type {
		case scanner.TokenEOF:
			if !p.sawRoot {
				return errors.New("theme CSS must contain a :root rule with custom-property declarations")
			}
			return nil
		case scanner.TokenError:
			return fmt.Errorf("theme CSS: tokenizer error at %s", truncate(tok.Value))
		case scanner.TokenBOM:
			continue
		case scanner.TokenChar:
			if tok.Value == ":" {
				if err := p.parseRootRule(); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("theme CSS: unexpected %q outside the :root rule", tok.Value)
		default:
			return fmt.Errorf("theme CSS: unexpected token %q outside the :root rule", truncate(tok.Value))
		}
	}
}

// next returns the next meaningful token (skipping whitespace and comments),
// honoring the pushback slot. Comments are inspected on the way: the first
// one matching the metadata header shape is captured for the sanitized
// output.
func (p *themeCSSParser) next() *scanner.Token {
	if p.pending != nil {
		t := p.pending
		p.pending = nil
		return t
	}
	for {
		tok := p.scanner.Next()
		switch tok.Type {
		case scanner.TokenS:
			continue
		case scanner.TokenComment:
			if p.header == "" && themeHeaderRe.MatchString(tok.Value) {
				p.header = tok.Value
			}
			continue
		default:
			return tok
		}
	}
}

// parseRootRule parses `: root … {` after the ':' token was consumed. Only
// the bare selector `:root` is accepted (no combinators, no extra selectors).
func (p *themeCSSParser) parseRootRule() error {
	tok := p.next()
	if tok.Type != scanner.TokenIdent || tok.Value != "root" {
		return fmt.Errorf("theme CSS: only the :root selector is allowed, got %q", truncate(tok.Value))
	}
	tok = p.next()
	if tok.Type != scanner.TokenChar || tok.Value != "{" {
		return fmt.Errorf("theme CSS: expected '{' after :root, got %q", truncate(tok.Value))
	}
	if p.sawRoot {
		return errors.New("theme CSS: at most one :root rule is allowed")
	}
	p.sawRoot = true
	return p.parseDeclarations()
}

// parseDeclarations parses custom-property declarations (plus the single
// permitted color-scheme declaration) until the closing '}' of the :root
// block.
func (p *themeCSSParser) parseDeclarations() error {
	for {
		tok := p.next()
		switch tok.Type {
		case scanner.TokenEOF, scanner.TokenError:
			return errors.New("theme CSS: unterminated :root block")
		case scanner.TokenChar:
			switch tok.Value {
			case "}":
				return nil
			case "-":
				if err := p.parseCustomProperty(); err != nil {
					return err
				}
			default:
				return fmt.Errorf("theme CSS: unexpected %q in :root (only custom properties and color-scheme are allowed)", truncate(tok.Value))
			}
		case scanner.TokenIdent:
			if !strings.EqualFold(tok.Value, "color-scheme") {
				return fmt.Errorf("theme CSS: unexpected declaration %q in :root (only custom properties and color-scheme are allowed)", truncate(tok.Value))
			}
			if err := p.parseColorScheme(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("theme CSS: unexpected token %q in :root", truncate(tok.Value))
		}
	}
}

// parseCustomProperty continues after the leading '-' of a custom-property
// name; the tokenizer splits `--foo` into CHAR '-' + IDENT '-foo'.
func (p *themeCSSParser) parseCustomProperty() error {
	tok := p.next()
	if tok.Type != scanner.TokenIdent || !strings.HasPrefix(tok.Value, "-") {
		return fmt.Errorf("theme CSS: malformed custom property near %q", truncate(tok.Value))
	}
	prop := "-" + tok.Value
	if !strings.HasPrefix(prop, "--") {
		return fmt.Errorf("theme CSS: %q is not a custom property (names start with --)", prop)
	}
	if containsEscape(prop) {
		return fmt.Errorf("theme CSS: escape sequences are not allowed in custom property names (%s)", quote(truncate(prop)))
	}

	tok = p.next()
	if tok.Type != scanner.TokenChar || tok.Value != ":" {
		return fmt.Errorf("theme CSS: expected ':' after %s, got %q", prop, truncate(tok.Value))
	}

	value, closed, err := p.parseValue()
	if err != nil {
		return fmt.Errorf("%s: %w", prop, err)
	}
	if value == "" {
		return fmt.Errorf("theme CSS: %s has an empty value", prop)
	}
	p.decls.add(prop, value)
	if closed {
		// The '}' terminator was pushed back; the declarations loop must see
		// it, so stop here and let parseDeclarations consume it.
		p.pending = &scanner.Token{Type: scanner.TokenChar, Value: "}"}
	}
	return nil
}

// parseColorScheme parses `color-scheme: dark|light ;`.
func (p *themeCSSParser) parseColorScheme() error {
	tok := p.next()
	if tok.Type != scanner.TokenChar || tok.Value != ":" {
		return fmt.Errorf("theme CSS: expected ':' after color-scheme, got %q", truncate(tok.Value))
	}
	if p.colorScheme != "" {
		return errors.New("theme CSS: at most one color-scheme declaration is allowed")
	}
	value, closed, err := p.parseValue()
	if err != nil {
		return fmt.Errorf("color-scheme: %w", err)
	}
	v := strings.ToLower(value)
	if v != themeTypeDark && v != themeTypeLight {
		return fmt.Errorf("theme CSS: color-scheme must be dark or light, got %q", truncate(value))
	}
	p.colorScheme = v
	if closed {
		p.pending = &scanner.Token{Type: scanner.TokenChar, Value: "}"}
	}
	return nil
}

// parseValue consumes one declaration value up to its terminating ';' or the
// block-closing '}'. It returns the canonical value, whether the '}' block
// terminator was consumed (then pushed back by the caller), and an error.
// Token-level allowlist enforcement happens here; nested function calls are
// descended into via parseFunctionArgs.
func (p *themeCSSParser) parseValue() (value string, closedBlock bool, err error) {
	var b strings.Builder
	pendingSpace := false

	appendTok := func(v string) {
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteString(v)
	}

	for {
		tok := p.scanner.Next()
		switch tok.Type {
		case scanner.TokenEOF, scanner.TokenError:
			return "", false, errors.New("unterminated declaration")
		case scanner.TokenS:
			if b.Len() > 0 {
				pendingSpace = true
			}
		case scanner.TokenComment:
			continue
		case scanner.TokenIdent, scanner.TokenHash, scanner.TokenNumber,
			scanner.TokenPercentage, scanner.TokenDimension, scanner.TokenString:
			if containsEscape(tok.Value) {
				return "", false, errThemeEscape(tok.Value, "declaration values")
			}
			appendTok(tok.Value)
		case scanner.TokenURI:
			if containsEscape(tok.Value) {
				return "", false, errThemeEscape(tok.Value, "declaration values")
			}
			if !isDataURLToken(tok.Value) {
				return "", false, fmt.Errorf("external resource reference %s is not allowed (only data: URLs)", quote(truncate(tok.Value)))
			}
			appendTok(tok.Value)
		case scanner.TokenFunction:
			name := strings.ToLower(tok.Value)
			if !themeFuncAllowlist[name] {
				return "", false, fmt.Errorf("function %s is not allowed in theme values", quote(truncate(tok.Value)))
			}
			appendTok(name)
			if err := p.parseFunctionArgs(&b, &pendingSpace); err != nil {
				return "", false, err
			}
		case scanner.TokenChar:
			switch tok.Value {
			case ",":
				appendTok(",")
			case "/":
				// Modern color syntax separator: rgb(82 139 255 / 50%).
				appendTok("/")
			case "!":
				appendTok("!")
			case ";":
				return strings.TrimSpace(b.String()), false, nil
			case "}":
				return strings.TrimSpace(b.String()), true, nil
			default:
				return "", false, fmt.Errorf("character %q is not allowed in declaration values", truncate(tok.Value))
			}
		default:
			return "", false, fmt.Errorf("token %s is not allowed in declaration values", quote(truncate(tok.Value)))
		}
	}
}

// parseFunctionArgs consumes the arguments of an allowlisted function up to
// its balancing ')'. Arguments obey the same token allowlist as top-level
// values; nested allowlisted functions are permitted.
func (p *themeCSSParser) parseFunctionArgs(b *strings.Builder, pendingSpace *bool) error {
	appendTok := func(v string) {
		if *pendingSpace {
			b.WriteByte(' ')
			*pendingSpace = false
		}
		b.WriteString(v)
	}

	for {
		tok := p.scanner.Next()
		switch tok.Type {
		case scanner.TokenEOF, scanner.TokenError:
			return errors.New("unterminated function call")
		case scanner.TokenS:
			if b.Len() > 0 {
				*pendingSpace = true
			}
		case scanner.TokenComment:
			continue
		case scanner.TokenIdent, scanner.TokenHash, scanner.TokenNumber,
			scanner.TokenPercentage, scanner.TokenDimension, scanner.TokenString:
			if containsEscape(tok.Value) {
				return errThemeEscape(tok.Value, "function arguments")
			}
			appendTok(tok.Value)
		case scanner.TokenURI:
			if containsEscape(tok.Value) {
				return errThemeEscape(tok.Value, "function arguments")
			}
			if !isDataURLToken(tok.Value) {
				return fmt.Errorf("external resource reference %s is not allowed (only data: URLs)", quote(truncate(tok.Value)))
			}
			appendTok(tok.Value)
		case scanner.TokenFunction:
			name := strings.ToLower(tok.Value)
			if !themeFuncAllowlist[name] {
				return fmt.Errorf("function %s is not allowed in theme values", quote(truncate(tok.Value)))
			}
			appendTok(name)
			if err := p.parseFunctionArgs(b, pendingSpace); err != nil {
				return err
			}
		case scanner.TokenChar:
			switch tok.Value {
			case ",":
				appendTok(",")
			case "/", "-", "*", "+":
				// Operators for calc() expressions and the modern color
				// syntax separator rgb(82 139 255 / 50%).
				appendTok(tok.Value)
			case ")":
				appendTok(")")
				return nil
			default:
				return fmt.Errorf("character %q is not allowed in function arguments", truncate(tok.Value))
			}
		default:
			return fmt.Errorf("token %s is not allowed in function arguments", quote(truncate(tok.Value)))
		}
	}
}

// containsEscape reports whether a token value still carries a raw CSS escape
// sequence (a backslash followed by hex digits or any character). The
// tokenizer keeps escapes verbatim in IDENT/STRING/URL token values, so a
// backslash in a KEPT token means the token's text was never validated in its
// decoded form: the webview would decode `\7d ` back to `}` (or any other
// structural character) after the canonical output is injected, closing the
// :root block early and re-enabling arbitrary rule injection — including
// external url() fetches — from inside a "validated" theme. The theme surface
// only needs literal tokens, so any escape is rejected outright (fail closed).
func containsEscape(v string) bool { return strings.Contains(v, "\\") }

// errThemeEscape builds the rejection error for a kept token that carries a
// CSS escape sequence; where names the position the token appeared in.
func errThemeEscape(value, where string) error {
	return fmt.Errorf("token %s contains a CSS escape sequence, which is not allowed in theme %s", quote(truncate(value)), where)
}

// isDataURLToken reports whether a URI token references a data: URL. The
// tokenizer emits the whole `url(…)` production as the token value; both
// `url(data:…)` and the whitespace form `url( data:… )` are accepted, with
// any quote style, case-insensitively.
func isDataURLToken(tok string) bool {
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(tok), "url("), ")"))
	inner = strings.Trim(inner, `"'`)
	return strings.HasPrefix(inner, "data:")
}

// quote wraps a value in single quotes for error messages.
func quote(v string) string { return "'" + v + "'" }

// truncate shortens a token value for error messages.
func truncate(v string) string {
	if len(v) > 40 {
		return v[:40] + "…"
	}
	return v
}
