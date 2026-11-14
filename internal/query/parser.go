package query

import "fmt"

type parser struct {
	tokens []token
	pos    int
}

func parse(tokens []token) (Expr, error) {
	p := &parser{tokens: tokens}
	expr, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokEOF {
		return nil, syntaxErr(p.peek().pos, "unexpected token %q", p.peek().val)
	}
	return expr, nil
}

func (p *parser) peek() token {
	return p.peekAt(0)
}

func (p *parser) peekAt(offset int) token {
	idx := p.pos + offset
	if idx >= len(p.tokens) {
		return token{kind: tokEOF}
	}
	return p.tokens[idx]
}

func (p *parser) next() token {
	t := p.peek()
	if t.kind != tokEOF {
		p.pos++
	}
	return t
}

func (p *parser) expect(kind tokenKind) (token, error) {
	t := p.next()
	if t.kind != kind {
		return t, syntaxErr(t.pos, "expected %d, got %q", kind, t.val)
	}
	return t, nil
}

// expr = or_expr
func (p *parser) parseExpr() (Expr, error) {
	return p.parseOr()
}

// or_expr = and_expr ("OR" and_expr)*
func (p *parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tokOr {
		p.next() // consume OR
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &OrExpr{Left: left, Right: right}
	}
	return left, nil
}

// and_expr = unary ("AND"? unary)*
func (p *parser) parseAnd() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		if p.peek().kind == tokAnd {
			p.next() // consume explicit AND
		} else if !p.isUnaryStart() {
			break
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &AndExpr{Left: left, Right: right}
	}
	return left, nil
}

// isUnaryStart returns true if the next token can start a unary expression.
func (p *parser) isUnaryStart() bool {
	switch p.peek().kind {
	case tokText, tokQuoted, tokLParen, tokNot, tokBang, tokColon, tokStar:
		return true
	}
	return false
}

// unary = "NOT" unary | "!" unary | primary
func (p *parser) parseUnary() (Expr, error) {
	if p.peek().kind == tokNot || p.peek().kind == tokBang {
		p.next() // consume NOT or !
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &NotExpr{Inner: inner}, nil
	}
	return p.parsePrimary()
}

// primary = "(" expr ")" | "*" | field_expr | phrase | text
func (p *parser) parsePrimary() (Expr, error) {
	switch p.peek().kind {
	case tokStar:
		p.next() // consume *
		return &MatchAllExpr{}, nil

	case tokLParen:
		p.next() // consume (
		expr, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokRParen); err != nil {
			return nil, syntaxErr(p.peek().pos, "unclosed parenthesis")
		}
		return expr, nil

	case tokColon:
		return p.parseField()

	case tokQuoted:
		t := p.next()
		return &PhraseExpr{Value: t.val}, nil

	case tokText:
		// field:value shorthand — lookahead for colon followed by text/quoted
		if p.peekAt(1).kind == tokColon {
			next2 := p.peekAt(2)
			if next2.kind == tokText || next2.kind == tokQuoted {
				return p.parseFieldShorthand()
			}
		}
		// field>value / field>=value / field<value / field<=value shorthand
		if k := p.peekAt(1).kind; k == tokGt || k == tokGe || k == tokLt || k == tokLe {
			return p.parseCompareShorthand()
		}
		t := p.next()
		return &TextExpr{Value: t.val}, nil

	default:
		return nil, syntaxErr(p.peek().pos, "unexpected token %q", p.peek().val)
	}
}

// field_expr = ":" IDENT "=" value | ":" IDENT "IN" "(" value_list ")"
func (p *parser) parseField() (Expr, error) {
	p.next() // consume :

	field, err := p.expect(tokText)
	if err != nil {
		return nil, syntaxErr(p.peek().pos, "expected field name after ':'")
	}

	switch p.peek().kind {
	case tokEquals:
		p.next() // consume =
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return &FieldEqExpr{Field: field.val, Value: val}, nil

	case tokGt, tokGe, tokLt, tokLe:
		op := compareOpFor(p.next().kind)
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return &FieldCompareExpr{Field: field.val, Op: op, Value: val}, nil

	case tokText:
		if p.peek().val == "IN" {
			return p.parseFieldIn(field.val)
		}
		return nil, syntaxErr(p.peek().pos, "expected '=', 'IN', or comparison operator after field name")

	default:
		return nil, syntaxErr(p.peek().pos, "expected '=', 'IN', or comparison operator after field name")
	}
}

func compareOpFor(k tokenKind) CompareOp {
	switch k {
	case tokGt:
		return OpGt
	case tokGe:
		return OpGe
	case tokLt:
		return OpLt
	default:
		return OpLe
	}
}

// parseFieldIn parses "IN" "(" value_list ")"
func (p *parser) parseFieldIn(field string) (Expr, error) {
	p.next() // consume IN
	if _, err := p.expect(tokLParen); err != nil {
		return nil, syntaxErr(p.peek().pos, "expected '(' after IN")
	}

	var values []string
	for {
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		values = append(values, val)
		if p.peek().kind != tokComma {
			break
		}
		p.next() // consume ,
	}

	if _, err := p.expect(tokRParen); err != nil {
		return nil, syntaxErr(p.peek().pos, "expected ')' after value list")
	}

	return &FieldInExpr{Field: field, Values: values}, nil
}

// parseFieldShorthand parses "field:value" as a FieldEqExpr (shorthand for ":field=value").
func (p *parser) parseFieldShorthand() (Expr, error) {
	field := p.next() // consume field name
	p.next()          // consume :
	val, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	return &FieldEqExpr{Field: field.val, Value: val}, nil
}

// parseCompareShorthand parses "field>value" etc. as a FieldCompareExpr.
func (p *parser) parseCompareShorthand() (Expr, error) {
	field := p.next() // field name
	op := compareOpFor(p.next().kind)
	val, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	return &FieldCompareExpr{Field: field.val, Op: op, Value: val}, nil
}

// parseValue returns a text or quoted string value.
func (p *parser) parseValue() (string, error) {
	switch p.peek().kind {
	case tokText:
		return p.next().val, nil
	case tokQuoted:
		return p.next().val, nil
	default:
		return "", syntaxErr(p.peek().pos, "expected value")
	}
}

// SyntaxError is a query that does not parse. Pos is the byte offset of the
// token it stopped at, for an editor to point at.
type SyntaxError struct {
	Pos int
	Msg string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%s at position %d", e.Msg, e.Pos)
}

func syntaxErr(pos int, format string, args ...any) error {
	return &SyntaxError{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}
