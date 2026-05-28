package query

import "unicode"

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokText
	tokQuoted
	tokLParen
	tokRParen
	tokNot
	tokAnd
	tokOr
	tokColon
	tokEquals
	tokBang
	tokComma
	tokStar
	tokGt
	tokGe
	tokLt
	tokLe
)

type token struct {
	kind tokenKind
	val  string
	pos  int
}

type lexer struct {
	input  string
	pos    int
	tokens []token
}

func lex(input string) []token {
	l := &lexer{input: input}
	l.run()
	return l.tokens
}

func (l *lexer) run() {
	for l.pos < len(l.input) {
		ch := l.input[l.pos]
		switch {
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			l.pos++
		case ch == '(':
			l.emit(tokLParen, "(")
		case ch == ')':
			l.emit(tokRParen, ")")
		case ch == ':':
			l.emit(tokColon, ":")
		case ch == '=':
			l.emit(tokEquals, "=")
		case ch == '!':
			l.emit(tokBang, "!")
		case ch == ',':
			l.emit(tokComma, ",")
		case ch == '*':
			l.emit(tokStar, "*")
		case ch == '>':
			if l.pos+1 < len(l.input) && l.input[l.pos+1] == '=' {
				l.emit(tokGe, ">=")
			} else {
				l.emit(tokGt, ">")
			}
		case ch == '<':
			if l.pos+1 < len(l.input) && l.input[l.pos+1] == '=' {
				l.emit(tokLe, "<=")
			} else {
				l.emit(tokLt, "<")
			}
		case ch == '"':
			l.lexQuoted()
		default:
			l.lexWord()
		}
	}
	l.tokens = append(l.tokens, token{kind: tokEOF, pos: l.pos})
}

func (l *lexer) emit(kind tokenKind, val string) {
	l.tokens = append(l.tokens, token{kind: kind, val: val, pos: l.pos})
	l.pos += len(val)
}

func (l *lexer) lexQuoted() {
	start := l.pos
	l.pos++ // skip opening "
	for l.pos < len(l.input) && l.input[l.pos] != '"' {
		if l.input[l.pos] == '\\' && l.pos+1 < len(l.input) {
			l.pos++ // skip escaped char
		}
		l.pos++
	}
	val := l.input[start+1 : l.pos]
	if l.pos < len(l.input) {
		l.pos++ // skip closing "
	}
	l.tokens = append(l.tokens, token{kind: tokQuoted, val: val, pos: start})
}

func (l *lexer) lexWord() {
	start := l.pos
	for l.pos < len(l.input) {
		ch := rune(l.input[l.pos])
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' ||
			ch == '(' || ch == ')' || ch == ':' || ch == '=' ||
			ch == '!' || ch == ',' || ch == '"' ||
			ch == '>' || ch == '<' {
			break
		}
		if ch == '*' {
			break
		}
		if ch >= 0x80 {
			// Handle multi-byte unicode.
			r, size := decodeRune(l.input[l.pos:])
			if unicode.IsSpace(r) {
				break
			}
			l.pos += size
		} else {
			l.pos++
		}
	}
	word := l.input[start:l.pos]

	var kind tokenKind
	switch word {
	case "AND":
		kind = tokAnd
	case "OR":
		kind = tokOr
	case "NOT":
		kind = tokNot
	case "IN":
		kind = tokText // handled by parser in field context
	default:
		kind = tokText
	}
	l.tokens = append(l.tokens, token{kind: kind, val: word, pos: start})
}

func decodeRune(s string) (rune, int) {
	r := rune(s[0])
	if r < 0x80 {
		return r, 1
	}
	var size int
	switch {
	case r&0xE0 == 0xC0:
		size = 2
	case r&0xF0 == 0xE0:
		size = 3
	case r&0xF8 == 0xF0:
		size = 4
	default:
		return r, 1
	}
	if len(s) < size {
		return r, 1
	}
	var result rune
	switch size {
	case 2:
		result = rune(s[0]&0x1F)<<6 | rune(s[1]&0x3F)
	case 3:
		result = rune(s[0]&0x0F)<<12 | rune(s[1]&0x3F)<<6 | rune(s[2]&0x3F)
	case 4:
		result = rune(s[0]&0x07)<<18 | rune(s[1]&0x3F)<<12 | rune(s[2]&0x3F)<<6 | rune(s[3]&0x3F)
	}
	return result, size
}
