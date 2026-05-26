// Package query implements a query language for matching records against
// boolean expressions with field equality, set membership, and text search.
//
// Syntax:
//
//   - match-all (every record matches)
//     term                       text match (any field contains term)
//     "exact phrase"             phrase match (any field contains phrase)
//     NOT term / !term           negation
//     term AND term              conjunction (implicit when space-separated)
//     term OR term               disjunction
//     (a OR b) AND c             grouping
//     :field=value               field equals value
//     :field="multi word"        field equals quoted value
//     :field IN (a, b, c)        field in set
//     field:value                shorthand for :field=value
package query

// Parse parses a query string into an expression tree.
func Parse(input string) (Expr, error) {
	tokens := lex(input)
	return parse(tokens)
}
