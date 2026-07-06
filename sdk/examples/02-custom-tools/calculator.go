package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// evaluate parses and evaluates a simple arithmetic expression.
// Supports +, -, *, /, parentheses, and integer/float literals.
// This is a minimal recursive-descent evaluator — not a production calculator.
func evaluate(expr string) (float64, error) {
	p := &parser{tokens: tokenize(expr)}
	return p.parseExpr()
}

// --- tokenizer ---

type tokenKind int

const (
	tokNumber tokenKind = iota
	tokPlus
	tokMinus
	tokStar
	tokSlash
	tokLParen
	tokRParen
	tokEOF
)

type token struct {
	kind  tokenKind
	value float64
}

func tokenize(s string) []token {
	var tokens []token
	i := 0
	for i < len(s) {
		ch := s[i]
		switch {
		case unicode.IsSpace(rune(ch)):
			i++
		case ch == '+':
			tokens = append(tokens, token{kind: tokPlus})
			i++
		case ch == '-':
			tokens = append(tokens, token{kind: tokMinus})
			i++
		case ch == '*':
			tokens = append(tokens, token{kind: tokStar})
			i++
		case ch == '/':
			tokens = append(tokens, token{kind: tokSlash})
			i++
		case ch == '(':
			tokens = append(tokens, token{kind: tokLParen})
			i++
		case ch == ')':
			tokens = append(tokens, token{kind: tokRParen})
			i++
		case ch >= '0' && ch <= '9' || ch == '.':
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			num, err := strconv.ParseFloat(s[i:j], 64)
			if err != nil {
				panic(err) // tokenize errors are caught by recover in parseExpr
			}
			tokens = append(tokens, token{kind: tokNumber, value: num})
			i = j
		default:
			panic(fmt.Sprintf("unexpected character: %q", ch))
		}
	}
	tokens = append(tokens, token{kind: tokEOF})
	return tokens
}

// --- recursive-descent parser ---

type parser struct {
	tokens []token
	pos    int
}

func (p *parser) peek() token { return p.tokens[p.pos] }
func (p *parser) next() token { t := p.tokens[p.pos]; p.pos++; return t }

func (p *parser) parseExpr() (val float64, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return p.parseAddSub(), nil
}

func (p *parser) parseAddSub() float64 {
	left := p.parseMulDiv()
	for {
		t := p.peek()
		switch t.kind {
		case tokPlus:
			p.next()
			left += p.parseMulDiv()
		case tokMinus:
			p.next()
			left -= p.parseMulDiv()
		default:
			return left
		}
	}
}

func (p *parser) parseMulDiv() float64 {
	left := p.parseUnary()
	for {
		t := p.peek()
		switch t.kind {
		case tokStar:
			p.next()
			left *= p.parseUnary()
		case tokSlash:
			p.next()
			div := p.parseUnary()
			if div == 0 {
				panic("division by zero")
			}
			left /= div
		default:
			return left
		}
	}
}

func (p *parser) parseUnary() float64 {
	t := p.peek()
	if t.kind == tokMinus {
		p.next()
		return -p.parseUnary()
	}
	if t.kind == tokPlus {
		p.next()
		return p.parseUnary()
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() float64 {
	t := p.next()
	switch t.kind {
	case tokNumber:
		return t.value
	case tokLParen:
		val := p.parseAddSub()
		if p.next().kind != tokRParen {
			panic("expected ')'")
		}
		return val
	default:
		panic("unexpected token: " + tokenKindName(t.kind))
	}
}

func tokenKindName(k tokenKind) string {
	switch k {
	case tokNumber:
		return "number"
	case tokPlus:
		return "+"
	case tokMinus:
		return "-"
	case tokStar:
		return "*"
	case tokSlash:
		return "/"
	case tokLParen:
		return "("
	case tokRParen:
		return ")"
	case tokEOF:
		return "EOF"
	default:
		return strings.ToLower(fmt.Sprintf("token(%d)", k))
	}
}
